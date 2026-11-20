package gb

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"gbserver/internal/sip"
)

// VerifySession 纯验签拉流会话：只收流 + 验签，不转码、不向上级推流
type VerifySession struct {
	CallID    string
	ChannelID string
	CertID    string

	DeviceID    string
	DeviceIP    string
	DevicePort  int
	DeviceProto string

	LowerFrom string
	LowerTo   string

	SSRC       uint32
	TransferID int
	RecvPort   int
	State      string
	Created    time.Time

	closeOnce sync.Once
}

// VerifySessionInfo 管理接口输出
type VerifySessionInfo struct {
	ID         string    `json:"id"`
	ChannelID  string    `json:"channel_id"`
	CertID     string    `json:"cert_id"`
	DeviceID   string    `json:"device_id"`
	DeviceAddr string    `json:"device_addr"`
	TransferID int       `json:"transfer_id"`
	RecvPort   int       `json:"recv_port"`
	State      string    `json:"state"`
	CreatedAt  time.Time `json:"created_at"`

	VerifyResult   *string `json:"verify_result"`
	VerifyDetail   string  `json:"verify_detail"`
	VerifySuccess  uint64  `json:"verify_success"`
	VerifyFail     uint64  `json:"verify_fail"`
	VerifyLastTime string  `json:"verify_last_time"`
}

// ChannelExists 通道是否存在
func (p *Platform) ChannelExists(id string) bool {
	ch, _ := p.reg.Channel(id)
	return ch != nil
}

// VerifyCerts 返回 TransferServer 可用的验签证书 ID
func (p *Platform) VerifyCerts() ([]string, error) {
	return p.tc.Certs()
}

// StartVerify 向下级通道发起 INVITE，仅收流并做软验签
func (p *Platform) StartVerify(channelID, certID string) (*VerifySession, error) {
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return nil, fmt.Errorf("missing channel")
	}
	certID = strings.TrimSpace(certID)

	_, dev := p.reg.Channel(channelID)
	if dev == nil {
		return nil, fmt.Errorf("channel %s not found", channelID)
	}

	// TransferServer：save 模式（/dev/null）+ verify，不转码不推送
	// cert_id 为空时不传，由 TransferServer 回退使用 channel_id 查找证书
	res, err := p.tc.StartVerify(channelID, certID)
	if err != nil {
		return nil, err
	}

	effectiveCert := certID
	if effectiveCert == "" {
		effectiveCert = channelID
	}

	ssrc := randomSSRC()
	callID := p.newCallID()
	cseq := p.nextCSeq()
	from := fmt.Sprintf("<sip:%s@%s>;tag=%s", p.cfg.SIPID, p.cfg.SIPDomain, sip.RandomTag())
	to := fmt.Sprintf("<sip:%s@%s>", channelID, dev.Domain)
	uri := fmt.Sprintf("sip:%s@%s", channelID, dev.Domain)

	req := p.buildRequest("INVITE", uri, callID, cseq, from, to, dev.Proto, p.lowerSDP(res.RecvPort, ssrc).Bytes())
	req.Set("Content-Type", "Application/SDP")
	req.Set("Subject", fmt.Sprintf("%s:%010d,%s:0", channelID, ssrc, p.cfg.SIPID))

	log.Printf("VERIFY INVITE %s -> device %s (%s:%d/%s) recv_port=%d cert=%s",
		channelID, dev.ID, dev.IP, dev.Port, dev.Proto, res.RecvPort, effectiveCert)

	resp, err := p.doRequest(req, dev.IP, dev.Port, dev.Proto, 10*time.Second)
	if err != nil || resp.StatusCode != 200 {
		if stopErr := p.tc.Stop(res.ID); stopErr != nil {
			log.Printf("transfer stop %d: %v", res.ID, stopErr)
		}
		if err != nil {
			return nil, fmt.Errorf("invite device: %w", err)
		}
		return nil, fmt.Errorf("device rejected: %d", resp.StatusCode)
	}

	lowerTo := resp.Get("To")
	ack := p.buildRequest("ACK", uri, callID, cseq, from, lowerTo, dev.Proto, nil)
	if err := p.transport.SendTo(dev.IP, dev.Port, dev.Proto, ack); err != nil {
		log.Printf("send ACK to %s failed: %v", dev.ID, err)
	}

	sess := &VerifySession{
		CallID:      callID,
		ChannelID:   channelID,
		CertID:      effectiveCert,
		DeviceID:    dev.ID,
		DeviceIP:    dev.IP,
		DevicePort:  dev.Port,
		DeviceProto: dev.Proto,
		LowerFrom:   from,
		LowerTo:     lowerTo,
		SSRC:        ssrc,
		TransferID:  res.ID,
		RecvPort:    res.RecvPort,
		State:       "verifying",
		Created:     time.Now(),
	}
	p.mu.Lock()
	p.verify[callID] = sess
	p.mu.Unlock()

	log.Printf("verify session %s started (channel %s, cert %s, transfer %d)",
		callID, channelID, effectiveCert, res.ID)
	return sess, nil
}

