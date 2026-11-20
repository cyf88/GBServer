package config

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

// UpperConfig 上级平台对接配置
type UpperConfig struct {
	Enable            bool   `json:"enable"`
	ID                string `json:"id"`
	Domain            string `json:"domain"`
	IP                string `json:"ip"`
	Port              int    `json:"port"`
	Transport         string `json:"transport"`
	Password          string `json:"password"`
	Expires           int    `json:"expires"`
	KeepaliveInterval int    `json:"keepalive_interval"`
	CatalogInterval   int    `json:"catalog_interval"`
}

// Config 全局配置
type Config struct {
	SIPID     string `json:"sip_id"`
	SIPDomain string `json:"sip_domain"`
	SIPIP     string `json:"sip_ip"`
	SIPPort   int    `json:"sip_port"`
	Transport string `json:"transport"`
	HTTPPort  int    `json:"http_port"`

	// SIPIPAuto 表示 SIPIP 为空、由程序自动探测；不写入配置文件
	SIPIPAuto bool `json:"-"`

	TransferURL     string `json:"transfer_url"`
	Transcode       bool   `json:"transcode"`
	TranscodeFormat string `json:"transcode_format"`
	SrcProtocol     string `json:"src_protocol"`
	Decrypt         bool   `json:"decrypt"`
	VKEK            string `json:"vkek"`
	FPS             int    `json:"fps"`
	Bitrate         int    `json:"bitrate"`

	Upper                UpperConfig `json:"upper"`
	LowerDefaultPassword string      `json:"lower_default_password"`
}

// Default 返回内置默认配置
func Default() *Config {
	return &Config{
		SIPID:           "34020000002000000001",
		SIPDomain:       "3402000000",
		SIPPort:         5060,
		Transport:       "udp",
		HTTPPort:        8081,
		TransferURL:     "http://127.0.0.1:8080",
		Transcode:       true,
		TranscodeFormat: "h264",
		SrcProtocol:     "tcp",
		FPS:             25,
		Bitrate:         2000000,
		Upper: UpperConfig{
			Port:              5060,
			Transport:         "udp",
			Expires:           3600,
			KeepaliveInterval: 60,
			CatalogInterval:   300,
		},
	}
}

// Load 从 ini 文件加载配置；文件不存在时返回 error（调用方可退回 Default）
func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	cfg := Default()
	vals := map[string]string{}
	section := ""

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		if i := strings.IndexByte(line, ';'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			continue
		}
		i := strings.IndexByte(line, '=')
		if i < 0 {
			continue
		}
		key := strings.TrimSpace(line[:i])
		val := strings.TrimSpace(line[i+1:])
		if section != "" {
			vals[section+"."+key] = val
		} else {
			vals[key] = val
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	// [sip]
	setString(vals, "sip", "id", &cfg.SIPID)
	setString(vals, "sip", "domain", &cfg.SIPDomain)
	setString(vals, "sip", "ip", &cfg.SIPIP)
	setInt(vals, "sip", "port", &cfg.SIPPort)
	setString(vals, "sip", "transport", &cfg.Transport)
	setInt(vals, "sip", "http_port", &cfg.HTTPPort)

	// [transfer]
	setString(vals, "transfer", "http_url", &cfg.TransferURL)
	setBool(vals, "transfer", "transcode", &cfg.Transcode)
	setString(vals, "transfer", "transcode_format", &cfg.TranscodeFormat)
	setString(vals, "transfer", "src_protocol", &cfg.SrcProtocol)
	setBool(vals, "transfer", "decrypt", &cfg.Decrypt)
	setString(vals, "transfer", "vkek", &cfg.VKEK)
	setInt(vals, "transfer", "fps", &cfg.FPS)
	setInt(vals, "transfer", "bitrate", &cfg.Bitrate)

	// [upper]
	setBool(vals, "upper", "enable", &cfg.Upper.Enable)
	setString(vals, "upper", "id", &cfg.Upper.ID)
	setString(vals, "upper", "domain", &cfg.Upper.Domain)
	setString(vals, "upper", "ip", &cfg.Upper.IP)
	setInt(vals, "upper", "port", &cfg.Upper.Port)
	setString(vals, "upper", "transport", &cfg.Upper.Transport)
	setString(vals, "upper", "password", &cfg.Upper.Password)
	setInt(vals, "upper", "expires", &cfg.Upper.Expires)
	setInt(vals, "upper", "keepalive_interval", &cfg.Upper.KeepaliveInterval)
	setInt(vals, "upper", "catalog_interval", &cfg.Upper.CatalogInterval)

	// [lower]
	setString(vals, "lower", "default_password", &cfg.LowerDefaultPassword)

	if cfg.Upper.ID == "" {
		cfg.Upper.ID = cfg.SIPID
	}
	if cfg.Upper.Domain == "" {
		cfg.Upper.Domain = cfg.SIPDomain
	}
	if cfg.Upper.Port <= 0 {
		cfg.Upper.Port = 5060
	}
	if cfg.Upper.Expires <= 0 {
		cfg.Upper.Expires = 3600
	}
	if cfg.TranscodeFormat != "h264" && cfg.TranscodeFormat != "h265" {
		cfg.TranscodeFormat = "h264"
	}
	cfg.SrcProtocol = normalizeTransport(cfg.SrcProtocol)
	return cfg, nil
}

