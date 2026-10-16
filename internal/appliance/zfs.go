package appliance

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func marshal(v any) ([]byte, error) { return json.Marshal(v) }

type PoolInfo struct {
	Name                  string `json:"name"`
	Size, Allocated, Free uint64
	Health                string `json:"health"`
	Capacity              string `json:"capacity"`
}
type DatasetInfo struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Used        uint64 `json:"used"`
	Available   uint64 `json:"available"`
	Referenced  uint64 `json:"referenced"`
	Mountpoint  string `json:"mountpoint"`
	Compression string `json:"compression"`
	Ratio       string `json:"ratio"`
	Quota       string `json:"quota"`
	Recordsize  string `json:"recordsize"`
	Encryption  string `json:"encryption"`
	KeyStatus   string `json:"key_status"`
	Origin      string `json:"origin"`
}
type Snapshot struct {
	Txg          uint64    `json:"txg"`
	Name         string    `json:"name"`
	RepositoryID uint      `json:"repository_id"`
	CreatedAt    time.Time `json:"created_at"`
	Used         uint64    `json:"used"`
	Referenced   uint64    `json:"referenced"`
	Clones       string    `json:"clones"`
	Holds        uint64    `json:"holds"`
	Managed      bool      `json:"managed"`
}

func (a *App) zfs(ctx context.Context, args ...string) (string, error) {
	return a.Run.Run(ctx, "zfs", args...)
}
func (a *App) pools(ctx context.Context) ([]PoolInfo, error) {
	out, err := a.Run.Run(ctx, "zpool", "list", "-Hp", "-o", "name,size,allocated,free,health,capacity", a.Config.Pool)
	if err != nil {
		return nil, err
	}
	p := []PoolInfo{}
	for _, f := range fields(out) {
		if len(f) != 6 {
			return nil, errors.New("invalid zpool output")
		}
		p = append(p, PoolInfo{f[0], number(f[1]), number(f[2]), number(f[3]), f[4], f[5]})
	}
	return p, nil
}
func (a *App) ensureRoot(ctx context.Context) error {
	if _, err := a.Run.Run(ctx, "zpool", "list", "-H", a.Config.Pool); err != nil {
		return fmt.Errorf("备份池不可用: %w", err)
	}
	if _, err := a.zfs(ctx, "list", "-H", a.root); err != nil {
		_, err = a.zfs(ctx, "create", "-o", "mountpoint=/srv/backup-center", "-o", "compression=lz4", "-o", "atime=off", "-o", "sync=standard", "-o", "org.backup-center:managed=yes", a.root)
		if err != nil {
			return err
		}
	}
	if err := a.owned(ctx, a.root); err != nil {
		return err
	}
	if _, err := a.zfs(ctx, "list", "-H", a.root+"/repos"); err != nil {
		_, err = a.zfs(ctx, "create", "-o", "org.backup-center:managed=yes", a.root+"/repos")
		return err
	}
	return a.owned(ctx, a.root+"/repos")
}
func (a *App) owned(ctx context.Context, ds string) error {
	if ds != a.root && !strings.HasPrefix(ds, a.root+"/") {
		return errors.New("拒绝访问平台管理范围外的数据集")
	}
	v, err := a.zfs(ctx, "get", "-H", "-o", "value", "org.backup-center:managed", ds)
	if err != nil {
		return err
	}
	if strings.TrimSpace(v) != "yes" {
		return errors.New("数据集缺少平台所有权标记")
	}
	return nil
}
func (a *App) dataset(ctx context.Context, r Repository) (DatasetInfo, error) {
	var d DatasetInfo
	out, err := a.zfs(ctx, "list", "-Hp", "-o", "name,type,used,available,referenced,mountpoint,compression,compressratio,quota,recordsize,encryption,keystatus,origin", r.Dataset)
	if err != nil {
		return d, err
	}
	f := strings.Split(strings.TrimSpace(out), "\t")
	if len(f) != 13 {
		return d, errors.New("invalid dataset output")
	}
	d = DatasetInfo{f[0], f[1], number(f[2]), number(f[3]), number(f[4]), f[5], f[6], f[7], f[8], f[9], f[10], f[11], f[12]}
	return d, nil
}
func (a *App) snapshots(ctx context.Context, r Repository) ([]Snapshot, error) {
	out, err := a.zfs(ctx, "list", "-Hp", "-r", "-t", "snapshot", "-o", "name,creation,used,referenced,clones,userrefs,createtxg", "-s", "creation", r.Dataset)
	if err != nil {
		return nil, err
	}
	ss := []Snapshot{}
	for _, f := range fields(out) {
		if len(f) != 7 {
			return nil, errors.New("invalid snapshot output")
		}
		ss = append(ss, Snapshot{Name: f[0], RepositoryID: r.ID, CreatedAt: time.Unix(int64(number(f[1])), 0), Used: number(f[2]), Referenced: number(f[3]), Clones: f[4], Holds: number(f[5]), Txg: number(f[6]), Managed: strings.Contains(f[0], "@bc-") && (strings.HasPrefix(f[0], r.Dataset+"@") || strings.HasPrefix(f[0], r.Dataset+"/"))})
	}
	return ss, nil
}
func validSnapshot(r Repository, s string) error {
	p := strings.Split(s, "@")
	if len(p) != 2 || (p[0] != r.Dataset && (!strings.HasPrefix(p[0], r.Dataset+"/") || !datasetRE.MatchString(p[0]))) || !snapshotRE.MatchString(p[1]) {
		return errors.New("快照路径无效或不属于此仓库")
	}
	return nil
}

