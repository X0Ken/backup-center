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
	if _, e := cron.ParseStandard(p.Cron); e != nil {
		return fmt.Errorf("Cron 表达式无效: %w", e)
	}
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
	formats := []string{"2006-01-02T15", "2006-01-02", "week", "2006-01", "2006"}
	counts := []int{p.KeepHourly, p.KeepDaily, p.KeepWeekly, p.KeepMonthly, p.KeepYearly}
	for i, f := range formats {
		seen := map[string]bool{}
		for _, s := range ss {
			if !s.Managed {
				continue
			}
			t := s.CreatedAt.UTC()
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
func (a *App) reloadSchedules() error {
	a.scheduleMu.Lock()
	defer a.scheduleMu.Unlock()
	for _, entry := range a.cron.Entries() {
		a.cron.Remove(entry.ID)
	}
	var policies []Policy
	if e := a.DB.Where("enabled = ?", true).Find(&policies).Error; e != nil {
		return e
	}
	for _, p := range policies {
		p := p
		if err := validatePolicy(p); err != nil {
			return err
		}
		_, e := a.cron.AddFunc(p.Cron, func() {
			_, err := a.enqueue("snapshot.scheduled", strconv.Itoa(int(p.RepositoryID)), ActionRequest{RepositoryID: p.RepositoryID, Recursive: p.Recursive}, 0)
			if err != nil {
				a.raise("schedule-"+strconv.Itoa(int(p.ID)), "warning", err.Error())
			}
		})
		if e != nil {
			return e
		}
	}
	var reps []Replication
	if e := a.DB.Where("enabled = ? AND cron <> ?", true, "").Find(&reps).Error; e != nil {
		return e
	}
	for _, rep := range reps {
		rep := rep
		if _, err := a.cron.AddFunc(rep.Cron, func() { a.enqueue("replicate", strconv.Itoa(int(rep.ID)), rep.ID, 0) }); err != nil {
			return err
		}
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
		return "协议配置已应用", a.applyExport(ctx, q)
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
			return "", errors.New("先关闭协议导出")
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
