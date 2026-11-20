package gb

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"gbserver/internal/config"
)

// StartResult /transfer/start 成功后的结果
type StartResult struct {
	ID       int
	RecvPort int
}

type startRequest struct {
	SrcProtocol     string `json:"src_protocol"`
	DstIP           string `json:"dst_ip"`
	DstPort         int    `json:"dst_port"`
	OutProtocol     string `json:"out_protocol"`
	Decrypt         bool   `json:"decrypt"`
	Transcode       bool   `json:"transcode"`
	TranscodeFormat string `json:"transcode_format,omitempty"`
	VKEK            string `json:"vkek,omitempty"`
	VKEKVersion     string `json:"vkek_version,omitempty"`
	FPS             int    `json:"fps,omitempty"`
	Bitrate         int    `json:"bitrate,omitempty"`
	SSRC            uint32 `json:"ssrc,omitempty"`
}

type transferResponse struct {
	OK       bool   `json:"ok"`
	ID       int    `json:"id"`
	RecvPort int    `json:"recv_port"`
	Error    string `json:"error"`
}

// TransferClient 转码服务 HTTP 控制客户端
type TransferClient struct {
	baseURL         string
	httpc           *http.Client
	transcode       bool
	transcodeFormat string
	srcProtocol     string
	decrypt         bool
	vkek            string
	fps             int
	bitrate         int
}

// NewTransferClient 根据配置创建客户端
func NewTransferClient(cfg *config.Config) *TransferClient {
	return &TransferClient{
		baseURL:         strings.TrimRight(cfg.TransferURL, "/"),
		httpc:           &http.Client{Timeout: 10 * time.Second},
		transcode:       cfg.Transcode,
		transcodeFormat: cfg.TranscodeFormat,
		srcProtocol:     cfg.SrcProtocol,
		decrypt:         cfg.Decrypt,
		vkek:            cfg.VKEK,
		fps:             cfg.FPS,
		bitrate:         cfg.Bitrate,
	}
}

