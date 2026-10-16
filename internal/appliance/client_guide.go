package appliance

import (
	"errors"
	"github.com/gin-gonic/gin"
	"strings"
)

func (a *App) clientGuide(c *gin.Context) {
	id, e := paramID(c)
	if e != nil {
		return
	}
	r, e := a.repo(id)
	if e != nil {
		failure(c, 404, e)
		return
	}
	var ex Export
	if a.DB.First(&ex, "repository_id = ? AND enabled = ?", id, true).Error != nil {
		failure(c, 400, errors.New("请先启用仓库的共享访问"))
		return
	}
	server := "192.0.2.10"
	if a.Config.ExternalURL != "" {
		server = strings.TrimPrefix(strings.TrimPrefix(a.Config.ExternalURL, "https://"), "http://")
		server = strings.Split(server, ":")[0]
	}
	script := ""
	mount := "/mnt/backup-center/" + r.Name
	if ex.Protocol == "nfs" {
		script = "#!/usr/bin/env bash\nset -euo pipefail\n# 在源服务器执行。先创建本仓库专用备份用户，并在服务端目录授予其匹配 UID/GID 的写权限。\ninstall -d -m 0750 " + mount + "\nmount -t nfs4 -o vers=4.2,nosuid,nodev,hard,timeo=600,retrans=2 " + server + ":/srv/backup-center/repos/" + r.Name + " " + mount + "\nmountpoint -q " + mount + " || { echo 'Backup mount missing' >&2; exit 1; }\n# 文件备份：替换 /srv/application-data；数据库目录不得直接在线 rsync。\n# rsync -aHAX --numeric-ids --delete-delay /srv/application-data/ " + mount + "/files/\n# MySQL：先在源端运行 xtrabackup --backup --target-dir=<独立临时目录>；\n# 成功后运行 xtrabackup --prepare，验证退出码，再同步至本仓库并触发快照。\n# PostgreSQL：使用 pg_basebackup -X stream 写入独立目录，验证后同步。\n# 定时快照仅保证存储崩溃一致性；应用一致性必须由源端备份工具保证。\n"
	} else {
		script = "#!/usr/bin/env bash\nset -euo pipefail\n# 确认本机 /etc/iscsi/initiatorname.iscsi 中的 IQN 已加入本仓库 ACL。\niscsiadm -m discovery -t sendtargets -p " + server + "\niscsiadm -m node -T " + ex.IQN + " -p " + server + " --login\n# 同一非集群文件系统仅允许一个写入端；禁止在多台服务器同时格式化或挂载。\n# 连接后先核对 lsblk，再由管理员创建文件系统；本脚本不自动格式化。\n"
	}
	scripts := map[string]string{}
	if ex.Protocol == "nfs" {
		for _, tool := range []string{"rsync", "xtrabackup", "pg_basebackup"} {
			s, e := backupScript(tool, mount)
			if e != nil {
				failure(c, 400, e)
				return
			}
			scripts[tool] = s
		}
	}
	c.JSON(200, gin.H{"scripts": scripts, "repository": r, "export": ex, "script": script, "consistency": "快照不替代数据库应用一致性备份；必须定期执行真实恢复演练"})
}

// Backup scripts publish only completed, verified runs. An atomic current symlink
// always points to a completed backup even when a scheduled snapshot races a write.
func backupScript(tool, mount string) (string, error) {
	if !contains([]string{"rsync", "xtrabackup", "pg_basebackup"}, tool) {
		return "", errors.New("工具必须为 rsync、xtrabackup 或 pg_basebackup")
	}
	common := `#!/usr/bin/env bash
set -euo pipefail
umask 077
# 由与仓库 owner_uid/owner_gid 匹配的专用备份用户运行；NFS 必须由管理员预先挂载。
BACKUP_ROOT='` + mount + `'
mountpoint -q "$BACKUP_ROOT" || { echo 'NFS mount missing; refusing local-disk fallback' >&2; exit 1; }
[[ -w "$BACKUP_ROOT" ]] || { echo 'Backup UID/GID lacks repository write access' >&2; exit 1; }
exec 9>"$BACKUP_ROOT/.client-backup.lock"
flock -n 9 || { echo 'Another source backup is running' >&2; exit 1; }
run="$(date -u +%Y%m%dT%H%M%S)-$$"
mkdir -p "$BACKUP_ROOT/runs"
work="$BACKUP_ROOT/.staging-$run"
mkdir -m 0700 "$work"
# Failed staging runs are retained for diagnosis; current is never advanced on failure.
trap 'echo "Backup failed; staging preserved at $work" >&2' ERR
`
	var command string
	switch tool {
	case "rsync":
		command = `: "${SOURCE_DIR:?Set SOURCE_DIR to the absolute source directory}"
[[ "$SOURCE_DIR" == /* && -d "$SOURCE_DIR" ]] || { echo 'Invalid source directory' >&2; exit 1; }
# Do not use rsync against a live database data directory. Use the database backup tool.
mkdir "$work/data"
link=()
if [[ -d "$BACKUP_ROOT/current/data" ]]; then
  previous="$(readlink -f "$BACKUP_ROOT/current/data")"
  [[ "$previous" == "$BACKUP_ROOT/runs/"* ]] || { echo 'Invalid previous backup path' >&2; exit 1; }
  link=(--link-dest="$previous")
fi
rsync -aHAX --numeric-ids --safe-links "${link[@]}" -- "$SOURCE_DIR/" "$work/data/"
`
	case "xtrabackup":
		command = `: "${MYSQL_BACKUP_CONFIG:?Set MYSQL_BACKUP_CONFIG to a protected MySQL backup credentials file}"
[[ -f "$MYSQL_BACKUP_CONFIG" && ! -L "$MYSQL_BACKUP_CONFIG" ]] || exit 1
[[ "$(stat -c '%a' "$MYSQL_BACKUP_CONFIG")" == 600 ]] || { echo 'MySQL credentials must be 0600' >&2; exit 1; }
xtrabackup --defaults-extra-file="$MYSQL_BACKUP_CONFIG" --backup --target-dir="$work/data"
xtrabackup --prepare --target-dir="$work/data"
[[ -s "$work/data/xtrabackup_checkpoints" ]] || { echo 'Missing XtraBackup completion metadata' >&2; exit 1; }
`
	case "pg_basebackup":
		command = `: "${PGSERVICE:?Set PGSERVICE; configure a protected pg_service.conf and .pgpass on the source}"
pg_basebackup --pgdata="$work/data" --wal-method=stream --checkpoint=fast --manifest-checksums=SHA256
pg_verifybackup "$work/data"
`
	}
	ending := `# Produce portable checksums before publishing the completed backup.
( cd "$work/data"; find . -type f -print0 | sort -z | xargs -0 -r sha256sum -- ) > "$work/SHA256SUMS"
( cd "$work/data"; sha256sum --check ../SHA256SUMS ) > "$work/verification.log"
printf '{"completed_at":"%s","tool":"` + tool + `","run":"%s"}\n' "$(date -u +%FT%TZ)" "$run" > "$work/complete.json"
sync -f "$work"
mv -- "$work" "$BACKUP_ROOT/runs/$run"
ln -s "runs/$run" "$BACKUP_ROOT/.current-$run"
mv -Tf -- "$BACKUP_ROOT/.current-$run" "$BACKUP_ROOT/current"
sync -f "$BACKUP_ROOT"
trap - ERR
# Retain prior completed runs conservatively. Review capacity and configure cleanup
# only after validating your snapshot policy and restoration process.
printf 'Verified backup published: %s/current\n' "$BACKUP_ROOT"
`
	return common + command + ending, nil
}
