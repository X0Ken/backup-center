package appliance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func (a *App) syncRepoRoot(ctx context.Context, r Repository) (string, error) {
	if r.Kind != "filesystem" {
		return "", errors.New("数据同步只支持文件仓库")
	}
	if err := a.owned(ctx, r.Dataset); err != nil {
		return "", err
	}
	d, err := a.dataset(ctx, r)
	if err != nil {
		return "", err
	}
	expected := "/srv/backup-center/repos/" + r.Name
	if d.Mountpoint != expected {
		return "", errors.New("仓库挂载位置不匹配")
	}
	mounted, err := a.zfs(ctx, "get", "-H", "-o", "value", "mounted", r.Dataset)
	if err != nil || strings.TrimSpace(mounted) != "yes" {
		return "", errors.New("仓库没有挂载，已阻止写入系统盘")
	}
	info, err := os.Lstat(expected)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("仓库挂载目录无效")
	}
	if d.Available < 64<<20 {
		return "", errors.New("仓库可用空间不足 64 MiB，暂停同步")
	}
	return expected, nil
}
func (a *App) syncBound(id uint) bool {
	var n int64
	a.DB.Model(&SyncTask{}).Where("repository_id = ?", id).Count(&n)
	return n > 0
}
func (a *App) syncBusyRepo(id uint) bool {
	a.syncMu.Lock()
	defer a.syncMu.Unlock()
	return a.syncRepos[id]
}
func (a *App) signalSync() {
	select {
	case a.syncWake <- struct{}{}:
	default:
	}
}
func (a *App) syncScheduler() {
	defer a.wg.Done()
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		if a.ctx.Err() != nil {
			return
		}
		a.dispatchSync()
		select {
		case <-a.ctx.Done():
			return
		case <-tick.C:
		case <-a.syncWake:
		}
	}
}
func (a *App) dispatchSync() {
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	var tasks []SyncTask
	if a.DB.Where("enabled = ? AND next_run_at <= ?", true, time.Now()).Find(&tasks).Error == nil {
		for _, t := range tasks {
			var n int64
			a.DB.Model(&Job{}).Where("kind = ? AND target = ? AND status IN ?", "sync.run", fmt.Sprint(t.ID), []string{"queued", "running"}).Count(&n)
			if n == 0 {
				a.enqueue("sync.run", fmt.Sprint(t.ID), t.ID, 0)
			}
		}
	}
	a.syncMu.Lock()
	slots := 2 - len(a.syncActive)
	a.syncMu.Unlock()
	if slots <= 0 {
		return
	}
	var jobs []Job
	if a.DB.Where("kind = ? AND status = ?", "sync.run", "queued").Order("id").Limit(slots).Find(&jobs).Error != nil {
		return
	}
	for _, j := range jobs {
		var id uint
		if jsonDecode(j.Payload, &id) != nil {
			continue
		}
		var task SyncTask
		if a.DB.First(&task, id).Error != nil {
			a.DB.Model(&j).Updates(map[string]any{"status": "failed", "error": "同步任务已删除", "finished_at": time.Now()})
			continue
		}
		a.syncMu.Lock()
		if a.syncActive[id] != nil || a.syncRepos[task.RepositoryID] {
			a.syncMu.Unlock()
			continue
		}
		ctx, cancel := context.WithTimeout(a.ctx, 24*time.Hour)
		a.syncActive[id] = cancel
		a.syncRepos[task.RepositoryID] = true
		a.syncMu.Unlock()
		now := time.Now()
		if err := a.DB.Model(&j).Where("status = ?", "queued").Updates(map[string]any{"status": "running", "started_at": now}).Error; err != nil {
			cancel()
			a.syncMu.Lock()
			delete(a.syncActive, id)
			delete(a.syncRepos, task.RepositoryID)
			a.syncMu.Unlock()
			continue
		}
		a.wg.Add(1)
		go a.runSyncJob(ctx, cancel, j, task)
	}
}
func (a *App) runSyncJob(ctx context.Context, cancel context.CancelFunc, j Job, t SyncTask) {
	defer a.wg.Done()
	defer cancel()
	defer func() {
		a.syncMu.Lock()
		delete(a.syncActive, t.ID)
		delete(a.syncRepos, t.RepositoryID)
		a.syncMu.Unlock()
		a.signalSync()
	}()
	run := SyncRun{TaskID: t.ID, JobID: j.ID, Status: "running", Phase: "preflight", StartedAt: time.Now()}
	if err := a.DB.Create(&run).Error; err != nil {
		a.DB.Model(&j).Updates(map[string]any{"status": "failed", "error": "无法创建同步运行记录", "finished_at": time.Now()})
		return
	}
	generation, err := a.pullSync(ctx, t, &run)
	finish := time.Now()
	status := "succeeded"
	msg := ""
	next := finish.Add(time.Duration(t.IntervalSeconds) * time.Second)
	updates := map[string]any{"last_error": "", "failures": 0, "next_run_at": next}
	if err != nil {
		status = "failed"
		msg = err.Error()
		if len(msg) > 12000 {
			msg = msg[:12000]
		}
		if ctx.Err() != nil {
			status = "interrupted"
			msg = "同步已停止，最近成功版本保持不变"
		}
		failures := t.Failures + 1
		shift := failures - 1
		if shift > 5 {
			shift = 5
		}
		delay := time.Duration(t.IntervalSeconds) * time.Second * time.Duration(1<<shift)
		if delay > 30*time.Minute {
			delay = 30 * time.Minute
		}
		updates = map[string]any{"last_error": msg, "failures": failures, "next_run_at": finish.Add(delay)}
		if status == "failed" {
			a.raise("sync-"+fmt.Sprint(t.ID), "critical", t.Name+": "+msg)
		}
	} else {
		// Publication persists LastSuccessAt before any optional snapshot work.
		a.DB.Model(&Alert{}).Where("key = ?", "sync-"+fmt.Sprint(t.ID)).Updates(map[string]any{"active": false, "updated_at": finish})
	}
	a.DB.Model(&t).Updates(updates)
	a.DB.Model(&run).Updates(map[string]any{"status": status, "phase": map[bool]string{true: "completed", false: status}[err == nil], "generation": generation, "error": msg, "finished_at": finish})
	a.DB.Model(&j).Updates(map[string]any{"status": status, "error": msg, "result": generation, "finished_at": finish})
	a.audit(j.UserID, "", "sync.run."+status, fmt.Sprint(t.ID), msg, "")
	if err == nil {
		a.signalPolicies()
	}
}

