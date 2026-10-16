package appliance

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type replicaReconcileInput struct {
	Snapshots map[string]string `json:"snapshots"`
	Baseline  string            `json:"baseline"`
}
type replicaReconcileResult struct {
	Deleted   []string `json:"deleted"`
	Protected []string `json:"protected"`
}

// Serialize receive/reseed/prune per destination while allowing unrelated
// repositories and the ordinary mutation worker to keep running.
func (a *App) lockReplicaReceiver(id uint) bool {
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	a.replicaMu.Lock()
	defer a.replicaMu.Unlock()
	if a.replicaReceiving[id] {
		return false
	}
	a.replicaReceiving[id] = true
	return true
}
func (a *App) unlockReplicaReceiver(id uint) {
	a.replicaMu.Lock()
	delete(a.replicaReceiving, id)
	a.replicaMu.Unlock()
}
func (a *App) reconcileReplica(c *gin.Context) {
	r, b, e := a.incomingRepo(c)
	if e != nil {
		failure(c, 404, e)
		return
	}
	var q replicaReconcileInput
	if !body(c, &q) {
		return
	}
	if len(q.Snapshots) == 0 {
		failure(c, 400, errors.New("必须保留有效的异地恢复点"))
		return
	}
	for name, guid := range q.Snapshots {
		if !strings.HasPrefix(name, "bc-") || !snapshotRE.MatchString(name) {
			failure(c, 400, errors.New("快照名称无效"))
			return
		}
		if _, e := strconv.ParseUint(guid, 10, 64); e != nil {
			failure(c, 400, errors.New("快照 GUID 无效"))
			return
		}
	}
	if !a.lockReplicaReceiver(r.ID) {
		failure(c, 409, errors.New("目标仓库正忙，稍后自动重试"))
		return
	}
	defer a.unlockReplicaReceiver(r.ID)
	result, e := a.reconcileReplicaSnapshots(c.Request.Context(), r, b, q)
	if e != nil {
		failure(c, 409, e)
		return
	}
	if len(result.Deleted) > 0 {
		a.audit(0, "", "replica.reconcile", fmt.Sprint(r.ID), fmt.Sprintf("清理 %d 个快照，保留 %d 个保护快照", len(result.Deleted), len(result.Protected)), c.ClientIP())
	}
	c.JSON(200, result)
}
func (a *App) reconcileReplicaSnapshots(ctx context.Context, r Repository, b ReplicaBinding, q replicaReconcileInput) (replicaReconcileResult, error) {
	result := replicaReconcileResult{Deleted: []string{}, Protected: []string{}}
	state, e := a.replicaStatus(ctx, r, b)
	if e != nil {
		return result, e
	}
	if state.ResumeToken != "" {
		return result, errors.New("目标仍有未完成传输，已阻止清理")
	}
	baselineFound := false
	for name, guid := range q.Snapshots {
		if state.Snapshots[name] != guid {
			return result, errors.New("目标快照尚未齐全或 GUID 不匹配，已阻止清理")
		}
		if guid == q.Baseline {
			baselineFound = true
		}
	}
	if !baselineFound {
		return result, errors.New("缺少已确认的增量基线，已阻止清理")
	}
	ss, e := a.snapshots(ctx, r)
	if e != nil {
		return result, e
	}
	for _, s := range ss {
		if e = ctx.Err(); e != nil {
			return result, e
		}
		if !s.Managed || !strings.HasPrefix(s.Name, r.Dataset+"@bc-") {
			continue
		}
		name := strings.SplitN(s.Name, "@", 2)[1]
		if q.Snapshots[name] != "" {
			continue
		}
		if s.Holds > 0 || (s.Clones != "" && s.Clones != "-") {
			result.Protected = append(result.Protected, s.Name)
			continue
		}
		// Recheck protection immediately before deleting; zfs destroy is non-recursive
		// and also refuses a clone/hold created concurrently by a local operator.
		if _, e = a.zfs(ctx, "destroy", s.Name); e != nil {
			current, e2 := a.snapshots(ctx, r)
			protected := false
			for _, v := range current {
				if v.Name == s.Name && (v.Holds > 0 || (v.Clones != "" && v.Clones != "-")) {
					protected = true
				}
			}
			if e2 == nil && protected {
				result.Protected = append(result.Protected, s.Name)
				continue
			}
			return result, e
		}
		result.Deleted = append(result.Deleted, s.Name)
	}
	return result, nil
}
func (a *App) reseedReplica(c *gin.Context) {
	r, b, e := a.incomingRepo(c)
	if e != nil {
		failure(c, 404, e)
		return
	}
	var q struct {
		CatalogHash string `json:"catalog_hash"`
	}
	if !body(c, &q) {
		return
	}
	if len(q.CatalogHash) != 64 {
		failure(c, 400, errors.New("快照目录指纹无效"))
		return
	}
	if !a.lockReplicaReceiver(r.ID) {
		failure(c, 409, errors.New("目标仓库正忙，稍后自动重试"))
		return
	}
	defer a.unlockReplicaReceiver(r.ID)
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	// Reusing the same catalog key is idempotent after lost responses/restarts.
	if b.ReseedKey != q.CatalogHash {
		state, e := a.replicaStatus(c.Request.Context(), r, b)
		if e != nil {
			failure(c, 409, e)
			return
		}
		if state.ResumeToken != "" {
			failure(c, 409, errors.New("请先完成已有续传"))
			return
		}
		if len(state.Snapshots) == 0 {
			c.JSON(200, state)
			return
		}
		suffix := randomPeerToken()[:8]
		suffix = strings.NewReplacer("-", "a", "_", "b").Replace(suffix)
		archive := Repository{Name: r.Name + "-old-" + suffix, Dataset: r.Dataset + "-old-" + suffix, DisplayName: repoLabel(r) + "（历史副本）", Kind: r.Kind, Replica: true, Description: "补齐早期快照前保留的原异地副本；已有恢复仓库继续引用此历史分支"}
		e = a.DB.Transaction(func(tx *gorm.DB) error {
			if e := tx.Create(&archive).Error; e != nil {
				return e
			}
			return tx.Model(&b).Updates(map[string]any{"reseed_key": q.CatalogHash, "archive_repository_id": archive.ID, "state": "archiving"}).Error
		})
		if e != nil {
			failure(c, 500, e)
			return
		}
	}
	if e = a.finishReplicaReseed(c.Request.Context(), r, &b); e != nil {
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

// The archive intent is committed before rename. Retrying after a crash finishes
// the same rename instead of overwriting data or creating duplicate archives.
func (a *App) finishReplicaReseed(ctx context.Context, r Repository, b *ReplicaBinding) error {
	if b.State != "archiving" {
		return nil
	}
	archive, e := a.repo(b.ArchiveRepositoryID)
	if e != nil {
		return e
	}
	_, oldErr := a.zfs(ctx, "list", "-H", r.Dataset)
	_, archiveErr := a.zfs(ctx, "list", "-H", archive.Dataset)
	if archiveErr != nil {
		if oldErr != nil {
			return errors.New("历史副本准备未完成，原数据集不可用")
		}
		if e = a.owned(ctx, r.Dataset); e != nil {
			return e
		}
		if _, e = a.zfs(ctx, "rename", r.Dataset, archive.Dataset); e != nil {
			return e
		}
	} else {
		if e = a.owned(ctx, archive.Dataset); e != nil {
			return e
		}
		if oldErr == nil {
			return errors.New("历史分支与当前数据集同时存在，拒绝覆盖")
		}
	}
	if e = a.DB.Model(b).Updates(map[string]any{"state": "waiting", "last_success_at": nil, "last_error": ""}).Error; e != nil {
		return e
	}
	a.audit(0, "", "replica.archive", fmt.Sprint(r.ID), archive.Name, "")
	return nil
}
func (a *App) recoverReplicaReseed(c *gin.Context, r Repository, b *ReplicaBinding) error {
	if b.State != "archiving" {
		return nil
	}
	if !a.lockReplicaReceiver(r.ID) {
		return errors.New("目标正在准备历史副本")
	}
	defer a.unlockReplicaReceiver(r.ID)
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	return a.finishReplicaReseed(c.Request.Context(), r, b)
}
