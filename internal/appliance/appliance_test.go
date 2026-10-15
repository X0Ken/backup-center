package appliance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeRunner struct {
	mu    sync.Mutex
	calls [][]string
	fn    func(string, []string) (string, error)
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string{name}, args...))
	f.mu.Unlock()
	if f.fn != nil {
		return f.fn(name, args)
	}
	return "", nil
}
func testApp(t *testing.T, r Runner) *App {
	t.Helper()
	a, e := Open(Config{DataDir: t.TempDir(), Pool: "tank", SecureCookie: false}, r)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(a.Close)
	return a
}
func TestInputBoundaries(t *testing.T) {
	for _, v := range []string{"-rf", "x;id", "../other", "a/b", "a\nroot", "a$(id)", "", "a b"} {
		if validName(v) == nil {
			t.Errorf("accepted unsafe name %q", v)
		}
	}
	for _, v := range []string{"repo", "mysql-01", "db_backup"} {
		if e := validName(v); e != nil {
			t.Fatal(e)
		}
	}
	for _, p := range []map[string]string{{"mountpoint": "/etc"}, {"sync": "disabled"}, {"quota": "-1T"}, {"recordsize": "x;id"}} {
		if validateProperties(p) == nil {
			t.Errorf("accepted unsafe properties %v", p)
		}
	}
	if e := validateProperties(map[string]string{"quota": "100G", "sync": "always", "compression": "zstd", "recordsize": "128K"}); e != nil {
		t.Fatal(e)
	}
}
func TestRepositoryOwnership(t *testing.T) {
	r := &fakeRunner{fn: func(_ string, _ []string) (string, error) { return "-\n", nil }}
	a := testApp(t, r)
	if e := a.owned(context.Background(), "outside/service"); e == nil {
		t.Fatal("accepted outside dataset")
	}
	if len(r.calls) > 0 {
		t.Fatal("outside path reached command runner")
	}
	if e := a.owned(context.Background(), "tank/backup-center/repos/service"); e == nil {
		t.Fatal("accepted unowned dataset")
	}
}
func TestRetentionCalendarAndProtectedPoints(t *testing.T) {
	now := time.Date(2026, 10, 15, 9, 0, 0, 0, time.Local)
	ss := []Snapshot{}
	for i := 0; i < 80; i++ {
		ss = append(ss, Snapshot{Name: fmt.Sprintf("tank/repo@bc-%03d", i), CreatedAt: now.Add(-time.Duration(i) * time.Hour), Managed: true, Clones: "-"})
	}
	ss[79].Holds = 1
	ss[78].Clones = "tank/clone"
	ss = append(ss, Snapshot{Name: "tank/repo@external", CreatedAt: now.Add(-365 * 24 * time.Hour)})
	keep := retentionKeep(ss, Policy{KeepHourly: 2, KeepDaily: 2})
	for _, name := range []string{"tank/repo@bc-000", "tank/repo@bc-001", "tank/repo@bc-010", "tank/repo@bc-079", "tank/repo@bc-078", "tank/repo@external"} {
		if !keep[name] {
			t.Errorf("lost protected or calendar recovery point %s", name)
		}
	}
	if keep["tank/repo@bc-002"] {
		t.Fatal("kept redundant hour in same day")
	}
	if e := validatePolicy(Policy{Cron: "* * * * *"}); e == nil {
		t.Fatal("allowed destructive zero retention")
	}
}
func TestSnapshotBelongsToRepository(t *testing.T) {
	r := Repository{Dataset: "tank/backup-center/repos/repo"}
	for _, s := range []string{"tank/other@bc-one", r.Dataset + "@bad;id", r.Dataset + "@../x", r.Dataset + "@a@b"} {
		if validSnapshot(r, s) == nil {
			t.Fatalf("unsafe snapshot %q", s)
		}
	}
}
func TestProtocolRestrictions(t *testing.T) {
	fs := Repository{Kind: "filesystem"}
	for _, client := range []string{"*", "*(rw,no_root_squash)", "0.0.0.0/0;id", ""} {
		if validateExport(Export{Protocol: "nfs", Clients: client}, fs) == nil {
			t.Fatal("unsafe clients accepted", client)
		}
	}
	if e := validateExport(Export{Protocol: "nfs", Clients: "198.51.100.0/24 198.51.100.4"}, fs); e != nil {
		t.Fatal(e)
	}
	if validateExport(Export{Protocol: "nfs", Clients: "198.51.100.4"}, Repository{Kind: "volume"}) == nil {
		t.Fatal("NFS on block accepted")
	}
	if validateReplication(Replication{Host: "host;touch /tmp/pwn", User: "root", Port: 22, TargetDataset: "tank/x"}) == nil {
		t.Fatal("replication shell injection")
	}
}
func TestQueuePersistenceAndRestart(t *testing.T) {
	dir := t.TempDir()
	a, e := Open(Config{DataDir: dir, Pool: "tank"}, &fakeRunner{})
	if e != nil {
		t.Fatal(e)
	}
	job, e := a.enqueue("snapshot.create", "1", ActionRequest{RepositoryID: 1}, 1)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = a.enqueue("snapshot.create", "1", ActionRequest{RepositoryID: 1}, 1); e == nil {
		t.Fatal("duplicate accepted")
	}
	a.DB.Model(&job).Update("status", "running")
	queued, e := a.enqueue("snapshot.create", "2", ActionRequest{RepositoryID: 2}, 1)
	if e != nil {
		t.Fatal(e)
	}
	a.Close()
	b, e := Open(Config{DataDir: dir, Pool: "tank"}, &fakeRunner{})
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	var j Job
	b.DB.First(&j, job.ID)
	if j.Status != "interrupted" {
		t.Fatal(j.Status)
	}
	j = Job{}
	b.DB.First(&j, queued.ID)
	if j.Status != "queued" {
		t.Fatal(j.Status)
	}
}
func TestExclusiveDataDirectory(t *testing.T) {
	dir := t.TempDir()
	a, e := Open(Config{DataDir: dir, Pool: "tank"}, &fakeRunner{})
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	if b, e := Open(Config{DataDir: dir, Pool: "tank"}, &fakeRunner{}); e == nil {
		b.Close()
		t.Fatal("two daemons opened the same metadata database")
	}
}
func request(t *testing.T, h http.Handler, method, path, body, token, csrf string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.AddCookie(&http.Cookie{Name: "bc_session", Value: token})
	}
	if csrf != "" {
		r.Header.Set("X-CSRF-Token", csrf)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func sessionFor(t *testing.T, a *App, role string) (string, string) {
	t.Helper()
	hash, e := passwordHash("test-password-long-enough")
	if e != nil {
		t.Fatal(e)
	}
	u := User{Username: "user-" + role, Password: hash, Role: role}
	if e = a.DB.Create(&u).Error; e != nil {
		t.Fatal(e)
	}
	token, _ := randomToken(32)
	csrf, _ := randomToken(32)
	if e = a.DB.Create(&Session{ID: hashToken(token), UserID: u.ID, CSRF: csrf, ExpiresAt: time.Now().Add(time.Hour)}).Error; e != nil {
		t.Fatal(e)
	}
	return token, csrf
}
func TestAuthenticationCSRFAndRole(t *testing.T) {
	a := testApp(t, &fakeRunner{})
	h := a.Handler()
	if w := request(t, h, "GET", "/api/repositories", "", "", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	token, csrf := sessionFor(t, a, "operator")
	if w := request(t, h, "POST", "/api/repositories", `{"name":"data"}`, token, csrf); w.Code != 403 {
		t.Fatal(w.Code, w.Body)
	}
	if w := request(t, h, "POST", "/api/actions/snapshot.create", `{"repository_id":1}`, token, ""); w.Code != 403 {
		t.Fatal("CSRF bypass", w.Code)
	}
	adminToken, adminCSRF := sessionFor(t, a, "admin")
	w := request(t, h, "POST", "/api/repositories", `{"name":"backup","kind":"filesystem"}`, adminToken, adminCSRF)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body)
	}
	var job Job
	if e := json.Unmarshal(w.Body.Bytes(), &job); e != nil || job.Status != "queued" {
		t.Fatal(e, w.Body)
	}
	if w = request(t, h, "POST", "/api/repositories", `{"name":"backup","unknown":true}`, adminToken, adminCSRF); w.Code != 400 {
		t.Fatal("unknown JSON accepted", w.Code)
	}
	if w = request(t, h, "POST", "/api/actions/restore.rollback", `{"repository_id":1}`, token, csrf); w.Code != 403 {
		t.Fatal("operator rollback", w.Code)
	}
}
func TestBootstrapAndSessionRevocation(t *testing.T) {
	a := testApp(t, &fakeRunner{})
	if e := a.InitAdmin("admin", "long-initial-password"); e != nil {
		t.Fatal(e)
	}
	if e := a.InitAdmin("admin", "other-long-password"); e == nil {
		t.Fatal("bootstrap takeover")
	}
	h := a.Handler()
	w := request(t, h, "POST", "/api/auth/login", `{"username":"admin","password":"long-initial-password"}`, "", "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var auth struct {
		CSRF string `json:"csrf"`
	}
	json.Unmarshal(w.Body.Bytes(), &auth)
	cookies := w.Result().Cookies()
	if len(cookies) == 0 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("unsafe cookie")
	}
	token := cookies[0].Value
	if w = request(t, h, "POST", "/api/repositories", `{"name":"repo"}`, token, auth.CSRF); w.Code != 403 {
		t.Fatal("password change bypass")
	}
	if w = request(t, h, "POST", "/api/auth/password", `{"current_password":"long-initial-password","new_password":"replacement-password-long"}`, token, auth.CSRF); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w = request(t, h, "POST", "/api/auth/logout", `{}`, token, auth.CSRF); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w = request(t, h, "GET", "/api/auth/me", "", token, ""); w.Code != 401 {
		t.Fatal("session not revoked")
	}
}
func TestDeleteChecksSnapshotsBeforeDestroy(t *testing.T) {
	r := &fakeRunner{fn: func(name string, args []string) (string, error) {
		if name != "zfs" {
			return "", errors.New("unexpected command")
		}
		switch args[0] {
		case "get":
			return "yes\n", nil
		case "list":
			return "tank/backup-center/repos/repo@bc-one\t1\t0\t0\t-\t0\t10\n", nil
		default:
			return "", errors.New("destroy must not be called")
		}
	}}
	a := testApp(t, r)
	repo := Repository{Name: "repo", Dataset: a.root + "/repos/repo", Kind: "filesystem"}
	a.DB.Create(&repo)
	_, e := a.action(context.Background(), "repository.delete", ActionRequest{RepositoryID: repo.ID, Confirm: "repo"})
	if e == nil || !strings.Contains(e.Error(), "快照") {
		t.Fatal(e)
	}
	for _, c := range r.calls {
		if len(c) > 1 && c[1] == "destroy" {
			t.Fatal("destruction attempted")
		}
	}
}
func TestFailedCommandsNeverReportSuccess(t *testing.T) {
	r := &fakeRunner{fn: func(_ string, args []string) (string, error) {
		if args[0] == "get" {
			return "yes\n", nil
		}
		return "", errors.New("zfs failed")
	}}
	a := testApp(t, r)
	repo := Repository{Name: "repo", Dataset: a.root + "/repos/repo", Kind: "filesystem"}
	a.DB.Create(&repo)
	if _, e := a.createSnapshot(context.Background(), ActionRequest{RepositoryID: repo.ID}); e == nil {
		t.Fatal("snapshot reported success after command failure")
	}
}
func TestAtomicConfigAndFilesystemConfinement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "exports", "test.exports")
	if e := atomicFile(path, []byte("complete\n"), 0600); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(path)
	if e != nil || string(b) != "complete\n" {
		t.Fatal(e)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	rootDir := filepath.Join(dir, "snapshot")
	os.Mkdir(rootDir, 0700)
	os.WriteFile(filepath.Join(dir, "secret"), []byte("private"), 0600)
	os.Symlink("../secret", filepath.Join(rootDir, "escape"))
	root, e := os.OpenRoot(rootDir)
	if e != nil {
		t.Fatal(e)
	}
	defer root.Close()
	if f, e := root.Open("escape"); e == nil {
		f.Close()
		t.Fatal("snapshot escaped through symlink")
	}
}
func TestEncryptedConfigRoundTrip(t *testing.T) {
	a := testApp(t, &fakeRunner{})
	if e := a.InitAdmin("admin", "a-very-long-admin-password"); e != nil {
		t.Fatal(e)
	}
	r := Repository{Name: "files", Dataset: a.root + "/repos/files", Kind: "filesystem"}
	if e := a.DB.Create(&r).Error; e != nil {
		t.Fatal(e)
	}
	os.Mkdir(filepath.Join(a.Config.DataDir, "keys"), 0700)
	secret := bytes.Repeat([]byte{42}, 32)
	os.WriteFile(filepath.Join(a.Config.DataDir, "keys", "files.key"), secret, 0600)
	data, e := a.backupBundle("a-very-long-backup-passphrase")
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(data, secret) || bytes.Contains(data, []byte("files")) {
		t.Fatal("plaintext secret in backup")
	}
	if _, e = decryptBundle(data, "incorrect-passphrase"); e == nil {
		t.Fatal("wrong password accepted")
	}
	tampered := append([]byte{}, data...)
	tampered[len(tampered)-1] ^= 1
	if _, e = decryptBundle(tampered, "a-very-long-backup-passphrase"); e == nil {
		t.Fatal("tampered backup accepted")
	}
	b := testApp(t, &fakeRunner{})
	if e = b.RestoreConfig(data, "a-very-long-backup-passphrase"); e != nil {
		t.Fatal(e)
	}
	var repos []Repository
	b.DB.Find(&repos)
	if len(repos) != 1 || repos[0].Name != "files" {
		t.Fatal(repos)
	}
	got, e := os.ReadFile(filepath.Join(b.Config.DataDir, "keys", "files.key"))
	if e != nil || !bytes.Equal(got, secret) {
		t.Fatal("key restore failed", e)
	}
	if e = b.RestoreConfig(data, "a-very-long-backup-passphrase"); e == nil {
		t.Fatal("overwrote initialized system")
	}
}
func TestRecursiveRetentionPreservesEachDataset(t *testing.T) {
	now := time.Now()
	ss := []Snapshot{{Name: "tank/repo@bc-new", CreatedAt: now, Managed: true, Clones: "-"}, {Name: "tank/repo/child@bc-new", CreatedAt: now, Managed: true, Clones: "-"}, {Name: "tank/repo/child@bc-old", CreatedAt: now.Add(-2 * time.Hour), Managed: true, Clones: "-"}}
	keep := retentionKeep(ss, Policy{KeepHourly: 1})
	if !keep[ss[0].Name] || !keep[ss[1].Name] || keep[ss[2].Name] {
		t.Fatal(keep)
	}
}
func TestVolumeCreationOmitsFilesystemProperties(t *testing.T) {
	r := &fakeRunner{fn: func(name string, args []string) (string, error) {
		if name == "zfs" && args[0] == "get" {
			return "yes\n", nil
		}
		return "", nil
	}}
	a := testApp(t, r)
	_, e := a.createRepo(context.Background(), RepoRequest{Name: "volume", Kind: "volume", VolumeSize: "1G", Properties: map[string]string{"quota": "2G", "recordsize": "128K", "atime": "off", "compression": "lz4"}})
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, c := range r.calls {
		if len(c) > 2 && c[1] == "create" && strings.Contains(strings.Join(c, " "), "-V 1G") {
			found = true
			for _, arg := range c {
				if strings.HasPrefix(arg, "quota=") || strings.HasPrefix(arg, "recordsize=") || strings.HasPrefix(arg, "atime=") {
					t.Fatal("filesystem property sent to zvol", arg)
				}
			}
		}
	}
	if !found {
		t.Fatal("no volume creation command")
	}
}