var sizeRE = regexp.MustCompile(`^[1-9][0-9]{0,14}[KMGT]?$`)

func validateProperties(p map[string]string) error {
	for k, v := range p {
		switch k {
		case "compression":
			if v != "lz4" && v != "zstd" && v != "off" {
				return errors.New("compression 仅支持 lz4/zstd/off")
			}
		case "recordsize":
			if !contains([]string{"16K", "32K", "64K", "128K", "256K", "512K", "1M"}, v) {
				return errors.New("recordsize 无效")
			}
		case "sync":
			if v != "standard" && v != "always" {
				return errors.New("为保护备份一致性，sync 仅支持 standard/always")
			}
		case "atime":
			if v != "on" && v != "off" {
				return errors.New("atime 无效")
			}
		case "quota", "reservation":
			if v != "none" && !sizeRE.MatchString(v) {
				return errors.New("容量应为正整数和 K/M/G/T 单位或 none")
			}
		default:
			return fmt.Errorf("不允许修改属性 %s", k)
		}
	}
	return nil
}
func contains(v []string, s string) bool {
	for _, x := range v {
		if x == s {
			return true
		}
	}
	return false
}

type RepoRequest struct {
	OwnerUID    uint32            `json:"owner_uid"`
	OwnerGID    uint32            `json:"owner_gid"`
	ID          uint              `json:"id"`
	Name        string            `json:"name"`
	Kind        string            `json:"kind"`
	Description string            `json:"description"`
	Properties  map[string]string `json:"properties"`
	VolumeSize  string            `json:"volume_size"`
	Encrypted   bool              `json:"encrypted"`
}
type ActionRequest struct {
	RepositoryID       uint    `json:"repository_id"`
	Snapshot           string  `json:"snapshot"`
	OtherSnapshot      string  `json:"other_snapshot"`
	Name               string  `json:"name"`
	Recursive          bool    `json:"recursive"`
	Confirm            string  `json:"confirm"`
	AcknowledgeOffline bool    `json:"acknowledge_offline"`
	Export             *Export `json:"export,omitempty"`
}

