package appliance

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

func TestSyncCredentialEncryptionAndBackup(t *testing.T) {
	a := testApp(t, &fakeRunner{})
	secret := syncSecret{Password: "a-very-private-password"}
	_, cipher, err := a.cryptSyncSecret(secret, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(cipher, secret.Password) {
		t.Fatal("plaintext credential stored")
	}
	task := SyncTask{Name: "source", RepositoryID: 12, Credential: cipher, HostKey: "test-host-key", Enabled: true}
	if err = a.DB.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(task)
	if strings.Contains(string(encoded), cipher) || strings.Contains(string(encoded), "test-host-key") {
		t.Fatal("API exposed credential")
	}
	b, err := a.backupBundle("a-long-backup-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	restored := testApp(t, &fakeRunner{})
	if err = restored.RestoreConfig(b, "a-long-backup-passphrase"); err != nil {
		t.Fatal(err)
	}
	var copy SyncTask
	restored.DB.First(&copy)
	decoded, _, err := restored.cryptSyncSecret(syncSecret{}, copy.Credential)
	if err != nil || decoded.Password != secret.Password || copy.Enabled {
		t.Fatalf("credential restore failed: %v", err)
	}
	key := filepath.Join(restored.Config.DataDir, "keys", ".sync-master.key")
	if err = os.Remove(key); err != nil {
		t.Fatal(err)
	}
	if _, _, err = restored.cryptSyncSecret(syncSecret{}, copy.Credential); err == nil {
		t.Fatal("missing key was silently regenerated")
	}
}
func TestSyncDeletionAndCurrentConfinement(t *testing.T) {
	task := SyncTask{ID: 1, MaxDeletePercent: 20}
	old := map[string]int64{"a": 1, "b": 1, "c": 1, "d": 1, "e": 1}
	if n, e := validateSyncDeletion(old, map[string]int64{"a": 1, "b": 1, "c": 1, "d": 1}, task); n != 1 || e != nil {
		t.Fatal(n, e)
	}
	if _, e := validateSyncDeletion(old, map[string]int64{"a": 1}, task); e == nil {
		t.Fatal("mass deletion accepted")
	}
	if _, e := validateSyncDeletion(nil, map[string]int64{}, task); e == nil {
		t.Fatal("empty source accepted")
	}
	task.AllowEmpty = true
	task.MaxDeletePercent = 100
	if _, e := validateSyncDeletion(old, map[string]int64{}, task); e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	if err := os.Symlink("../../etc", filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	if _, e := loadSyncManifest(root, task); e == nil {
		t.Fatal("current escaped repository")
	}
}
func TestSyncHostPinningAndSFTPWithoutExec(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, _ := ssh.NewSignerFromKey(private)
	var authCount atomic.Int32
	config := &ssh.ServerConfig{PasswordCallback: func(_ ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
		authCount.Add(1)
		if string(p) != "secret" {
			return nil, fmt.Errorf("bad password")
		}
		return nil, nil
	}}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			raw, e := listener.Accept()
			if e != nil {
				return
			}
			go func() {
				conn, ch, req, e := ssh.NewServerConn(raw, config)
				if e != nil {
					raw.Close()
					return
				}
				defer conn.Close()
				go ssh.DiscardRequests(req)
				for channel := range ch {
					if channel.ChannelType() != "session" {
						channel.Reject(ssh.UnknownChannelType, "")
						continue
					}
					stream, requests, e := channel.Accept()
					if e != nil {
						continue
					}
					go func() {
						defer stream.Close()
						for request := range requests {
							if request.Type == "subsystem" {
								var sub struct{ Name string }
								ssh.Unmarshal(request.Payload, &sub)
								if sub.Name == "sftp" {
									request.Reply(true, nil)
									server, e := sftp.NewServer(stream)
									if e == nil {
										server.Serve()
										server.Close()
									}
									return
								}
							}
							request.Reply(false, nil)
						}
					}()
				}
			}()
		}
	}()
	q := syncInput{AccessMethod: "sftp", Host: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port, Username: "backup", AuthMethod: "password", SourcePath: t.TempDir()}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, _, err := probeSync(ctx, q, syncSecret{Password: "secret"})
	if err != nil || result["needs_trust"] != true || authCount.Load() != 0 {
		t.Fatal("untrusted connection sent credentials", result, err)
	}
	q.Fingerprint = "SHA256:wrong"
	if _, _, e := probeSync(ctx, q, syncSecret{Password: "secret"}); e == nil || authCount.Load() != 0 {
		t.Fatal("host mismatch authenticated")
	}
	q.Fingerprint = ssh.FingerprintSHA256(signer.PublicKey())
	result, key, err := probeSync(ctx, q, syncSecret{Password: "secret"})
	if err != nil || key == "" || result["engine"] != "rclone" || authCount.Load() != 1 {
		t.Fatal("SFTP-only source rejected", result, err)
	}
}
