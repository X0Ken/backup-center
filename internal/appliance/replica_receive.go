package appliance

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type replicaRemoteState struct {
	Repository  Repository        `json:"repository"`
	ResumeToken string            `json:"resume_token"`
	Snapshots   map[string]string `json:"snapshots"`
	State       string            `json:"state"`
}

func (a *App) createReplicaReservation(c *gin.Context) {
	p := c.MustGet("peer").(PlatformPeer)
	var q struct {
		EncryptionKey []byte `json:"encryption_key"`
		TaskKey       string `json:"task_key"`
		Name          string `json:"name"`
		Kind          string `json:"kind"`
	}
	if !body(c, &q) {
		return
	}
	if (len(q.EncryptionKey) != 0 && len(q.EncryptionKey) != 32) || validName(q.TaskKey) != nil || validReplicaLabel(q.Name) != nil || !contains([]string{"filesystem", "volume"}, q.Kind) {
		failure(c, 400, errors.New("接收仓库参数无效"))
		return
	}
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	var binding ReplicaBinding
	if a.DB.Where("peer_id = ? AND task_key = ?", p.ID, q.TaskKey).First(&binding).Error == nil {
		repo, e := a.repo(binding.RepositoryID)
		if e != nil {
			failure(c, 409, e)
			return
		}
		c.JSON(200, replicaRemoteState{Repository: repo, State: binding.State})
		return
	}
	if e := a.ensureRoot(c.Request.Context()); e != nil {
		failure(c, 409, e)
		return
	}
	name := "replica-" + peerHash(p.InstanceID + ":" + q.TaskKey)[:24]
	if len(q.EncryptionKey) > 0 {
		dir := filepath.Join(a.Config.DataDir, "keys")
		if e := os.MkdirAll(dir, 0700); e != nil {
			failure(c, 500, e)
			return
		}
		if e := atomicFile(filepath.Join(dir, name+".key"), q.EncryptionKey, 0600); e != nil {
			failure(c, 500, e)
			return
		}
	}
	// The metadata reservation precedes the first ZFS receive. No empty dataset is
	// created, so receive never needs destructive -F and retries are idempotent.
	repo := Repository{Name: name, DisplayName: q.Name, Dataset: a.root + "/repos/" + name, Kind: q.Kind, Replica: true, Description: "异地复制接收仓库"}
	e := a.DB.Transaction(func(tx *gorm.DB) error {
		if e := tx.Create(&repo).Error; e != nil {
			return e
		}
		binding = ReplicaBinding{PeerID: p.ID, TaskKey: q.TaskKey, RepositoryID: repo.ID, State: "waiting"}
		return tx.Create(&binding).Error
	})
	if e != nil {
		failure(c, 409, e)
		return
	}
	a.audit(0, "", "replica.create", fmt.Sprint(repo.ID), q.Name, c.ClientIP())
	c.JSON(201, replicaRemoteState{Repository: repo, State: binding.State})
}
func (a *App) incomingRepo(c *gin.Context) (Repository, ReplicaBinding, error) {
	id, e := strconv.ParseUint(c.Param("id"), 10, 32)
	if e != nil {
		return Repository{}, ReplicaBinding{}, e
	}
	p := c.MustGet("peer").(PlatformPeer)
	var b ReplicaBinding
	if e = a.DB.Where("peer_id = ? AND repository_id = ?", p.ID, id).First(&b).Error; e != nil {
		return Repository{}, b, errors.New("该仓库不属于此平台连接")
	}
	r, e := a.repo(uint(id))
	if e == nil && !r.Replica {
		e = errors.New("目标不是接收仓库")
	}
	return r, b, e
}
func (a *App) replicaStatus(ctx context.Context, r Repository, b ReplicaBinding) (replicaRemoteState, error) {
	result := replicaRemoteState{Repository: r, State: b.State, Snapshots: map[string]string{}}
	exists, e := a.zfs(ctx, "list", "-H", r.Dataset)
	_ = exists
	if e != nil {
		if b.LastSuccessAt == nil && b.State != "ready" {
			return result, nil
		}
		return result, e
	}
	if e = a.owned(ctx, r.Dataset); e != nil {
		return result, e
	}
	token, e := a.zfs(ctx, "get", "-H", "-o", "value", "receive_resume_token", r.Dataset)
	if e != nil {
		return result, e
	}
	if v := strings.TrimSpace(token); v != "-" {
		result.ResumeToken = v
	}
	out, e := a.zfs(ctx, "list", "-Hp", "-d", "1", "-t", "snapshot", "-o", "name,guid", r.Dataset)
	if e != nil {
		return result, e
	}
	for _, f := range fields(out) {
		if len(f) == 2 && strings.HasPrefix(f[0], r.Dataset+"@bc-") {
			result.Snapshots[strings.SplitN(f[0], "@", 2)[1]] = f[1]
		}
	}
	return result, nil
}
func (a *App) replicaState(c *gin.Context) {
	r, b, e := a.incomingRepo(c)
	if e != nil {
		failure(c, 404, e)
		return
	}
	if e = a.recoverReplicaReseed(c, r, &b); e != nil {
		failure(c, 409, e)
		return
	}
	state, e := a.replicaStatus(c.Request.Context(), r, b)
	if e != nil {
		failure(c, 409, e)
		return
	}
	c.JSON(200, state)
}
func (a *App) receiveReplica(c *gin.Context) {
	r, b, e := a.incomingRepo(c)
	if e != nil {
		failure(c, 404, e)
		return
	}
	if b.State == "archiving" {
		failure(c, 409, errors.New("历史副本尚未准备完毕，请稍后重试"))
		return
	}
	a.mutationMu.Lock()
	a.replicaMu.Lock()
	busy := a.replicaReceiving[r.ID]
	if !busy {
		a.replicaReceiving[r.ID] = true
	}
	a.replicaMu.Unlock()
	a.mutationMu.Unlock()
	if busy {
		failure(c, 409, errors.New("该仓库正在接收，稍后自动重试"))
		return
	}
	defer func() { a.replicaMu.Lock(); delete(a.replicaReceiving, r.ID); a.replicaMu.Unlock() }()
	// Authenticated streams can outlive the ordinary JSON API's read deadline.
	rc := http.NewResponseController(c.Writer)
	_ = rc.SetReadDeadline(time.Time{})
	ctx, cancel := context.WithTimeout(a.ctx, 7*24*time.Hour)
	defer cancel()
	// Client disconnect cancels receive while preserving the ZFS resume token.
	stop := context.AfterFunc(c.Request.Context(), cancel)
	defer stop()
	a.DB.Model(&b).Updates(map[string]any{"state": "receiving", "last_error": ""})
	args := []string{"receive", "-s", "-u", "-o", "org.backup-center:managed=yes", "-o", "readonly=on"}
	if r.Kind == "filesystem" {
		args = append(args, "-o", "mountpoint=/srv/backup-center/repos/"+r.Name, "-o", "canmount=off", "-o", "exec=off", "-o", "setuid=off", "-o", "devices=off")
	} else {
		args = append(args, "-o", "volmode=none")
	}
	args = append(args, r.Dataset)
	cmd := command(ctx, "zfs", args...)
	cmd.Stdin = c.Request.Body
	var log cappedBuffer
	cmd.Stderr = &log
	cmd.Stdout = &log
	e = cmd.Run()
	if e != nil {
		msg := "接收中断，已保留可续传状态: " + log.String()
		a.DB.Model(&b).Updates(map[string]any{"state": "interrupted", "last_error": msg})
		failure(c, 409, errors.New(msg))
		return
	}
	key := filepath.Join(a.Config.DataDir, "keys", r.Name+".key")
	if _, err := os.Stat(key); err == nil {
		if _, err = a.zfs(ctx, "set", "keylocation=file://"+key, r.Dataset); err != nil {
			a.DB.Model(&b).Updates(map[string]any{"state": "interrupted", "last_error": err.Error()})
			failure(c, 409, err)
			return
		}
	}
	if _, err := os.Stat(key); err == nil {
		status, err := a.zfs(ctx, "get", "-H", "-o", "value", "keystatus", r.Dataset)
		if err == nil && strings.TrimSpace(status) == "unavailable" {
			if _, err = a.zfs(ctx, "load-key", r.Dataset); err != nil {
				failure(c, 409, err)
				return
			}
		}
	}
	// The sender independently verifies the received GUID before releasing holds.
	now := time.Now()
	a.DB.Model(&b).Updates(map[string]any{"state": "ready", "last_error": "", "last_success_at": now})
	a.audit(0, "", "replica.receive", fmt.Sprint(r.ID), "", c.ClientIP())
	c.JSON(200, gin.H{"ok": true})
}
