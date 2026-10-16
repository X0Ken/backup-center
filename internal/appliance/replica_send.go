package appliance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

func (a *App) signalReplications() {
	select {
	case a.replicaWake <- struct{}{}:
	default:
	}
}

// Caller holds mutationMu, serializing control changes with claiming a job.
func (a *App) setReplicationRunning(rep *Replication, running bool) error {
	updates := map[string]any{"enabled": running}
	if running {
		updates["next_attempt_at"] = time.Now()
		updates["state"] = "pending"
	}
	if err := a.DB.Transaction(func(tx *gorm.DB) error {
		if !running {
			if err := tx.Model(&Job{}).Where("kind = ? AND target = ? AND status = ?", "replicate", fmt.Sprint(rep.ID), "queued").Updates(map[string]any{"status": "interrupted", "error": "任务已暂停，未开始传输", "finished_at": time.Now()}).Error; err != nil {
				return err
			}
		}
		return tx.Model(rep).Updates(updates).Error
	}); err != nil {
		return err
	}

	if !running {
		a.replicaMu.Lock()
		cancel := a.replicaActive[rep.ID]
		a.replicaMu.Unlock()
		if cancel != nil {
			cancel()
		} else {
			a.DB.Model(rep).Update("state", "paused")
		}
	}
	a.signalReplications()
	return nil
}
func (a *App) replicationScheduler() {
	defer a.wg.Done()
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	a.DB.Model(&Replication{}).Where("peer_id = ?", 0).Updates(map[string]any{"enabled": false, "cron": "", "state": "legacy", "last_error": "请通过系统管理配对目标平台后重新创建任务"})
	a.DB.Model(&Replication{}).Where("peer_id <> ?", 0).Updates(map[string]any{"cron": "", "after_snapshot": true})
	for {
		a.dispatchReplications()
		select {
		case <-a.ctx.Done():
			return
		case <-tick.C:
		case <-a.replicaWake:
		}
	}
}
func (a *App) dispatchReplications() {
	if a.ctx.Err() != nil {
		return
	}
	a.replicaMu.Lock()
	busy := len(a.replicaActive) > 0
	a.replicaMu.Unlock()
	if busy {
		return
	}
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	var reps []Replication
	a.DB.Where("peer_id <> ? AND enabled = ? AND next_attempt_at <= ?", 0, true, time.Now()).Order("next_attempt_at,id").Find(&reps)
	for _, rep := range reps {
		needed := rep.RemoteRepositoryID == 0 || contains([]string{"pending", "retrying", "sending", "catching_up"}, rep.State) || rep.TransferSnapshot != "" || rep.LastReconciledAt == nil || time.Since(*rep.LastReconciledAt) >= time.Minute
		if !needed {
			r, e := a.repo(rep.RepositoryID)
			if e != nil {
				continue
			}
			catalog, e := a.replicaCatalog(a.ctx, r)
			if e != nil {
				needed = true
			} else {
				needed = rep.LastCatalogHash != catalogHash(catalog)
			}
		}
		if needed {
			_, _ = a.enqueue("replicate", fmt.Sprint(rep.ID), rep.ID, 0)
		}
	}
	var j Job
	if a.DB.Order("id").First(&j, "kind = ? AND status = ?", "replicate", "queued").Error != nil {
		return
	}
	var id uint
	if jsonDecode(j.Payload, &id) != nil {
		a.DB.Model(&j).Updates(map[string]any{"status": "failed", "error": "复制任务参数无效"})
		return
	}
	var rep Replication
	if a.DB.First(&rep, id).Error != nil || rep.PeerID == 0 || !rep.Enabled {
		a.DB.Model(&j).Updates(map[string]any{"status": "interrupted", "error": "任务已暂停或需要迁移", "finished_at": time.Now()})
		return
	}
	ctx, cancel := context.WithTimeout(a.ctx, 7*24*time.Hour)
	a.replicaMu.Lock()
	a.replicaActive[id] = cancel
	a.replicaMu.Unlock()
	now := time.Now()
	if e := a.DB.Model(&j).Updates(map[string]any{"status": "running", "started_at": now}).Error; e != nil {
		cancel()
		a.replicaMu.Lock()
		delete(a.replicaActive, id)
		a.replicaMu.Unlock()
		return
	}
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		defer cancel()
		defer func() { a.replicaMu.Lock(); delete(a.replicaActive, id); a.replicaMu.Unlock(); a.signalReplications() }()
		result, e := a.replicate(ctx, id)
		finish := time.Now()
		status, msg := "succeeded", ""
		a.mutationMu.Lock()
		var latest Replication
		a.DB.First(&latest, id)
		if e != nil {
			status = "failed"
			msg = e.Error()
			if ctx.Err() != nil {
				status = "interrupted"
			}
			state := "retrying"
			shift := rep.Failures
			if shift > 6 {
				shift = 6
			}
			delay := 30 * time.Second * time.Duration(1<<shift)
			if delay > 30*time.Minute {
				delay = 30 * time.Minute
			}
			next := finish.Add(delay)
			failures := rep.Failures + 1
			if ctx.Err() != nil {
				failures = latest.Failures
				msg = "传输已停止，续传状态和已完成的异地副本已保留"
				if latest.Enabled {
					state = "pending"
					next = finish
				} else {
					state = "paused"
				}
			}
			a.DB.Model(&Replication{}).Where("id = ?", id).Updates(map[string]any{"state": state, "last_error": map[bool]string{true: "", false: msg}[ctx.Err() != nil], "failures": failures, "next_attempt_at": next})
			if status == "failed" {
				a.raise("replication-"+fmt.Sprint(id), "warning", msg)
			}
		} else {
			updates := map[string]any{"last_error": "", "failures": 0, "next_attempt_at": finish.Add(5 * time.Second)}
			if !latest.Enabled {
				updates["state"] = "paused"
			}
			a.DB.Model(&Replication{}).Where("id = ?", id).Updates(updates)
			a.DB.Model(&Alert{}).Where("key = ?", "replication-"+fmt.Sprint(id)).Update("active", false)
		}
		a.DB.Model(&j).Updates(map[string]any{"status": status, "error": msg, "result": result, "finished_at": finish})
		a.mutationMu.Unlock()
		a.audit(j.UserID, "", "replicate."+status, fmt.Sprint(id), msg, "")
	}()
}
func (a *App) replicate(ctx context.Context, id uint) (string, error) {
	var rep Replication
	if e := a.DB.First(&rep, id).Error; e != nil {
		return "", e
	}
	if !rep.Enabled {
		return "", context.Canceled
	}
	if rep.PeerID == 0 {
		return "", errors.New("旧版任务需要重新配对")
	}
	var peer PlatformPeer
	if a.DB.Where("id = ? AND enabled = ? AND direction = ?", rep.PeerID, true, "outbound").First(&peer).Error != nil {
		return "", errors.New("目标平台连接不可用")
	}
	repo, e := a.repo(rep.RepositoryID)
	if e != nil {
		return "", e
	}
	if e = a.owned(ctx, repo.Dataset); e != nil {
		return "", e
	}
	start := time.Now()
	var bytes int64
	defer func() {
		a.DB.Model(&Replication{}).Where("id = ?", id).Updates(map[string]any{"last_run_at": start, "duration_seconds": time.Since(start).Seconds(), "bytes": bytes})
	}()
	if rep.RemoteRepositoryID == 0 {
		a.DB.Model(&rep).Update("state", "provisioning")
		key, e := a.replicaEncryptionKey(ctx, repo)
		if e != nil {
			return "", e
		}
		var state replicaRemoteState
		if e = a.replicaJSON(ctx, peer, "POST", "/repositories", map[string]any{"task_key": "task-" + strconv.Itoa(int(id)), "name": rep.RemoteName, "kind": repo.Kind, "encryption_key": key}, &state); e != nil {
			return "", e
		}
		if state.Repository.ID == 0 || !state.Repository.Replica || state.Repository.Kind != repo.Kind {
			return "", errors.New("目标仓库登记响应无效")
		}
		rep.RemoteRepositoryID = state.Repository.ID
		if e = a.DB.Model(&rep).Updates(map[string]any{"remote_repository_id": rep.RemoteRepositoryID, "target_dataset": state.Repository.Dataset, "state": "waiting"}).Error; e != nil {
			return "", e
		}
	}
	path := "/repositories/" + strconv.Itoa(int(rep.RemoteRepositoryID))
	var remote replicaRemoteState
	if e = a.replicaJSON(ctx, peer, "GET", path, nil, &remote); e != nil {
		return "", e
	}
	if remote.ResumeToken != "" {
		a.DB.Model(&rep).Update("state", "sending")
		n, e := a.streamPeer(ctx, rep, peer, path, []string{"send", "-t", remote.ResumeToken})
		bytes += n
		if e != nil {
			return "", e
		}
		if e = a.replicaJSON(ctx, peer, "GET", path, nil, &remote); e != nil {
			return "", e
		}
	}
	catalog, e := a.replicaCatalog(ctx, repo)
	if e != nil {
		return "", e
	}
	// Old releases sent only the newest snapshot. Preserve that branch, including
	// clone dependencies, before rebuilding a complete chronological history.
	if needsReplicaReseed(catalog, remote.Snapshots) {
		if e = a.replicaJSON(ctx, peer, "POST", path+"/reseed", map[string]string{"catalog_hash": catalogHash(catalog)}, &remote); e != nil {
			return "", e
		}
		rep.TransferSnapshot = ""
	}
	pending := missingReplicaSnapshots(catalog, remote.Snapshots)
	a.DB.Model(&rep).Update("pending_snapshots", len(pending))
	for i, target := range pending {
		if i >= 8 {
			break
		}
		if e = ctx.Err(); e != nil {
			return "", e
		}
		base := ""
		for _, s := range catalog {
			if s.Txg < target.Txg && remote.Snapshots[s.shortName()] == s.GUID {
				base = s.Name
			}
		}
		a.mutationMu.Lock()
		e = a.holdReplica(ctx, target.Name, fmt.Sprintf("bc-transfer-%d", id))
		if e == nil && base != "" {
			e = a.holdReplica(ctx, base, fmt.Sprintf("bc-base-%d", id))
		}
		if e == nil {
			e = a.DB.Model(&rep).Updates(map[string]any{"transfer_snapshot": target.Name, "state": "sending"}).Error
		}
		a.mutationMu.Unlock()
		if e != nil {
			return "", e
		}
		args := []string{"send", "-w"}
		if base != "" {
			args = append(args, "-i", base)
		}
		args = append(args, target.Name)
		n, e := a.streamPeer(ctx, rep, peer, path, args)
		bytes += n
		if e != nil {
			return "", e
		}
		if e = a.replicaJSON(ctx, peer, "GET", path, nil, &remote); e != nil {
			return "", e
		}
		if remote.Snapshots[target.shortName()] != target.GUID {
			return "", errors.New("异地快照 GUID 校验失败，保留源快照等待重试")
		}
		if e = a.ackReplicaSnapshot(ctx, &rep, target, base); e != nil {
			return "", e
		}
	}
	// Re-read after transfer: retention may have coalesced obsolete local snapshots
	// while the network was busy. Only reconcile a freshly verified complete set.
	fresh, e := a.replicaCatalog(ctx, repo)
	if e != nil {
		return "", e
	}
	pending = missingReplicaSnapshots(fresh, remote.Snapshots)
	if len(pending) > 0 {
		a.DB.Model(&rep).Updates(map[string]any{"state": "catching_up", "pending_snapshots": len(pending)})
		return fmt.Sprintf("已传输 %d 字节，剩余 %d 个快照等待自动处理", bytes, len(pending)), nil
	}
	now := time.Now()
	if len(fresh) == 0 {
		a.DB.Model(&rep).Updates(map[string]any{"state": "waiting", "pending_snapshots": 0, "last_catalog_hash": catalogHash(fresh), "last_reconciled_at": now})
		return "等待源快照，保留已有异地副本", nil
	}
	latest := fresh[len(fresh)-1]
	if e = a.ackReplicaSnapshot(ctx, &rep, latest); e != nil {
		return "", e
	}
	keep := map[string]string{}
	for _, s := range fresh {
		keep[s.shortName()] = s.GUID
	}
	var reconciled replicaReconcileResult
	if e = a.replicaJSON(ctx, peer, "POST", path+"/reconcile", replicaReconcileInput{Snapshots: keep, Baseline: latest.GUID}, &reconciled); e != nil {
		return "", e
	}
	if e = ctx.Err(); e != nil {
		return "", e
	}
	a.DB.Model(&rep).Updates(map[string]any{"state": "ready", "pending_snapshots": 0, "protected_snapshots": len(reconciled.Protected), "last_catalog_hash": catalogHash(fresh), "last_reconciled_at": now})
	return fmt.Sprintf("异地快照集合已核对，传输 %d 字节；清理 %d 个过期快照，保留 %d 个受保护快照", bytes, len(reconciled.Deleted), len(reconciled.Protected)), nil
}
func (a *App) replicaJSON(ctx context.Context, p PlatformPeer, method, path string, input, output any) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var body io.Reader
	if input != nil {
		raw, e := json.Marshal(input)
		if e != nil {
			return e
		}
		body = strings.NewReader(string(raw))
	}
	// A peer response is a full catalog, not a patch. json.Unmarshal otherwise
	// retains map entries omitted by a reseed or snapshot deletion.
	if state, ok := output.(*replicaRemoteState); ok {
		*state = replicaRemoteState{}
	}
	return a.peerRequest(ctx, p, method, path, body, output)
}
func (a *App) ackReplicaSnapshot(ctx context.Context, rep *Replication, target replicaSnapshot, bases ...string) error {
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	if e := ctx.Err(); e != nil {
		return e
	}
	previous := append([]string{rep.LastSnapshot, rep.TransferSnapshot, target.Name}, bases...)
	tag := fmt.Sprintf("bc-base-%d", rep.ID)
	if e := a.holdReplica(ctx, target.Name, tag); e != nil {
		return e
	}
	if e := a.DB.Model(rep).Updates(map[string]any{"last_snapshot": target.Name, "last_guid": target.GUID, "transfer_snapshot": ""}).Error; e != nil {
		return e
	}
	// Only the previous base, active transfer and the actual common base can
	// carry this task's holds. Avoid scanning the entire history for every point.
	seen := map[string]bool{}
	prefix := strings.SplitN(target.Name, "@", 2)[0] + "@"
	for _, name := range previous {
		if name == "" || seen[name] || !strings.HasPrefix(name, prefix) {
			continue
		}
		seen[name] = true
		holds, e := a.zfs(ctx, "holds", "-H", name)
		if e != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}
		for _, t := range []string{tag, fmt.Sprintf("bc-transfer-%d", rep.ID)} {
			if (name != target.Name || t != tag) && strings.Contains(holds, "\t"+t+"\t") {
				if _, e = a.zfs(ctx, "release", t, name); e != nil {
					return e
				}
			}
		}
	}
	return nil
}
func (a *App) snapshotGUID(ctx context.Context, s string) (string, error) {
	v, e := a.zfs(ctx, "get", "-Hp", "-o", "value", "guid", s)
	return strings.TrimSpace(v), e
}
func (a *App) holdReplica(ctx context.Context, s, tag string) error {
	holds, e := a.zfs(ctx, "holds", "-H", s)
	if e != nil {
		return e
	}
	if strings.Contains(holds, "\t"+tag+"\t") {
		return nil
	}
	_, e = a.zfs(ctx, "hold", tag, s)
	return e
}

