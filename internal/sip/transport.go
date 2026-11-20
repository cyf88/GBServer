package sip

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Peer 描述一个 SIP 对端；TCP 时 Conn 为该连接
type Peer struct {
	IP    string
	Port  int
	Proto string
	Conn  net.Conn
}

func (p Peer) String() string {
	return fmt.Sprintf("%s:%d/%s", p.IP, p.Port, p.Proto)
}

// Transport 同时监听 UDP 与 TCP 的 SIP 传输层
type Transport struct {
	udp     *net.UDPConn
	tcp     net.Listener
	handler func(*Message, Peer)

	mu       sync.Mutex
	tcpConns map[string]net.Conn
	writeMu  sync.Mutex
}

// NewTransport 在 0.0.0.0:port 上同时监听 UDP 与 TCP
func NewTransport(port int, handler func(*Message, Peer)) (*Transport, error) {
	t := &Transport{
		handler:  handler,
		tcpConns: map[string]net.Conn{},
	}

	udpAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		return nil, err
	}
	udpConn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return nil, err
	}
	t.udp = udpConn

	ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		udpConn.Close()
		return nil, err
	}
	t.tcp = ln

	go t.readUDP()
	go t.acceptTCP()
	return t, nil
}

func (t *Transport) readUDP() {
	buf := make([]byte, 65535)
	for {
		n, addr, err := t.udp.ReadFromUDP(buf)
		if err != nil {
			return
		}
		data := append([]byte(nil), buf[:n]...)
		msg, err := Parse(data)
		if err != nil {
			continue
		}
		peer := Peer{IP: addr.IP.String(), Port: addr.Port, Proto: "udp"}
		go t.handler(msg, peer)
	}
}

func (t *Transport) acceptTCP() {
	for {
		conn, err := t.tcp.Accept()
		if err != nil {
			return
		}
		go t.readTCP(conn)
	}
}

func (t *Transport) readTCP(conn net.Conn) {
	ip, port := "", 0
	if ta, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
		ip = ta.IP.String()
		port = ta.Port
	}
	key := peerKey(ip, port)

	defer func() {
		conn.Close()
		t.mu.Lock()
		if cur, ok := t.tcpConns[key]; ok && cur == conn {
			delete(t.tcpConns, key)
		}
		t.mu.Unlock()
	}()

	r := bufio.NewReaderSize(conn, 64*1024)
	for {
		data, err := readSIPFrame(r)
		if err != nil {
			return
		}
		msg, err := Parse(data)
		if err != nil {
			continue
		}
		t.mu.Lock()
		t.tcpConns[key] = conn
		t.mu.Unlock()
		go t.handler(msg, Peer{IP: ip, Port: port, Proto: "tcp", Conn: conn})
	}
}

// readSIPFrame 按 Content-Length 从 TCP 流中读取一条完整 SIP 消息
func readSIPFrame(r *bufio.Reader) ([]byte, error) {
	var head strings.Builder
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		head.WriteString(line)
		if line == "\r\n" || line == "\n" {
			break
		}
		if head.Len() > 64*1024 {
			return nil, errors.New("sip header too large")
		}
	}

	data := []byte(head.String())
	msg, err := Parse(data)
	if err != nil {
		return nil, err
	}

	length := 0
	if v := msg.Get("Content-Length"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			length = n
		}
	}
	if length > 0 {
		body := make([]byte, length)
		if _, err := io.ReadFull(r, body); err != nil {
			return nil, err
		}
		data = append(data, body...)
	}
	return data, nil
}

func peerKey(ip string, port int) string {
	return fmt.Sprintf("%s:%d", ip, port)
}

// Send 向指定对端发送消息
func (t *Transport) Send(peer Peer, msg *Message) error {
	data := msg.Bytes()
	if peer.Proto == "tcp" {
		conn := peer.Conn
		if conn == nil {
			c, err := t.getTCPConn(peer.IP, peer.Port)
			if err != nil {
				return err
			}
			conn = c
		}
		t.writeMu.Lock()
		_, err := conn.Write(data)
		t.writeMu.Unlock()
		return err
	}

	addr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(peer.IP, strconv.Itoa(peer.Port)))
	if err != nil {
		return err
	}
	_, err = t.udp.WriteToUDP(data, addr)
	return err
}

// SendTo 向 ip:port 发送消息（TCP 时复用或新建连接）
func (t *Transport) SendTo(ip string, port int, proto string, msg *Message) error {
	return t.Send(Peer{IP: ip, Port: port, Proto: proto}, msg)
}

func (t *Transport) getTCPConn(ip string, port int) (net.Conn, error) {
	key := peerKey(ip, port)
	t.mu.Lock()
	if c, ok := t.tcpConns[key]; ok {
		t.mu.Unlock()
		return c, nil
	}
	t.mu.Unlock()

	conn, err := net.DialTimeout("tcp", net.JoinHostPort(ip, strconv.Itoa(port)), 5*time.Second)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	t.tcpConns[key] = conn
	t.mu.Unlock()
	return conn, nil
}

// Close 关闭监听
func (t *Transport) Close() {
	if t.udp != nil {
		t.udp.Close()
	}
	if t.tcp != nil {
		t.tcp.Close()
	}
	t.mu.Lock()
	for _, c := range t.tcpConns {
		c.Close()
	}
	t.tcpConns = map[string]net.Conn{}
	t.mu.Unlock()
}