// Start 启动一路转发：按配置的 src_protocol 收流（下级推流到 recv_port），
// 输出到 dstIP:dstPort，并按配置开启解密/转码。
func (c *TransferClient) Start(dstIP string, dstPort int, outProto string, ssrc uint32) (*StartResult, error) {
	req := startRequest{
		SrcProtocol:     c.srcProtocol,
		DstIP:           dstIP,
		DstPort:         dstPort,
		OutProtocol:     outProto,
		Decrypt:         c.decrypt,
		Transcode:       c.transcode,
		TranscodeFormat: c.transcodeFormat,
		FPS:             c.fps,
		Bitrate:         c.bitrate,
		SSRC:            ssrc,
	}
	if c.decrypt && c.vkek != "" {
		req.VKEK = c.vkek
		req.VKEKVersion = "1.0"
	}

	body, err := json.Marshal(&req)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpc.Post(c.baseURL+"/transfer/start", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("transfer start: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var tr transferResponse
	if err := json.Unmarshal(data, &tr); err != nil {
		return nil, fmt.Errorf("invalid transfer response: %s", strings.TrimSpace(string(data)))
	}
	if !tr.OK {
		if tr.Error != "" {
			return nil, errors.New(tr.Error)
		}
		return nil, fmt.Errorf("transfer start failed (http %d)", resp.StatusCode)
	}
	if tr.RecvPort <= 0 {
		return nil, fmt.Errorf("transfer returned invalid recv_port")
	}
	return &StartResult{ID: tr.ID, RecvPort: tr.RecvPort}, nil
}

// StartVerify 启动一路“只收流 + 验签”的转发：不转码、不推流、不落盘。
// certID 为空时由 TransferServer 回退使用 channelID 查找证书。
func (c *TransferClient) StartVerify(channelID, certID string) (*StartResult, error) {
	req := struct {
		SrcProtocol string `json:"src_protocol"`
		RecvFile    string `json:"recv_file"`
		OutFile     string `json:"out_file"`
		Decrypt     bool   `json:"decrypt"`
		Transcode   bool   `json:"transcode"`
		Verify      bool   `json:"verify"`
		ChannelID   string `json:"channel_id"`
		CertID      string `json:"cert_id,omitempty"`
	}{
		SrcProtocol: c.srcProtocol,
		RecvFile:    "/dev/null",
		OutFile:     "/dev/null",
		Decrypt:     false,
		Transcode:   false,
		Verify:      true,
		ChannelID:   channelID,
		CertID:      certID,
	}

	body, err := json.Marshal(&req)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpc.Post(c.baseURL+"/transfer/save", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("transfer save: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var tr transferResponse
	if err := json.Unmarshal(data, &tr); err != nil {
		return nil, fmt.Errorf("invalid transfer response: %s", strings.TrimSpace(string(data)))
	}
	if !tr.OK {
		if tr.Error != "" {
			return nil, errors.New(tr.Error)
		}
		return nil, fmt.Errorf("transfer save failed (http %d)", resp.StatusCode)
	}
	if tr.RecvPort <= 0 {
		return nil, fmt.Errorf("transfer returned invalid recv_port")
	}
	return &StartResult{ID: tr.ID, RecvPort: tr.RecvPort}, nil
}

// VerifyResult 验签结果（来自 TransferServer /transfer/verify）
type VerifyResult struct {
	Enabled  bool    `json:"enabled"`
	Result   *string `json:"result"`
	Detail   string  `json:"detail"`
	Success  uint64  `json:"success"`
	Fail     uint64  `json:"fail"`
	LastTime string  `json:"last_time"`
}

// VerifyResult 查询指定转发路的验签结果
func (c *TransferClient) VerifyResult(id int) (*VerifyResult, error) {
	resp, err := c.httpc.Get(fmt.Sprintf("%s/transfer/verify?id=%d", c.baseURL, id))
	if err != nil {
		return nil, fmt.Errorf("transfer verify: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("transfer %d not found", id)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var r struct {
		OK     bool         `json:"ok"`
		Verify VerifyResult `json:"verify"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("invalid verify response: %s", strings.TrimSpace(string(data)))
	}
	if !r.OK {
		return nil, fmt.Errorf("verify query failed")
	}
	return &r.Verify, nil
}

// Certs 查询 TransferServer 可用的验签证书 ID（certs/<id>_sign.cer）
func (c *TransferClient) Certs() ([]string, error) {
	resp, err := c.httpc.Get(c.baseURL + "/transfer/certs")
	if err != nil {
		return nil, fmt.Errorf("transfer certs: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var r struct {
		OK    bool     `json:"ok"`
		Certs []string `json:"certs"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("invalid certs response: %s", strings.TrimSpace(string(data)))
	}
	sort.Strings(r.Certs)
	return r.Certs, nil
}

// Stop 停止指定转发路
func (c *TransferClient) Stop(id int) error {
	url := fmt.Sprintf("%s/transfer/stop?id=%d", c.baseURL, id)
	resp, err := c.httpc.Post(url, "application/json", nil)
	if err != nil {
		return fmt.Errorf("transfer stop: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("transfer %d not found", id)
	}
	return nil
}

// SecurityInfo 原始码流安全标志（nil 表示未知）
type SecurityInfo struct {
	Encrypted *bool
	Signed    *bool
}

// SecurityInfo 查询指定转发路的原始码流加密/签名标志
func (c *TransferClient) SecurityInfo(id int) (*SecurityInfo, error) {
	resp, err := c.httpc.Get(c.baseURL + "/transfer/transcode")
	if err != nil {
		return nil, fmt.Errorf("transfer transcode: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var tr struct {
		OK        bool `json:"ok"`
		Transfers []struct {
			ID        int   `json:"id"`
			Encrypted *bool `json:"encrypted"`
			Signed    *bool `json:"signed"`
		} `json:"transfers"`
	}
	if err := json.Unmarshal(data, &tr); err != nil {
		return nil, fmt.Errorf("invalid transcode response: %s", strings.TrimSpace(string(data)))
	}
	for _, item := range tr.Transfers {
		if item.ID == id {
			return &SecurityInfo{Encrypted: item.Encrypted, Signed: item.Signed}, nil
		}
	}
	return nil, fmt.Errorf("transfer %d not found", id)
}