func lookup(vals map[string]string, section, key string) (string, bool) {
	if v, ok := vals[section+"."+key]; ok {
		return v, true
	}
	v, ok := vals[key]
	return v, ok
}

func setString(vals map[string]string, section, key string, dst *string) {
	if v, ok := lookup(vals, section, key); ok {
		*dst = v
	}
}

func setInt(vals map[string]string, section, key string, dst *int) {
	if v, ok := lookup(vals, section, key); ok {
		if n, err := strconv.Atoi(v); err == nil {
			*dst = n
		}
	}
}

func setBool(vals map[string]string, section, key string, dst *bool) {
	if v, ok := lookup(vals, section, key); ok {
		switch strings.ToLower(v) {
		case "1", "true", "yes", "on":
			*dst = true
		case "0", "false", "no", "off":
			*dst = false
		}
	}
}

// DetectLocalIP 探测本机出口 IP；target 可为空（此时取第一个非回环 IPv4）
func DetectLocalIP(target string) string {
	if target != "" {
		if conn, err := net.Dial("udp", net.JoinHostPort(target, "5060")); err == nil {
			ip := ""
			if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok {
				ip = addr.IP.String()
			}
			conn.Close()
			if ip != "" && ip != "0.0.0.0" && ip != "::" {
				return ip
			}
		}
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() {
			continue
		}
		if ip4 := ipnet.IP.To4(); ip4 != nil {
			return ip4.String()
		}
	}
	return ""
}

// Normalize 规范化配置：去空格、补默认值、统一协议名
func Normalize(cfg *Config) {
	cfg.SIPID = strings.TrimSpace(cfg.SIPID)
	cfg.SIPDomain = strings.TrimSpace(cfg.SIPDomain)
	cfg.SIPIP = strings.TrimSpace(cfg.SIPIP)
	cfg.TransferURL = strings.TrimSpace(cfg.TransferURL)
	cfg.VKEK = strings.TrimSpace(cfg.VKEK)
	cfg.LowerDefaultPassword = strings.TrimSpace(cfg.LowerDefaultPassword)

	cfg.Transport = normalizeTransport(cfg.Transport)
	cfg.Upper.Transport = normalizeTransport(cfg.Upper.Transport)
	cfg.SrcProtocol = normalizeTransport(cfg.SrcProtocol)

	cfg.Upper.ID = strings.TrimSpace(cfg.Upper.ID)
	cfg.Upper.Domain = strings.TrimSpace(cfg.Upper.Domain)
	cfg.Upper.IP = strings.TrimSpace(cfg.Upper.IP)
	cfg.Upper.Password = strings.TrimSpace(cfg.Upper.Password)
	if cfg.Upper.ID == "" {
		cfg.Upper.ID = cfg.SIPID
	}
	if cfg.Upper.Domain == "" {
		cfg.Upper.Domain = cfg.SIPDomain
	}
	if cfg.Upper.Port == 0 {
		cfg.Upper.Port = 5060
	}
	if cfg.Upper.Expires <= 0 {
		cfg.Upper.Expires = 3600
	}
	if cfg.Upper.KeepaliveInterval <= 0 {
		cfg.Upper.KeepaliveInterval = 60
	}
	if cfg.Upper.CatalogInterval < 0 {
		cfg.Upper.CatalogInterval = 0
	}

	if cfg.SIPPort == 0 {
		cfg.SIPPort = 5060
	}
	if cfg.HTTPPort == 0 {
		cfg.HTTPPort = 8081
	}
	if cfg.FPS <= 0 {
		cfg.FPS = 25
	}
	if cfg.Bitrate <= 0 {
		cfg.Bitrate = 2000000
	}
	cfg.TranscodeFormat = strings.ToLower(strings.TrimSpace(cfg.TranscodeFormat))
	if cfg.TranscodeFormat != "h264" && cfg.TranscodeFormat != "h265" {
		cfg.TranscodeFormat = "h264"
	}
}

