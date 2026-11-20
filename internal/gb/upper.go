package gb

import (
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"

	"gbserver/internal/config"
	"gbserver/internal/sip"
)

// Upper 上级平台对接：主动注册、心跳、目录上报，并响应上级目录查询
type Upper struct {
	p   *Platform
	cfg config.UpperConfig

	mu         sync.Mutex
	registered bool
	realm      string
	nonce      string
	qop        string
	cseq       int

	stopCh   chan struct{}
	stopOnce sync.Once
}

// NewUpper 创建上级对接模块
func NewUpper(p *Platform) *Upper {
	return &Upper{p: p, cfg: p.cfg.Upper, stopCh: make(chan struct{})}
}

// Start 启动注册 / 心跳 / 目录上报循环
func (u *Upper) Start() {
	if !u.cfg.Enable || u.cfg.IP == "" {
		return
	}
	go u.registerLoop()
	go u.keepaliveLoop()
	go u.catalogLoop()
}

// Stop 停止所有循环
func (u *Upper) Stop() {
	u.stopOnce.Do(func() { close(u.stopCh) })
}

// DeviceID 本机在上级平台的注册 ID
func (u *Upper) DeviceID() string {
	if u.cfg.ID != "" {
		return u.cfg.ID
	}
	return u.p.cfg.SIPID
}

// Domain 上级平台域
func (u *Upper) Domain() string {
	if u.cfg.Domain != "" {
		return u.cfg.Domain
	}
	return u.p.cfg.SIPDomain
}

// Proto 向上级发送请求使用的传输协议
func (u *Upper) Proto() string {
	if u.cfg.Transport == "tcp" {
		return "tcp"
	}
	return "udp"
}

// IsRegistered 是否已注册成功
func (u *Upper) IsRegistered() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.registered
}

func (u *Upper) setRegistered(v bool) {
	u.mu.Lock()
	if u.registered != v {
		u.registered = v
	}
	u.mu.Unlock()
}

func (u *Upper) registerLoop() {
	for {
		err := u.register()
		wait := time.Duration(u.cfg.Expires-30) * time.Second
		if wait < 30*time.Second {
			wait = 30 * time.Second
		}
		if err != nil {
			log.Printf("upper register failed: %v", err)
			wait = 30 * time.Second
		} else {
			log.Printf("upper registered to %s:%d (%s)", u.cfg.IP, u.cfg.Port, u.Proto())
		}

		select {
		case <-u.stopCh:
			return
		case <-time.After(wait):
		}
	}
}

func (u *Upper) register() error {
	for attempt := 0; attempt < 2; attempt++ {
		resp, err := u.doRegister()
		if err != nil {
			u.setRegistered(false)
			return err
		}
		switch resp.StatusCode {
		case 200:
			u.setRegistered(true)
			return nil
		case 401:
			realm, nonce, qop := sip.ParseChallenge(resp.Get("WWW-Authenticate"))
			u.mu.Lock()
			u.realm = realm
			u.nonce = nonce
			u.qop = qop
			u.mu.Unlock()
		default:
			u.setRegistered(false)
			return fmt.Errorf("status %d", resp.StatusCode)
		}
	}
	u.setRegistered(false)
	return fmt.Errorf("unauthorized")
}

func (u *Upper) doRegister() (*sip.Message, error) {
	domain := u.Domain()
	uri := "sip:" + domain
	from := fmt.Sprintf("<sip:%s@%s>;tag=%s", u.DeviceID(), domain, sip.RandomTag())
	to := fmt.Sprintf("<sip:%s@%s>", u.DeviceID(), domain)

	u.mu.Lock()
	u.cseq++
	cseq := u.cseq
	realm, nonce, qop := u.realm, u.nonce, u.qop
	u.mu.Unlock()

	req := u.p.buildRequest("REGISTER", uri, u.p.newCallID(), cseq, from, to, u.Proto(), nil)
	req.Set("Expires", strconv.Itoa(u.cfg.Expires))
	if nonce != "" && u.cfg.Password != "" {
		req.Set("Authorization", sip.BuildDigest(u.DeviceID(), realm, nonce, uri, "REGISTER", u.cfg.Password, qop != ""))
	}
	return u.p.doRequest(req, u.cfg.IP, u.cfg.Port, u.Proto(), 5*time.Second)
}

func (u *Upper) keepaliveLoop() {
	interval := time.Duration(u.cfg.KeepaliveInterval) * time.Second
	if interval <= 0 {
		interval = 60 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-u.stopCh:
			return
		case <-ticker.C:
			if !u.IsRegistered() {
				continue
			}
			if err := u.SendKeepalive(); err != nil {
				log.Printf("upper keepalive failed: %v", err)
			}
		}
	}
}

// SendKeepalive 向上级发送一次心跳
func (u *Upper) SendKeepalive() error {
	body := BuildKeepalive(u.DeviceID(), u.p.nextSN())
	return u.sendMessage(u.cfg.IP, u.cfg.Port, u.Proto(), body)
}

func (u *Upper) catalogLoop() {
	if u.cfg.CatalogInterval <= 0 {
		return
	}
	ticker := time.NewTicker(time.Duration(u.cfg.CatalogInterval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-u.stopCh:
			return
		case <-ticker.C:
			if !u.IsRegistered() {
				continue
			}
			u.SendCatalogTo(u.cfg.IP, u.cfg.Port, u.Proto(), u.p.nextSN(), u.DeviceID())
		}
	}
}

// SendCatalogTo 向指定地址推送目录（用于周期上报与响应上级查询）
func (u *Upper) SendCatalogTo(ip string, port int, proto string, sn int, deviceID string) {
	items := u.p.reg.CatalogItems()
	body := BuildCatalogResponse(deviceID, sn, items)
	if err := u.sendMessage(ip, port, proto, body); err != nil {
		log.Printf("upper catalog push to %s:%d failed: %v", ip, port, err)
	}
}

func (u *Upper) sendMessage(ip string, port int, proto string, body []byte) error {
	domain := u.Domain()
	uri := fmt.Sprintf("sip:%s@%s", u.DeviceID(), domain)
	from := fmt.Sprintf("<sip:%s@%s>;tag=%s", u.DeviceID(), domain, sip.RandomTag())
	to := fmt.Sprintf("<sip:%s@%s>", u.DeviceID(), domain)

	req := u.p.buildRequest("MESSAGE", uri, u.p.newCallID(), u.p.nextCSeq(), from, to, proto, body)
	req.Set("Content-Type", "Application/MANSCDP+xml")
	_, err := u.p.doRequest(req, ip, port, proto, 5*time.Second)
	return err
}