type replicaReader struct {
	r      io.Reader
	n      atomic.Int64
	last   time.Time
	update func(int64)
}

func (r *replicaReader) Read(p []byte) (int, error) {
	n, e := r.r.Read(p)
	r.n.Add(int64(n))
	if time.Since(r.last) > time.Second {
		r.last = time.Now()
		r.update(r.n.Load())
	}
	return n, e
}
func (a *App) streamPeer(ctx context.Context, rep Replication, peer PlatformPeer, path string, args []string) (int64, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := command(ctx, "zfs", args...)
	out, e := cmd.StdoutPipe()
	if e != nil {
		return 0, e
	}
	var log cappedBuffer
	cmd.Stderr = &log
	if e = cmd.Start(); e != nil {
		return 0, e
	}
	reader := &replicaReader{r: out, update: func(n int64) { a.DB.Model(&Replication{}).Where("id = ?", rep.ID).Update("bytes", n) }}
	e = a.peerRequest(ctx, peer, "POST", path+"/receive", reader, nil)
	if e != nil {
		cancel()
	}
	sendErr := cmd.Wait()
	if e != nil {
		return reader.n.Load(), e
	}
	if sendErr != nil {
		return reader.n.Load(), fmt.Errorf("ZFS 发送失败: %v: %s", sendErr, log.String())
	}
	return reader.n.Load(), nil
}

