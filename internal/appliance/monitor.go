package appliance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

func (a *App) raise(key, severity, msg string) {
	var alert Alert
	err := a.DB.First(&alert, "key = ?", key).Error
	notify := err != nil || !alert.Active || alert.Message != msg
	alert.Key = key
	alert.Severity = severity
	alert.Message = msg
	alert.Active = true
	if notify {
		alert.Acknowledged = false
	}
	if e := a.DB.Save(&alert).Error; e != nil {
		slog.Error("save alert", "error", e)
		return
	}
	if notify {
		a.notify(alert)
	}
}
func (a *App) notify(alert Alert) {
	type notification struct {
		Webhook      string `json:"webhook"`
		SMTPHost     string `json:"smtp_host"`
		SMTPPort     int    `json:"smtp_port"`
		SMTPUser     string `json:"smtp_user"`
		SMTPPassword string `json:"smtp_password"`
		From         string `json:"from"`
		To           string `json:"to"`
	}
	var n notification
	if e := json.Unmarshal([]byte(a.setting("notifications")), &n); e != nil {
		return
	}
	if n.Webhook != "" {
		body, _ := json.Marshal(map[string]any{"msgtype": "text", "text": map[string]string{"content": "[Backup Center] " + alert.Severity + ": " + alert.Message}})
		ctx, cancel := context.WithTimeout(a.ctx, 10*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, "POST", n.Webhook, bytes.NewReader(body))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			resp, e := client.Do(req)
			if e != nil {
				slog.Error("webhook notification", "error", e)
			} else {
				resp.Body.Close()
				if resp.StatusCode/100 != 2 {
					slog.Error("webhook rejected", "status", resp.StatusCode)
				}
			}
		}
	}
	// SMTP delivery requires STARTTLS and system CA validation; no plaintext credentials.
	if n.SMTPHost != "" && n.To != "" {
		if err := sendMail(n.SMTPHost, n.SMTPPort, n.SMTPUser, n.SMTPPassword, n.From, n.To, alert.Message); err != nil {
			slog.Error("email notification", "error", err)
		}
	}
}
func sendMail(host string, port int, user, password, from, to, msg string) error {
	return sendTLSMail(host, port, user, password, from, to, msg)
}
func (a *App) monitor() {
	defer a.wg.Done()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		a.collect()
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (a *App) collect() {
	ctx, cancel := context.WithTimeout(a.ctx, 40*time.Second)
	defer cancel()
	ps, err := a.pools(ctx)
	if err != nil {
		a.raise("pool-unavailable", "critical", err.Error())
		return
	}
	a.DB.Model(&Alert{}).Where("key = ?", "pool-unavailable").Update("active", false)
	if len(ps) == 0 {
		return
	}
	p := ps[0]
	threshold := 80.
	if v, e := strconv.ParseFloat(a.setting("capacity_threshold"), 64); e == nil && v >= 50 && v <= 99 {
		threshold = v
	}
	if p.Health != "ONLINE" {
		a.raise("pool-health", "critical", "存储池状态: "+p.Health)
	} else {
		a.DB.Model(&Alert{}).Where("key = ?", "pool-health").Update("active", false)
	}
	if p.Size > 0 && float64(p.Allocated)/float64(p.Size)*100 >= threshold {
		a.raise("pool-capacity", "warning", fmt.Sprintf("存储池使用率超过 %.0f%%", threshold))
	} else {
		a.DB.Model(&Alert{}).Where("key = ?", "pool-capacity").Update("active", false)
	}
	m := Metric{Used: p.Allocated, Size: p.Size, CreatedAt: time.Now()}
	out, e := a.Run.Run(ctx, "zpool", "iostat", "-Hp", a.Config.Pool)
	if e == nil {
		for _, line := range strings.Split(out, "\n") {
			f := strings.Fields(line)
			if len(f) >= 7 && f[0] == a.Config.Pool {
				m.ReadOps = number(f[3])
				m.WriteOps = number(f[4])
				m.ReadBytes = number(f[5])
				m.WriteBytes = number(f[6])
			}
		}
	}
	arc, _ := os.ReadFile("/proc/spl/kstat/zfs/arcstats")
	var hits, miss uint64
	for _, line := range strings.Split(string(arc), "\n") {
		f := strings.Fields(line)
		if len(f) == 3 {
			switch f[0] {
			case "hits":
				hits = number(f[2])
			case "misses":
				miss = number(f[2])
			}
		}
	}
	if hits+miss > 0 {
		m.ARCHit = float64(hits) / float64(hits+miss) * 100
	}
	a.DB.Create(&m)
	a.DB.Where("created_at < ?", time.Now().AddDate(0, 0, -31)).Delete(&Metric{})
	a.DB.Where("expires_at < ?", time.Now()).Delete(&Session{})
	var reps []Replication
	a.DB.Where("enabled = ?", true).Find(&reps)
	for _, r := range reps {
		key := fmt.Sprintf("replication-lag-%d", r.ID)
		if r.LastRunAt == nil || time.Since(*r.LastRunAt) > 24*time.Hour || r.LastError != "" {
			a.raise(key, "warning", fmt.Sprintf("复制任务 %d 超过 24 小时未成功完成或上次失败", r.ID))
		} else {
			a.DB.Model(&Alert{}).Where("key = ?", key).Update("active", false)
		}
	}
}
func (a *App) diskHealth(ctx context.Context) []map[string]any {
	out, e := a.Run.Run(ctx, "lsblk", "-dn", "-o", "NAME,TYPE")
	if e != nil {
		return []map[string]any{{"error": e.Error()}}
	}
	items := []map[string]any{}
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) != 2 || f[1] != "disk" {
			continue
		}
		dev := "/dev/" + f[0]
		out, err := a.Run.Run(ctx, "smartctl", "-j", "-a", dev)
		var data any
		if json.Unmarshal([]byte(out), &data) != nil {
			items = append(items, map[string]any{"device": dev, "error": fmt.Sprint(err)})
		} else {
			items = append(items, map[string]any{"device": dev, "smart": data})
		}
	}
	return items
}