func normalizeTransport(s string) string {
	if strings.EqualFold(strings.TrimSpace(s), "tcp") {
		return "tcp"
	}
	return "udp"
}

// Validate 校验配置，返回第一个错误
func Validate(cfg *Config) error {
	if cfg.SIPID == "" {
		return errors.New("sip.id 不能为空")
	}
	if cfg.SIPDomain == "" {
		return errors.New("sip.domain 不能为空")
	}
	if cfg.SIPPort < 1 || cfg.SIPPort > 65535 {
		return errors.New("sip.port 必须在 1-65535 之间")
	}
	if cfg.HTTPPort < 1 || cfg.HTTPPort > 65535 {
		return errors.New("sip.http_port 必须在 1-65535 之间")
	}
	if cfg.TransferURL == "" {
		return errors.New("transfer.http_url 不能为空")
	}
	if !strings.HasPrefix(cfg.TransferURL, "http://") && !strings.HasPrefix(cfg.TransferURL, "https://") {
		return errors.New("transfer.http_url 必须以 http:// 或 https:// 开头")
	}
	if cfg.TranscodeFormat != "h264" && cfg.TranscodeFormat != "h265" {
		return errors.New("transfer.transcode_format 只能是 h264 或 h265")
	}
	if cfg.SrcProtocol != "tcp" && cfg.SrcProtocol != "udp" {
		return errors.New("transfer.src_protocol 只能是 tcp 或 udp")
	}
	if cfg.FPS <= 0 || cfg.FPS > 120 {
		return errors.New("transfer.fps 必须在 1-120 之间")
	}
	if cfg.Bitrate <= 0 {
		return errors.New("transfer.bitrate 必须大于 0")
	}
	if cfg.Upper.Port < 1 || cfg.Upper.Port > 65535 {
		return errors.New("upper.port 必须在 1-65535 之间")
	}
	if cfg.Upper.Enable {
		if cfg.Upper.IP == "" {
			return errors.New("启用上级对接时 upper.ip 不能为空")
		}
		if cfg.Upper.ID == "" || cfg.Upper.Domain == "" {
			return errors.New("启用上级对接时 upper.id / upper.domain 不能为空")
		}
	}
	if cfg.Upper.Expires <= 0 {
		return errors.New("upper.expires 必须大于 0")
	}
	if cfg.Upper.KeepaliveInterval <= 0 {
		return errors.New("upper.keepalive_interval 必须大于 0")
	}
	return nil
}

