package gb

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gbserver/internal/config"
	"gbserver/internal/sip"
)

// Platform 国标转码平台总编排：SIP UAS/UAC、注册、目录、点播、转码控制
type Platform struct {
	cfg       *config.Config
	transport *sip.Transport
	reg       *Registry
	tc        *TransferClient
	tm        *sip.TransactionManager
	upper     *Upper

	mu       sync.Mutex
	sessions map[string]*Session
	invites  map[string]bool
	verify   map[string]*VerifySession

	cseq    uint32
	sn      uint32
	started time.Time
}

// StatusInfo 管理接口状态信息
type StatusInfo struct {
	SIPID           string    `json:"sip_id"`
	SIPDomain       string    `json:"sip_domain"`
	SIPIP           string    `json:"sip_ip"`
	SIPPort         int       `json:"sip_port"`
	HTTPPort        int       `json:"http_port"`
	TransferURL     string    `json:"transfer_url"`
	Transcode       bool      `json:"transcode"`
	TranscodeFormat string    `json:"transcode_format"`
	UpperEnabled    bool      `json:"upper_enabled"`
	UpperRegistered bool      `json:"upper_registered"`
	UpperAddr       string    `json:"upper_addr"`
	DeviceCount     int       `json:"device_count"`
	ChannelCount    int       `json:"channel_count"`
	SessionCount    int       `json:"session_count"`
	StartedAt       time.Time `json:"started_at"`
	Uptime          string    `json:"uptime"`
}

// SessionInfo 管理接口会话信息
type SessionInfo struct {
	ID         string    `json:"id"`
	ChannelID  string    `json:"channel_id"`
	DeviceID   string    `json:"device_id"`
	DeviceAddr string    `json:"device_addr"`
	UpperAddr  string    `json:"upper_addr"`
	State      string    `json:"state"`
	SSRC       uint32    `json:"ssrc"`
	TransferID int       `json:"transfer_id"`
	RecvPort   int       `json:"recv_port"`
	Encrypted  *bool     `json:"encrypted"`
	Signed     *bool     `json:"signed"`
	CreatedAt  time.Time `json:"created_at"`
}

// New 创建平台并监听 SIP 端口
func New(cfg *config.Config) (*Platform, error) {
	p := &Platform{
		cfg:      cfg,
		reg:      NewRegistry(),
		tc:       NewTransferClient(cfg),
		tm:       sip.NewTransactionManager(),
		sessions: map[string]*Session{},
		invites:  map[string]bool{},
		verify:   map[string]*VerifySession{},
		started:  time.Now(),
	}
	p.upper = NewUpper(p)

	tr, err := sip.NewTransport(cfg.SIPPort, p.onMessage)
	if err != nil {
		return nil, fmt.Errorf("listen sip %d: %w", cfg.SIPPort, err)
	}
	p.transport = tr
	return p, nil
}

// Start 启动上级注册/心跳/目录上报
func (p *Platform) Start() { p.upper.Start() }

// Close 关闭所有资源
func (p *Platform) Close() {
	p.upper.Stop()
	p.transport.Close()
}

func (p *Platform) nextCSeq() int { return int(atomic.AddUint32(&p.cseq, 1)) }
func (p *Platform) nextSN() int   { return int(atomic.AddUint32(&p.sn, 1)) }

func (p *Platform) newCallID() string {
	return sip.RandomHex(16) + "@" + p.cfg.SIPIP
}

func randomSSRC() uint32 {
	return 0x10000000 | uint32(time.Now().UnixNano()&0x00FFFFFF)
}

// buildRequest 构造带标准头域的 SIP 请求
func (p *Platform) buildRequest(method, uri, callID string, cseq int, from, to, viaProto string, body []byte) *sip.Message {
	proto := "UDP"
	if viaProto == "tcp" {
		proto = "TCP"
	}
	m := sip.NewRequest(method, uri)
	m.Headers = append(m.Headers, sip.Header{
		Name:  "Via",
		Value: fmt.Sprintf("SIP/2.0/%s %s:%d;rport;branch=%s", proto, p.cfg.SIPIP, p.cfg.SIPPort, sip.NewBranch()),
	})
	m.Headers = append(m.Headers, sip.Header{Name: "From", Value: from})
	m.Headers = append(m.Headers, sip.Header{Name: "To", Value: to})
	m.Headers = append(m.Headers, sip.Header{Name: "Call-ID", Value: callID})
	m.Headers = append(m.Headers, sip.Header{Name: "CSeq", Value: fmt.Sprintf("%d %s", cseq, method)})
	m.Headers = append(m.Headers, sip.Header{Name: "Max-Forwards", Value: "70"})
	m.Headers = append(m.Headers, sip.Header{Name: "User-Agent", Value: "GBServer"})
	m.Headers = append(m.Headers, sip.Header{
		Name:  "Contact",
		Value: fmt.Sprintf("<sip:%s@%s:%d>", p.cfg.SIPID, p.cfg.SIPIP, p.cfg.SIPPort),
	})
	if body != nil {
		m.Body = body
	}
	return m
}

