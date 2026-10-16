package appliance

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"
)

type syncSecret struct {
	Password      string `json:"password"`
	PrivateKey    string `json:"private_key"`
	KeyPassphrase string `json:"key_passphrase"`
}
type syncInput struct {
	ID               uint   `json:"id"`
	Name             string `json:"name"`
	RepositoryID     uint   `json:"repository_id"`
	AccessMethod     string `json:"access_method"`
	Host             string `json:"host"`
	Port             int    `json:"port"`
	Username         string `json:"username"`
	AuthMethod       string `json:"auth_method"`
	Password         string `json:"password"`
	PrivateKey       string `json:"private_key"`
	KeyPassphrase    string `json:"key_passphrase"`
	Fingerprint      string `json:"fingerprint"`
	SourcePath       string `json:"source_path"`
	BandwidthKiBps   int    `json:"bandwidth_kibps"`
	IntervalSeconds  int    `json:"interval_seconds"`
	MaxDeletePercent int    `json:"max_delete_percent"`
	AllowEmpty       bool   `json:"allow_empty"`
	Enabled          bool   `json:"enabled"`
}

func validateSyncInput(q syncInput, saving bool) error {
	if !contains([]string{"ssh", "sftp"}, q.AccessMethod) || !hostRE.MatchString(q.Host) || !userRE.MatchString(q.Username) || q.Port < 1 || q.Port > 65535 || !contains([]string{"password", "key"}, q.AuthMethod) {
		return errors.New("主机地址、访问方式、端口、用户名或认证方式无效")
	}
	if len(q.SourcePath) > 4096 || strings.ContainsAny(q.SourcePath, "\x00\r\n") || (q.SourcePath != "" && !filepath.IsAbs(q.SourcePath)) {
		return errors.New("源目录必须为不含换行的绝对路径")
	}
	if q.BandwidthKiBps < 0 || q.BandwidthKiBps > 1048576 {
		return errors.New("限速范围为 0 到 1048576 KiB/s")
	}
	if saving && (validName(q.Name) != nil || q.SourcePath == "" || filepath.Clean(q.SourcePath) == "/" || q.IntervalSeconds < 60 || q.IntervalSeconds > 86400 || q.MaxDeletePercent < 1 || q.MaxDeletePercent > 100) {
		return errors.New("请填写合法任务名和业务源目录；同步间隔为 60 到 86400 秒，删除阈值为 1 到 100%")
	}
	if strings.ContainsAny(q.Password, "\r\n") {
		return errors.New("密码不能包含换行")
	}
	if len(q.Password) > 4096 || len(q.PrivateKey) > 65536 || len(q.KeyPassphrase) > 4096 {
		return errors.New("凭证长度超出限制")
	}
	return nil
}
func (a *App) syncMasterKey() ([]byte, error) {
	a.syncMu.Lock()
	defer a.syncMu.Unlock()
	p := filepath.Join(a.Config.DataDir, "keys", ".sync-master.key")
	if b, e := os.ReadFile(p); e == nil {
		if len(b) != 32 {
			return nil, errors.New("同步凭证密钥损坏")
		}
		return b, nil
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return nil, err
	}
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		return nil, e
	}
	f, e := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(e) {
		return os.ReadFile(p)
	}
	if e != nil {
		return nil, e
	}
	if _, e = f.Write(b); e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return nil, e
	}
	return b, ce
}
func (a *App) cryptSyncSecret(secret syncSecret, existing string) (syncSecret, string, error) {
	if existing != "" {
		if _, e := os.Stat(filepath.Join(a.Config.DataDir, "keys", ".sync-master.key")); e != nil {
			return secret, "", errors.New("同步凭证密钥缺失，请恢复完整配置备份")
		}
	}
	key, err := a.syncMasterKey()
	if err != nil {
		return secret, "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return secret, "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return secret, "", err
	}
	if existing != "" {
		data, e := base64.StdEncoding.DecodeString(existing)
		if e != nil || len(data) < gcm.NonceSize() {
			return secret, "", errors.New("同步凭证格式无效")
		}
		plain, e := gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], []byte("backup-center-sync-v1"))
		if e != nil {
			return secret, "", errors.New("同步凭证无法解密，请恢复密钥或重新设置凭证")
		}
		e = json.Unmarshal(plain, &secret)
		return secret, "", e
	}
	plain, err := json.Marshal(secret)
	if err != nil {
		return secret, "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return secret, "", err
	}
	return secret, base64.StdEncoding.EncodeToString(append(nonce, gcm.Seal(nil, nonce, plain, []byte("backup-center-sync-v1"))...)), nil
}
func (a *App) inputSecret(q syncInput) (syncSecret, error) {
	s := syncSecret{q.Password, q.PrivateKey, q.KeyPassphrase}
	if q.ID > 0 && ((q.AuthMethod == "password" && q.Password == "") || (q.AuthMethod == "key" && q.PrivateKey == "")) {
		var old SyncTask
		if err := a.DB.First(&old, q.ID).Error; err != nil {
			return s, err
		}
		if old.Host != q.Host || old.Port != q.Port || old.Username != q.Username || old.AuthMethod != q.AuthMethod {
			return s, errors.New("连接信息变化后必须重新填写凭证")
		}
		var err error
		s, _, err = a.cryptSyncSecret(syncSecret{}, old.Credential)
		return s, err
	}
	if q.AuthMethod == "password" {
		if s.Password == "" {
			return s, errors.New("请填写 SSH 密码")
		}
		s.PrivateKey = ""
		s.KeyPassphrase = ""
		return s, nil
	}
	var raw any
	var err error
	if s.KeyPassphrase != "" {
		raw, err = ssh.ParseRawPrivateKeyWithPassphrase([]byte(s.PrivateKey), []byte(s.KeyPassphrase))
	} else {
		raw, err = ssh.ParseRawPrivateKey([]byte(s.PrivateKey))
	}
	if err != nil {
		return s, errors.New("私钥或私钥口令无效")
	}
	p, err := ssh.MarshalPrivateKey(raw, "")
	if err != nil {
		return s, errors.New("不支持的 SSH 私钥类型")
	}
	s.PrivateKey = string(pem.EncodeToMemory(p))
	s.Password = ""
	s.KeyPassphrase = ""
	return s, nil
}

