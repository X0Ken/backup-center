package appliance

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestReplicaCatalogChangesAndHistoryGap(t *testing.T) {
	rows := []replicaSnapshot{{"tank/repo@bc-a", "1", 10}, {"tank/repo@bc-b", "2", 20}, {"tank/repo@bc-c", "3", 30}}
	remote := map[string]string{"bc-c": "3"}
	if !needsReplicaReseed(rows, remote) {
		t.Fatal("older history gap was ignored")
	}
	if n := len(missingReplicaSnapshots(rows, remote)); n != 2 {
		t.Fatal(n)
	}
	if needsReplicaReseed(rows, map[string]string{"bc-a": "1"}) {
		t.Fatal("ordinary incremental backlog needs no reseed")
	}
	if catalogHash(rows) == catalogHash(rows[1:]) {
		t.Fatal("deletion did not change catalog fingerprint")
	}
	r := &fakeRunner{fn: func(_ string, args []string) (string, error) {
		return "tank/repo@bc-c\t3\t30\ntank/repo@bc-a\t1\t10\ntank/repo/child@bc-child\t8\t8\n", nil
	}}
	a := testApp(t, r)
	catalog, e := a.replicaCatalog(context.Background(), Repository{Dataset: "tank/repo"})
	if e != nil || len(catalog) != 2 || catalog[0].GUID != "1" {
		t.Fatal(catalog, e)
	}
}
func TestPauseClearsQueueCancelsActiveAndDisallowsManualBypass(t *testing.T) {
	a := testApp(t, &fakeRunner{})
	token, csrf := sessionFor(t, a, "admin")
	rep := Replication{RepositoryID: 1, PeerID: 1, Enabled: true}
	a.DB.Create(&rep)
	job, e := a.enqueue("replicate", fmt.Sprint(rep.ID), rep.ID, 0)
	if e != nil {
		t.Fatal(e)
	}
	var canceled atomic.Bool
	a.replicaActive[rep.ID] = func() { canceled.Store(true) }
	w := request(t, a.Handler(), "PUT", "/api/replications", fmt.Sprintf(`{"id":%d,"enabled":false}`, rep.ID), token, csrf)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	a.DB.First(&rep, rep.ID)
	a.DB.First(&job, job.ID)
	if rep.Enabled || !canceled.Load() || job.Status != "interrupted" {
		t.Fatal(rep, job, canceled.Load())
	}
	w = request(t, a.Handler(), "POST", "/api/actions/replicate", fmt.Sprintf(`{"id":%d}`, rep.ID), token, csrf)
	if w.Code != 400 {
		t.Fatal("manual bypass", w.Code)
	}
	delete(a.replicaActive, rep.ID)
	w = request(t, a.Handler(), "PUT", "/api/replications", fmt.Sprintf(`{"id":%d,"enabled":true}`, rep.ID), token, csrf)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	a.DB.First(&rep, rep.ID)
	if !rep.Enabled || rep.State != "pending" || time.Until(rep.NextAttemptAt) > time.Second {
		t.Fatal("start did not request immediate reconciliation", rep)
	}
}
func TestReplicaReconcileRefusesIncompleteSetAndProtectsClones(t *testing.T) {
	var destroyed []string
	r := &fakeRunner{fn: func(_ string, args []string) (string, error) {
		joined := strings.Join(args, " ")
		if args[0] == "get" {
			if strings.Contains(joined, "receive_resume_token") {
				return "-\n", nil
			}
			return "yes\n", nil
		}
		if strings.Contains(joined, "name,guid") {
			return "tank/backup-center/repos/replica@bc-new\t3\ntank/backup-center/repos/replica@bc-old\t1\ntank/backup-center/repos/replica@bc-held\t2\n", nil
		}
		if strings.Contains(joined, "name,creation,") {
			return "tank/backup-center/repos/replica@bc-old\t1\t0\t0\t-\t0\t1\ntank/backup-center/repos/replica@bc-held\t2\t0\t0\ttank/clone\t0\t2\ntank/backup-center/repos/replica@bc-new\t3\t0\t0\t-\t0\t3\n", nil
		}
		if args[0] == "destroy" {
			destroyed = append(destroyed, args[1])
		}
		return "", nil
	}}
	a := testApp(t, r)
	repo := Repository{Dataset: a.root + "/repos/replica", Replica: true}
	if _, e := a.reconcileReplicaSnapshots(context.Background(), repo, ReplicaBinding{}, replicaReconcileInput{Snapshots: map[string]string{"bc-missing": "4"}, Baseline: "4"}); e == nil || len(destroyed) != 0 {
		t.Fatal("pruned before full GUID acknowledgement")
	}
	result, e := a.reconcileReplicaSnapshots(context.Background(), repo, ReplicaBinding{}, replicaReconcileInput{Snapshots: map[string]string{"bc-new": "3"}, Baseline: "3"})
	if e != nil || len(result.Deleted) != 1 || len(result.Protected) != 1 || len(destroyed) != 1 || !strings.HasSuffix(destroyed[0], "@bc-old") {
		t.Fatal(result, e, destroyed)
	}
}