func (a *App) createRepo(ctx context.Context, q RepoRequest) (string, error) {
	if err := validName(q.Name); err != nil {
		return "", err
	}
	if err := validateProperties(q.Properties); err != nil {
		return "", err
	}
	if q.Kind != "filesystem" && q.Kind != "volume" {
		return "", errors.New("仓库类型必须为 filesystem 或 volume")
	}
	if err := a.ensureRoot(ctx); err != nil {
		return "", err
	}
	r := Repository{Name: q.Name, Dataset: a.root + "/repos/" + q.Name, Kind: q.Kind, Description: q.Description}
	var count int64
	a.DB.Model(&Repository{}).Where("name = ?", q.Name).Count(&count)
	if count > 0 {
		return "", errors.New("仓库名称已存在")
	}
	args := []string{"create", "-o", "org.backup-center:managed=yes"}
	if q.Kind == "volume" {
		if !sizeRE.MatchString(q.VolumeSize) {
			return "", errors.New("volume_size 无效")
		}
		args = append(args, "-V", q.VolumeSize, "-o", "volblocksize=16K")
		delete(q.Properties, "recordsize")
		delete(q.Properties, "quota")
		delete(q.Properties, "reservation")
		delete(q.Properties, "atime")
	}
	for k, v := range q.Properties {
		args = append(args, "-o", k+"="+v)
	}
	if q.Encrypted {
		path := filepath.Join(a.Config.DataDir, "keys", q.Name+".key")
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return "", err
		}
		if _, err := os.Stat(path); err == nil {
			return "", errors.New("同名加密密钥已存在，拒绝覆盖")
		}
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return "", err
		}
		if err := os.WriteFile(path, key, 0600); err != nil {
			return "", err
		}
		args = append(args, "-o", "encryption=aes-256-gcm", "-o", "keyformat=raw", "-o", "keylocation=file://"+path)
	}
	args = append(args, r.Dataset)
	if _, err := a.zfs(ctx, args...); err != nil {
		return "", err
	}
	if q.Kind == "filesystem" {
		d, e := a.dataset(ctx, r)
		if e != nil {
			return r.Dataset, e
		}
		if d.Mountpoint != "/srv/backup-center/repos/"+r.Name {
			return r.Dataset, errors.New("unexpected dataset mountpoint")
		}
		uid, gid := q.OwnerUID, q.OwnerGID
		if uid == 0 {
			uid = 1000
		}
		if gid == 0 {
			gid = 1000
		}
		if e = os.Chown(d.Mountpoint, int(uid), int(gid)); e != nil {
			return r.Dataset, e
		}
		if e = os.Chmod(d.Mountpoint, 0770); e != nil {
			return r.Dataset, e
		}
	}
	if err := a.DB.Create(&r).Error; err != nil {
		return "", fmt.Errorf("ZFS 仓库已创建但元数据写入失败，请人工核对: %w", err)
	}
	return r.Dataset, nil
}
func (a *App) createSnapshot(ctx context.Context, q ActionRequest) (string, error) {
	r, err := a.repo(q.RepositoryID)
	if err != nil {
		return "", err
	}
	if err = a.owned(ctx, r.Dataset); err != nil {
		return "", err
	}
	name := "bc-" + time.Now().UTC().Format("20060102T150405.000000000Z")
	if q.Name != "" {
		if err = validName(q.Name); err != nil {
			return "", err
		}
		name = "bc-" + q.Name
	}
	args := []string{"snapshot"}
	if q.Recursive {
		children, e := a.zfs(ctx, "list", "-H", "-r", "-t", "filesystem,volume", "-o", "name", r.Dataset)
		if e != nil {
			return "", e
		}
		for _, child := range strings.Fields(children) {
			if e = a.owned(ctx, child); e != nil {
				return "", e
			}
		}
		args = append(args, "-r")
	}
	s := r.Dataset + "@" + name
	args = append(args, s)
	if _, err = a.zfs(ctx, args...); err != nil {
		return "", err
	}
	var reps []Replication
	a.DB.Where("repository_id = ? AND enabled = ? AND after_snapshot = ?", r.ID, true, true).Find(&reps)
	for _, rep := range reps {
		if _, e := a.enqueue("replicate", strconv.Itoa(int(rep.ID)), rep.ID, 0); e != nil {
			return s, e
		}
	}
	return s, nil
}
func (a *App) action(ctx context.Context, kind string, q ActionRequest) (string, error) {
	r, err := a.repo(q.RepositoryID)
	if err != nil {
		return "", err
	}
	if err = a.owned(ctx, r.Dataset); err != nil {
		return "", err
	}
	if kind == "repository.delete" {
		if q.Confirm != r.Name {
			return "", errors.New("确认名称不匹配")
		}
		var n int64
		a.DB.Model(&Export{}).Where("repository_id = ? AND enabled = ?", r.ID, true).Count(&n)
		if n > 0 {
			return "", errors.New("先关闭此仓库的共享访问")
		}
		ss, e := a.snapshots(ctx, r)
		if e != nil {
			return "", e
		}
		if len(ss) > 0 {
			return "", errors.New("仓库仍有快照，请先显式处理快照和克隆依赖")
		}
		if _, err = a.zfs(ctx, "destroy", r.Dataset); err != nil {
			return "", err
		}
		err = a.DB.Transaction(func(tx *gorm.DB) error {
			if err := tx.Where("repository_id = ?", r.ID).Delete(&Policy{}).Error; err != nil {
				return err
			}
			if err := tx.Where("repository_id = ?", r.ID).Delete(&Export{}).Error; err != nil {
				return err
			}
			if err := tx.Where("repository_id = ?", r.ID).Delete(&Replication{}).Error; err != nil {
				return err
			}
			return tx.Delete(&r).Error
		})
		if err != nil {
			return r.Dataset, err
		}
		return r.Dataset, a.reloadSchedules()
	}
	if err = validSnapshot(r, q.Snapshot); err != nil {
		return "", err
	}
	if err = a.owned(ctx, strings.SplitN(q.Snapshot, "@", 2)[0]); err != nil {
		return "", err
	}
	switch kind {
	case "snapshot.delete":
		if q.Confirm != q.Snapshot {
			return "", errors.New("请输入完整快照名称确认")
		}
		_, err = a.zfs(ctx, "destroy", q.Snapshot)
	case "snapshot.hold":
		_, err = a.zfs(ctx, "hold", "bc-protect", q.Snapshot)
	case "snapshot.release":
		_, err = a.zfs(ctx, "release", "bc-protect", q.Snapshot)
	case "restore.clone":
		if err = validName(q.Name); err != nil {
			return "", err
		}
		target := a.root + "/repos/" + q.Name
		if _, err = a.zfs(ctx, "clone", "-o", "org.backup-center:managed=yes", q.Snapshot, target); err != nil {
			return "", err
		}
		clone := Repository{Name: q.Name, Dataset: target, Kind: r.Kind, Description: "恢复自 " + q.Snapshot}
		if err = a.DB.Create(&clone).Error; err != nil {
			return "", err
		}
		if q.Export != nil {
			q.Export.RepositoryID = clone.ID
			if err = a.applyExport(ctx, *q.Export); err != nil {
				return target, err
			}
		}
		return target, nil
	case "restore.rollback":
		if q.Confirm != r.Name || !q.AcknowledgeOffline {
			return "", errors.New("必须输入仓库名并确认已停止所有客户端写入")
		}
		var n int64
		a.DB.Model(&Export{}).Where("repository_id = ? AND enabled = ?", r.ID, true).Count(&n)
		if n > 0 {
			return "", errors.New("回滚前必须关闭共享访问")
		}
		_, err = a.zfs(ctx, "rollback", q.Snapshot)
	default:
		return "", errors.New("未知快照操作")
	}
	return q.Snapshot, err
}

// Count only registered repository trees; unrelated snapshots are not backup points.
func (a *App) snapshotCount(ctx context.Context) (*int, string) {
	var repos []Repository
	if err := a.DB.Find(&repos).Error; err != nil {
		return nil, err.Error()
	}
	count := 0
	if len(repos) == 0 {
		return &count, ""
	}
	names := map[string]bool{}
	for _, r := range repos {
		names[r.Dataset] = true
	}
	out, err := a.zfs(ctx, "list", "-H", "-r", "-t", "snapshot", "-o", "name", a.root+"/repos")
	if err != nil {
		return nil, err.Error()
	}
	for _, line := range strings.Fields(out) {
		ds, _, ok := strings.Cut(line, "@")
		if !ok {
			return nil, "快照计数返回格式无效"
		}
		for strings.HasPrefix(ds, a.root+"/repos/") {
			if names[ds] {
				count++
				break
			}
			cut := strings.LastIndex(ds, "/")
			if cut < 0 {
				break
			}
			ds = ds[:cut]
		}
	}
	return &count, ""
}
