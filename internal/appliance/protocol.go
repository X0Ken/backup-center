package appliance

import (
	"context"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// Access protocol belongs to the repository type, never to user selection.
func normalizeExport(e Export, r Repository, existingIQN ...string) (Export, error) {
	protocol := "nfs"
	if r.Kind == "volume" {
		protocol = "iscsi"
	} else if r.Kind != "filesystem" {
		return e, errors.New("不支持的仓库类型")
	}
	if e.Protocol != "" && e.Protocol != protocol {
		return e, errors.New("访问方式必须与仓库类型一致")
	}
	e.Protocol = protocol
	if protocol == "nfs" {
		e.IQN = ""
		e.Initiators = ""
	} else {
		e.Clients = ""
		e.ReadOnly = false
		iqn := fmt.Sprintf("iqn.2026-10.local.backup-center:repo-%d-%s", r.ID, strings.ToLower(strings.ReplaceAll(r.Name, "_", "-")))
		if len(existingIQN) > 0 && existingIQN[0] != "" {
			iqn = existingIQN[0]
		}
		if e.IQN != "" && e.IQN != iqn {
			return e, errors.New("目标端 IQN 由平台生成，不能自行填写或修改")
		}
		e.IQN = iqn
	}
	return e, validateExport(e, r)
}
func (a *App) prepareExport(e Export, r Repository) (Export, error) {
	if a.syncBound(r.ID) && e.Enabled && !e.ReadOnly {
		return e, errors.New("数据同步仓库只允许只读共享访问")
	}
	var old Export
	if err := a.DB.First(&old, "repository_id = ?", r.ID).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return e, err
	}
	return normalizeExport(e, r, old.IQN)
}
func validateExport(e Export, r Repository) error {
	if e.Protocol == "nfs" {
		if r.Kind != "filesystem" {
			return errors.New("NFS 仅支持文件仓库")
		}
		clients := strings.Fields(e.Clients)
		if len(clients) == 0 || len(clients) > 64 {
			return errors.New("必须明确指定 1 到 64 个客户端 IP/网段")
		}
		for _, c := range clients {
			if net.ParseIP(c) == nil {
				if _, _, err := net.ParseCIDR(c); err != nil {
					return errors.New("客户端必须是 IP 或 CIDR，禁止通配符")
				}
			}
		}
	} else if e.Protocol == "iscsi" {
		if r.Kind != "volume" {
			return errors.New("iSCSI 必须使用块卷仓库")
		}
		if !iqnRE.MatchString(e.IQN) {
			return errors.New("Target IQN 无效")
		}
		inits := strings.Fields(e.Initiators)
		if len(inits) == 0 || len(inits) > 64 {
			return errors.New("至少指定一个 initiator IQN")
		}
		for _, i := range inits {
			if !iqnRE.MatchString(i) {
				return errors.New("Initiator IQN 无效")
			}
		}
	} else {
		return errors.New("协议必须是 nfs 或 iscsi")
	}
	return nil
}
func atomicFile(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".backup-center-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func (a *App) applyExport(ctx context.Context, e Export) error {
	r, err := a.repo(e.RepositoryID)
	if err != nil {
		return err
	}
	if err = a.owned(ctx, r.Dataset); err != nil {
		return err
	}
	if e, err = a.prepareExport(e, r); err != nil {
		return err
	}
	var old Export
	a.DB.First(&old, "repository_id = ?", r.ID)
	e.ID = old.ID
	if old.ID != 0 && old.Protocol != e.Protocol && old.Enabled {
		return errors.New("请先停用原协议")
	}
	if e.Protocol == "nfs" {
		d, err := a.dataset(ctx, r)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(d.Mountpoint, "/srv/backup-center/repos/") || strings.ContainsAny(d.Mountpoint, " \t\n\"") {
			return errors.New("仓库挂载路径无效")
		}
		path := fmt.Sprintf("/etc/exports.d/backup-center-%d.exports", r.ID)
		previous, readErr := os.ReadFile(path)
		if readErr != nil && !os.IsNotExist(readErr) {
			return readErr
		}
		content := ""
		if e.Enabled {
			opts := "rw"
			if e.ReadOnly {
				opts = "ro"
			}
			parts := []string{d.Mountpoint}
			for _, c := range strings.Fields(e.Clients) {
				parts = append(parts, c+"("+opts+",sync,root_squash,no_subtree_check,sec=sys)")
			}
			content = strings.Join(parts, " ") + "\n"
		}
		if err = atomicFile(path, []byte(content), 0644); err != nil {
			return err
		}
		if e.Enabled {
			if _, err = a.Run.Run(ctx, "systemctl", "enable", "--now", "nfs-server.service"); err != nil {
				atomicFile(path, previous, 0644)
				return err
			}
		}
		if _, err = a.Run.Run(ctx, "exportfs", "-ra"); err != nil {
			atomicFile(path, previous, 0644)
			a.Run.Run(ctx, "exportfs", "-ra")
			return err
		}
	} else {
		if err = a.applyISCSI(ctx, r, old, e); err != nil {
			return err
		}
	}
	if err = a.DB.Save(&e).Error; err != nil {
		return fmt.Errorf("协议实际配置已更新，但数据库保存失败: %w", err)
	}
	return nil
}
func (a *App) target(ctx context.Context, args ...string) error {
	_, err := a.Run.Run(ctx, "targetcli", args...)
	return err
}
func (a *App) applyISCSI(ctx context.Context, r Repository, old, e Export) (resultErr error) {
	back := fmt.Sprintf("bc-%d", r.ID)
	tpg := "/iscsi/" + e.IQN + "/tpg1"
	if !e.Enabled {
		if old.ID != 0 {
			if err := a.target(ctx, "/iscsi", "delete", old.IQN); err != nil {
				return err
			}
			if err := a.target(ctx, "/backstores/block", "delete", back); err != nil {
				return err
			}
			return a.target(ctx, "saveconfig")
		}
		return nil
	}
	if old.Enabled {
		if old.IQN != e.IQN {
			return errors.New("先停用旧 Target 再更改 IQN")
		}
		if strings.TrimSpace(old.Initiators) != strings.TrimSpace(e.Initiators) {
			return errors.New("更改 ACL 前请先停用 Target")
		}
		return nil
	}
	if _, err := a.Run.Run(ctx, "systemctl", "enable", "--now", "target.service"); err != nil {
		return err
	}
	createdBack, createdTarget := false, false
	defer func() {
		if resultErr == nil {
			return
		}
		if createdTarget {
			if e2 := a.target(context.Background(), "/iscsi", "delete", e.IQN); e2 != nil {
				resultErr = fmt.Errorf("%w; rollback target: %v", resultErr, e2)
			}
		}
		if createdBack {
			if e2 := a.target(context.Background(), "/backstores/block", "delete", back); e2 != nil {
				resultErr = fmt.Errorf("%w; rollback backstore: %v", resultErr, e2)
			}
		}
		a.target(context.Background(), "saveconfig")
	}()
	steps := [][]string{{"/backstores/block", "create", back, "/dev/zvol/" + r.Dataset}, {"/iscsi", "create", e.IQN}, {tpg, "set", "attribute", "authentication=0", "generate_node_acls=0", "demo_mode_write_protect=1", "cache_dynamic_acls=0"}, {tpg + "/luns", "create", "/backstores/block/" + back}}
	for _, i := range strings.Fields(e.Initiators) {
		steps = append(steps, []string{tpg + "/acls", "create", i})
	}
	steps = append(steps, []string{"saveconfig"})
	for i, args := range steps {
		if err := a.target(ctx, args...); err != nil {
			return fmt.Errorf("iSCSI 配置未完成，请核对 targetcli 后重试: %w", err)
		}
		if i == 0 {
			createdBack = true
		}
		if i == 1 {
			createdTarget = true
		}
	}
	return nil
}
