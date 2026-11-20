package gb

import (
	"fmt"
	"log"
	"sync"
	"time"

	"gbserver/internal/sip"
)

// Session 一次级联点播会话：上级 INVITE -> 转码 -> 下级推流 -> 转码后推给上级
type Session struct {
	ChannelID string
	DeviceID  string

	DeviceIP    string
	DevicePort  int
	DeviceProto string

	UpperIP     string
	UpperPort   int
	UpperProto  string
	UpperUser   string
	UpperDomain string

	UpperCallID string
	UpperFrom   string
	UpperTo     string

	LowerCallID string
	LowerFrom   string
	LowerTo     string

	SSRC       uint32
	TransferID int
	RecvPort   int
	State      string
	Created    time.Time

	closeOnce sync.Once
}

// handleInvite 处理上级平台点播：先启动转码服务，再向下级设备拉流，最后向上级应答
func (p *Platform) handleInvite(msg *sip.Message, peer sip.Peer) {
	inviteCallID := msg.CallID()
	if inviteCallID != "" {
		p.mu.Lock()
		duplicated := p.invites[inviteCallID] || p.sessions[inviteCallID] != nil
		if !duplicated {
			p.invites[inviteCallID] = true
		}
		p.mu.Unlock()
		if duplicated {
			return
		}
		defer func() {
			p.mu.Lock()
			delete(p.invites, inviteCallID)
			p.mu.Unlock()
		}()
	}

	channelID := sip.UserPart(msg.Get("To"))
	if channelID == "" {
		p.replyError(msg, peer, 400, "Bad Request")
		return
	}

	sdp, err := sip.ParseSDP(msg.Body)
	if err != nil {
		log.Printf("INVITE %s bad sdp: %v", channelID, err)
		p.replyError(msg, peer, 488, "Bad SDP")
		return
	}
	video := sdp.Video()
	if video == nil || video.Port == 0 {
		p.replyError(msg, peer, 488, "Bad SDP")
		return
	}

	upperIP := video.ReceiveIP(sdp)
	if upperIP == "" {
		upperIP = peer.IP
	}
	upperSSRC := sdp.SSRC
	if upperSSRC == 0 {
		upperSSRC = randomSSRC()
	}

	channel, dev := p.reg.Channel(channelID)
	if channel == nil || dev == nil {
		log.Printf("INVITE for unknown channel %s", channelID)
		p.replyError(msg, peer, 404, "Channel Not Found")
		return
	}

	outProto := "udp"
	if video.IsTCP() {
		outProto = "tcp"
	}

	// 1) 启动转码服务：收流端口由 TransferServer 分配，转码后推向上级媒体地址
	res, err := p.tc.Start(upperIP, video.Port, outProto, upperSSRC)
	if err != nil {
		log.Printf("transfer start for channel %s failed: %v", channelID, err)
		p.replyError(msg, peer, 500, "Transfer Failed")
		return
	}

	// 2) 向下级设备发起 INVITE，要求其按配置的协议推流到 recv_port
	lowerSDP := p.lowerSDP(res.RecvPort, upperSSRC)

	callID := p.newCallID()
	cseq := p.nextCSeq()
	lowerFrom := fmt.Sprintf("<sip:%s@%s>;tag=%s", p.cfg.SIPID, p.cfg.SIPDomain, sip.RandomTag())
	lowerTo := fmt.Sprintf("<sip:%s@%s>", channelID, dev.Domain)
	uri := fmt.Sprintf("sip:%s@%s", channelID, dev.Domain)

	req := p.buildRequest("INVITE", uri, callID, cseq, lowerFrom, lowerTo, dev.Proto, lowerSDP.Bytes())
	req.Set("Content-Type", "Application/SDP")
	req.Set("Subject", fmt.Sprintf("%s:%010d,%s:0", channelID, upperSSRC, p.cfg.SIPID))

	log.Printf("INVITE %s -> device %s (%s:%d/%s) recv_port=%d ssrc=%010d",
		channelID, dev.ID, dev.IP, dev.Port, dev.Proto, res.RecvPort, upperSSRC)

	resp, err := p.doRequest(req, dev.IP, dev.Port, dev.Proto, 10*time.Second)
	if err != nil || resp.StatusCode != 200 {
		code := 480
		if err == nil && resp.StatusCode >= 400 {
			code = resp.StatusCode
		}
		log.Printf("device invite for %s failed: %v", channelID, err)
		if stopErr := p.tc.Stop(res.ID); stopErr != nil {
			log.Printf("transfer stop %d: %v", res.ID, stopErr)
		}
		p.replyError(msg, peer, code, "Device Unavailable")
		return
	}

	// 3) 向下级回 ACK
	lowerTo = resp.Get("To")
	ack := p.buildRequest("ACK", uri, callID, cseq, lowerFrom, lowerTo, dev.Proto, nil)
	if err := p.transport.SendTo(dev.IP, dev.Port, dev.Proto, ack); err != nil {
		log.Printf("send ACK to %s failed: %v", dev.ID, err)
	}

	// 4) 向上级回 200 OK，声明送流地址（推流由转码服务完成）
	answer := &sip.SDP{
		OriginUser: p.cfg.SIPID,
		Name:       "Play",
		ConnIP:     p.cfg.SIPIP,
		SSRC:       upperSSRC,
		Medias: []*sip.SDPMedia{{
			Kind:      "video",
			Port:      video.Port,
			Proto:     video.Proto,
			Payload:   96,
			Encoding:  "PS",
			ClockRate: 90000,
			SendOnly:  true,
		}},
	}

	ok := sip.NewResponse(msg, 200, "OK")
	ok.Set("Content-Type", "Application/SDP")
	ok.Set("Contact", fmt.Sprintf("<sip:%s@%s:%d>", p.cfg.SIPID, p.cfg.SIPIP, p.cfg.SIPPort))
	ok.Body = answer.Bytes()
	p.replyMsg(peer, ok)

	sess := &Session{
		ChannelID:   channelID,
		DeviceID:    dev.ID,
		DeviceIP:    dev.IP,
		DevicePort:  dev.Port,
		DeviceProto: dev.Proto,
		UpperIP:     peer.IP,
		UpperPort:   peer.Port,
		UpperProto:  peer.Proto,
		UpperUser:   sip.UserPart(msg.Get("From")),
		UpperDomain: sip.DomainPart(msg.Get("From")),
		UpperCallID: msg.CallID(),
		UpperFrom:   msg.Get("From"),
		UpperTo:     ok.Get("To"),
		LowerCallID: callID,
		LowerFrom:   lowerFrom,
		LowerTo:     lowerTo,
		SSRC:        upperSSRC,
		TransferID:  res.ID,
		RecvPort:    res.RecvPort,
		State:       "playing",
		Created:     time.Now(),
	}
	if sess.UpperDomain == "" {
		sess.UpperDomain = p.cfg.SIPDomain
	}
	p.addSession(sess)
	log.Printf("session %s established (channel %s, transfer %d)", sess.UpperCallID, channelID, res.ID)
}

