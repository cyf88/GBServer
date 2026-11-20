package sip

import (
	"fmt"
	"strconv"
	"strings"
)

// SDPMedia 一路媒体描述
type SDPMedia struct {
	Kind      string
	Port      int
	Proto     string
	Payload   int
	Encoding  string
	ClockRate int
	Setup     string
	ConnIP    string
	SendOnly  bool
	RecvOnly  bool
}

// SDP 会话描述
type SDP struct {
	OriginUser string
	Name       string
	ConnIP     string
	SSRC       uint32
	Medias     []*SDPMedia
}

// ParseSDP 解析 SDP 文本
func ParseSDP(data []byte) (*SDP, error) {
	s := &SDP{}
	var media *SDPMedia

	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if len(line) < 2 || line[1] != '=' {
			continue
		}
		typ := line[0]
		val := line[2:]
		switch typ {
		case 'o':
			f := strings.Fields(val)
			if len(f) > 0 {
				s.OriginUser = f[0]
			}
		case 's':
			s.Name = val
		case 'c':
			ip := connIP(val)
			if media != nil {
				media.ConnIP = ip
			} else {
				s.ConnIP = ip
			}
		case 'y':
			if n, err := strconv.ParseUint(strings.TrimSpace(val), 10, 32); err == nil {
				s.SSRC = uint32(n)
			}
		case 'm':
			media = parseMedia(val)
			if media != nil {
				s.Medias = append(s.Medias, media)
			}
		case 'a':
			if media != nil {
				parseAttr(media, val)
			}
		}
	}

	if len(s.Medias) == 0 {
		return nil, fmt.Errorf("sdp has no media")
	}
	return s, nil
}

func connIP(v string) string {
	f := strings.Fields(v)
	if len(f) >= 3 {
		return f[2]
	}
	return ""
}

func parseMedia(v string) *SDPMedia {
	f := strings.Fields(v)
	if len(f) < 4 {
		return nil
	}
	portStr := f[1]
	if i := strings.IndexByte(portStr, '/'); i >= 0 {
		portStr = portStr[:i]
	}
	port, _ := strconv.Atoi(portStr)

	m := &SDPMedia{
		Kind:      strings.ToLower(f[0]),
		Port:      port,
		Proto:     strings.ToUpper(f[2]),
		Payload:   96,
		Encoding:  "PS",
		ClockRate: 90000,
	}
	if n, err := strconv.Atoi(f[3]); err == nil {
		m.Payload = n
	}
	return m
}

func parseAttr(m *SDPMedia, v string) {
	switch {
	case strings.HasPrefix(v, "rtpmap:"):
		f := strings.Fields(strings.TrimPrefix(v, "rtpmap:"))
		if len(f) < 2 {
			return
		}
		if p, err := strconv.Atoi(f[0]); err == nil {
			m.Payload = p
		}
		if i := strings.IndexByte(f[1], '/'); i >= 0 {
			m.Encoding = f[1][:i]
			if cr, err := strconv.Atoi(f[1][i+1:]); err == nil {
				m.ClockRate = cr
			}
		} else {
			m.Encoding = f[1]
		}
	case strings.HasPrefix(v, "setup:"):
		m.Setup = strings.ToLower(strings.TrimPrefix(v, "setup:"))
	case v == "sendonly":
		m.SendOnly = true
	case v == "recvonly":
		m.RecvOnly = true
	}
}

// Video 返回视频媒体（无视频时取第一路）
func (s *SDP) Video() *SDPMedia {
	for _, m := range s.Medias {
		if m.Kind == "video" {
			return m
		}
	}
	if len(s.Medias) > 0 {
		return s.Medias[0]
	}
	return nil
}

// ReceiveIP 返回该媒体实际接收地址
func (m *SDPMedia) ReceiveIP(s *SDP) string {
	if m.ConnIP != "" {
		return m.ConnIP
	}
	return s.ConnIP
}

// IsTCP 是否为 TCP 承载
func (m *SDPMedia) IsTCP() bool {
	return strings.Contains(m.Proto, "TCP")
}

// Bytes 序列化 SDP
func (s *SDP) Bytes() []byte {
	var b strings.Builder
	b.WriteString("v=0\r\n")

	user := s.OriginUser
	if user == "" {
		user = "-"
	}
	b.WriteString(fmt.Sprintf("o=%s 0 0 IN IP4 %s\r\n", user, s.ConnIP))

	name := s.Name
	if name == "" {
		name = "Play"
	}
	b.WriteString("s=" + name + "\r\n")
	b.WriteString("c=IN IP4 " + s.ConnIP + "\r\n")
	b.WriteString("t=0 0\r\n")

	for _, m := range s.Medias {
		b.WriteString(fmt.Sprintf("m=%s %d %s %d\r\n", m.Kind, m.Port, m.Proto, m.Payload))
		if m.SendOnly {
			b.WriteString("a=sendonly\r\n")
		}
		if m.RecvOnly {
			b.WriteString("a=recvonly\r\n")
		}
		enc := m.Encoding
		if enc == "" {
			enc = "PS"
		}
		cr := m.ClockRate
		if cr == 0 {
			cr = 90000
		}
		b.WriteString(fmt.Sprintf("a=rtpmap:%d %s/%d\r\n", m.Payload, enc, cr))
		if m.Setup != "" {
			b.WriteString("a=setup:" + m.Setup + "\r\n")
		}
		if strings.Contains(m.Proto, "TCP") {
			b.WriteString("a=connection:new\r\n")
		}
	}

	if s.SSRC != 0 {
		b.WriteString(fmt.Sprintf("y=%010d\r\n", s.SSRC))
	}
	return []byte(b.String())
}