type syncManifest struct {
	TaskID      uint             `json:"task_id"`
	Generation  string           `json:"generation"`
	CompletedAt time.Time        `json:"completed_at"`
	Files       map[string]int64 `json:"files"`
	Bytes       int64            `json:"bytes"`
}

func scanSyncTree(root string) (map[string]int64, int64, error) {
	files := map[string]int64{}
	var bytes int64
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if path == root {
			return nil
		}
		rel, e := filepath.Rel(root, path)
		if e != nil {
			return e
		}
		if d.IsDir() {
			return nil
		}
		info, e := d.Info()
		if e != nil {
			return e
		}
		if info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			files[rel] = info.Size()
			bytes += info.Size()
		}
		if len(files) > 2000000 {
			return errors.New("单任务文件数量超过两百万，请按业务目录拆分任务")
		}
		return nil
	})
	return files, bytes, err
}
func validateSyncDeletion(old map[string]int64, newFiles map[string]int64, t SyncTask) (int64, error) {
	var deleted int64
	for name := range old {
		if _, ok := newFiles[name]; !ok {
			deleted++
		}
	}
	if len(newFiles) == 0 && !t.AllowEmpty {
		return deleted, errors.New("源目录为空，已暂停发布并保留最近成功版本；确认后可在高级设置允许空目录")
	}
	if len(old) > 0 && deleted*100 > int64(len(old))*int64(t.MaxDeletePercent) {
		return deleted, fmt.Errorf("本轮删除 %d/%d 个文件，超过 %d%% 阈值，已保留最近成功版本；请核对源端并调整删除阈值", deleted, len(old), t.MaxDeletePercent)
	}
	return deleted, nil
}
func loadSyncManifest(root string, t SyncTask) (syncManifest, error) {
	var m syncManifest
	link, err := os.Readlink(filepath.Join(root, "current"))
	if os.IsNotExist(err) {
		return m, nil
	}
	if err != nil {
		return m, errors.New("current 必须为平台管理的版本链接")
	}
	expected := filepath.Join(".versions", filepath.Base(filepath.Dir(link)), "data")
	if link != expected || !strings.HasPrefix(filepath.Base(filepath.Dir(link)), "g-") {
		return m, errors.New("current 指向平台范围外，已停止同步")
	}
	data, err := os.ReadFile(filepath.Join(root, filepath.Dir(link), "manifest.json"))
	if err != nil {
		return m, err
	}
	if err = json.Unmarshal(data, &m); err != nil {
		return m, err
	}
	if m.TaskID != t.ID || m.Generation != filepath.Base(filepath.Dir(link)) {
		return m, errors.New("已发布版本的任务标识不匹配")
	}
	return m, nil
}
func (a *App) pullSync(ctx context.Context, t SyncTask, run *SyncRun) (string, error) {
	repo, err := a.repo(t.RepositoryID)
	if err != nil {
		return "", err
	}
	root, err := a.syncRepoRoot(ctx, repo)
	if err != nil {
		return "", err
	}
	secret, _, err := a.cryptSyncSecret(syncSecret{}, t.Credential)
	if err != nil {
		return "", err
	}
	q := syncInput{Host: t.Host, Port: t.Port, Username: t.Username, AuthMethod: t.AuthMethod, Fingerprint: t.Fingerprint, AccessMethod: t.AccessMethod, SourcePath: t.SourcePath}
	_, _, err = probeSync(ctx, q, secret)
	if err != nil {
		return "", err
	}
	previous, err := loadSyncManifest(root, t)
	if err != nil {
		return "", err
	}
	// Recover a crash between the atomic publication and the database commit.
	if previous.Generation != "" && previous.Generation != t.CurrentGeneration {
		if err = a.DB.Model(&t).Updates(map[string]any{"current_generation": previous.Generation, "last_success_at": previous.CompletedAt}).Error; err != nil {
			return "", err
		}
		t.CurrentGeneration = previous.Generation
	}
	incoming := filepath.Join(root, ".incoming")
	versions := filepath.Join(root, ".versions")
	for _, p := range []string{incoming, versions} {
		if err = os.MkdirAll(p, 0700); err != nil {
			return "", err
		}
		info, e := os.Lstat(p)
		if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("版本工作目录无效")
		}
	}
	if err = os.Chmod(incoming, 0700); err != nil {
		return "", err
	}
	if err = os.Chmod(versions, 0755); err != nil {
		return "", err
	}
	work := filepath.Join(incoming, "work")
	// Interrupted rclone local downloads can leave .partial files that race its
	// destination listing on the next sync. Rebuild its isolated work tree from
	// the published generation; do not treat partial files as valid content.
	if t.AccessMethod == "sftp" {
		if err = os.RemoveAll(work); err != nil {
			return "", err
		}
	}

	if _, e := os.Lstat(work); os.IsNotExist(e) {
		if err = os.Mkdir(work, 0700); err != nil {
			return "", err
		}
		if err = os.Mkdir(filepath.Join(work, "data"), 0755); err != nil {
			return "", err
		}
		if previous.Generation != "" {
			a.syncPhase(run, "preparing")
			source := filepath.Join(root, ".versions", previous.Generation, "data") + string(os.PathSeparator) + "."
			// Reflinks create independent inodes, unlike cross-generation hardlinks.
			// GNU cp falls back to a local copy when block cloning is unavailable.
			if _, err = a.syncCommand(ctx, run, secret, "cp", []string{"-a", "--reflink=auto", "--", source, filepath.Join(work, "data")}, nil, nil); err != nil {
				os.RemoveAll(work)
				return "", errors.New("创建隔离的增量工作副本失败")
			}
		}
	} else if e != nil {
		return "", e
	}
	if info, e := os.Lstat(work); e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("暂存版本无效")
	}
	runtimeDir, err := os.MkdirTemp(a.Config.DataDir, ".sync-runtime-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(runtimeDir)
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(t.HostKey))
	if err != nil {
		return "", errors.New("已绑定的主机公钥损坏")
	}
	known := filepath.Join(runtimeDir, "known_hosts")
	line := knownhosts.Line([]string{netAddress(t.Host, t.Port)}, key) + "\n"
	if err = atomicFile(known, []byte(line), 0600); err != nil {
		return "", err
	}
	keyFile := filepath.Join(runtimeDir, "id_key")
	if t.AuthMethod == "key" {
		if err = atomicFile(keyFile, []byte(secret.PrivateKey), 0600); err != nil {
			return "", err
		}
	}
	a.syncPhase(run, "transferring")
	dataDir := filepath.Join(work, "data")
	if t.AccessMethod == "ssh" {
		sshArgs := []string{"ssh", "-F", "/dev/null", "-p", strconv.Itoa(t.Port), "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=" + known, "-o", "ConnectTimeout=10", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3", "-o", "HostKeyAlgorithms=" + syncHostAlgorithms(key)}
		if t.AuthMethod == "key" {
			sshArgs = append(sshArgs, "-i", keyFile, "-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes")
		} else {
			sshArgs = append(sshArgs, "-o", "PreferredAuthentications=password,keyboard-interactive", "-o", "NumberOfPasswordPrompts=1")
		}
		quoted := []string{}
		for _, arg := range sshArgs {
			quoted = append(quoted, shellQuote(arg))
		}
		args := []string{"-aHAX", "--numeric-ids", "--protect-args", "--partial-dir=.rsync-partial", "--delete-delay", "--one-file-system", "--timeout=120", "--info=progress2", "--stats", "--outbuf=L", "-e", strings.Join(quoted, " "), "--", t.Username + "@" + t.Host + ":" + t.SourcePath + "/", dataDir + "/"}
		if t.BandwidthKiBps > 0 {
			args = append([]string{"--bwlimit=" + strconv.Itoa(t.BandwidthKiBps)}, args...)
		}
		program := "rsync"
		var password []byte
		if t.AuthMethod == "password" {
			program = "sshpass"
			args = append([]string{"-d", "3", "rsync"}, args...)
			password = []byte(secret.Password + "\n")
		}
		_, err = a.syncCommand(ctx, run, secret, program, args, nil, password)
	} else {
		config := fmt.Sprintf("[source]\ntype = sftp\nhost = %s\nport = %d\nuser = %s\nknown_hosts_file = %s\nshell_type = none\ndisable_hashcheck = true\nskip_links = true\nhost_key_algorithms = %s\n", t.Host, t.Port, t.Username, known, strings.ReplaceAll(syncHostAlgorithms(key), ",", " "))
		if t.AuthMethod == "key" {
			config += "key_file = " + keyFile + "\n"
		} else {
			obscured, e := a.syncCommand(ctx, nil, secret, "rclone", []string{"obscure", "-"}, strings.NewReader(secret.Password), nil)
			if e != nil {
				return "", errors.New("rclone 凭证配置失败")
			}
			config += "pass = " + strings.TrimSpace(obscured) + "\n"
		}
		configFile := filepath.Join(runtimeDir, "rclone.conf")
		if err = atomicFile(configFile, []byte(config), 0600); err != nil {
			return "", err
		}
		args := []string{"sync", "source:" + t.SourcePath, dataDir, "--config", configFile, "--transfers", "4", "--checkers", "8", "--contimeout", "15s", "--timeout", "120s", "--retries", "1", "--low-level-retries", "3", "--create-empty-src-dirs", "--use-json-log", "--stats", "5s", "--stats-log-level", "NOTICE"}
		if t.BandwidthKiBps > 0 {
			args = append(args, "--bwlimit", strconv.Itoa(t.BandwidthKiBps)+"k")
		}
		_, err = a.syncCommand(ctx, run, secret, "rclone", args, nil, nil)
	}
	if err != nil {
		return "", err
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	a.syncPhase(run, "validating")
	if t.AccessMethod == "sftp" {
		var owner unix.Stat_t
		if err = unix.Stat(root, &owner); err != nil {
			return "", err
		}
		err = filepath.WalkDir(dataDir, func(p string, d os.DirEntry, e error) error {
			if e != nil {
				return e
			}
			mode := os.FileMode(0640)
			if d.IsDir() {
				mode = 0750
			} else {
				info, e := d.Info()
				if e != nil {
					return e
				}
				if !info.Mode().IsRegular() {
					return errors.New("SFTP 副本包含不支持的特殊文件")
				}
			}
			if e = os.Chown(p, int(owner.Uid), int(owner.Gid)); e != nil {
				return e
			}
			return os.Chmod(p, mode)
		})
		if err != nil {
			return "", err
		}
	}
	files, bytes, err := scanSyncTree(dataDir)
	if err != nil {
		return "", err
	}
	deleted, err := validateSyncDeletion(previous.Files, files, t)
	a.DB.Model(run).Updates(map[string]any{"files": len(files), "bytes": bytes, "deleted_files": deleted})
	if err != nil {
		return "", err
	}
	generation := fmt.Sprintf("g-%020d", run.JobID)
	manifest := syncManifest{TaskID: t.ID, Generation: generation, CompletedAt: time.Now(), Files: files, Bytes: bytes}
	plain, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	if err = os.Chmod(work, 0755); err != nil {
		return "", err
	}
	if err = atomicFile(filepath.Join(work, "manifest.json"), plain, 0600); err != nil {
		return "", err
	}
	a.syncPhase(run, "publishing")
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if _, err = a.syncRepoRoot(ctx, repo); err != nil {
		return "", err
	}
	// Flush data and metadata before publishing the durable directory and pointer.
	f, err := os.Open(work)
	if err != nil {
		return "", err
	}
	err = unix.Syncfs(int(f.Fd()))
	f.Close()
	if err != nil {
		return "", err
	}
	if err = os.Rename(work, filepath.Join(versions, generation)); err != nil {
		return "", err
	}
	for _, directory := range []string{versions, incoming} {
		f, e := os.Open(directory)
		if e != nil {
			return "", e
		}
		e = f.Sync()
		f.Close()
		if e != nil {
			return "", e
		}
	}
	tempLink := filepath.Join(root, ".current-next")
	if _, e := os.Lstat(tempLink); e == nil {
		if err = os.Remove(tempLink); err != nil {
			return "", err
		}
	}
	if err = os.Symlink(filepath.Join(".versions", generation, "data"), tempLink); err != nil {
		return "", err
	}
	if err = os.Rename(tempLink, filepath.Join(root, "current")); err != nil {
		return "", err
	}
	parent, err := os.Open(root)
	if err != nil {
		return "", err
	}
	err = parent.Sync()
	parent.Close()
	if err != nil {
		return "", err
	}
	if err = a.DB.Model(&t).Updates(map[string]any{"current_generation": generation, "last_success_at": manifest.CompletedAt}).Error; err != nil {
		return generation, err
	}
	// Keep the current and previous live generations; snapshots retain older data.
	entries, _ := os.ReadDir(versions)
	for _, entry := range entries {
		if entry.Name() != generation && entry.Name() != previous.Generation && strings.HasPrefix(entry.Name(), "g-") {
			if e := os.RemoveAll(filepath.Join(versions, entry.Name())); e != nil {
				return generation, e
			}
		}
	}
	// The first successful version always gets a baseline, independently of policy.
	if t.LastSnapshotGeneration == "" {
		_, snapErr := a.createSnapshotMode(ctx, ActionRequest{RepositoryID: t.RepositoryID}, true)
		if snapErr == nil {
			a.DB.Model(&Alert{}).Where("key = ?", "sync-snapshot-"+fmt.Sprint(t.ID)).Update("active", false)
		}
		if snapErr != nil {
			a.raise("sync-snapshot-"+fmt.Sprint(t.ID), "critical", "同步版本已发布，但基线快照失败: "+snapErr.Error())
		}
	}
	return generation, nil
}
func (a *App) syncPhase(run *SyncRun, phase string) { a.DB.Model(run).Update("phase", phase) }
func shellQuote(s string) string                    { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func syncHostAlgorithms(key ssh.PublicKey) string {
	if key.Type() == ssh.KeyAlgoRSA {
		return "rsa-sha2-512,rsa-sha2-256"
	}
	return key.Type()
}
func netAddress(host string, port int) string {
	if port == 22 {
		return host
	}
	return "[" + host + "]:" + strconv.Itoa(port)
}

// Dedicated transfer runner: the generic management runner has a two-minute limit.
func (a *App) syncCommand(ctx context.Context, run *SyncRun, secret syncSecret, program string, args []string, stdin io.Reader, password []byte) (string, error) {
	cmd := command(ctx, program, args...)
	cmd.Stdin = stdin
	var output cappedBuffer
	writer := &syncProgress{app: a, run: run, output: &output}
	cmd.Stdout = writer
	cmd.Stderr = writer
	var read, write *os.File
	if len(password) > 0 {
		var err error
		read, write, err = os.Pipe()
		if err != nil {
			return "", err
		}
		defer read.Close()
		defer write.Close()
		cmd.ExtraFiles = []*os.File{read}
		if _, err = write.Write(password); err != nil {
			return "", err
		}
		write.Close()
	}
	err := cmd.Run()
	text := output.String()
	for _, s := range []string{secret.Password, secret.PrivateKey, secret.KeyPassphrase} {
		if s != "" {
			text = strings.ReplaceAll(text, s, "[REDACTED]")
		}
	}
	if len(text) > 12000 {
		text = text[len(text)-12000:]
	}
	if run != nil {
		matches := regexp.MustCompile(`Literal data: ([0-9,]+) bytes`).FindStringSubmatch(text)
		if len(matches) == 2 {
			if n, e := strconv.ParseInt(strings.ReplaceAll(matches[1], ",", ""), 10, 64); e == nil {
				a.DB.Model(run).Update("transferred_bytes", n)
			}
		}
	}
	if run != nil {
		a.DB.Model(run).Update("log", text)
	}
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return "", fmt.Errorf("%s 同步退出码 %d，未发布本轮版本: %s", program, exit.ExitCode(), text)
		}
		return "", fmt.Errorf("无法启动 %s，请检查平台依赖安装", program)
	}
	return text, nil
}