func TestSameSecondRetentionKeepsNewestTransaction(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	ss := []Snapshot{{Name: "tank/repo@bc-old", CreatedAt: now, Txg: 10, Managed: true, Clones: "-"}, {Name: "tank/repo@bc-new", CreatedAt: now, Txg: 12, Managed: true, Clones: "-"}}
	keep := retentionKeep(ss, Policy{KeepHourly: 1})
	if !keep["tank/repo@bc-new"] || keep["tank/repo@bc-old"] {
		t.Fatal(keep)
	}
}

func TestGeneratedSnapshotNamesAreUsable(t *testing.T) {
	r := Repository{Dataset: "tank/backup-center/repos/repo"}
	if e := validSnapshot(r, r.Dataset+"@bc-20261015T165501.123456789Z"); e != nil {
		t.Fatal(e)
	}
}

func TestCommandOutputTruncationIsExplicit(t *testing.T) {
	var b cappedBuffer
	chunk := bytes.Repeat([]byte{'x'}, 1<<20)
	for i := 0; i < 17; i++ {
		if n, e := b.Write(chunk); e != nil || n != len(chunk) {
			t.Fatal(n, e)
		}
	}
	if !b.Truncated() || len(b.String()) != 16<<20 {
		t.Fatal("output limit not recorded")
	}
}

