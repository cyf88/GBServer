package sip

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Header SIP 头域
type Header struct {
	Name  string
	Value string
}

// Message SIP 消息（请求或响应）
type Message struct {
	Method     string
	URI        string
	Version    string
	StatusCode int
	Reason     string
	Headers    []Header
	Body       []byte
}

// 简写头域 -> 完整头域
var compactHeaders = map[string]string{
	"i": "Call-ID",
	"m": "Contact",
	"e": "Content-Encoding",
	"l": "Content-Length",
	"c": "Content-Type",
	"f": "From",
	"s": "Subject",
	"k": "Supported",
	"t": "To",
	"v": "Via",
	"o": "Event",
	"u": "Allow-Events",
	"r": "Refer-To",
	"b": "Referred-By",
	"x": "Session-Expires",
}

var statusText = map[int]string{
	100: "Trying",
	180: "Ringing",
	200: "OK",
	202: "Accepted",
	400: "Bad Request",
	401: "Unauthorized",
	403: "Forbidden",
	404: "Not Found",
	405: "Method Not Allowed",
	408: "Request Timeout",
	480: "Temporarily Unavailable",
	486: "Busy Here",
	487: "Request Terminated",
	488: "Not Acceptable Here",
	500: "Server Internal Error",
	503: "Service Unavailable",
	504: "Server Time-out",
	603: "Decline",
}

func canonicalHeader(name string) string {
	l := strings.ToLower(strings.TrimSpace(name))
	if full, ok := compactHeaders[l]; ok {
		return full
	}
	return name
}

// StatusText 返回状态码的默认原因短语
func StatusText(code int) string {
	if s, ok := statusText[code]; ok {
		return s
	}
	return "Unknown"
}

// RandomHex 生成 n 字节随机数的十六进制字符串
func RandomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		for i := range b {
			b[i] = byte(i*31 + 7)
		}
	}
	return hex.EncodeToString(b)
}

// RandomTag 生成 From/To 的 tag
func RandomTag() string { return RandomHex(6) }

// NewBranch 生成 Via branch
func NewBranch() string { return "z9hG4bK" + RandomHex(8) }

// NewRequest 构造一条 SIP 请求
func NewRequest(method, uri string) *Message {
	return &Message{Method: strings.ToUpper(method), URI: uri, Version: "SIP/2.0"}
}

// NewResponse 基于请求构造响应（复制 Via / From / To / Call-ID / CSeq）
func NewResponse(req *Message, code int, reason string) *Message {
	if reason == "" {
		reason = StatusText(code)
	}
	m := &Message{StatusCode: code, Reason: reason, Version: "SIP/2.0"}
	for _, v := range req.GetAll("Via") {
		m.Headers = append(m.Headers, Header{Name: "Via", Value: v})
	}
	from := req.Get("From")
	to := req.Get("To")
	if to != "" && Tag(to) == "" {
		to += ";tag=" + RandomTag()
	}
	m.Headers = append(m.Headers, Header{Name: "From", Value: from})
	m.Headers = append(m.Headers, Header{Name: "To", Value: to})
	m.Headers = append(m.Headers, Header{Name: "Call-ID", Value: req.CallID()})
	m.Headers = append(m.Headers, Header{Name: "CSeq", Value: req.Get("CSeq")})
	return m
}

// IsResponse 是否为响应消息
func (m *Message) IsResponse() bool { return m.StatusCode != 0 }

// Get 取首个同名头域（忽略大小写与简写）
func (m *Message) Get(name string) string {
	target := strings.ToLower(canonicalHeader(name))
	for _, h := range m.Headers {
		if strings.ToLower(canonicalHeader(h.Name)) == target {
			return h.Value
		}
	}
	return ""
}

// GetAll 取全部同名头域
func (m *Message) GetAll(name string) []string {
	target := strings.ToLower(canonicalHeader(name))
	var out []string
	for _, h := range m.Headers {
		if strings.ToLower(canonicalHeader(h.Name)) == target {
			out = append(out, h.Value)
		}
	}
	return out
}

// Set 替换同名头域（只保留一个）
func (m *Message) Set(name, value string) {
	name = canonicalHeader(name)
	target := strings.ToLower(name)
	found := false
	out := make([]Header, 0, len(m.Headers)+1)
	for _, h := range m.Headers {
		if strings.ToLower(canonicalHeader(h.Name)) == target {
			if !found {
				out = append(out, Header{Name: name, Value: value})
				found = true
			}
			continue
		}
		out = append(out, h)
	}
	if !found {
		out = append(out, Header{Name: name, Value: value})
	}
	m.Headers = out
}

// CallID 取 Call-ID
func (m *Message) CallID() string { return strings.TrimSpace(m.Get("Call-ID")) }