var errSyncTrust = errors.New("需要确认 SSH 主机指纹")

func dialSync(ctx context.Context, q syncInput, s syncSecret) (*ssh.Client, ssh.PublicKey, error) {
	var observed ssh.PublicKey
	var auth ssh.AuthMethod
	if q.AuthMethod == "key" {
		signer, e := ssh.ParsePrivateKey([]byte(s.PrivateKey))
		if e != nil {
			return nil, nil, errors.New("SSH 私钥无效")
		}
		auth = ssh.PublicKeys(signer)
	} else {
		auth = ssh.Password(s.Password)
	}
	config := &ssh.ClientConfig{User: q.Username, Auth: []ssh.AuthMethod{auth}, Timeout: 15 * time.Second, HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
		observed = key
		if q.Fingerprint == "" {
			return errSyncTrust
		}
		if ssh.FingerprintSHA256(key) != q.Fingerprint {
			return errors.New("SSH 主机指纹不匹配，连接已拒绝")
		}
		return nil
	}}
	address := net.JoinHostPort(q.Host, strconv.Itoa(q.Port))
	raw, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, nil, errors.New("无法连接源主机 SSH 端口")
	}
	raw.SetDeadline(time.Now().Add(20 * time.Second))
	stop := context.AfterFunc(ctx, func() { raw.Close() })
	defer stop()
	conn, ch, req, err := ssh.NewClientConn(raw, address, config)
	if err != nil {
		raw.Close()
		if observed != nil && q.Fingerprint == "" {
			return nil, observed, errSyncTrust
		}
		return nil, observed, errors.New("SSH 认证失败或主机指纹不匹配")
	}
	raw.SetDeadline(time.Time{})
	return ssh.NewClient(conn, ch, req), observed, nil
}
func probeSync(ctx context.Context, q syncInput, s syncSecret) (gin.H, string, error) {
	client, key, err := dialSync(ctx, q, s)
	result := gin.H{}
	if key != nil {
		result["fingerprint"] = ssh.FingerprintSHA256(key)
	}
	if errors.Is(err, errSyncTrust) {
		result["needs_trust"] = true
		return result, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	defer client.Close()
	stop := context.AfterFunc(ctx, func() { client.Close() })
	defer stop()
	if q.AccessMethod == "ssh" {
		session, e := client.NewSession()
		if e != nil {
			return nil, "", e
		}
		out, e := session.Output("rsync --version")
		session.Close()
		if e != nil || !strings.Contains(string(out), "rsync") {
			return nil, "", errors.New("SSH 方式要求源端安装 rsync 并允许执行；仅提供 SFTP 的主机请选择 SFTP")
		}
	}
	sc, err := sftp.NewClient(client)
	if err != nil {
		return nil, "", errors.New("源端 SFTP 目录浏览不可用，请检查 SFTP 子系统")
	}
	defer sc.Close()
	path := q.SourcePath
	if path == "" {
		path = "/"
	}
	path = filepath.Clean(path)
	info, err := sc.Stat(path)
	if err != nil || !info.IsDir() {
		return nil, "", errors.New("源目录不存在或账号没有访问权限")
	}
	entries, err := sc.ReadDir(path)
	if err != nil {
		return nil, "", errors.New("无权读取源目录")
	}
	dirs := []gin.H{}
	for _, entry := range entries {
		if entry.IsDir() {
			dirs = append(dirs, gin.H{"name": entry.Name(), "path": filepath.Join(path, entry.Name())})
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i]["name"].(string) < dirs[j]["name"].(string) })
	truncated := len(dirs) > 500
	if truncated {
		dirs = dirs[:500]
	}
	result["directories"] = dirs
	result["path"] = path
	result["parent"] = filepath.Dir(path)
	result["truncated"] = truncated
	result["needs_trust"] = false
	result["engine"] = map[string]string{"ssh": "rsync", "sftp": "rclone"}[q.AccessMethod]
	return result, string(ssh.MarshalAuthorizedKey(key)), nil
}
func (a *App) syncProbe(c *gin.Context) {
	var q syncInput
	if !body(c, &q) {
		return
	}
	if err := validateSyncInput(q, false); err != nil {
		failure(c, 400, err)
		return
	}
	for _, program := range []string{map[string]string{"ssh": "rsync", "sftp": "rclone"}[q.AccessMethod], "ssh"} {
		if _, e := exec.LookPath(program); e != nil {
			failure(c, 400, fmt.Errorf("平台尚未安装 %s", program))
			return
		}
	}
	if q.AccessMethod == "ssh" && q.AuthMethod == "password" {
		if _, e := exec.LookPath("sshpass"); e != nil {
			failure(c, 400, errors.New("平台尚未安装 sshpass"))
			return
		}
	}
	secret, err := a.inputSecret(q)
	if err != nil {
		failure(c, 400, err)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	result, _, err := probeSync(ctx, q, secret)
	if err != nil {
		failure(c, 400, err)
		return
	}
	c.JSON(200, result)
}
func (a *App) listSyncTasks(c *gin.Context) {
	var rows []SyncTask
	if err := a.DB.Order("id desc").Find(&rows).Error; err != nil {
		failure(c, 500, err)
		return
	}
	result := []gin.H{}
	for _, row := range rows {
		var run SyncRun
		a.DB.Where("task_id = ?", row.ID).Order("id desc").First(&run)
		result = append(result, gin.H{"task": row, "run": run, "engine": map[string]string{"ssh": "rsync", "sftp": "rclone"}[row.AccessMethod]})
	}
	c.JSON(200, result)
}
func (a *App) saveSyncTask(c *gin.Context) {
	var q syncInput
	if !body(c, &q) {
		return
	}
	if err := validateSyncInput(q, true); err != nil {
		failure(c, 400, err)
		return
	}
	for _, program := range []string{map[string]string{"ssh": "rsync", "sftp": "rclone"}[q.AccessMethod], "ssh"} {
		if _, e := exec.LookPath(program); e != nil {
			failure(c, 400, fmt.Errorf("平台尚未安装 %s", program))
			return
		}
	}
	if q.AccessMethod == "ssh" && q.AuthMethod == "password" {
		if _, e := exec.LookPath("sshpass"); e != nil {
			failure(c, 400, errors.New("平台尚未安装 sshpass"))
			return
		}
	}
	secret, err := a.inputSecret(q)
	if err != nil {
		failure(c, 400, err)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	_, hostKey, err := probeSync(ctx, q, secret)
	if err != nil || hostKey == "" {
		failure(c, 400, errors.New("请先测试连接并确认主机指纹"))
		return
	}
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	var old SyncTask
	if q.ID > 0 {
		if err = a.DB.First(&old, q.ID).Error; err != nil {
			failure(c, 404, err)
			return
		}
	}
	if old.ID > 0 && (old.RepositoryID != q.RepositoryID || old.AccessMethod != q.AccessMethod || old.Host != q.Host || old.Port != q.Port || old.Username != q.Username || old.SourcePath != filepath.Clean(q.SourcePath)) {
		failure(c, 400, errors.New("已绑定的主机、访问方式、源目录及仓库不能更换；请新建任务"))
		return
	}
	var pending int64
	a.DB.Model(&Job{}).Where("kind = ? AND target = ? AND status IN ?", "sync.run", fmt.Sprint(old.ID), []string{"queued", "running"}).Count(&pending)
	if old.ID > 0 && pending > 0 {
		failure(c, 409, errors.New("请先停止当前同步任务，再编辑配置"))
		return
	}
	repo, err := a.repo(q.RepositoryID)
	if err != nil || repo.Kind != "filesystem" {
		failure(c, 400, errors.New("请选择专用文件仓库"))
		return
	}
	root, err := a.syncRepoRoot(ctx, repo)
	if err != nil {
		failure(c, 400, err)
		return
	}
	var exports int64
	a.DB.Model(&Export{}).Where("repository_id = ? AND enabled = ? AND read_only = ?", repo.ID, true, false).Count(&exports)
	if exports > 0 {
		failure(c, 400, errors.New("同步仓库不能同时提供读写共享，请选择未被其他程序写入的专用仓库"))
		return
	}
	if old.ID == 0 {
		entries, e := os.ReadDir(root)
		if e != nil {
			failure(c, 400, e)
			return
		}
		for _, entry := range entries {
			if entry.Name() != ".zfs" {
				failure(c, 400, errors.New("首次绑定需要空的专用仓库；原有数据不会覆盖"))
				return
			}
		}
	}
	if _, err = a.zfs(ctx, "set", "exec=off", "setuid=off", "devices=off", repo.Dataset); err != nil {
		failure(c, 400, err)
		return
	}
	_, ciphertext, err := a.cryptSyncSecret(secret, "")
	if err != nil {
		failure(c, 500, err)
		return
	}
	task := old
	task.Name = q.Name
	task.RepositoryID = q.RepositoryID
	task.AccessMethod = q.AccessMethod
	task.Host = q.Host
	task.Port = q.Port
	task.Username = q.Username
	task.AuthMethod = q.AuthMethod
	task.Credential = ciphertext
	task.HostKey = hostKey
	task.Fingerprint = q.Fingerprint
	task.SourcePath = filepath.Clean(q.SourcePath)
	task.BandwidthKiBps = q.BandwidthKiBps
	task.IntervalSeconds = q.IntervalSeconds
	task.MaxDeletePercent = q.MaxDeletePercent
	task.AllowEmpty = q.AllowEmpty
	task.Enabled = q.Enabled
	task.NextRunAt = time.Now()
	if err = a.DB.Transaction(func(tx *gorm.DB) error {
		if e := tx.Save(&task).Error; e != nil {
			return e
		}
		var count int64
		if e := tx.Model(&Policy{}).Where("repository_id = ?", task.RepositoryID).Count(&count).Error; e != nil {
			return e
		}
		if count == 0 {
			p := Policy{RepositoryID: task.RepositoryID, Enabled: true, KeepHourly: 24, KeepDaily: 30, KeepWeekly: 12, KeepMonthly: 12, KeepYearly: 3}
			p.Cron = policyCron(p)
			return tx.Create(&p).Error
		}
		return nil
	}); err != nil {
		failure(c, 409, errors.New("任务名称或仓库已被其他同步任务占用"))
		return
	}
	a.audit(current(c).ID, current(c).Username, "sync.save", fmt.Sprint(task.ID), task.Name, c.ClientIP())
	a.signalSync()
	c.JSON(200, task)
}
func (a *App) syncControl(c *gin.Context) {
	id, err := paramID(c)
	if err != nil {
		return
	}
	var task SyncTask
	if err = a.DB.First(&task, id).Error; err != nil {
		failure(c, 404, err)
		return
	}
	action := c.Param("operation")
	switch action {
	case "run":
		a.mutationMu.Lock()
		defer a.mutationMu.Unlock()
		j, e := a.enqueue("sync.run", fmt.Sprint(id), id, current(c).ID)
		if e != nil {
			failure(c, 409, e)
			return
		}
		a.signalSync()
		c.JSON(202, j)
	case "pause", "stop", "resume":
		a.mutationMu.Lock()
		defer a.mutationMu.Unlock()
		if action == "pause" || action == "resume" {
			if err = a.DB.Model(&task).Updates(map[string]any{"enabled": action == "resume", "next_run_at": time.Now()}).Error; err != nil {
				failure(c, 500, err)
				return
			}
		}
		if action != "resume" {
			a.syncMu.Lock()
			if cancel := a.syncActive[id]; cancel != nil {
				cancel()
			}
			a.syncMu.Unlock()
			a.DB.Model(&Job{}).Where("kind = ? AND target = ? AND status = ?", "sync.run", fmt.Sprint(id), "queued").Updates(map[string]any{"status": "interrupted", "error": "用户停止同步", "finished_at": time.Now()})
			a.DB.Model(&task).Update("next_run_at", time.Now().Add(time.Duration(task.IntervalSeconds)*time.Second))
		}
		a.audit(current(c).ID, current(c).Username, "sync."+action, fmt.Sprint(id), "", c.ClientIP())
		a.signalSync()
		c.JSON(200, gin.H{"ok": true})
	default:
		failure(c, 400, errors.New("未知同步操作"))
	}
}
func (a *App) deleteSyncTask(c *gin.Context) {
	id, err := paramID(c)
	if err != nil {
		return
	}
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	var task SyncTask
	if err = a.DB.First(&task, id).Error; err != nil {
		failure(c, 404, err)
		return
	}
	var n int64
	a.DB.Model(&Job{}).Where("kind = ? AND target = ? AND status IN ?", "sync.run", fmt.Sprint(id), []string{"queued", "running"}).Count(&n)
	if task.Enabled || n > 0 {
		failure(c, 409, errors.New("请先暂停自动同步并等待当前任务停止"))
		return
	}
	if err = a.DB.Delete(&task).Error; err != nil {
		failure(c, 500, err)
		return
	}
	a.audit(current(c).ID, current(c).Username, "sync.delete", fmt.Sprint(id), "仅删除任务配置，仓库和快照保留", c.ClientIP())
	c.JSON(200, gin.H{"ok": true})
}