// StopVerify 按会话 ID 或通道 ID 停止验签拉流
func (p *Platform) StopVerify(id string) bool {
	p.mu.Lock()
	var target *VerifySession
	for _, s := range p.verify {
		if s.CallID == id || s.ChannelID == id {
			target = s
			break
		}
	}
	p.mu.Unlock()
	if target == nil {
		return false
	}
	p.closeVerifySession(target, "api stop")
	return true
}

// VerifySessions 返回全部验签会话（含验签结果）
func (p *Platform) VerifySessions() []VerifySessionInfo {
	p.mu.Lock()
	out := make([]VerifySessionInfo, 0, len(p.verify))
	transferIDs := make([]int, 0, len(p.verify))
	for _, s := range p.verify {
		out = append(out, VerifySessionInfo{
			ID:         s.CallID,
			ChannelID:  s.ChannelID,
			CertID:     s.CertID,
			DeviceID:   s.DeviceID,
			DeviceAddr: fmt.Sprintf("%s:%d/%s", s.DeviceIP, s.DevicePort, s.DeviceProto),
			TransferID: s.TransferID,
			RecvPort:   s.RecvPort,
			State:      s.State,
			CreatedAt:  s.Created,
		})
		transferIDs = append(transferIDs, s.TransferID)
	}
	p.mu.Unlock()

	// 锁外查询 TransferServer 的验签结果
	for i := range out {
		if transferIDs[i] <= 0 {
			continue
		}
		vr, err := p.tc.VerifyResult(transferIDs[i])
		if err != nil || vr == nil || !vr.Enabled {
			continue
		}
		out[i].VerifyResult = vr.Result
		out[i].VerifyDetail = vr.Detail
		out[i].VerifySuccess = vr.Success
		out[i].VerifyFail = vr.Fail
		out[i].VerifyLastTime = vr.LastTime
	}
	return out
}

func (p *Platform) verifyByLower(callID string) *VerifySession {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.verify[callID]
}

// closeVerifySession 停止验签转发并向下级发 BYE
func (p *Platform) closeVerifySession(s *VerifySession, reason string) {
	s.closeOnce.Do(func() {
		s.State = "closed"
		if s.TransferID > 0 {
			if err := p.tc.Stop(s.TransferID); err != nil {
				log.Printf("transfer stop %d: %v", s.TransferID, err)
			}
		}

		domain := sip.DomainPart(s.LowerTo)
		if domain == "" {
			domain = p.cfg.SIPDomain
		}
		uri := fmt.Sprintf("sip:%s@%s", s.ChannelID, domain)
		bye := p.buildRequest("BYE", uri, s.CallID, p.nextCSeq(), s.LowerFrom, s.LowerTo, s.DeviceProto, nil)
		go func() {
			if _, err := p.doRequest(bye, s.DeviceIP, s.DevicePort, s.DeviceProto, 5*time.Second); err != nil {
				log.Printf("send BYE to device %s failed: %v", s.DeviceID, err)
			}
		}()

		p.mu.Lock()
		if cur, ok := p.verify[s.CallID]; ok && cur == s {
			delete(p.verify, s.CallID)
		}
		p.mu.Unlock()
		log.Printf("verify session %s closed (%s)", s.CallID, reason)
	})
}
