package appliance

import (
	"context"
	"errors"
	"fmt"
	"github.com/robfig/cron/v3"
	"sort"
	"strconv"
	"strings"
	"time"
)

func validatePolicy(p Policy) error {
	n := []int{p.KeepHourly, p.KeepDaily, p.KeepWeekly, p.KeepMonthly, p.KeepYearly}
	total := 0
	for _, x := range n {
		if x < 0 || x > 10000 {
			return errors.New("保留数量必须在 0 到 10000 之间")
		}
		total += x
	}
	if total == 0 {
		return errors.New("至少保留一个时间层级，禁止自动清空所有快照")
	}
	return nil
}
func retentionKeep(ss []Snapshot, p Policy) map[string]bool {
	groups := map[string][]Snapshot{}
	for _, s := range ss {
		ds := strings.SplitN(s.Name, "@", 2)[0]
		groups[ds] = append(groups[ds], s)
	}
	result := map[string]bool{}
	for _, group := range groups {
		for name := range retentionKeepOne(group, p) {
			result[name] = true
		}
	}
	return result
}
func retentionKeepOne(ss []Snapshot, p Policy) map[string]bool {
	sort.Slice(ss, func(i, j int) bool {
		if ss[i].CreatedAt.Equal(ss[j].CreatedAt) {
			return ss[i].Txg > ss[j].Txg
		}
		return ss[i].CreatedAt.After(ss[j].CreatedAt)
	})
	keep := map[string]bool{}
	if len(ss) > 0 {
		keep[ss[0].Name] = true
	}
	formats := []string{"2006-01-02T15-0700", "2006-01-02", "week", "2006-01", "2006"}
	counts := []int{p.KeepHourly, p.KeepDaily, p.KeepWeekly, p.KeepMonthly, p.KeepYearly}
	for i, f := range formats {
		seen := map[string]bool{}
		for _, s := range ss {
			if !s.Managed {
				continue
			}
			t := s.CreatedAt.In(time.Local)
			key := t.Format(f)
			if f == "week" {
				y, w := t.ISOWeek()
				key = fmt.Sprintf("%d-%02d", y, w)
			}
			if !seen[key] && len(seen) < counts[i] {
				keep[s.Name] = true
				seen[key] = true
			}
		}
	}
	for _, s := range ss {
		if !s.Managed || s.Holds > 0 || (s.Clones != "-" && s.Clones != "") {
			keep[s.Name] = true
		}
	}
	return keep
}
func (a *App) prune(ctx context.Context, id uint) (string, error) {
	r, err := a.repo(id)
	if err != nil {
		return "", err
	}
	if r.Replica {
		return "", errors.New("异地接收仓库由复制任务维护，禁止独立清理快照")
	}
	if err = a.owned(ctx, r.Dataset); err != nil {
		return "", err
	}
	var p Policy
	if err = a.DB.First(&p, "repository_id = ?", id).Error; err != nil {
		return "", err
	}
	if err = validatePolicy(p); err != nil {
		return "", err
	}
	ss, err := a.snapshots(ctx, r)
	if err != nil {
		return "", err
	}
	keep := retentionKeep(ss, p)
	var reps []Replication
	if err = a.DB.Where("repository_id = ?", id).Find(&reps).Error; err != nil {
		return "", err
	}
	for _, rep := range reps {
		if rep.LastSnapshot != "" {
			keep[rep.LastSnapshot] = true
		}
	}
	n := 0
	for _, s := range ss {
		if !keep[s.Name] && s.Managed {
			if err = a.owned(ctx, strings.SplitN(s.Name, "@", 2)[0]); err != nil {
				return fmt.Sprint(n), err
			}
			if _, err = a.zfs(ctx, "destroy", s.Name); err != nil {
				return fmt.Sprint(n), err
			}
			n++
		}
	}
	return fmt.Sprintf("删除 %d 个过期快照", n), nil
}