// handleBye 处理上级或下级的 BYE
func (p *Platform) handleBye(msg *sip.Message, peer sip.Peer) {
	p.replyMsg(peer, sip.NewResponse(msg, 200, "OK"))

	if s := p.sessionByUpper(msg.CallID()); s != nil {
		p.closeSession(s, "upper bye", false, true)
		return
	}
	if s := p.sessionByLower(msg.CallID()); s != nil {
		p.closeSession(s, "lower bye", true, false)
		return
	}
	if s := p.verifyByLower(msg.CallID()); s != nil {
		p.closeVerifySession(s, "lower bye")
	}
}

// lowerSDP 构造向下级拉流用的 SDP，协议由 src_protocol 决定
func (p *Platform) lowerSDP(recvPort int, ssrc uint32) *sip.SDP {
	media := &sip.SDPMedia{
		Kind:      "video",
		Port:      recvPort,
		Payload:   96,
		Encoding:  "PS",
		ClockRate: 90000,
		RecvOnly:  true,
	}
	if p.cfg.SrcProtocol == "udp" {
		media.Proto = "RTP/AVP"
	} else {
		media.Proto = "TCP/RTP/AVP"
		media.Setup = "passive"
	}
	return &sip.SDP{
		OriginUser: p.cfg.SIPID,
		Name:       "Play",
		ConnIP:     p.cfg.SIPIP,
		SSRC:       ssrc,
		Medias:     []*sip.SDPMedia{media},
	}
}

// closeSession 停止转码并结束会话
func (p *Platform) closeSession(s *Session, reason string, notifyUpper, notifyLower bool) {
	s.closeOnce.Do(func() {
		s.State = "closed"

		if s.TransferID > 0 {
			if err := p.tc.Stop(s.TransferID); err != nil {
				log.Printf("transfer stop %d: %v", s.TransferID, err)
			}
		}

		if notifyLower {
			domain := sip.DomainPart(s.LowerTo)
			if domain == "" {
				domain = p.cfg.SIPDomain
			}
			uri := fmt.Sprintf("sip:%s@%s", s.ChannelID, domain)
			bye := p.buildRequest("BYE", uri, s.LowerCallID, p.nextCSeq(), s.LowerFrom, s.LowerTo, s.DeviceProto, nil)
			go func() {
				if _, err := p.doRequest(bye, s.DeviceIP, s.DevicePort, s.DeviceProto, 5*time.Second); err != nil {
					log.Printf("send BYE to device %s failed: %v", s.DeviceID, err)
				}
			}()
		}

		if notifyUpper {
			uri := fmt.Sprintf("sip:%s@%s", s.UpperUser, s.UpperDomain)
			bye := p.buildRequest("BYE", uri, s.UpperCallID, p.nextCSeq(), s.UpperTo, s.UpperFrom, s.UpperProto, nil)
			go func() {
				if _, err := p.doRequest(bye, s.UpperIP, s.UpperPort, s.UpperProto, 5*time.Second); err != nil {
					log.Printf("send BYE to upper failed: %v", err)
				}
			}()
		}

		p.removeSession(s)
		log.Printf("session %s closed (%s)", s.UpperCallID, reason)
	})
}
