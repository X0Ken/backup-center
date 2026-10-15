package appliance

import (
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func (a *App) listPolicies(c *gin.Context) { var rows []Policy; a.DB.Find(&rows); c.JSON(200, rows) }
func (a *App) savePolicy(c *gin.Context) {
	var q Policy
	if !body(c, &q) {
		return
	}
	if _, e := a.repo(q.RepositoryID); e != nil {
		failure(c, 400, e)
		return
	}
	if e := validatePolicy(q); e != nil {
		failure(c, 400, e)
		return
	}
	var old Policy
	a.DB.First(&old, "repository_id = ?", q.RepositoryID)
	q.ID = old.ID
	if e := a.DB.Save(&q).Error; e != nil {
		failure(c, 500, e)
		return
	}
	if e := a.reloadSchedules(); e != nil {
		failure(c, 500, e)
		return
	}
	a.audit(current(c).ID, current(c).Username, "policy.save", fmt.Sprint(q.RepositoryID), q.Cron, c.ClientIP())
	c.JSON(200, q)
}
func (a *App) retentionPreview(c *gin.Context) {
	id, e := paramID(c)
	if e != nil {
		return
	}
	r, e := a.repo(id)
	if e != nil {
		failure(c, 404, e)
		return
	}
	var p Policy
	if a.DB.First(&p, "repository_id = ?", id).Error != nil {
		failure(c, 404, errors.New("尚未配置策略"))
		return
	}
	ss, e := a.snapshots(c.Request.Context(), r)
	if e != nil {
		failure(c, 503, e)
		return
	}
	keep := retentionKeep(ss, p)
	var reps []Replication
	a.DB.Where("repository_id = ?", id).Find(&reps)
	for _, r := range reps {
		keep[r.LastSnapshot] = true
	}
	c.JSON(200, gin.H{"snapshots": ss, "keep": keep})
}
func (a *App) listExports(c *gin.Context) { var rows []Export; a.DB.Find(&rows); c.JSON(200, rows) }
func (a *App) saveExport(c *gin.Context) {
	var q Export
	if !body(c, &q) {
		return
	}
	r, e := a.repo(q.RepositoryID)
	if e != nil {
		failure(c, 404, e)
		return
	}
	if e = validateExport(q, r); e != nil {
		failure(c, 400, e)
		return
	}
	a.submit(c, "export.apply", fmt.Sprint(r.ID), q)
}
func (a *App) connections(c *gin.Context) {
	nfs, e1 := a.Run.Run(c.Request.Context(), "cat", "/proc/fs/nfsd/clients")
	if e1 != nil {
		nfs = ""
		entries, _ := os.ReadDir("/proc/fs/nfsd/clients")
		for _, e := range entries {
			b, err := os.ReadFile(filepath.Join("/proc/fs/nfsd/clients", e.Name(), "info"))
			if err == nil {
				nfs += string(b) + "\n"
			}
		}
	}
	iscsi, e2 := a.Run.Run(c.Request.Context(), "targetcli", "sessions")
	msg := ""
	if e2 != nil {
		msg = e2.Error()
	}
	c.JSON(200, gin.H{"nfs": nfs, "iscsi": iscsi, "iscsi_error": msg})
}
func (a *App) listReplications(c *gin.Context) {
	var rows []Replication
	a.DB.Find(&rows)
	c.JSON(200, rows)
}
func (a *App) saveReplication(c *gin.Context) {
	var q Replication
	if !body(c, &q) {
		return
	}
	if e := validateReplication(q); e != nil {
		failure(c, 400, e)
		return
	}
	if _, e := a.repo(q.RepositoryID); e != nil {
		failure(c, 404, e)
		return
	}
	if q.ID != 0 {
		var old Replication
		if a.DB.First(&old, q.ID).Error != nil {
			failure(c, 404, errors.New("复制任务不存在"))
			return
		}
		if old.RepositoryID != q.RepositoryID || old.Host != q.Host || old.User != q.User || old.Port != q.Port || old.TargetDataset != q.TargetDataset {
			failure(c, 400, errors.New("复制目标不可原地修改，请新建任务"))
			return
		}
		q.LastSnapshot = old.LastSnapshot
		q.LastGUID = old.LastGUID
		q.LastRunAt = old.LastRunAt
		q.Bytes = old.Bytes
		q.DurationSeconds = old.DurationSeconds
		q.LastError = old.LastError
	} else {
		q.LastSnapshot = ""
		q.LastGUID = ""
		q.LastRunAt = nil
		q.Bytes = 0
		q.DurationSeconds = 0
		q.LastError = ""
	}
	if e := a.DB.Save(&q).Error; e != nil {
		failure(c, 500, e)
		return
	}
	if e := a.reloadSchedules(); e != nil {
		failure(c, 500, e)
		return
	}
	a.audit(current(c).ID, current(c).Username, "replication.save", fmt.Sprint(q.ID), "", c.ClientIP())
	c.JSON(200, q)
}
func (a *App) deleteReplication(c *gin.Context) {
	id, e := paramID(c)
	if e != nil {
		return
	}
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	var rep Replication
	if a.DB.First(&rep, id).Error != nil {
		failure(c, 404, errors.New("任务不存在"))
		return
	}
	var jobs int64
	a.DB.Model(&Job{}).Where("kind = ? AND target = ? AND status IN ?", "replicate", fmt.Sprint(id), []string{"queued", "running"}).Count(&jobs)
	if jobs > 0 {
		failure(c, 409, errors.New("复制任务正在运行或排队"))
		return
	}
	if rep.LastSnapshot != "" {
		if _, e = a.zfs(c.Request.Context(), "release", fmt.Sprintf("bc-base-%d", id), rep.LastSnapshot); e != nil {
			failure(c, 409, e)
			return
		}
	}
	if e = a.DB.Delete(&rep).Error; e != nil {
		failure(c, 500, e)
		return
	}
	a.reloadSchedules()
	a.audit(current(c).ID, current(c).Username, "replication.delete", fmt.Sprint(id), "", c.ClientIP())
	c.JSON(200, gin.H{"ok": true})
}
func (a *App) sshInfo(c *gin.Context) {
	key, e := a.ensureSSH()
	if e != nil {
		failure(c, 500, e)
		return
	}
	known, _ := os.ReadFile(filepath.Join(a.Config.DataDir, "ssh", "known_hosts"))
	c.JSON(200, gin.H{"public_key": key, "known_hosts": string(known)})
}
func (a *App) trustHost(c *gin.Context) {
	var q struct {
		Host        string `json:"host"`
		Port        int    `json:"port"`
		Fingerprint string `json:"fingerprint"`
	}
	if !body(c, &q) {
		return
	}
	if !hostRE.MatchString(q.Host) || q.Port < 1 || q.Port > 65535 || !strings.HasPrefix(q.Fingerprint, "SHA256:") {
		failure(c, 400, errors.New("主机、端口或 SHA256 指纹无效"))
		return
	}
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	if _, e := a.ensureSSH(); e != nil {
		failure(c, 500, e)
		return
	}
	out, e := a.Run.Run(c.Request.Context(), "ssh-keyscan", "-T", "8", "-p", strconv.Itoa(q.Port), "-t", "ed25519", q.Host)
	if e != nil {
		failure(c, 400, e)
		return
	}
	matched := []string{}
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "#") {
			continue
		}
		_, _, key, _, _, err := ssh.ParseKnownHosts([]byte(l))
		if err == nil && ssh.FingerprintSHA256(key) == q.Fingerprint {
			matched = append(matched, l)
		}
	}
	if len(matched) == 0 {
		failure(c, 400, errors.New("扫描结果与人工核实的指纹不一致，拒绝信任"))
		return
	}
	path := filepath.Join(a.Config.DataDir, "ssh", "known_hosts")
	previous, _ := os.ReadFile(path)
	data := append(previous, []byte(strings.Join(matched, "\n")+"\n")...)
	if e = atomicFile(path, data, 0600); e != nil {
		failure(c, 500, e)
		return
	}
	a.audit(current(c).ID, current(c).Username, "ssh.trust", q.Host, q.Fingerprint, c.ClientIP())
	c.JSON(200, gin.H{"ok": true})
}
func (a *App) listUsers(c *gin.Context) { var rows []User; a.DB.Find(&rows); c.JSON(200, rows) }
func (a *App) saveUser(c *gin.Context) {
	var q struct {
		ID       uint   `json:"id"`
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
		Disabled bool   `json:"disabled"`
	}
	if !body(c, &q) {
		return
	}
	if !contains([]string{"admin", "operator"}, q.Role) {
		failure(c, 400, errors.New("角色无效"))
		return
	}
	if e := validName(q.Username); e != nil {
		failure(c, 400, e)
		return
	}
	if q.ID == current(c).ID && (q.Disabled || q.Role != "admin") {
		failure(c, 400, errors.New("不能禁用自己或降低自己的管理员权限"))
		return
	}
	var u User
	if q.ID != 0 {
		if a.DB.First(&u, q.ID).Error != nil {
			failure(c, 404, errors.New("用户不存在"))
			return
		}
	}
	u.Username = q.Username
	u.Role = q.Role
	u.Disabled = q.Disabled
	if q.Password != "" || q.ID == 0 {
		hash, e := passwordHash(q.Password)
		if e != nil {
			failure(c, 400, e)
			return
		}
		u.Password = hash
		u.MustChange = true
	}
	if e := a.DB.Save(&u).Error; e != nil {
		failure(c, 409, e)
		return
	}
	a.DB.Where("user_id = ?", u.ID).Delete(&Session{})
	a.audit(current(c).ID, current(c).Username, "user.save", u.Username, "", c.ClientIP())
	c.JSON(200, u)
}
func (a *App) getSettings(c *gin.Context) {
	c.JSON(200, gin.H{"capacity_threshold": a.setting("capacity_threshold"), "notifications_configured": a.setting("notifications") != ""})
}
func (a *App) saveSettings(c *gin.Context) {
	var q struct {
		CapacityThreshold int `json:"capacity_threshold"`
		Notifications     *struct {
			Webhook      string `json:"webhook"`
			SMTPHost     string `json:"smtp_host"`
			SMTPPort     int    `json:"smtp_port"`
			SMTPUser     string `json:"smtp_user"`
			SMTPPassword string `json:"smtp_password"`
			From         string `json:"from"`
			To           string `json:"to"`
		} `json:"notifications"`
	}
	if !body(c, &q) {
		return
	}
	if q.CapacityThreshold < 50 || q.CapacityThreshold > 99 {
		failure(c, 400, errors.New("容量阈值应在 50% 到 99% 之间"))
		return
	}
	if n := q.Notifications; n != nil {
		if n.Webhook != "" {
			u, e := url.Parse(n.Webhook)
			if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
				failure(c, 400, errors.New("Webhook 必须为 HTTPS 地址"))
				return
			}
		}
		if n.SMTPHost != "" {
			if !hostRE.MatchString(n.SMTPHost) || n.SMTPPort < 1 || n.SMTPPort > 65535 || strings.ContainsAny(n.From+n.To, "\r\n") {
				failure(c, 400, errors.New("SMTP 配置无效"))
				return
			}
		}
	}
	if e := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(&Setting{Key: "capacity_threshold", Value: fmt.Sprint(q.CapacityThreshold)}).Error; err != nil {
			return err
		}
		if q.Notifications != nil {
			b, _ := marshal(q.Notifications)
			return tx.Save(&Setting{Key: "notifications", Value: string(b)}).Error
		}
		return nil
	}); e != nil {
		failure(c, 500, e)
		return
	}
	a.audit(current(c).ID, current(c).Username, "settings.save", "", "", c.ClientIP())
	c.JSON(200, gin.H{"ok": true})
}
func (a *App) systemInfo(c *gin.Context) {
	host, _ := os.Hostname()
	uptime, _ := os.ReadFile("/proc/uptime")
	version, e := a.zfs(c.Request.Context(), "version")
	msg := ""
	if e != nil {
		msg = e.Error()
	}
	network, _ := a.Run.Run(c.Request.Context(), "ip", "-j", "address")
	c.JSON(200, gin.H{"version": Version, "hostname": host, "uptime": string(uptime), "zfs_version": version, "zfs_error": msg, "network": network, "pool": a.Config.Pool, "timezone": time.Local.String(), "secure_cookie": a.Config.SecureCookie, "external_url": a.Config.ExternalURL})
}
func (a *App) exportConfig(c *gin.Context) {
	var repos []Repository
	var policies []Policy
	var exports []Export
	var reps []Replication
	a.DB.Find(&repos)
	a.DB.Find(&policies)
	a.DB.Find(&exports)
	a.DB.Find(&reps)
	c.Header("Content-Disposition", "attachment; filename=backup-center-config.json")
	a.audit(current(c).ID, current(c).Username, "config.export", "", "", c.ClientIP())
	c.JSON(200, gin.H{"schema": 1, "version": Version, "exported_at": time.Now(), "pool": a.Config.Pool, "repositories": repos, "policies": policies, "exports": exports, "replications": reps, "includes_secrets": false, "note": "用户、加密密钥、SSH 私钥和通知凭证需通过离线配置备份另行保管"})
}
func (a *App) configCheckpoint(c *gin.Context) {
	path := filepath.Join(a.Config.DataDir, "checkpoints")
	if err := os.MkdirAll(path, 0700); err != nil {
		failure(c, 500, err)
		return
	}
	path = filepath.Join(path, time.Now().UTC().Format("20060102T150405.000000000")+".db")
	if e := a.DB.Exec("VACUUM INTO ?", path).Error; e != nil {
		failure(c, 500, e)
		return
	}
	os.Chmod(path, 0600)
	a.audit(current(c).ID, current(c).Username, "config.checkpoint", "", "", c.ClientIP())
	c.JSON(200, gin.H{"path": path, "note": "一致性 SQLite 检查点已生成；请将数据目录的 keys/、ssh/ 和该文件复制至独立安全介质"})
}
