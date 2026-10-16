package appliance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPlatformPairingScopeAndIdempotentReservation(t *testing.T) {
	runner := &fakeRunner{fn: func(_ string, args []string) (string, error) {
		if len(args) > 0 && args[0] == "get" {
			return "yes\n", nil
		}
		return "", nil
	}}
	target := testApp(t, runner)
	source := testApp(t, &fakeRunner{})
	server := httptest.NewTLSServer(target.Handler())
	defer server.Close()
	hash := sha256.Sum256(server.Certificate().Raw)
	fp := hex.EncodeToString(hash[:])
	code := pairCode{InstanceID: target.instanceID(), Version: Version, Fingerprint: fp, Ticket: randomPeerToken(), ExpiresAt: time.Now().Add(time.Minute)}
	inv := code
	inv.Ticket = peerHash(code.Ticket)
	raw, _ := json.Marshal(inv)
	target.set("peer_pair_invitation", string(raw))
	payload, _ := json.Marshal(map[string]string{"ticket": code.Ticket, "instance_id": source.instanceID(), "name": "source"})
	var paired struct {
		Token string `json:"token"`
	}
	if e := peerHTTPRequest(context.Background(), server.URL, fp, "", "POST", "/pair", strings.NewReader(string(payload)), &paired); e != nil {
		t.Fatal(e)
	}
	if paired.Token == "" {
		t.Fatal("no scoped credential")
	}
	if e := peerHTTPRequest(context.Background(), server.URL, fp, "", "POST", "/pair", strings.NewReader(string(payload)), nil); e == nil {
		t.Fatal("single-use invitation reused")
	}
	_, cipher, e := source.cryptSyncSecret(syncSecret{Password: paired.Token}, "")
	if e != nil {
		t.Fatal(e)
	}
	peer := PlatformPeer{URL: server.URL, Fingerprint: fp, Credential: cipher}
	var first, second replicaRemoteState
	body := `{"task_key":"task-1","name":"【异地复制】重要数据","kind":"filesystem"}`
	for _, out := range []*replicaRemoteState{&first, &second} {
		if e = source.peerRequest(context.Background(), peer, "POST", "/repositories", strings.NewReader(body), out); e != nil {
			t.Fatal(e)
		}
	}
	if first.Repository.ID == 0 || first.Repository.ID != second.Repository.ID || first.Repository.DisplayName != "【异地复制】重要数据" {
		t.Fatal(first, second)
	}
	if e = source.peerRequest(context.Background(), peer, "GET", "/repositories/999", nil, nil); e == nil {
		t.Fatal("accessed unbound repository")
	}
	// Peer tokens cannot authenticate human/admin endpoints.
	req, _ := http.NewRequest("GET", server.URL+"/api/users", nil)
	req.Header.Set("Authorization", "Bearer "+paired.Token)
	resp, e := server.Client().Do(req)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatal(resp.StatusCode)
	}
	client, _ := pinnedPeerClient(fp)
	defer client.CloseIdleConnections()
	req, _ = http.NewRequest("GET", server.URL+"/api/peer/repositories/1", nil)
	req.Header.Set("Authorization", "Bearer "+paired.Token)
	req.Header.Set("X-Backup-Center-Version", "wrong-version")
	resp, e = client.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 409 {
		t.Fatal("version mismatch accepted", resp.StatusCode)
	}
	if e = peerHTTPRequest(context.Background(), server.URL, strings.Repeat("0", 64), paired.Token, "GET", "/repositories/1", nil, nil); e == nil {
		t.Fatal("certificate mismatch accepted")
	}
	var inbound PlatformPeer
	target.DB.First(&inbound)
	encoded, _ := json.Marshal(inbound)
	if strings.Contains(string(encoded), paired.Token) || strings.Contains(string(encoded), peerHash(paired.Token)) {
		t.Fatal("peer credential leaked")
	}
}
func TestReplicationSubmissionIsAsynchronousAndSimple(t *testing.T) {
	a := testApp(t, &fakeRunner{})
	token, csrf := sessionFor(t, a, "admin")
	r := Repository{Name: "service", Dataset: a.root + "/repos/service", Kind: "filesystem"}
	a.DB.Create(&r)
	peer := PlatformPeer{Direction: "outbound", Enabled: true}
	a.DB.Create(&peer)
	w := request(t, a.Handler(), "PUT", "/api/replications", fmt.Sprintf(`{"repository_id":%d}`, r.ID), token, csrf)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body)
	}
	var rep Replication
	a.DB.First(&rep)
	if rep.RemoteName != "【异地复制】service" || rep.State != "provisioning" || !rep.AfterSnapshot || rep.Cron != "" || rep.RemoteRepositoryID != 0 {
		t.Fatal(rep)
	}
	if len(a.Run.(*fakeRunner).calls) > 0 {
		t.Fatal("submission blocked on remote or storage commands")
	}
}
func TestPeerSecretsBackupRestorePaused(t *testing.T) {
	a := testApp(t, &fakeRunner{})
	_, cipher, e := a.cryptSyncSecret(syncSecret{Password: "private-scoped-token"}, "")
	if e != nil {
		t.Fatal(e)
	}
	p := PlatformPeer{Direction: "outbound", Credential: cipher, Enabled: true}
	a.DB.Create(&p)
	a.DB.Create(&Replication{PeerID: p.ID, Enabled: true})
	a.DB.Create(&ReplicaBinding{RepositoryID: 77, PeerID: p.ID, ReseedKey: "saved-catalog", ArchiveRepositoryID: 78, State: "waiting"})
	blob, e := a.backupBundle("long-enough-backup-password")
	if e != nil {
		t.Fatal(e)
	}
	b := testApp(t, &fakeRunner{})
	if e = b.RestoreConfig(blob, "long-enough-backup-password"); e != nil {
		t.Fatal(e)
	}
	var restored PlatformPeer
	b.DB.First(&restored)
	secret, _, e := b.cryptSyncSecret(syncSecret{}, restored.Credential)
	if e != nil || secret.Password != "private-scoped-token" || restored.Enabled {
		t.Fatal("peer restore", e)
	}
	var rep Replication
	b.DB.First(&rep)
	if rep.Enabled {
		t.Fatal("restored replication automatically enabled")
	}
	var binding ReplicaBinding
	b.DB.First(&binding)
	if binding.ReseedKey != "saved-catalog" || binding.ArchiveRepositoryID != 78 {
		t.Fatal("archive recovery intent lost", binding)
	}
}

func TestReplicaStateResponseReplacesCatalog(t *testing.T) {
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			fmt.Fprint(w, `{"snapshots":{"bc-old":"1"}}`)
		} else {
			fmt.Fprint(w, `{"snapshots":{}}`)
		}
	}))
	defer server.Close()
	h := sha256.Sum256(server.Certificate().Raw)
	a := testApp(t, &fakeRunner{})
	_, cipher, e := a.cryptSyncSecret(syncSecret{Password: "scoped-token"}, "")
	if e != nil {
		t.Fatal(e)
	}
	peer := PlatformPeer{URL: server.URL, Fingerprint: hex.EncodeToString(h[:]), Credential: cipher}
	var state replicaRemoteState
	for range 2 {
		if e = a.replicaJSON(context.Background(), peer, "GET", "/repositories/1", nil, &state); e != nil {
			t.Fatal(e)
		}
	}
	if len(state.Snapshots) != 0 {
		t.Fatal("obsolete peer snapshot survived a full response", state.Snapshots)
	}
}