// doRequest 发送请求并等待最终响应（带简单重传）
func (p *Platform) doRequest(req *sip.Message, ip string, port int, proto string, timeout time.Duration) (*sip.Message, error) {
	callID := req.CallID()
	cseq, method := req.CSeq()
	if method == "" {
		method = req.Method
	}

	ch := p.tm.New(callID, cseq, method)
	defer p.tm.Remove(callID, cseq, method)

	deadline := time.Now().Add(timeout)
	interval := 500 * time.Millisecond

	for {
		if err := p.transport.SendTo(ip, port, proto, req); err != nil {
			return nil, err
		}

		wait := interval
		if remain := time.Until(deadline); remain < wait {
			wait = remain
		}
		if wait <= 0 {
			return nil, fmt.Errorf("sip %s timeout", method)
		}

		select {
		case resp := <-ch:
			if resp.StatusCode >= 200 {
				return resp, nil
			}
		case <-time.After(wait):
		}

		if time.Now().After(deadline) {
			return nil, fmt.Errorf("sip %s timeout", method)
		}
		if interval < 4*time.Second {
			interval *= 2
		}
	}
}

func (p *Platform) replyMsg(peer sip.Peer, msg *sip.Message) {
	if err := p.transport.Send(peer, msg); err != nil {
		log.Printf("send response to %s failed: %v", peer.String(), err)
	}
}

func (p *Platform) replyError(req *sip.Message, peer sip.Peer, code int, reason string) {
	p.replyMsg(peer, sip.NewResponse(req, code, reason))
}

// onMessage 收到 SIP 消息的总入口
func (p *Platform) onMessage(msg *sip.Message, peer sip.Peer) {
	if msg.IsResponse() {
		p.tm.Handle(msg)
		return
	}
	switch msg.Method {
	case "REGISTER":
		p.handleRegister(msg, peer)
	case "MESSAGE":
		p.handleMessage(msg, peer)
	case "INVITE":
		p.handleInvite(msg, peer)
	case "ACK":
		// 已建立会话，忽略
	case "BYE":
		p.handleBye(msg, peer)
	case "OPTIONS":
		p.replyMsg(peer, sip.NewResponse(msg, 200, "OK"))
	case "CANCEL":
		p.replyMsg(peer, sip.NewResponse(msg, 200, "OK"))
	default:
		p.replyMsg(peer, sip.NewResponse(msg, 405, "Method Not Allowed"))
	}
}

// handleRegister 处理下级平台注册
func (p *Platform) handleRegister(msg *sip.Message, peer sip.Peer) {
	deviceID := sip.UserPart(msg.Get("From"))
	if deviceID == "" {
		deviceID = sip.UserPart(msg.Get("To"))
	}
	if deviceID == "" {
		p.replyError(msg, peer, 400, "Bad Request")
		return
	}

	if p.cfg.LowerDefaultPassword != "" {
		auth := msg.Get("Authorization")
		if auth == "" || !sip.CheckDigest(auth, "REGISTER", p.cfg.LowerDefaultPassword) {
			resp := sip.NewResponse(msg, 401, "Unauthorized")
			resp.Set("WWW-Authenticate", fmt.Sprintf(`Digest realm="%s", nonce="%s", algorithm=MD5`, p.cfg.SIPDomain, sip.RandomHex(16)))
			p.replyMsg(peer, resp)
			return
		}
	}

	expires := 3600
	if v := msg.Get("Expires"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			expires = n
		}
	} else if c := msg.Get("Contact"); c != "" {
		expires = contactExpires(c, expires)
	}

	log.Printf("REGISTER %s from %s expires=%d", deviceID, peer.String(), expires)

	if expires == 0 {
		p.reg.RemoveDevice(deviceID)
	} else {
		domain := sip.DomainPart(msg.Get("From"))
		if domain == "" {
			domain = p.cfg.SIPDomain
		}
		p.reg.UpsertDevice(deviceID, domain, peer.IP, peer.Port, peer.Proto)
		go p.queryCatalog(deviceID)
	}

	resp := sip.NewResponse(msg, 200, "OK")
	resp.Set("Expires", strconv.Itoa(expires))
	resp.Set("Date", time.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT"))
	p.replyMsg(peer, resp)
}

func contactExpires(c string, def int) int {
	lower := strings.ToLower(c)
	i := strings.Index(lower, "expires=")
	if i < 0 {
		return def
	}
	s := c[i+len("expires="):]
	if j := strings.IndexAny(s, ";>, "); j >= 0 {
		s = s[:j]
	}
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return def
}