// Save 将配置写回 ini 文件（覆盖写，保留标准注释）
func Save(path string, cfg *Config) error {
	var b strings.Builder

	b.WriteString("# GBServer 国标转码平台配置\n")
	b.WriteString("# 说明：key=value 形式，支持 [section] 分节与 # 注释。\n")
	b.WriteString("# 本文件由网页配置页生成，修改后需重启 GBServer 生效。\n\n")

	b.WriteString("[sip]\n")
	b.WriteString("# 本机国标 ID（SIP 用户 ID），上级看到的平台 ID\n")
	fmt.Fprintf(&b, "id = %s\n", cfg.SIPID)
	b.WriteString("# SIP 域（通常为 ID 前 10 位）\n")
	fmt.Fprintf(&b, "domain = %s\n", cfg.SIPDomain)
	b.WriteString("# 本机 SIP 监听 IP，留空则自动探测\n")
	fmt.Fprintf(&b, "ip = %s\n", cfg.SIPIP)
	b.WriteString("# SIP 监听端口（UDP 与 TCP 同时监听）\n")
	fmt.Fprintf(&b, "port = %d\n", cfg.SIPPort)
	b.WriteString("# 向上级发起请求时优先使用的传输协议：udp / tcp\n")
	fmt.Fprintf(&b, "transport = %s\n", cfg.Transport)
	b.WriteString("# HTTP 管理接口端口\n")
	fmt.Fprintf(&b, "http_port = %d\n\n", cfg.HTTPPort)

	b.WriteString("[transfer]\n")
	b.WriteString("# 转码服务 TransferServer 的 HTTP 控制地址\n")
	fmt.Fprintf(&b, "http_url = %s\n", cfg.TransferURL)
	b.WriteString("# 是否开启转码\n")
	fmt.Fprintf(&b, "transcode = %t\n", cfg.Transcode)
	b.WriteString("# 转码输出格式：h264 / h265\n")
	fmt.Fprintf(&b, "transcode_format = %s\n", cfg.TranscodeFormat)
	b.WriteString("# 拉流协议（下级 -> 转码服务）：tcp / udp\n")
	fmt.Fprintf(&b, "src_protocol = %s\n", cfg.SrcProtocol)
	b.WriteString("# 是否解密（SVAC 流）\n")
	fmt.Fprintf(&b, "decrypt = %t\n", cfg.Decrypt)
	b.WriteString("# 解密密钥（16 字节的 base64）\n")
	fmt.Fprintf(&b, "vkek = %s\n", cfg.VKEK)
	b.WriteString("# 转码帧率与码率\n")
	fmt.Fprintf(&b, "fps = %d\n", cfg.FPS)
	fmt.Fprintf(&b, "bitrate = %d\n\n", cfg.Bitrate)

	b.WriteString("[upper]\n")
	b.WriteString("# 是否主动对接上级平台\n")
	fmt.Fprintf(&b, "enable = %t\n", cfg.Upper.Enable)
	b.WriteString("# 本机在上级平台的注册 ID（留空则使用 sip.id）\n")
	fmt.Fprintf(&b, "id = %s\n", cfg.Upper.ID)
	b.WriteString("# 上级平台域（留空则使用 sip.domain）\n")
	fmt.Fprintf(&b, "domain = %s\n", cfg.Upper.Domain)
	b.WriteString("# 上级平台 SIP 地址与端口\n")
	fmt.Fprintf(&b, "ip = %s\n", cfg.Upper.IP)
	fmt.Fprintf(&b, "port = %d\n", cfg.Upper.Port)
	b.WriteString("# 向上级发送请求的传输协议：udp / tcp\n")
	fmt.Fprintf(&b, "transport = %s\n", cfg.Upper.Transport)
	b.WriteString("# 注册密码\n")
	fmt.Fprintf(&b, "password = %s\n", cfg.Upper.Password)
	b.WriteString("# 注册有效期（秒）\n")
	fmt.Fprintf(&b, "expires = %d\n", cfg.Upper.Expires)
	b.WriteString("# 心跳间隔（秒）\n")
	fmt.Fprintf(&b, "keepalive_interval = %d\n", cfg.Upper.KeepaliveInterval)
	b.WriteString("# 目录上报间隔（秒），0 表示不上报\n")
	fmt.Fprintf(&b, "catalog_interval = %d\n\n", cfg.Upper.CatalogInterval)

	b.WriteString("[lower]\n")
	b.WriteString("# 下级平台注册密码，留空表示不鉴权\n")
	fmt.Fprintf(&b, "default_password = %s\n", cfg.LowerDefaultPassword)

	return os.WriteFile(path, []byte(b.String()), 0644)
}
