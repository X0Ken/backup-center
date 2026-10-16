package appliance

import (
	"context"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func (a *App) Handler() http.Handler {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.SetTrustedProxies([]string{"127.0.0.1", "::1"})
	r.Use(func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Referrer-Policy", "same-origin")
		if strings.HasPrefix(c.Request.URL.Path, "/api/") {
			c.Header("Cache-Control", "no-store")
		}
		c.Next()
	})
	r.GET("/healthz", func(c *gin.Context) {
		db, e := a.DB.DB()
		if e == nil {
			e = db.PingContext(c.Request.Context())
		}
		if e != nil {
			failure(c, 503, e)
			return
		}
		c.JSON(200, gin.H{"status": "ok", "version": Version})
	})
	r.POST("/api/auth/login", a.login)
	r.POST("/api/peer/pair", a.acceptPair)
	peer := r.Group("/api/peer", a.peerAuth)
	peer.POST("/repositories", a.createReplicaReservation)
	peer.GET("/repositories/:id", a.replicaState)
	peer.POST("/repositories/:id/receive", a.receiveReplica)
	peer.POST("/repositories/:id/reconcile", a.reconcileReplica)
	peer.POST("/repositories/:id/reseed", a.reseedReplica)
	api := r.Group("/api", a.auth)
	api.GET("/auth/me", func(c *gin.Context) {
		c.JSON(200, gin.H{"user": current(c), "csrf": c.MustGet("session").(Session).CSRF})
	})
	api.POST("/auth/password", a.password)
	api.POST("/auth/logout", func(c *gin.Context) {
		a.DB.Delete(&Session{}, "id = ?", c.MustGet("session").(Session).ID)
		http.SetCookie(c.Writer, &http.Cookie{Name: "bc_session", Path: "/", MaxAge: -1, HttpOnly: true, Secure: a.Config.SecureCookie, SameSite: http.SameSiteStrictMode})
		c.JSON(200, gin.H{"ok": true})
	})
	api.GET("/overview", a.overview)
	api.GET("/repositories", a.repositories)
	api.GET("/repositories/:id/snapshots", a.listSnapshots)
	api.GET("/snapshots", a.allSnapshots)
	api.GET("/snapshots/compare", a.compareSnapshots)
	api.GET("/snapshots/browse", a.browseSnapshot)
	api.GET("/snapshots/download", a.downloadFile)
	api.POST("/repositories", admin, a.createRepository)
	api.PATCH("/repositories/:id", admin, a.editRepository)
	api.POST("/actions/:kind", a.submitAction)
	api.GET("/policies", a.listPolicies)
	api.PUT("/policies", admin, a.savePolicy)
	api.GET("/policies/:id/preview", a.retentionPreview)
	api.GET("/exports", a.listExports)
	api.PUT("/exports", admin, a.saveExport)
	api.GET("/connections", a.connections)
	api.GET("/client-guide/:id", a.clientGuide)
	api.GET("/sync/tasks", a.listSyncTasks)
	api.POST("/sync/probe", admin, a.syncProbe)
	api.PUT("/sync/tasks", admin, a.saveSyncTask)
	api.DELETE("/sync/tasks/:id", admin, a.deleteSyncTask)
	api.POST("/sync/tasks/:id/:operation", admin, a.syncControl)
	api.GET("/sync/runs", func(c *gin.Context) {
		pagedList[SyncRun](c, a.DB.Model(&SyncRun{}).Order("id desc"), []string{"status", "phase", "error", "generation"}, 300)
	})
	api.GET("/peers", admin, a.listPeers)
	api.POST("/peers/invitation", admin, a.pairInvitation)
	api.POST("/peers", admin, a.savePeer)
	api.DELETE("/peers/:id", admin, a.deletePeer)
	api.GET("/replications", a.listReplications)
	api.PUT("/replications", admin, a.saveReplication)
	api.DELETE("/replications/:id", admin, a.deleteReplication)
	api.GET("/jobs", func(c *gin.Context) {
		pagedList[Job](c, a.DB.Model(&Job{}).Order("id desc"), []string{"kind", "target", "status", "error", "result"}, 300)
	})
	api.GET("/jobs/:id", func(c *gin.Context) {
		id, e := paramID(c)
		if e != nil {
			return
		}
		var j Job
		if a.DB.First(&j, id).Error != nil {
			failure(c, 404, errors.New("任务不存在"))
			return
		}
		c.JSON(200, j)
	})
	api.GET("/audit", admin, func(c *gin.Context) {
		pagedList[Audit](c, a.DB.Model(&Audit{}).Order("id desc"), []string{"username", "action", "target", "detail", "ip"}, 1000)
	})
	api.GET("/monitor", a.monitorInfo)
	api.GET("/metrics", func(c *gin.Context) {
		hours, _ := strconv.Atoi(c.DefaultQuery("hours", "24"))
		if hours < 1 || hours > 744 {
			hours = 24
		}
		var rows []Metric
		a.DB.Where("created_at > ?", time.Now().Add(-time.Duration(hours)*time.Hour)).Order("created_at").Find(&rows)
		c.JSON(200, rows)
	})
	api.GET("/events", a.events)
	api.GET("/alerts", func(c *gin.Context) {
		pagedList[Alert](c, a.DB.Model(&Alert{}).Order("active desc, updated_at desc"), []string{"key", "message", "severity"}, 300)
	})
	api.POST("/alerts/:id/ack", func(c *gin.Context) {
		id, err := paramID(c)
		if err != nil {
			return
		}
		a.DB.Model(&Alert{}).Where("id = ?", id).Update("acknowledged", true)
		a.audit(current(c).ID, current(c).Username, "alert.ack", fmt.Sprint(id), "", c.ClientIP())
		c.JSON(200, gin.H{"ok": true})
	})
	api.GET("/system", a.systemInfo)
	api.GET("/users", admin, a.listUsers)
	api.POST("/users", admin, a.saveUser)
	api.GET("/settings", admin, a.getSettings)
	api.PUT("/settings", admin, a.saveSettings)
	api.GET("/config/export", admin, a.exportConfig)
	api.POST("/config/checkpoint", admin, a.configCheckpoint)
	api.POST("/config/backup", admin, a.configBackup)
	if a.Config.WebDir != "" {
		r.NoRoute(func(c *gin.Context) {
			if strings.HasPrefix(c.Request.URL.Path, "/api/") {
				failure(c, 404, errors.New("API 不存在"))
				return
			}
			if c.Request.Method != "GET" {
				c.Status(405)
				return
			}
			path := filepath.Join(a.Config.WebDir, filepath.Clean("/"+c.Request.URL.Path))
			if f, e := os.Stat(path); e == nil && !f.IsDir() {
				c.File(path)
			} else {
				c.File(filepath.Join(a.Config.WebDir, "index.html"))
			}
		})
	}
	return r
}
func paramID(c *gin.Context) (uint, error) {
	n, e := strconv.ParseUint(c.Param("id"), 10, 32)
	if e != nil || n == 0 {
		e = errors.New("ID 无效")
		failure(c, 400, e)
	}
	return uint(n), e
}
func (a *App) submit(c *gin.Context, kind, target string, payload any) {
	u := current(c)
	if err := a.audit(u.ID, u.Username, kind+".requested", target, "", c.ClientIP()); err != nil {
		failure(c, 500, err)
		return
	}
	j, err := a.enqueue(kind, target, payload, u.ID)
	if err != nil {
		failure(c, 409, err)
		return
	}
	c.JSON(202, j)
}
func (a *App) createRepository(c *gin.Context) {
	var q RepoRequest
	if !body(c, &q) {
		return
	}
	if err := validName(q.Name); err != nil {
		failure(c, 400, err)
		return
	}
	if q.Kind == "" {
		q.Kind = "filesystem"
	}
	if err := validateProperties(q.Properties); err != nil {
		failure(c, 400, err)
		return
	}
	a.submit(c, "repository.create", q.Name, q)
}
func (a *App) editRepository(c *gin.Context) {
	id, e := paramID(c)
	if e != nil {
		return
	}
	var q RepoRequest
	if !body(c, &q) {
		return
	}
	q.ID = id
	if e = validateProperties(q.Properties); e != nil {
		failure(c, 400, e)
		return
	}
	a.submit(c, "repository.properties", fmt.Sprint(id), q)
}
func (a *App) submitAction(c *gin.Context) {
	kind := c.Param("kind")
	u := current(c)
	if !contains([]string{"snapshot.create", "restore.clone", "snapshot.hold"}, kind) && u.Role != "admin" {
		failure(c, 403, errors.New("需要管理员权限"))
		return
	}
	if kind == "pool.scrub" {
		a.submit(c, kind, a.Config.Pool, nil)
		return
	}
	if kind == "replicate" {
		failure(c, 400, errors.New("异地复制由开始和暂停控制，后台自动安排传输"))
		return
	}
	if kind == "retention.prune" || kind == "key.load" || kind == "key.unload" {
		var q struct {
			ID uint `json:"id"`
		}
		if !body(c, &q) {
			return
		}
		if q.ID == 0 {
			failure(c, 400, errors.New("ID required"))
			return
		}
		a.submit(c, kind, fmt.Sprint(q.ID), q.ID)
		return
	}
	if !contains([]string{"snapshot.create", "snapshot.delete", "snapshot.hold", "snapshot.release", "repository.delete", "restore.clone", "restore.rollback"}, kind) {
		failure(c, 400, errors.New("未知操作"))
		return
	}
	var q ActionRequest
	if !body(c, &q) {
		return
	}
	r, e := a.repo(q.RepositoryID)
	if e != nil {
		failure(c, 404, e)
		return
	}
	if kind == "repository.delete" || kind == "restore.rollback" {
		if q.Confirm != r.Name {
			failure(c, 400, errors.New("确认名称不匹配"))
			return
		}
	}
	a.submit(c, kind, fmt.Sprint(r.ID), q)
}
func (a *App) repositories(c *gin.Context) {
	var repos []Repository
	if e := a.DB.Order("name").Find(&repos).Error; e != nil {
		failure(c, 500, e)
		return
	}
	rows := []gin.H{}
	for _, r := range repos {
		d, e := a.dataset(c.Request.Context(), r)
		msg := ""
		if e != nil {
			if r.Replica {
				msg = "异地接收仓库尚未完成首轮传输，完成后可从快照创建恢复仓库"
			} else {
				msg = e.Error()
			}
		}
		var p Policy
		a.DB.First(&p, "repository_id = ?", r.ID)
		var binding ReplicaBinding
		if r.Replica {
			a.DB.Where("repository_id = ?", r.ID).First(&binding)
		}
		rows = append(rows, gin.H{"repository": r, "storage": d, "error": msg, "policy": p, "replica_state": binding.State})
	}
	c.JSON(200, rows)
}
func (a *App) listSnapshots(c *gin.Context) {
	id, e := paramID(c)
	if e != nil {
		return
	}
	r, e := a.repo(id)
	if e != nil {
		failure(c, 404, e)
		return
	}
	ss, e := a.snapshots(c.Request.Context(), r)
	if e != nil {
		failure(c, 503, e)
		return
	}
	c.JSON(200, ss)
}
func (a *App) allSnapshots(c *gin.Context) {
	var repos []Repository
	a.DB.Find(&repos)
	ss := []Snapshot{}
	for _, r := range repos {
		rows, e := a.snapshots(c.Request.Context(), r)
		if e != nil {
			failure(c, 503, e)
			return
		}
		ss = append(ss, rows...)
	}
	c.JSON(200, ss)
}
func (a *App) overview(c *gin.Context) {
	p, e := a.pools(c.Request.Context())
	msg := ""
	if e != nil {
		msg = e.Error()
	}
	var nr, nj, na int64
	a.DB.Model(&Repository{}).Count(&nr)
	a.DB.Model(&Job{}).Where("status IN ?", []string{"queued", "running"}).Count(&nj)
	a.DB.Model(&Alert{}).Where("active = ? AND acknowledged = ?", true, false).Count(&na)
	var recent []Job
	a.DB.Order("id desc").Limit(12).Find(&recent)
	ns, snapshotErr := a.snapshotCount(c.Request.Context())
	c.JSON(200, gin.H{"snapshots": ns, "snapshot_error": snapshotErr, "pools": p, "storage_error": msg, "repositories": nr, "active_jobs": nj, "alerts": na, "recent_jobs": recent, "version": Version, "time": time.Now()})
}
func (a *App) monitorInfo(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	out, e := a.Run.Run(ctx, "zpool", "status", "-P", a.Config.Pool)
	msg := ""
	if e != nil {
		msg = e.Error()
	}
	c.JSON(200, gin.H{"pool_status": out, "error": msg, "disks": a.diskHealth(ctx)})
}
func (a *App) events(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("X-Accel-Buffering", "no")
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	deadline := time.NewTimer(10 * time.Minute)
	defer deadline.Stop()
	c.Stream(func(w io.Writer) bool {
		select {
		case <-c.Request.Context().Done():
			return false
		case <-deadline.C:
			return false
		case <-ticker.C:
			c.SSEvent("update", gin.H{"time": time.Now()})
			return true
		}
	})
}
func (a *App) compareSnapshots(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Query("repository_id"), 10, 32)
	r, e := a.repo(uint(id))
	if e != nil {
		failure(c, 404, e)
		return
	}
	one, two := c.Query("from"), c.Query("to")
	if validSnapshot(r, one) != nil || validSnapshot(r, two) != nil {
		failure(c, 400, errors.New("快照无效"))
		return
	}
	out, e := a.zfs(c.Request.Context(), "get", "-Hp", "-o", "name,property,value", "written@"+strings.SplitN(one, "@", 2)[1]+",used,referenced", two)
	if e != nil {
		failure(c, 400, e)
		return
	}
	c.JSON(200, gin.H{"comparison": out})
}