// queryCatalog 向下级设备发起目录查询
func (p *Platform) queryCatalog(deviceID string) {
	d := p.reg.Device(deviceID)
	if d == nil {
		return
	}
	body := BuildCatalogQuery(deviceID, p.nextSN())
	uri := fmt.Sprintf("sip:%s@%s", deviceID, d.Domain)
	from := fmt.Sprintf("<sip:%s@%s>;tag=%s", p.cfg.SIPID, p.cfg.SIPDomain, sip.RandomTag())
	to := fmt.Sprintf("<sip:%s@%s>", deviceID, d.Domain)

	req := p.buildRequest("MESSAGE", uri, p.newCallID(), p.nextCSeq(), from, to, d.Proto, body)
	req.Set("Content-Type", "Application/MANSCDP+xml")

	if _, err := p.doRequest(req, d.IP, d.Port, d.Proto, 5*time.Second); err != nil {
		log.Printf("catalog query to %s failed: %v", deviceID, err)
	}
}

// handleMessage 处理 MANSCDP 消息（心跳 / 目录 / 设备信息等）
func (p *Platform) handleMessage(msg *sip.Message, peer sip.Peer) {
	p.replyMsg(peer, sip.NewResponse(msg, 200, "OK"))

	env, err := ParseEnvelope(msg.Body)
	if err != nil {
		log.Printf("bad MANSCDP message from %s: %v", peer.String(), err)
		return
	}

	switch env.CmdType {
	case "Keepalive":
		if !p.reg.Touch(env.DeviceID) && env.DeviceID != "" {
			// 服务重启后注册表为空，用心跳来源地址补注册，避免等待下级重新 REGISTER
			domain := sip.DomainPart(msg.Get("From"))
			if domain == "" {
				domain = p.cfg.SIPDomain
			}
			p.reg.UpsertDevice(env.DeviceID, domain, peer.IP, peer.Port, peer.Proto)
			log.Printf("device %s registered by keepalive from %s", env.DeviceID, peer.String())
			go p.queryCatalog(env.DeviceID)
		}
	case "Catalog":
		if env.XMLName.Local == "Query" {
			sn := env.SN
			if sn == 0 {
				sn = p.nextSN()
			}
			deviceID := env.DeviceID
			if deviceID == "" {
				deviceID = p.upper.DeviceID()
			}
			go p.upper.SendCatalogTo(peer.IP, peer.Port, peer.Proto, sn, deviceID)
			return
		}
		cr, err := ParseCatalogResponse(msg.Body)
		if err != nil {
			log.Printf("bad catalog response from %s: %v", peer.String(), err)
			return
		}
		channels := make([]*Channel, 0, len(cr.DeviceList.Items))
		for _, it := range cr.DeviceList.Items {
			channels = append(channels, &Channel{
				ID:           it.DeviceID,
				DeviceID:     cr.DeviceID,
				Name:         it.Name,
				Manufacturer: it.Manufacturer,
				Model:        it.Model,
				Owner:        it.Owner,
				CivilCode:    it.CivilCode,
				Address:      it.Address,
				ParentID:     it.ParentID,
				Status:       it.Status,
			})
		}
		p.reg.SetChannels(cr.DeviceID, channels, cr.SN)
		log.Printf("catalog from %s: %d channels", cr.DeviceID, len(channels))
	case "DeviceInfo":
		if env.XMLName.Local == "Query" {
			deviceID := env.DeviceID
			if deviceID == "" {
				deviceID = p.upper.DeviceID()
			}
			body := BuildDeviceInfoResponse(deviceID, env.SN, "GBServer", "GBServer", "GBServer", "1.0", p.reg.ChannelCount())
			go p.sendMessageTo(peer.IP, peer.Port, peer.Proto, deviceID, body)
		}
	case "DeviceStatus":
		if env.XMLName.Local == "Query" {
			body := BuildSimpleResponse(env.DeviceID, env.SN, "DeviceStatus")
			go p.sendMessageTo(peer.IP, peer.Port, peer.Proto, env.DeviceID, body)
		}
	}
}

// sendMessageTo 向指定地址发送 MANSCDP MESSAGE
func (p *Platform) sendMessageTo(ip string, port int, proto string, targetID string, body []byte) {
	domain := p.cfg.SIPDomain
	uri := fmt.Sprintf("sip:%s@%s", targetID, domain)
	from := fmt.Sprintf("<sip:%s@%s>;tag=%s", p.cfg.SIPID, p.cfg.SIPDomain, sip.RandomTag())
	to := fmt.Sprintf("<sip:%s@%s>", targetID, domain)

	req := p.buildRequest("MESSAGE", uri, p.newCallID(), p.nextCSeq(), from, to, proto, body)
	req.Set("Content-Type", "Application/MANSCDP+xml")

	if _, err := p.doRequest(req, ip, port, proto, 5*time.Second); err != nil {
		log.Printf("send message to %s:%d failed: %v", ip, port, err)
	}
}

