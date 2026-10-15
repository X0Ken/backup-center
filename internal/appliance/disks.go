package appliance

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

type blockDevice struct {
	Name        string        `json:"name"`
	Type        string        `json:"type"`
	Size        uint64        `json:"size"`
	Model       string        `json:"model"`
	Serial      string        `json:"serial"`
	Transport   string        `json:"tran"`
	Rotational  bool          `json:"rota"`
	Mountpoints []string      `json:"mountpoints"`
	Filesystem  string        `json:"fstype"`
	Children    []blockDevice `json:"children"`
}
type diskInfo struct {
	Name        string   `json:"name"`
	Model       string   `json:"model"`
	Serial      string   `json:"serial"`
	Size        uint64   `json:"size"`
	Transport   string   `json:"transport"`
	Media       string   `json:"media"`
	Mountpoints []string `json:"mountpoints"`
	PoolState   string   `json:"pool_state"`
	Role        string   `json:"role"`
}

func parseDisks(raw, status string) ([]diskInfo, error) {
	var data struct {
		Devices []blockDevice `json:"blockdevices"`
	}
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return nil, fmt.Errorf("无法解析磁盘信息: %w", err)
	}
	states := map[string]string{}
	for _, line := range strings.Split(status, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && strings.HasPrefix(f[0], "/dev/") {
			states[filepath.Base(f[0])] = f[1]
		}
	}
	disks := []diskInfo{}
	for _, device := range data.Devices {
		if device.Type != "disk" || strings.HasPrefix(filepath.Base(device.Name), "zd") {
			continue
		}
		d := diskInfo{Name: device.Name, Model: strings.TrimSpace(device.Model), Serial: strings.TrimSpace(device.Serial), Size: device.Size, Transport: device.Transport, Media: "SSD", Mountpoints: []string{}, Role: "其他磁盘"}
		if device.Rotational {
			d.Media = "HDD"
		}
		var visit func(blockDevice)
		visit = func(b blockDevice) {
			if state := states[filepath.Base(b.Name)]; state != "" {
				d.PoolState = state
				d.Role = "备份数据盘"
			}
			for _, m := range b.Mountpoints {
				if m != "" {
					d.Mountpoints = append(d.Mountpoints, m)
					if m == "/" {
						d.Role = "系统盘"
					}
				}
			}
			for _, child := range b.Children {
				visit(child)
			}
		}
		visit(device)
		disks = append(disks, d)
	}
	return disks, nil
}
func (a *App) disks(ctx context.Context) ([]diskInfo, string) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	raw, err := a.Run.Run(ctx, "lsblk", "--json", "--bytes", "--paths", "--output", "NAME,TYPE,SIZE,MODEL,SERIAL,TRAN,ROTA,MOUNTPOINTS,FSTYPE")
	if err != nil {
		return nil, err.Error()
	}
	status, err := a.Run.Run(ctx, "zpool", "status", "-P", "-L", a.Config.Pool)
	disks, parseErr := parseDisks(raw, status)
	if parseErr != nil {
		return nil, parseErr.Error()
	}
	if err != nil {
		return disks, "磁盘清单已读取，备份池状态暂不可用: " + err.Error()
	}
	return disks, ""
}
