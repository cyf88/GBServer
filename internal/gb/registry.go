package gb

import (
	"sort"
	"sync"
	"time"
)

// Channel 设备通道
type Channel struct {
	ID           string `json:"id"`
	DeviceID     string `json:"device_id"`
	Name         string `json:"name"`
	Manufacturer string `json:"manufacturer"`
	Model        string `json:"model"`
	Owner        string `json:"owner"`
	CivilCode    string `json:"civil_code"`
	Address      string `json:"address"`
	ParentID     string `json:"parent_id"`
	Status       string `json:"status"`
}

// Device 下级国标平台
type Device struct {
	ID         string              `json:"id"`
	Domain     string              `json:"domain"`
	IP         string              `json:"ip"`
	Port       int                 `json:"port"`
	Proto      string              `json:"proto"`
	RegisterAt time.Time           `json:"register_at"`
	LastSeen   time.Time           `json:"last_seen"`
	Channels   map[string]*Channel `json:"-"`
}

// Registry 下级设备与通道注册表
type Registry struct {
	mu      sync.RWMutex
	devices map[string]*Device
}

// NewRegistry 创建注册表
func NewRegistry() *Registry {
	return &Registry{devices: map[string]*Device{}}
}

// UpsertDevice 新增或更新设备注册信息
func (r *Registry) UpsertDevice(id, domain, ip string, port int, proto string) *Device {
	r.mu.Lock()
	defer r.mu.Unlock()

	d, ok := r.devices[id]
	if !ok {
		d = &Device{ID: id, Channels: map[string]*Channel{}, RegisterAt: time.Now()}
		r.devices[id] = d
	}
	if domain != "" {
		d.Domain = domain
	}
	d.IP = ip
	d.Port = port
	d.Proto = proto
	d.LastSeen = time.Now()
	return d
}

// Touch 更新设备最近活跃时间
func (r *Registry) Touch(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.devices[id]
	if !ok {
		return false
	}
	d.LastSeen = time.Now()
	return true
}

// RemoveDevice 移除设备及其通道
func (r *Registry) RemoveDevice(id string) {
	r.mu.Lock()
	delete(r.devices, id)
	r.mu.Unlock()
}

// Device 返回设备副本，避免并发读写
func (r *Registry) Device(id string) *Device {
	r.mu.RLock()
	defer r.mu.RUnlock()

	d, ok := r.devices[id]
	if !ok {
		return nil
	}
	cp := *d
	cp.Channels = make(map[string]*Channel, len(d.Channels))
	for k, v := range d.Channels {
		ch := *v
		cp.Channels[k] = &ch
	}
	return &cp
}

// Devices 返回全部设备副本（按 ID 排序）
func (r *Registry) Devices() []*Device {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]*Device, 0, len(r.devices))
	for _, d := range r.devices {
		cp := *d
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// SetChannels 用目录响应替换设备的通道集合
func (r *Registry) SetChannels(deviceID string, channels []*Channel, sn int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	d, ok := r.devices[deviceID]
	if !ok {
		d = &Device{ID: deviceID, Channels: map[string]*Channel{}, RegisterAt: time.Now(), LastSeen: time.Now()}
		r.devices[deviceID] = d
	}
	d.Channels = make(map[string]*Channel, len(channels))
	for _, ch := range channels {
		d.Channels[ch.ID] = ch
	}
}

// Channel 按通道 ID 查找通道及其所属设备
func (r *Registry) Channel(channelID string) (*Channel, *Device) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, d := range r.devices {
		if ch, ok := d.Channels[channelID]; ok {
			chCopy := *ch
			dCopy := *d
			return &chCopy, &dCopy
		}
	}
	return nil, nil
}

// AllChannels 返回全部通道副本（按设备 ID、通道 ID 排序）
func (r *Registry) AllChannels() []*Channel {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var out []*Channel
	for _, d := range r.devices {
		for _, ch := range d.Channels {
			cp := *ch
			if cp.DeviceID == "" {
				cp.DeviceID = d.ID
			}
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].DeviceID == out[j].DeviceID {
			return out[i].ID < out[j].ID
		}
		return out[i].DeviceID < out[j].DeviceID
	})
	return out
}

// ChannelCount 通道总数
func (r *Registry) ChannelCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	n := 0
	for _, d := range r.devices {
		n += len(d.Channels)
	}
	return n
}

// DeviceCount 设备总数
func (r *Registry) DeviceCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.devices)
}

// CatalogItems 生成上报给上级的目录项
func (r *Registry) CatalogItems() []CatalogItem {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var items []CatalogItem
	for _, d := range r.devices {
		for _, ch := range d.Channels {
			status := ch.Status
			if status == "" {
				status = "ON"
			}
			items = append(items, CatalogItem{
				DeviceID:     ch.ID,
				Name:         ch.Name,
				Manufacturer: ch.Manufacturer,
				Model:        ch.Model,
				Owner:        ch.Owner,
				CivilCode:    ch.CivilCode,
				Address:      ch.Address,
				ParentID:     ch.ParentID,
				RegisterWay:  1,
				Status:       status,
			})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].DeviceID < items[j].DeviceID })
	return items
}