func (p *Platform) addSession(s *Session) {
	p.mu.Lock()
	p.sessions[s.UpperCallID] = s
	p.mu.Unlock()
}

func (p *Platform) removeSession(s *Session) {
	p.mu.Lock()
	if cur, ok := p.sessions[s.UpperCallID]; ok && cur == s {
		delete(p.sessions, s.UpperCallID)
	}
	p.mu.Unlock()
}

func (p *Platform) sessionByUpper(callID string) *Session {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sessions[callID]
}

func (p *Platform) sessionByLower(callID string) *Session {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, s := range p.sessions {
		if s.LowerCallID == callID {
			return s
		}
	}
	return nil
}

// StopSession 按上级 Call-ID 或通道 ID 停止会话
func (p *Platform) StopSession(id string) bool {
	p.mu.Lock()
	var target *Session
	for _, s := range p.sessions {
		if s.UpperCallID == id || s.ChannelID == id {
			target = s
			break
		}
	}
	p.mu.Unlock()
	if target == nil {
		return false
	}
	p.closeSession(target, "api stop", true, true)
	return true
}

// Status 返回平台运行状态
func (p *Platform) Status() *StatusInfo {
	p.mu.Lock()
	sessionCount := len(p.sessions)
	p.mu.Unlock()

	upperAddr := ""
	if p.cfg.Upper.Enable {
		upperAddr = fmt.Sprintf("%s:%d", p.cfg.Upper.IP, p.cfg.Upper.Port)
	}
	return &StatusInfo{
		SIPID:           p.cfg.SIPID,
		SIPDomain:       p.cfg.SIPDomain,
		SIPIP:           p.cfg.SIPIP,
		SIPPort:         p.cfg.SIPPort,
		HTTPPort:        p.cfg.HTTPPort,
		TransferURL:     p.cfg.TransferURL,
		Transcode:       p.cfg.Transcode,
		TranscodeFormat: p.cfg.TranscodeFormat,
		UpperEnabled:    p.cfg.Upper.Enable,
		UpperRegistered: p.upper.IsRegistered(),
		UpperAddr:       upperAddr,
		DeviceCount:     p.reg.DeviceCount(),
		ChannelCount:    p.reg.ChannelCount(),
		SessionCount:    sessionCount,
		StartedAt:       p.started,
		Uptime:          time.Since(p.started).Round(time.Second).String(),
	}
}

// Config 返回当前生效配置（只读）
func (p *Platform) Config() *config.Config { return p.cfg }

// Devices 返回注册设备列表
func (p *Platform) Devices() []*Device { return p.reg.Devices() }

// Channels 返回通道列表
func (p *Platform) Channels() []*Channel { return p.reg.AllChannels() }

// Sessions 返回会话列表（含原始码流加密/签名标志）
func (p *Platform) Sessions() []SessionInfo {
	p.mu.Lock()
	out := make([]SessionInfo, 0, len(p.sessions))
	transferIDs := make([]int, 0, len(p.sessions))
	for _, s := range p.sessions {
		out = append(out, SessionInfo{
			ID:         s.UpperCallID,
			ChannelID:  s.ChannelID,
			DeviceID:   s.DeviceID,
			DeviceAddr: fmt.Sprintf("%s:%d/%s", s.DeviceIP, s.DevicePort, s.DeviceProto),
			UpperAddr:  fmt.Sprintf("%s:%d/%s", s.UpperIP, s.UpperPort, s.UpperProto),
			State:      s.State,
			SSRC:       s.SSRC,
			TransferID: s.TransferID,
			RecvPort:   s.RecvPort,
			CreatedAt:  s.Created,
		})
		transferIDs = append(transferIDs, s.TransferID)
	}
	p.mu.Unlock()

	// 锁外查询 TransferServer，避免网络请求阻塞会话表
	for i := range out {
		if transferIDs[i] <= 0 {
			continue
		}
		if info, err := p.tc.SecurityInfo(transferIDs[i]); err == nil {
			out[i].Encrypted = info.Encrypted
			out[i].Signed = info.Signed
		}
	}
	return out
}

// RefreshCatalog 触发指定下级设备目录刷新
func (p *Platform) RefreshCatalog(deviceID string) bool {
	if p.reg.Device(deviceID) == nil {
		return false
	}
	go p.queryCatalog(deviceID)
	return true
}

// PushCatalog 立即向上级平台推送目录
func (p *Platform) PushCatalog() error {
	if !p.cfg.Upper.Enable {
		return fmt.Errorf("upper platform not enabled")
	}
	p.upper.SendCatalogTo(p.cfg.Upper.IP, p.cfg.Upper.Port, p.upper.Proto(), p.nextSN(), p.upper.DeviceID())
	return nil
}