type blockingRunner struct{ started chan struct{} }

func (r blockingRunner) Run(ctx context.Context, _ string, _ ...string) (string, error) {
	close(r.started)
	<-ctx.Done()
	return "", ctx.Err()
}
func TestGracefulShutdownMarksJobInterrupted(t *testing.T) {
	dir := t.TempDir()
	runner := blockingRunner{started: make(chan struct{})}
	a, e := Open(Config{DataDir: dir, Pool: "tank"}, runner)
	if e != nil {
		t.Fatal(e)
	}
	j, e := a.enqueue("pool.scrub", "tank", nil, 1)
	if e != nil {
		t.Fatal(e)
	}
	a.wg.Add(1)
	go a.worker()
	select {
	case <-runner.started:
	case <-time.After(3 * time.Second):
		t.Fatal("worker never started")
	}
	a.Close()
	b, e := Open(Config{DataDir: dir, Pool: "tank"}, &fakeRunner{})
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	var job Job
	b.DB.First(&job, j.ID)
	if job.Status != "interrupted" {
		t.Fatal(job.Status, job.Error)
	}
}

func TestPolicyCadenceFromRetention(t *testing.T) {
	cases := []struct {
		p    Policy
		cron string
	}{
		{Policy{KeepHourly: 24, KeepDaily: 30}, "0 * * * *"},
		{Policy{KeepDaily: 30, KeepWeekly: 12}, "0 0 * * *"},
		{Policy{KeepWeekly: 12, KeepMonthly: 12}, "0 0 * * 1"},
		{Policy{KeepMonthly: 12, KeepYearly: 3}, "0 0 1 * *"},
		{Policy{KeepYearly: 3}, "0 0 1 1 *"},
	}
	for _, c := range cases {
		c.p.Cron = "invalid legacy schedule"
		if err := validatePolicy(c.p); err != nil {
			t.Fatal(err)
		}
		if got := policyCron(c.p); got != c.cron {
			t.Fatalf("got %s, want %s", got, c.cron)
		}
	}
	if validatePolicy(Policy{}) == nil || validatePolicy(Policy{KeepDaily: -1}) == nil {
		t.Fatal("invalid retention accepted")
	}
	p := Policy{KeepWeekly: 2}
	if policyPeriod(p, time.Date(2026, 12, 31, 12, 0, 0, 0, time.Local)) != policyPeriod(p, time.Date(2027, 1, 1, 12, 0, 0, 0, time.Local)) {
		t.Fatal("ISO week crosses year incorrectly")
	}
}
func TestAutomaticPolicyCatchupAndPause(t *testing.T) {
	now := time.Now()
	repo := Repository{Name: "auto", Dataset: "tank/backup-center/repos/auto", Kind: "filesystem"}
	output := ""
	r := &fakeRunner{fn: func(_ string, args []string) (string, error) { return output, nil }}
	a := testApp(t, r)
	if err := a.DB.Create(&repo).Error; err != nil {
		t.Fatal(err)
	}
	p := Policy{RepositoryID: repo.ID, Enabled: true, KeepDaily: 3, Cron: "* * * * *"}
	if err := a.DB.Create(&p).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.reloadSchedules(); err != nil {
		t.Fatal(err)
	}
	a.DB.First(&p, p.ID)
	if p.Cron != "0 0 * * *" {
		t.Fatal("legacy cron not normalized")
	}
	output = fmt.Sprintf("%s@bc-present\t%d\t0\t0\t-\t0\t1\n", repo.Dataset, now.Unix())
	if err := a.ensurePolicyPeriod(context.Background(), p, now); err != nil {
		t.Fatal(err)
	}
	var count int64
	a.DB.Model(&Job{}).Count(&count)
	if count != 0 {
		t.Fatal("duplicate snapshot in protected period")
	}
	output = ""
	if err := a.ensurePolicyPeriod(context.Background(), p, now); err != nil {
		t.Fatal(err)
	}
	a.DB.Model(&Job{}).Count(&count)
	if count != 1 {
		t.Fatal("missing catch-up snapshot")
	}
	a.DB.Model(&p).Update("enabled", false)
	var j Job
	a.DB.First(&j)
	if _, err := a.execute(j); err != nil {
		t.Fatal(err)
	}
	for _, call := range r.calls {
		if len(call) > 1 && call[1] == "snapshot" {
			t.Fatal("paused policy created snapshot")
		}
	}
}
func TestRepositoryDeterminesAccessProtocol(t *testing.T) {
	fs := Repository{Name: "files", Kind: "filesystem"}
	e, err := normalizeExport(Export{Clients: "198.51.100.0/24", IQN: "irrelevant"}, fs)
	if err != nil || e.Protocol != "nfs" || e.IQN != "" {
		t.Fatalf("NFS inference failed: %+v %v", e, err)
	}
	if _, err := normalizeExport(Export{Protocol: "iscsi"}, fs); err == nil {
		t.Fatal("protocol override accepted")
	}
	e, err = normalizeExport(Export{Initiators: "iqn.2026-10.local.client:one", Clients: "irrelevant", ReadOnly: true}, Repository{Name: "mysql", Kind: "volume"})
	if err != nil || e.Protocol != "iscsi" || e.IQN != "iqn.2026-10.local.backup-center:repo-0-mysql" || e.Clients != "" || e.ReadOnly {
		t.Fatalf("iSCSI inference failed: %+v %v", e, err)
	}
}
func TestPhysicalDiskInventory(t *testing.T) {
	raw := `{"blockdevices":[{"name":"/dev/sda","type":"disk","size":480103981056,"model":"SAMSUNG","serial":"system","tran":"sata","rota":false,"children":[{"name":"/dev/sda1","mountpoints":["/"]}]},{"name":"/dev/zd0","type":"disk","size":100},{"name":"/dev/nvme0n1","type":"disk","size":3200000000000,"model":"INTEL","tran":"nvme","rota":false,"mountpoints":[null],"children":[{"name":"/dev/nvme0n1p1","mountpoints":[null]}]}]}`
	disks, err := parseDisks(raw, "\t/dev/nvme0n1p1 ONLINE 0 0 0\n")
	if err != nil || len(disks) != 2 {
		t.Fatalf("unexpected inventory %+v %v", disks, err)
	}
	if disks[0].Role != "系统盘" || disks[1].PoolState != "ONLINE" || disks[1].Role != "备份数据盘" {
		t.Fatalf("incorrect disk roles: %+v", disks)
	}
	if _, err := parseDisks("bad json", ""); err == nil {
		t.Fatal("invalid inventory silently accepted")
	}
}