// CSeq 解析 CSeq，返回序号与方法
func (m *Message) CSeq() (int, string) {
	f := strings.Fields(m.Get("CSeq"))
	if len(f) < 2 {
		return 0, ""
	}
	n, _ := strconv.Atoi(f[0])
	return n, strings.ToUpper(f[1])
}

// UserPart 从 From/To 头域中提取用户部分
func UserPart(v string) string {
	if i := strings.IndexByte(v, '<'); i >= 0 {
		if j := strings.IndexByte(v[i:], '>'); j >= 0 {
			v = v[i+1 : i+j]
		}
	}
	v = strings.TrimSpace(v)
	if k := strings.Index(v, "sips:"); k >= 0 {
		v = v[k+5:]
	} else if k := strings.Index(v, "sip:"); k >= 0 {
		v = v[k+4:]
	}
	if k := strings.IndexAny(v, "@;"); k >= 0 {
		v = v[:k]
	}
	return strings.TrimSpace(v)
}

// DomainPart 从 From/To 头域中提取域部分
func DomainPart(v string) string {
	if i := strings.IndexByte(v, '<'); i >= 0 {
		if j := strings.IndexByte(v[i:], '>'); j >= 0 {
			v = v[i+1 : i+j]
		}
	}
	v = strings.TrimSpace(v)
	if k := strings.Index(v, "sips:"); k >= 0 {
		v = v[k+5:]
	} else if k := strings.Index(v, "sip:"); k >= 0 {
		v = v[k+4:]
	}
	if k := strings.IndexByte(v, '@'); k >= 0 {
		v = v[k+1:]
	}
	if k := strings.IndexAny(v, ";>:"); k >= 0 {
		v = v[:k]
	}
	return strings.TrimSpace(v)
}

// Tag 从 From/To 头域中提取 tag
func Tag(v string) string {
	for _, part := range strings.Split(v, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(strings.ToLower(part), "tag=") {
			return strings.TrimSpace(part[4:])
		}
	}
	return ""
}

// Parse 解析一条完整的 SIP 消息（需包含消息头与可选的消息体）
func Parse(data []byte) (*Message, error) {
	data = bytes.TrimLeft(data, "\r\n")
	if len(data) == 0 {
		return nil, errors.New("empty sip message")
	}

	head := data
	var body []byte
	if idx := bytes.Index(data, []byte("\r\n\r\n")); idx >= 0 {
		head = data[:idx]
		body = data[idx+4:]
	} else if idx := bytes.Index(data, []byte("\n\n")); idx >= 0 {
		head = data[:idx]
		body = data[idx+2:]
	}

	text := strings.ReplaceAll(string(head), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		return nil, errors.New("empty start line")
	}

	m := &Message{Version: "SIP/2.0"}
	first := strings.TrimSpace(lines[0])
	if strings.HasPrefix(strings.ToUpper(first), "SIP/2.0") {
		f := strings.Fields(first)
		if len(f) < 2 {
			return nil, fmt.Errorf("invalid status line %q", first)
		}
		m.StatusCode, _ = strconv.Atoi(f[1])
		if len(f) > 2 {
			m.Reason = strings.Join(f[2:], " ")
		}
	} else {
		f := strings.Fields(first)
		if len(f) < 3 {
			return nil, fmt.Errorf("invalid request line %q", first)
		}
		m.Method = strings.ToUpper(f[0])
		m.URI = f[1]
		m.Version = f[2]
	}

	for _, line := range lines[1:] {
		if line == "" {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			if n := len(m.Headers); n > 0 {
				m.Headers[n-1].Value += " " + strings.TrimSpace(line)
			}
			continue
		}
		i := strings.IndexByte(line, ':')
		if i <= 0 {
			continue
		}
		m.Headers = append(m.Headers, Header{
			Name:  strings.TrimSpace(line[:i]),
			Value: strings.TrimSpace(line[i+1:]),
		})
	}
	m.Body = body
	return m, nil
}

// Bytes 序列化消息，自动补齐 Content-Length
func (m *Message) Bytes() []byte {
	var b strings.Builder
	if m.Method != "" {
		version := m.Version
		if version == "" {
			version = "SIP/2.0"
		}
		uri := m.URI
		if uri == "" {
			uri = "sip:invalid"
		}
		b.WriteString(m.Method + " " + uri + " " + version + "\r\n")
	} else {
		reason := m.Reason
		if reason == "" {
			reason = StatusText(m.StatusCode)
		}
		b.WriteString(fmt.Sprintf("SIP/2.0 %d %s\r\n", m.StatusCode, reason))
	}

	for _, h := range m.Headers {
		if strings.EqualFold(canonicalHeader(h.Name), "Content-Length") {
			continue
		}
		b.WriteString(h.Name + ": " + h.Value + "\r\n")
	}
	b.WriteString("Content-Length: " + strconv.Itoa(len(m.Body)) + "\r\n\r\n")
	return append([]byte(b.String()), m.Body...)
}