// policyCron derives the calendar cadence from the finest retained tier.
func policyCron(p Policy) string {
	switch {
	case p.KeepHourly > 0:
		return "0 * * * *"
	case p.KeepDaily > 0:
		return "0 0 * * *"
	case p.KeepWeekly > 0:
		return "0 0 * * 1"
	case p.KeepMonthly > 0:
		return "0 0 1 * *"
	default:
		return "0 0 1 1 *"
	}
}
func policyPeriod(p Policy, t time.Time) string {
	t = t.In(time.Local)
	switch {
	case p.KeepHourly > 0:
		return t.Format("2006-01-02T15-0700")
	case p.KeepDaily > 0:
		return t.Format("2006-01-02")
	case p.KeepWeekly > 0:
		y, w := t.ISOWeek()
		return fmt.Sprintf("%d-W%02d", y, w)
	case p.KeepMonthly > 0:
		return t.Format("2006-01")
	default:
		return t.Format("2006")
	}
}
func policyView(p Policy) map[string]any {
	b := map[string]any{"id": p.ID, "repository_id": p.RepositoryID, "enabled": p.Enabled,
		"keep_hourly": p.KeepHourly, "keep_daily": p.KeepDaily, "keep_weekly": p.KeepWeekly,
		"keep_monthly": p.KeepMonthly, "keep_yearly": p.KeepYearly, "recursive": p.Recursive,
		"timezone": time.Local.String()}
	labels := map[string]string{"0 * * * *": "每小时整点", "0 0 * * *": "每天零点", "0 0 * * 1": "每周一零点", "0 0 1 * *": "每月 1 日零点", "0 0 1 1 *": "每年 1 月 1 日零点"}
	b["schedule_label"] = labels[policyCron(p)]
	if p.Enabled {
		if schedule, err := cron.ParseStandard(policyCron(p)); err == nil {
			b["next_run_at"] = schedule.Next(time.Now())
		}
	}
	return b
}
func (a *App) signalPolicies() {
	select {
	case a.policyWake <- struct{}{}:
	default:
	}
}
func (a *App) policyScheduler() {
	defer a.wg.Done()
	a.reconcilePolicies()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-a.policyWake:
			a.reconcilePolicies()
		}
	}
}
func policyPeriodProtected(ss []Snapshot, r Repository, p Policy, now time.Time) bool {
	for _, s := range ss {
		if s.Managed && strings.SplitN(s.Name, "@", 2)[0] == r.Dataset && policyPeriod(p, s.CreatedAt) == policyPeriod(p, now) {
			return true
		}
	}
	return false
}
func (a *App) reconcilePolicies() {
	var policies []Policy
	if err := a.DB.Where("enabled = ?", true).Find(&policies).Error; err != nil {
		return
	}
	for _, p := range policies {
		if a.ctx.Err() != nil {
			return
		}
		ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
		err := a.ensurePolicyPeriod(ctx, p, time.Now())
		cancel()
		if err != nil {
			a.raise("schedule-"+strconv.Itoa(int(p.ID)), "warning", err.Error())
		}
	}
}
func (a *App) ensurePolicyPeriod(ctx context.Context, p Policy, now time.Time) error {
	if !p.Enabled {
		return nil
	}
	if err := validatePolicy(p); err != nil {
		return err
	}
	var task SyncTask
	if a.DB.Where("repository_id = ?", p.RepositoryID).First(&task).Error == nil {
		if a.syncBusyRepo(p.RepositoryID) || task.CurrentGeneration == "" || task.LastSnapshotGeneration == task.CurrentGeneration || task.LastSuccessAt == nil || policyPeriod(p, *task.LastSuccessAt) != policyPeriod(p, now) {
			return nil
		}
	}
	r, err := a.repo(p.RepositoryID)
	if err != nil {
		return err
	}
	ss, err := a.snapshots(ctx, r)
	if err != nil {
		return err
	}
	if policyPeriodProtected(ss, r, p, now) {
		return nil
	}
	var pending int64
	if err := a.DB.Model(&Job{}).Where("kind = ? AND target = ? AND status IN ?", "snapshot.scheduled", strconv.Itoa(int(p.RepositoryID)), []string{"queued", "running"}).Count(&pending).Error; err != nil {
		return err
	}
	if pending > 0 {
		return nil
	}
	_, err = a.enqueue("snapshot.scheduled", strconv.Itoa(int(p.RepositoryID)), ActionRequest{RepositoryID: p.RepositoryID, Recursive: p.Recursive}, 0)
	return err
}
func (a *App) reloadSchedules() error {
	a.scheduleMu.Lock()
	defer a.scheduleMu.Unlock()
	for _, entry := range a.cron.Entries() {
		a.cron.Remove(entry.ID)
	}
	// Normalize legacy schedules: retention tiers are the sole source of cadence.
	var policies []Policy
	if err := a.DB.Find(&policies).Error; err != nil {
		return err
	}
	for _, p := range policies {
		if err := validatePolicy(p); err != nil {
			return err
		}
		if p.Cron != policyCron(p) {
			if err := a.DB.Model(&p).UpdateColumn("cron", policyCron(p)).Error; err != nil {
				return err
			}
		}
	}
	// Reconcile each minute so startup, downtime and failed attempts cannot leave
	// the current calendar period unprotected. enqueue deduplicates active work.
	if _, err := a.cron.AddFunc("* * * * *", a.signalPolicies); err != nil {
		return err
	}

	return nil
}
func (a *App) execute(j Job) (string, error) {
	ctx, cancel := context.WithTimeout(a.ctx, 24*time.Hour)
	defer cancel()
	switch j.Kind {
	case "repository.create":
		var q RepoRequest
		if err := jsonDecode(j.Payload, &q); err != nil {
			return "", err
		}
		return a.createRepo(ctx, q)
	case "repository.properties":
		var q RepoRequest
		if err := jsonDecode(j.Payload, &q); err != nil {
			return "", err
		}
		r, err := a.repo(q.ID)
		if err != nil {
			return "", err
		}
		if r.Replica {
			return "", errors.New("接收仓库属性由异地复制维护")
		}
		if a.syncBusyRepo(r.ID) {
			return "", errors.New("该仓库正在同步，请稍后修改属性")
		}
		if r.Kind == "volume" {
			for k := range q.Properties {
				if k == "quota" || k == "reservation" || k == "recordsize" || k == "atime" {
					return "", errors.New("块卷不支持文件系统配额、recordsize 或 atime 属性")
				}
			}
		}
		if err = validateProperties(q.Properties); err != nil {
			return "", err
		}
		if err = a.owned(ctx, r.Dataset); err != nil {
			return "", err
		}
		args := []string{"set"}
		for k, v := range q.Properties {
			args = append(args, k+"="+v)
		}
		if len(args) == 1 {
			return "", errors.New("属性不能为空")
		}
		args = append(args, r.Dataset)
		return a.zfs(ctx, args...)
	case "snapshot.create", "snapshot.scheduled":
		var q ActionRequest
		if err := jsonDecode(j.Payload, &q); err != nil {
			return "", err
		}
		if j.Kind == "snapshot.scheduled" {
			var p Policy
			if err := a.DB.First(&p, "repository_id = ?", q.RepositoryID).Error; err != nil {
				return "策略已删除，跳过自动快照", nil
			}
			if !p.Enabled {
				return "策略已暂停，跳过自动快照", nil
			}
			r, err := a.repo(p.RepositoryID)
			if err != nil {
				return "", err
			}
			ss, err := a.snapshots(ctx, r)
			if err != nil {
				return "", err
			}
			if policyPeriodProtected(ss, r, p, time.Now()) {
				return a.prune(ctx, q.RepositoryID)
			}
			q.Recursive = p.Recursive
		}
		s, err := a.createSnapshot(ctx, q)
		if err == nil && j.Kind == "snapshot.scheduled" {
			_, err = a.prune(ctx, q.RepositoryID)
		}
		return s, err
	case "retention.prune":
		var id uint
		if err := jsonDecode(j.Payload, &id); err != nil {
			return "", err
		}
		return a.prune(ctx, id)
	case "repository.delete", "snapshot.delete", "snapshot.hold", "snapshot.release", "restore.clone", "restore.rollback":
		var q ActionRequest
		if err := jsonDecode(j.Payload, &q); err != nil {
			return "", err
		}
		return a.action(ctx, j.Kind, q)
	case "export.apply":
		var q Export
		if err := jsonDecode(j.Payload, &q); err != nil {
			return "", err
		}
		return "共享访问设置已应用", a.applyExport(ctx, q)
	case "replicate":
		var id uint
		if err := jsonDecode(j.Payload, &id); err != nil {
			return "", err
		}
		return a.replicate(ctx, id)
	case "pool.scrub":
		return a.Run.Run(ctx, "zpool", "scrub", a.Config.Pool)
	case "key.load", "key.unload":
		var id uint
		if err := jsonDecode(j.Payload, &id); err != nil {
			return "", err
		}
		r, err := a.repo(id)
		if err != nil {
			return "", err
		}
		if r.Replica {
			return "", errors.New("接收仓库属性由异地复制维护")
		}
		if a.syncBusyRepo(r.ID) {
			return "", errors.New("仓库正在同步，请先暂停并等待任务停止")
		}
		if err = a.owned(ctx, r.Dataset); err != nil {
			return "", err
		}
		if j.Kind == "key.load" {
			if _, err = a.zfs(ctx, "load-key", r.Dataset); err != nil {
				return "", err
			}
			if r.Kind == "filesystem" {
				return a.zfs(ctx, "mount", r.Dataset)
			}
			return "loaded", nil
		}
		var n int64
		a.DB.Model(&Export{}).Where("repository_id = ? AND enabled = ?", id, true).Count(&n)
		if n > 0 {
			return "", errors.New("先关闭共享访问")
		}
		if r.Kind == "filesystem" {
			if _, err = a.zfs(ctx, "unmount", r.Dataset); err != nil {
				return "", err
			}
		}
		return a.zfs(ctx, "unload-key", r.Dataset)
	}
	return "", errors.New("不支持的任务类型")
}
