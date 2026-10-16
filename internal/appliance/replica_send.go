package appliance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

func (a *App) replicationScheduler() {
	defer a.wg.Done()
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	// Legacy SSH targets cannot meet same-platform trust; retain their records for
	// inspection, but never continue them silently after upgrading.
	a.DB.Model(&Replication{}).Where("peer_id = ?", 0).Updates(map[string]any{"enabled": false, "cron": "", "state": "legacy", "last_error": "请通过系统管理配对目标平台后重新创建任务"})
	a.DB.Model(&Replication{}).Where("peer_id <> ?", 0).Updates(map[string]any{"cron": "", "after_snapshot": true})
	for {
		a.dispatchReplications()
		select {
		case <-a.ctx.Done():
			return
		case <-tick.C:
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
	a.DB.Where("peer_id <> ? AND enabled = ? AND next_attempt_at <= ?", 0, true, time.Now()).Order("id").Find(&reps)
	for _, rep := range reps {
		needed := rep.RemoteRepositoryID == 0 || rep.State == "retrying" || rep.State == "sending" || rep.TransferSnapshot != ""
		if !needed {
			r, e := a.repo(rep.RepositoryID)
			if e != nil {
				continue
			}
			ss, e := a.snapshots(a.ctx, r)
			if e != nil {
				continue
			}
			var latest Snapshot
			for _, s := range ss {
				if s.Managed && strings.HasPrefix(s.Name, r.Dataset+"@bc-") && s.Txg >= latest.Txg {
					latest = s
				}
			}
			needed = latest.Name != "" && latest.Name != rep.LastSnapshot
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
		a.DB.Model(&j).Updates(map[string]any{"status": "interrupted", "error": "任务已暂停或需要迁移"})
		return
	}
	ctx, cancel := context.WithTimeout(a.ctx, 7*24*time.Hour)
	a.replicaMu.Lock()
	a.replicaActive[id] = cancel
	a.replicaMu.Unlock()
	now := time.Now()
	a.DB.Model(&j).Updates(map[string]any{"status": "running", "started_at": now})
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		defer cancel()
		defer func() { a.replicaMu.Lock(); delete(a.replicaActive, id); a.replicaMu.Unlock() }()
		result, e := a.replicate(ctx, id)
		finish := time.Now()
		status, msg := "succeeded", ""
		if e != nil {
			status = "failed"
			msg = e.Error()
			if ctx.Err() != nil {
				status = "interrupted"
			}
			shift := rep.Failures
			if shift > 6 {
				shift = 6
			}
			delay := 30 * time.Second * time.Duration(1<<shift)
			if delay > 30*time.Minute {
				delay = 30 * time.Minute
			}
			a.DB.Model(&Replication{}).Where("id = ?", id).Updates(map[string]any{"state": "retrying", "last_error": msg, "failures": rep.Failures + 1, "next_attempt_at": finish.Add(delay)})
			if status == "failed" {
				a.raise("replication-"+fmt.Sprint(id), "warning", msg)
			}
		} else {
			a.DB.Model(&Replication{}).Where("id = ?", id).Updates(map[string]any{"last_error": "", "failures": 0, "next_attempt_at": finish.Add(5 * time.Second)})
			a.DB.Model(&Alert{}).Where("key = ?", "replication-"+fmt.Sprint(id)).Update("active", false)
		}
		a.DB.Model(&j).Updates(map[string]any{"status": status, "error": msg, "result": result, "finished_at": finish})
		a.audit(j.UserID, "", "replicate."+status, fmt.Sprint(id), msg, "")
	}()
}
func (a *App) replicate(ctx context.Context, id uint) (string, error) {
	var rep Replication
	if e := a.DB.First(&rep, id).Error; e != nil {
		return "", e
	}
	if rep.PeerID == 0 {
		return "", errors.New("旧版复制任务需要重新配对目标平台")
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
	var transferred int64
	defer func() {
		a.DB.Model(&Replication{}).Where("id = ?", id).Updates(map[string]any{"last_run_at": start, "duration_seconds": time.Since(start).Seconds(), "bytes": transferred})
	}()
	if rep.RemoteRepositoryID == 0 {
		a.DB.Model(&rep).Update("state", "provisioning")
		key, err := a.replicaEncryptionKey(ctx, repo)
		if err != nil {
			return "", err
		}
		raw, _ := json.Marshal(map[string]any{"task_key": "task-" + strconv.Itoa(int(id)), "name": rep.RemoteName, "kind": repo.Kind, "encryption_key": key})
		var state replicaRemoteState
		short, cancel := context.WithTimeout(ctx, 30*time.Second)
		e = a.peerRequest(short, peer, "POST", "/repositories", strings.NewReader(string(raw)), &state)
		cancel()
		if e != nil {
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
	short, cancel := context.WithTimeout(ctx, 30*time.Second)
	e = a.peerRequest(short, peer, "GET", path, nil, &remote)
	cancel()
	if e != nil {
		return "", e
	}
	if remote.ResumeToken != "" {
		a.DB.Model(&rep).Update("state", "sending")
		n, e := a.streamPeer(ctx, rep, peer, path, []string{"send", "-t", remote.ResumeToken})
		transferred += n
		if e != nil {
			return "", e
		}
		short, cancel = context.WithTimeout(ctx, 30*time.Second)
		e = a.peerRequest(short, peer, "GET", path, nil, &remote)
		cancel()
		if e != nil {
			return "", e
		}
	}
	// A transfer hold survives failures/restarts until the remote has acknowledged
	// its GUID, so a resume token always retains its matching source snapshot.

	ss, e := a.snapshots(ctx, repo)
	if e != nil {
		return "", e
	}
	var managed []Snapshot
	for _, s := range ss {
		if s.Managed && strings.HasPrefix(s.Name, repo.Dataset+"@bc-") {
			managed = append(managed, s)
		}
	}
	if len(managed) == 0 {
		a.DB.Model(&rep).Update("state", "waiting")
		return "目标仓库已登记，等待首个快照", nil
	}
	sort.Slice(managed, func(i, j int) bool { return managed[i].Txg < managed[j].Txg })
	latest := managed[len(managed)-1]
	guid, e := a.snapshotGUID(ctx, latest.Name)
	if e != nil {
		return "", e
	}
	latestName := strings.SplitN(latest.Name, "@", 2)[1]
	if remote.Snapshots[latestName] != guid {
		base := ""
		for _, s := range managed {
			g, e := a.snapshotGUID(ctx, s.Name)
			if e != nil {
				return "", e
			}
			if remote.Snapshots[strings.SplitN(s.Name, "@", 2)[1]] == g {
				base = s.Name
			}
		}
		// Acknowledged older interrupted transfer can now be released after the base
		// hold below has been set, including when newer snapshots arrived meanwhile.
		a.mutationMu.Lock()
		if base != "" {
			e = a.holdReplica(ctx, base, fmt.Sprintf("bc-base-%d", id))
			if e != nil {
				a.mutationMu.Unlock()
				return "", e
			}
		}
		e = a.holdReplica(ctx, latest.Name, fmt.Sprintf("bc-transfer-%d", id))
		if e == nil {
			e = a.DB.Model(&rep).Updates(map[string]any{"transfer_snapshot": latest.Name, "state": "sending"}).Error
		}
		a.mutationMu.Unlock()
		if e != nil {
			return "", e
		}
		args := []string{"send", "-w"}
		if base != "" {
			args = append(args, "-i", base)
		}
		args = append(args, latest.Name)
		n, e := a.streamPeer(ctx, rep, peer, path, args)
		transferred += n
		if e != nil {
			return "", e
		}
		short, cancel = context.WithTimeout(ctx, 30*time.Second)
		e = a.peerRequest(short, peer, "GET", path, nil, &remote)
		cancel()
		if e != nil {
			return "", e
		}
	}
	if remote.Snapshots[latestName] != guid {
		return "", errors.New("复制后快照 GUID 校验失败，未推进基线")
	}
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	if e = a.holdReplica(ctx, latest.Name, fmt.Sprintf("bc-base-%d", id)); e != nil {
		return "", e
	}
	if e = a.DB.Model(&rep).Updates(map[string]any{"last_snapshot": latest.Name, "last_guid": guid, "transfer_snapshot": "", "state": "ready"}).Error; e != nil {
		return "", e
	}
	// Release only this task's obsolete holds after durable acknowledgement.
	for _, s := range managed {
		holds, e := a.zfs(ctx, "holds", "-H", s.Name)
		if e != nil {
			continue
		}
		for _, tag := range []string{fmt.Sprintf("bc-base-%d", id), fmt.Sprintf("bc-transfer-%d", id)} {
			if (s.Name != latest.Name || strings.HasPrefix(tag, "bc-transfer-")) && strings.Contains(holds, "\t"+tag+"\t") {
				_, _ = a.zfs(ctx, "release", tag, s.Name)
			}
		}
	}
	return fmt.Sprintf("远端仓库 #%d，快照 GUID 校验完成，传输 %d 字节", rep.RemoteRepositoryID, transferred), nil
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
