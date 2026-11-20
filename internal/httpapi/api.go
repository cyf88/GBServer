package httpapi

import (
	_ "embed"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"gbserver/internal/config"
	"gbserver/internal/gb"
)

//go:embed index.html
var indexHTML []byte

// Server HTTP 管理接口与网页配置页
type Server struct {
	platform *gb.Platform
	cfgPath  string
}

// New 创建管理接口，cfgPath 为配置文件的读写路径
func New(p *gb.Platform, cfgPath string) *Server {
	return &Server{platform: p, cfgPath: cfgPath}
}

// Handler 注册全部路由
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/devices", s.handleDevices)
	mux.HandleFunc("/api/channels", s.handleChannels)
	mux.HandleFunc("/api/sessions", s.handleSessions)
	mux.HandleFunc("/api/session/stop", s.handleSessionStop)
	mux.HandleFunc("/api/catalog/refresh", s.handleCatalogRefresh)
	mux.HandleFunc("/api/upper/catalog", s.handleUpperCatalog)
	mux.HandleFunc("/api/verify/start", s.handleVerifyStart)
	mux.HandleFunc("/api/verify/stop", s.handleVerifyStop)
	mux.HandleFunc("/api/verify/sessions", s.handleVerifySessions)
	mux.HandleFunc("/api/verify/certs", s.handleVerifyCerts)
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/restart", s.handleRestart)
	mux.HandleFunc("/", s.handleIndex)
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"ok": false, "error": "not found"})
		return
	}
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]interface{}{"ok": false, "error": "method not allowed"})
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(indexHTML)
}

// handleConfig 读取 / 保存配置
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cur := *s.platform.Config()
		if cur.SIPIPAuto {
			cur.SIPIP = ""
		}
		writeJSON(w, http.StatusOK, &cur)
	case http.MethodPost, http.MethodPut:
		var cfg config.Config
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "配置格式错误: " + err.Error()})
			return
		}
		config.Normalize(&cfg)
		if err := config.Validate(&cfg); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		if err := config.Save(s.cfgPath, &cfg); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"ok": false, "error": "写入配置失败: " + err.Error()})
			return
		}
		log.Printf("config saved to %s", s.cfgPath)
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "status": "saved", "need_restart": true})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]interface{}{"ok": false, "error": "method not allowed"})
	}
}

// handleRestart 保存后重启进程，使配置生效
func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]interface{}{"ok": false, "error": "method not allowed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "status": "restarting"})
	go func() {
		time.Sleep(500 * time.Millisecond)
		if err := restartProcess(); err != nil {
			log.Printf("restart failed: %v", err)
		}
	}()
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]interface{}{"ok": false, "error": "method not allowed"})
		return
	}
	writeJSON(w, http.StatusOK, s.platform.Status())
}

func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]interface{}{"ok": false, "error": "method not allowed"})
		return
	}
	devices := s.platform.Devices()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":    true,
		"count": len(devices),
		"list":  devices,
	})
}

func (s *Server) handleChannels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]interface{}{"ok": false, "error": "method not allowed"})
		return
	}
	channels := s.platform.Channels()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":    true,
		"count": len(channels),
		"list":  channels,
	})
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]interface{}{"ok": false, "error": "method not allowed"})
		return
	}
	sessions := s.platform.Sessions()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":    true,
		"count": len(sessions),
		"list":  sessions,
	})
}

func (s *Server) handleSessionStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]interface{}{"ok": false, "error": "method not allowed"})
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "missing id"})
		return
	}
	if !s.platform.StopSession(id) {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"ok": false, "error": "session not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "status": "stopped"})
}

func (s *Server) handleCatalogRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]interface{}{"ok": false, "error": "method not allowed"})
		return
	}
	device := r.URL.Query().Get("device")
	if device == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "missing device"})
		return
	}
	if !s.platform.RefreshCatalog(device) {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"ok": false, "error": "device not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "status": "querying"})
}

func (s *Server) handleUpperCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]interface{}{"ok": false, "error": "method not allowed"})
		return
	}
	if err := s.platform.PushCatalog(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "status": "sent"})
}

// handleVerifyStart 只拉流验签，不转码、不推流
func (s *Server) handleVerifyStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]interface{}{"ok": false, "error": "method not allowed"})
		return
	}
	channel := r.URL.Query().Get("channel")
	if channel == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "missing channel"})
		return
	}
	if !s.platform.ChannelExists(channel) {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"ok": false, "error": "channel not found"})
		return
	}
	sess, err := s.platform.StartVerify(channel, r.URL.Query().Get("cert_id"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":          true,
		"id":          sess.CallID,
		"channel_id":  sess.ChannelID,
		"cert_id":     sess.CertID,
		"transfer_id": sess.TransferID,
		"recv_port":   sess.RecvPort,
	})
}

func (s *Server) handleVerifyStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]interface{}{"ok": false, "error": "method not allowed"})
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "missing id"})
		return
	}
	if !s.platform.StopVerify(id) {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"ok": false, "error": "verify session not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "status": "stopped"})
}

func (s *Server) handleVerifySessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]interface{}{"ok": false, "error": "method not allowed"})
		return
	}
	sessions := s.platform.VerifySessions()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":    true,
		"count": len(sessions),
		"list":  sessions,
	})
}

func (s *Server) handleVerifyCerts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]interface{}{"ok": false, "error": "method not allowed"})
		return
	}
	certs, err := s.platform.VerifyCerts()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":    true,
		"count": len(certs),
		"list":  certs,
	})
}