// Only platform-managed raw keys may cross the authenticated pinned connection.
func (a *App) replicaEncryptionKey(ctx context.Context, r Repository) ([]byte, error) {
	enc, e := a.zfs(ctx, "get", "-H", "-o", "value", "encryption", r.Dataset)
	if e != nil {
		return nil, e
	}
	if strings.TrimSpace(enc) == "off" {
		return nil, nil
	}
	root, e := a.zfs(ctx, "get", "-H", "-o", "value", "encryptionroot", r.Dataset)
	if e != nil {
		return nil, e
	}
	if e = a.owned(ctx, strings.TrimSpace(root)); e != nil {
		return nil, e
	}
	loc, e := a.zfs(ctx, "get", "-H", "-o", "value", "keylocation", strings.TrimSpace(root))
	if e != nil {
		return nil, e
	}
	path := strings.TrimPrefix(strings.TrimSpace(loc), "file://")
	rel, e := filepath.Rel(a.Config.DataDir, path)
	if e != nil || !strings.HasPrefix(rel, "keys/") || !validBackupFile(rel) {
		return nil, errors.New("加密仓库的密钥不在平台密钥目录，无法自动建立灾备")
	}
	key, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	if len(key) != 32 {
		return nil, errors.New("加密仓库密钥格式无效")
	}
	return key, nil
}