type syncProgress struct {
	mu      sync.Mutex
	app     *App
	run     *SyncRun
	output  *cappedBuffer
	last    time.Time
	pending string
}

func (p *syncProgress) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.output.Write(b)
	if p.run == nil {
		return len(b), nil
	}
	p.pending += strings.ReplaceAll(string(b), "\r", "\n")
	if len(p.pending) > 65536 {
		p.pending = p.pending[len(p.pending)-65536:]
	}
	lines := strings.Split(p.pending, "\n")
	p.pending = lines[len(lines)-1]
	for _, line := range lines[:len(lines)-1] {
		if time.Since(p.last) < time.Second {
			continue
		}
		var msg struct {
			Stats *struct {
				Bytes int64 `json:"bytes"`
			} `json:"stats"`
		}
		if json.Unmarshal([]byte(line), &msg) == nil && msg.Stats != nil {
			p.app.DB.Model(p.run).Update("transferred_bytes", msg.Stats.Bytes)
			p.last = time.Now()
			continue
		}
		fields := strings.Fields(line)
		if len(fields) > 1 && strings.HasSuffix(fields[1], "%") {
			n, e := strconv.ParseInt(strings.ReplaceAll(fields[0], ",", ""), 10, 64)
			if e == nil {
				p.app.DB.Model(p.run).Update("transferred_bytes", n)
				p.last = time.Now()
			}
		}
	}
	return len(b), nil
}