func TestReplicaArchiveIntentRecoversAfterRename(t *testing.T) {
	datasets := map[string]bool{"tank/backup-center/repos/replica": true}
	renames := 0
	r := &fakeRunner{fn: func(_ string, args []string) (string, error) {
		switch args[0] {
		case "get":
			return "yes\n", nil
		case "list":
			if !datasets[args[len(args)-1]] {
				return "", fmt.Errorf("absent")
			}
		case "rename":
			delete(datasets, args[1])
			datasets[args[2]] = true
			renames++
		}
		return "", nil
	}}
	a := testApp(t, r)
	repo := Repository{Name: "replica", Dataset: a.root + "/repos/replica", Kind: "filesystem", Replica: true}
	archive := Repository{Name: "replica-old", Dataset: a.root + "/repos/replica-old", Kind: "filesystem", Replica: true}
	a.DB.Create(&repo)
	a.DB.Create(&archive)
	b := ReplicaBinding{RepositoryID: repo.ID, ArchiveRepositoryID: archive.ID, ReseedKey: "catalog", State: "archiving"}
	a.DB.Create(&b)
	if e := a.finishReplicaReseed(context.Background(), repo, &b); e != nil {
		t.Fatal(e)
	}
	if b.State != "waiting" || !datasets[archive.Dataset] || datasets[repo.Dataset] {
		t.Fatal(b, datasets)
	}
	// Simulate a crash after rename but before the final metadata update.
	b.State = "archiving"
	a.DB.Model(&b).Update("state", "archiving")
	if e := a.finishReplicaReseed(context.Background(), repo, &b); e != nil {
		t.Fatal(e)
	}
	if renames != 1 || b.State != "waiting" {
		t.Fatal("archive duplicated after restart", renames, b)
	}
}

func TestReplicaAcknowledgementReleasesOnlyRelevantTaskHolds(t *testing.T) {
	var released []string
	r := &fakeRunner{fn: func(_ string, args []string) (string, error) {
		if args[0] == "holds" {
			return args[2] + "\tbc-base-1\tdate\n" + args[2] + "\tbc-transfer-1\tdate\n" + args[2] + "\tbc-protect\tdate\n", nil
		}
		if args[0] == "release" {
			released = append(released, args[1]+":"+args[2])
		}
		if args[0] == "list" {
			return "", fmt.Errorf("should not enumerate whole history for an acknowledgement")
		}
		return "", nil
	}}
	a := testApp(t, r)
	rep := Replication{ID: 1, LastSnapshot: a.root + "/repos/r@bc-old", TransferSnapshot: a.root + "/repos/r@bc-new"}
	a.DB.Create(&rep)
	target := replicaSnapshot{Name: rep.TransferSnapshot, GUID: "99"}
	if e := a.ackReplicaSnapshot(context.Background(), &rep, target, a.root+"/repos/r@bc-common"); e != nil {
		t.Fatal(e)
	}
	if len(released) != 5 {
		t.Fatal(released)
	}
	for _, entry := range released {
		if strings.Contains(entry, "bc-protect") || entry == "bc-base-1:"+target.Name {
			t.Fatal("released protection or current base", entry)
		}
	}
}
