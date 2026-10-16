package appliance

import (
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (a *App) listPolicies(c *gin.Context) {
	var rows []Policy
	if err := a.DB.Find(&rows).Error; err != nil {
		failure(c, 500, err)
		return
	}
	views := make([]map[string]any, 0, len(rows))
	for _, p := range rows {
		views = append(views, policyView(p))
	}
	c.JSON(200, views)
}
func (a *App) savePolicy(c *gin.Context) {
	var q Policy
	if !body(c, &q) {
		return
	}
	if repo, e := a.repo(q.RepositoryID); e != nil || repo.Replica {
		if e == nil {
			e = errors.New("接收仓库由异地复制维护，无需配置本地快照策略")
		}
		failure(c, 400, e)
		return
	}
	if e := validatePolicy(q); e != nil {
		failure(c, 400, e)
		return
	}
	q.Cron = policyCron(q)
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
	a.audit(current(c).ID, current(c).Username, "policy.save", fmt.Sprint(q.RepositoryID), fmt.Sprint(policyView(q)["schedule_label"]), c.ClientIP())
	a.signalPolicies()
	c.JSON(200, policyView(q))
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
	if q, e = a.prepareExport(q, r); e != nil {
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
	var q struct {
		ID           uint   `json:"id"`
		RepositoryID uint   `json:"repository_id"`
		RemoteName   string `json:"remote_name"`
		Enabled      bool   `json:"enabled"`
	}
	if !body(c, &q) {
		return
	}
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	if q.ID != 0 {
		var old Replication
		if a.DB.First(&old, q.ID).Error != nil {
			failure(c, 404, errors.New("复制任务不存在"))
			return
		}
		if old.PeerID == 0 {
			failure(c, 409, errors.New("旧版任务请删除后通过平台互信重新创建"))
			return
		}
		if err := a.DB.Model(&old).Update("enabled", q.Enabled).Error; err != nil {
			failure(c, 500, err)
			return
		}
		if !q.Enabled {
			a.replicaMu.Lock()
			if cancel := a.replicaActive[q.ID]; cancel != nil {
				cancel()
			}
			a.replicaMu.Unlock()
		} else {
			a.DB.Model(&old).Update("next_attempt_at", time.Now())
		}
		a.audit(current(c).ID, current(c).Username, "replication.save", fmt.Sprint(q.ID), "", c.ClientIP())
		c.JSON(200, gin.H{"id": q.ID})
		return
	}
	repo, err := a.repo(q.RepositoryID)
	if err != nil || repo.Replica {
		failure(c, 400, errors.New("请选择本机的业务仓库"))
		return
	}
	var peer PlatformPeer
	if a.DB.Where("direction = ? AND enabled = ?", "outbound", true).First(&peer).Error != nil {
		failure(c, 409, errors.New("请先在系统管理中配置异地目标平台"))
		return
	}
	if q.RemoteName == "" {
		q.RemoteName = "【异地复制】" + repoLabel(repo)
	}
	if err := validReplicaLabel(q.RemoteName); err != nil {
		failure(c, 400, err)
		return
	}
	var n int64
	a.DB.Model(&Replication{}).Where("repository_id = ? AND peer_id = ?", repo.ID, peer.ID).Count(&n)
	if n > 0 {
		failure(c, 409, errors.New("该仓库已有异地复制任务"))
		return
	}
	rep := Replication{RepositoryID: repo.ID, PeerID: peer.ID, RemoteName: q.RemoteName, Enabled: true, AfterSnapshot: true, State: "provisioning", NextAttemptAt: time.Now()}
	if err := a.DB.Create(&rep).Error; err != nil {
		failure(c, 500, err)
		return
	}
	a.audit(current(c).ID, current(c).Username, "replication.save", fmt.Sprint(rep.ID), "", c.ClientIP())
	c.JSON(202, rep)
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
	repo, err := a.repo(rep.RepositoryID)
	if err != nil {
		failure(c, 409, err)
		return
	}
	ss, err := a.snapshots(c.Request.Context(), repo)
	if err != nil {
		failure(c, 409, err)
		return
	}
	for _, snap := range ss {
		holds, err := a.zfs(c.Request.Context(), "holds", "-H", snap.Name)
		if err != nil {
			failure(c, 409, err)
			return
		}
		for _, tag := range []string{fmt.Sprintf("bc-base-%d", id), fmt.Sprintf("bc-transfer-%d", id)} {
			if strings.Contains(holds, "\t"+tag+"\t") {
				if _, err = a.zfs(c.Request.Context(), "release", tag, snap.Name); err != nil {
					failure(c, 409, err)
					return
				}
			}
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
	disks, diskError := a.disks(c.Request.Context())
	c.JSON(200, gin.H{"disks": disks, "disk_error": diskError, "version": Version, "hostname": host, "uptime": string(uptime), "zfs_version": version, "zfs_error": msg, "network": network, "pool": a.Config.Pool, "timezone": time.Local.String(), "secure_cookie": a.Config.SecureCookie, "external_url": a.Config.ExternalURL})
}
func (a *App) exportConfig(c *gin.Context) {
	var repos []Repository
	var policies []Policy
	var exports []Export
	var syncTasks []SyncTask
	a.DB.Find(&syncTasks)
	var reps []Replication
	a.DB.Find(&repos)
	a.DB.Find(&policies)
	a.DB.Find(&exports)
	a.DB.Find(&reps)
	c.Header("Content-Disposition", "attachment; filename=backup-center-config.json")
	a.audit(current(c).ID, current(c).Username, "config.export", "", "", c.ClientIP())
	c.JSON(200, gin.H{"schema": 1, "version": Version, "exported_at": time.Now(), "pool": a.Config.Pool, "repositories": repos, "policies": policies, "exports": exports, "replications": reps, "sync_tasks": syncTasks, "includes_secrets": false, "note": "用户、加密密钥、SSH 私钥和通知凭证需通过离线配置备份另行保管"})
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
