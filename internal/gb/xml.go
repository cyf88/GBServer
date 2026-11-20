package gb

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strings"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// Envelope 用于快速识别 MANSCDP 消息中的命令类型与根元素名
type Envelope struct {
	XMLName  xml.Name
	CmdType  string `xml:"CmdType"`
	SN       int    `xml:"SN"`
	DeviceID string `xml:"DeviceID"`
}

// Query 查询请求
type Query struct {
	XMLName  xml.Name `xml:"Query"`
	CmdType  string   `xml:"CmdType"`
	SN       int      `xml:"SN"`
	DeviceID string   `xml:"DeviceID"`
}

// Notify 通知（心跳等）
type Notify struct {
	XMLName  xml.Name `xml:"Notify"`
	CmdType  string   `xml:"CmdType"`
	SN       int      `xml:"SN"`
	DeviceID string   `xml:"DeviceID"`
	Status   string   `xml:"Status,omitempty"`
}

// SimpleResponse 简单响应
type SimpleResponse struct {
	XMLName  xml.Name `xml:"Response"`
	CmdType  string   `xml:"CmdType"`
	SN       int      `xml:"SN"`
	DeviceID string   `xml:"DeviceID"`
}

// CatalogItem 目录项
type CatalogItem struct {
	DeviceID     string `xml:"DeviceID"`
	Name         string `xml:"Name"`
	Manufacturer string `xml:"Manufacturer"`
	Model        string `xml:"Model"`
	Owner        string `xml:"Owner"`
	CivilCode    string `xml:"CivilCode"`
	Address      string `xml:"Address"`
	Parental     int    `xml:"Parental"`
	ParentID     string `xml:"ParentID"`
	SafetyWay    int    `xml:"SafetyWay"`
	RegisterWay  int    `xml:"RegisterWay"`
	Secrecy      int    `xml:"Secrecy"`
	Status       string `xml:"Status"`
}

// CatalogDeviceList 目录列表
type CatalogDeviceList struct {
	Num   int           `xml:"Num,attr"`
	Items []CatalogItem `xml:"Item"`
}

// CatalogResponse 目录响应
type CatalogResponse struct {
	XMLName    xml.Name          `xml:"Response"`
	CmdType    string            `xml:"CmdType"`
	SN         int               `xml:"SN"`
	DeviceID   string            `xml:"DeviceID"`
	SumNum     int               `xml:"SumNum"`
	DeviceList CatalogDeviceList `xml:"DeviceList"`
}

// DeviceInfoResponse 设备信息响应
type DeviceInfoResponse struct {
	XMLName      xml.Name `xml:"Response"`
	CmdType      string   `xml:"CmdType"`
	SN           int      `xml:"SN"`
	DeviceID     string   `xml:"DeviceID"`
	Result       string   `xml:"Result"`
	DeviceName   string   `xml:"DeviceName"`
	Manufacturer string   `xml:"Manufacturer"`
	Model        string   `xml:"Model"`
	Firmware     string   `xml:"Firmware"`
	Channel      int      `xml:"Channel"`
}

func marshal(v interface{}) []byte {
	b, err := xml.Marshal(v)
	if err != nil {
		return nil
	}
	return append([]byte(xml.Header), b...)
}

// charsetReader 支持下级常用的 GB2312 / GBK / GB18030 报文
func charsetReader(charset string, input io.Reader) (io.Reader, error) {
	switch strings.ToLower(strings.TrimSpace(charset)) {
	case "gb2312", "gbk", "gb18030", "x-gbk":
		return simplifiedchinese.GB18030.NewDecoder().Reader(input), nil
	default:
		return input, nil
	}
}

func unmarshal(data []byte, v interface{}) error {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.CharsetReader = charsetReader
	return dec.Decode(v)
}

// ParseEnvelope 解析报文头信息（命令类型 / SN / DeviceID）
func ParseEnvelope(data []byte) (*Envelope, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty xml body")
	}
	var env Envelope
	if err := unmarshal(data, &env); err != nil {
		return nil, err
	}
	return &env, nil
}

// ParseCatalogResponse 解析下级上报的目录响应
func ParseCatalogResponse(data []byte) (*CatalogResponse, error) {
	var cr CatalogResponse
	if err := unmarshal(data, &cr); err != nil {
		return nil, err
	}
	return &cr, nil
}

// BuildKeepalive 构造心跳 Notify
func BuildKeepalive(deviceID string, sn int) []byte {
	return marshal(&Notify{CmdType: "Keepalive", SN: sn, DeviceID: deviceID, Status: "OK"})
}

// BuildCatalogQuery 构造目录查询
func BuildCatalogQuery(deviceID string, sn int) []byte {
	return marshal(&Query{CmdType: "Catalog", SN: sn, DeviceID: deviceID})
}

// BuildCatalogResponse 构造目录响应
func BuildCatalogResponse(deviceID string, sn int, items []CatalogItem) []byte {
	list := CatalogDeviceList{Num: len(items), Items: items}
	return marshal(&CatalogResponse{
		CmdType:    "Catalog",
		SN:         sn,
		DeviceID:   deviceID,
		SumNum:     len(items),
		DeviceList: list,
	})
}

// BuildDeviceInfoResponse 构造设备信息响应
func BuildDeviceInfoResponse(deviceID string, sn int, name, manufacturer, model, firmware string, channels int) []byte {
	return marshal(&DeviceInfoResponse{
		CmdType:      "DeviceInfo",
		SN:           sn,
		DeviceID:     deviceID,
		Result:       "OK",
		DeviceName:   name,
		Manufacturer: manufacturer,
		Model:        model,
		Firmware:     firmware,
		Channel:      channels,
	})
}

// BuildSimpleResponse 构造简单响应（如 DeviceStatus 应答）
func BuildSimpleResponse(deviceID string, sn int, cmdType string) []byte {
	return marshal(&SimpleResponse{CmdType: cmdType, SN: sn, DeviceID: deviceID})
}
