package appliance

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func validateReplication(r Replication) error {
	if !hostRE.MatchString(r.Host) || !userRE.MatchString(r.User) || r.Port < 1 || r.Port > 65535 || !datasetRE.MatchString(r.TargetDataset) || !strings.Contains(r.TargetDataset, "/") {
		return errors.New("复制主机、用户名、端口或目标数据集无效")
	}
	if r.Cron != "" {
		p := Policy{Cron: r.Cron, KeepDaily: 1}
		return validatePolicy(p)
	}
	return nil
}
func (a *App) sshArgs(r Replication, remote ...string) []string {
	return append([]string{"-i", filepath.Join(a.Config.DataDir, "ssh", "id_ed25519"), "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=" + filepath.Join(a.Config.DataDir, "ssh", "known_hosts"), "-o", "ConnectTimeout=10", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3", "-p", strconv.Itoa(r.Port), r.User + "@" + r.Host}, remote...)
}
func (a *App) remote(ctx context.Context, r Replication, args ...string) (string, error) {
	return a.Run.Run(ctx, "ssh", a.sshArgs(r, args...)...)
}
func (a *App) stream(ctx context.Context, r Replication, args []string) (int64, error) {
	send := command(ctx, "zfs", args...)
	recv := command(ctx, "ssh", a.sshArgs(r, "zfs", "receive", "-s", "-u", r.TargetDataset)...)
	var sb, rb cappedBuffer
	send.Stderr = &sb
	recv.Stderr = &rb
	recv.Stdout = &rb
	rd, wr := io.Pipe()
	sendOut, err := send.StdoutPipe()
	if err != nil {
		return 0, err
	}
	recv.Stdin = rd
	if err = recv.Start(); err != nil {
		return 0, err
	}
	if err = send.Start(); err != nil {
		wr.CloseWithError(err)
		recv.Wait()
		return 0, err
	}
	n, copyErr := copyStream(wr, sendOut)
	wr.CloseWithError(copyErr)
	sendErr := send.Wait()
	recvErr := recv.Wait()
	rd.Close()
	if sendErr != nil || recvErr != nil || copyErr != nil {
		return n, fmt.Errorf("复制失败（接收端保留续传状态）send=%v receive=%v io=%v: %s %s", sendErr, recvErr, copyErr, sb.String(), rb.String())
	}
	return n, nil
}
func (a *App) replicate(ctx context.Context, id uint) (result string, err error) {
	var rep Replication
	if err = a.DB.First(&rep, id).Error; err != nil {
		return "", err
	}
	if err = validateReplication(rep); err != nil {
		return "", err
	}
	repo, err := a.repo(rep.RepositoryID)
	if err != nil {
		return "", err
	}
	if err = a.owned(ctx, repo.Dataset); err != nil {
		return "", err
	}
	ss, err := a.snapshots(ctx, repo)
	if err != nil {
		return "", err
	}
	managed := []Snapshot{}
	for _, s := range ss {
		if s.Managed && strings.HasPrefix(s.Name, repo.Dataset+"@bc-") {
			managed = append(managed, s)
		}
	}
	if len(managed) == 0 {
		return "", errors.New("请先创建快照")
	}
	sort.Slice(managed, func(i, j int) bool {
		if managed[i].CreatedAt.Equal(managed[j].CreatedAt) {
			return managed[i].Txg < managed[j].Txg
		}
		return managed[i].CreatedAt.Before(managed[j].CreatedAt)
	})
	start := time.Now()
	var transferred int64
	defer func() {
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		a.DB.Model(&rep).Updates(map[string]any{"last_run_at": start, "last_error": msg, "bytes": transferred, "duration_seconds": time.Since(start).Seconds()})
	}()
	if _, e := a.remote(ctx, rep, "true"); e != nil {
		return "", fmt.Errorf("SSH 连接失败，请配置公钥和已验证的主机指纹: %w", e)
	}
	// Probe the parent separately: an absent target is allowed; an unreachable host is not.
	parent := rep.TargetDataset[:strings.LastIndex(rep.TargetDataset, "/")]
	if _, e := a.remote(ctx, rep, "zfs", "list", "-H", parent); e != nil {
		return "", fmt.Errorf("目标父数据集必须预先创建: %w", e)
	}
	token, e := a.remote(ctx, rep, "zfs", "get", "-H", "-o", "value", "receive_resume_token", rep.TargetDataset)
	if e == nil && strings.TrimSpace(token) != "-" && strings.TrimSpace(token) != "" {
		n, e := a.stream(ctx, rep, []string{"send", "-t", strings.TrimSpace(token)})
		transferred += n
		if e != nil {
			return "", e
		}
	}
	remoteSnaps, e := a.remote(ctx, rep, "zfs", "list", "-Hp", "-r", "-t", "snapshot", "-o", "name,guid", rep.TargetDataset)
	byGUID := map[string]string{}
	if e == nil {
		for _, f := range fields(remoteSnaps) {
			if len(f) == 2 {
				byGUID[f[1]] = f[0]
			}
		}
	}
	latest := managed[len(managed)-1]
	guid, e := a.zfs(ctx, "get", "-H", "-p", "-o", "value", "guid", latest.Name)
	if e != nil {
		return "", e
	}
	guid = strings.TrimSpace(guid)
	if byGUID[guid] == "" {
		base := ""
		for _, s := range managed {
			g, e := a.zfs(ctx, "get", "-H", "-p", "-o", "value", "guid", s.Name)
			if e != nil {
				return "", e
			}
			if rs := byGUID[strings.TrimSpace(g)]; rs != "" && strings.SplitN(rs, "@", 2)[1] == strings.SplitN(s.Name, "@", 2)[1] {
				base = s.Name
			}
		}
		args := []string{"send", "-w"}
		if base != "" {
			args = append(args, "-i", base)
		}
		args = append(args, latest.Name)
		if _, err = a.zfs(ctx, "hold", fmt.Sprintf("bc-transfer-%d", id), latest.Name); err != nil {
			return "", err
		}
		defer a.zfs(context.Background(), "release", fmt.Sprintf("bc-transfer-%d", id), latest.Name)
		n, e := a.stream(ctx, rep, args)
		transferred += n
		if e != nil {
			return "", e
		}
	}
	remoteName := rep.TargetDataset + "@" + strings.SplitN(latest.Name, "@", 2)[1]
	got, e := a.remote(ctx, rep, "zfs", "get", "-Hp", "-o", "value", "guid", remoteName)
	if e != nil || strings.TrimSpace(got) != guid {
		return "", errors.New("复制后的快照 GUID 校验失败，未推进增量基线")
	}
	tag := fmt.Sprintf("bc-base-%d", id)
	holds, _ := a.zfs(ctx, "holds", "-H", latest.Name)
	if !strings.Contains(holds, "\t"+tag+"\t") {
		if _, err = a.zfs(ctx, "hold", tag, latest.Name); err != nil {
			return "", err
		}
	}
	old := rep.LastSnapshot
	rep.LastSnapshot = latest.Name
	rep.LastGUID = guid
	if err = a.DB.Save(&rep).Error; err != nil {
		return "", err
	}
	if old != "" && old != latest.Name {
		if _, e := a.zfs(ctx, "release", tag, old); e != nil {
			a.raise("replication-hold-"+strconv.Itoa(int(id)), "warning", e.Error())
		}
	}
	return fmt.Sprintf("GUID 校验完成，传输 %d 字节", transferred), nil
}
func (a *App) ensureSSH() (string, error) {
	dir := filepath.Join(a.Config.DataDir, "ssh")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	key := filepath.Join(dir, "id_ed25519")
	if _, e := os.Stat(key); os.IsNotExist(e) {
		if _, err := a.Run.Run(a.ctx, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key, "-C", "backup-center"); err != nil {
			return "", err
		}
	}
	b, e := os.ReadFile(key + ".pub")
	return string(b), e
}
