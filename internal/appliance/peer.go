package appliance

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func randomPeerToken() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func peerHash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func repoLabel(r Repository) string {
	if r.DisplayName != "" {
		return r.DisplayName
	}
	return r.Name
}
func validReplicaLabel(s string) error {
	if strings.TrimSpace(s) == "" || !utf8.ValidString(s) || len([]rune(s)) > 128 || strings.ContainsAny(s, "\x00\r\n") {
		return errors.New("目标仓库名称须为 1 至 128 个字符且不能包含换行")
	}
	return nil
}
func (a *App) instanceID() string {
	a.replicaMu.Lock()
	defer a.replicaMu.Unlock()
	id := a.setting("platform_instance_id")
	if id == "" {
		id = randomPeerToken()
		_ = a.set("platform_instance_id", id)
	}
	return id
}

type pairCode struct {
	InstanceID  string    `json:"instance_id"`
	Version     string    `json:"version"`
	Fingerprint string    `json:"fingerprint"`
	Ticket      string    `json:"ticket"`
	ExpiresAt   time.Time `json:"expires_at"`
}

func validPeerURL(raw string) error {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("请输入目标平台的 HTTPS 地址，例如 https://peer.example.com")
	}
	return nil
}
func pinnedPeerClient(fingerprint string) (*http.Client, error) {
	if len(fingerprint) != 64 {
		return nil, errors.New("TLS 证书指纹无效")
	}
	if _, e := hex.DecodeString(fingerprint); e != nil {
		return nil, e
	}
	tr := &http.Transport{Proxy: nil, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 0, IdleConnTimeout: 30 * time.Second, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true, VerifyConnection: func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return errors.New("目标没有证书")
		}
		h := sha256.Sum256(cs.PeerCertificates[0].Raw)
		if hex.EncodeToString(h[:]) != fingerprint {
			return errors.New("目标平台 TLS 证书已变更，请重新配对")
		}
		return nil
	}}}
	return &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("平台连接不允许重定向") }}, nil
}
func (a *App) peerRequest(ctx context.Context, p PlatformPeer, method, path string, body io.Reader, out any) error {
	secret, _, e := a.cryptSyncSecret(syncSecret{}, p.Credential)
	if e != nil {
		return e
	}
	return peerHTTPRequest(ctx, p.URL, p.Fingerprint, secret.Password, method, path, body, out)
}
func peerHTTPRequest(ctx context.Context, origin, fp, token, method, path string, body io.Reader, out any) error {
	client, e := pinnedPeerClient(fp)
	if e != nil {
		return e
	}
	defer client.CloseIdleConnections()
	req, e := http.NewRequestWithContext(ctx, method, strings.TrimRight(origin, "/")+"/api/peer"+path, body)
	if e != nil {
		return e
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Backup-Center-Version", Version)
	if strings.HasSuffix(path, "/receive") {
		req.Header.Set("Content-Type", "application/octet-stream")
	} else {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, e := client.Do(req)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	b, e := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if e != nil {
		return e
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var v struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(b, &v)
		if v.Error == "" {
			v.Error = http.StatusText(resp.StatusCode)
		}
		return fmt.Errorf("目标平台: %s", v.Error)
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}
func (a *App) pairInvitation(c *gin.Context) {
	b, e := os.ReadFile("/etc/backup-center/tls/server.crt")
	if e != nil {
		failure(c, 500, errors.New("无法读取平台 HTTPS 证书"))
		return
	}
	block, _ := pem.Decode(b)
	if block == nil {
		failure(c, 500, errors.New("平台证书格式无效"))
		return
	}
	if _, e = x509.ParseCertificate(block.Bytes); e != nil {
		failure(c, 500, e)
		return
	}
	h := sha256.Sum256(block.Bytes)
	id := a.instanceID()
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	code := pairCode{id, Version, hex.EncodeToString(h[:]), randomPeerToken(), time.Now().Add(10 * time.Minute)}
	stored := code
	stored.Ticket = peerHash(code.Ticket)
	raw, _ := json.Marshal(stored)
	if e = a.set("peer_pair_invitation", string(raw)); e != nil {
		failure(c, 500, e)
		return
	}
	raw, _ = json.Marshal(code)
	a.audit(current(c).ID, current(c).Username, "peer.invite", "", "", c.ClientIP())
	c.JSON(200, gin.H{"code": base64.RawURLEncoding.EncodeToString(raw), "expires_at": code.ExpiresAt})
}
func (a *App) acceptPair(c *gin.Context) {
	if c.GetHeader("X-Backup-Center-Version") != Version {
		failure(c, 409, errors.New("两端平台版本必须完全相同"))
		return
	}
	var q struct {
		Ticket     string `json:"ticket"`
		InstanceID string `json:"instance_id"`
		Name       string `json:"name"`
	}
	if !body(c, &q) {
		return
	}
	if len(q.InstanceID) < 20 || len(q.InstanceID) > 128 || q.InstanceID == a.instanceID() {
		failure(c, 400, errors.New("来源平台身份无效或指向本机"))
		return
	}
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	var inv pairCode
	if json.Unmarshal([]byte(a.setting("peer_pair_invitation")), &inv) != nil || inv.Ticket != peerHash(q.Ticket) || time.Now().After(inv.ExpiresAt) {
		failure(c, 403, errors.New("配对码无效或已过期，请在目标平台重新生成"))
		return
	}
	token := randomPeerToken()
	peer := PlatformPeer{Name: q.Name, Direction: "inbound", InstanceID: q.InstanceID, TokenHash: peerHash(token), Enabled: true}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		var old PlatformPeer
		if tx.Where("direction = ? AND instance_id = ?", "inbound", q.InstanceID).First(&old).Error == nil {
			peer.ID = old.ID
		}
		if e := tx.Save(&peer).Error; e != nil {
			return e
		}
		return tx.Delete(&Setting{}, "key = ?", "peer_pair_invitation").Error
	})
	if err != nil {
		failure(c, 500, err)
		return
	}
	a.audit(0, "", "peer.paired", strconv.Itoa(int(peer.ID)), q.InstanceID, c.ClientIP())
	c.JSON(200, gin.H{"token": token, "instance_id": a.instanceID(), "version": Version})
}
func (a *App) savePeer(c *gin.Context) {
	var q struct {
		URL  string `json:"url"`
		Code string `json:"code"`
		Name string `json:"name"`
	}
	if !body(c, &q) {
		return
	}
	if q.Name == "" {
		q.Name = "异地备份平台"
	}
	if e := validReplicaLabel(q.Name); e != nil {
		failure(c, 400, e)
		return
	}
	if e := validPeerURL(q.URL); e != nil {
		failure(c, 400, e)
		return
	}
	raw, e := base64.RawURLEncoding.DecodeString(strings.TrimSpace(q.Code))
	var code pairCode
	if e != nil || json.Unmarshal(raw, &code) != nil || code.Version != Version || code.InstanceID == a.instanceID() || time.Now().After(code.ExpiresAt) {
		failure(c, 400, errors.New("配对码无效、已过期、版本不一致或目标为本机"))
		return
	}
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	var old PlatformPeer
	if a.DB.Where("direction = ?", "outbound").First(&old).Error == nil && old.InstanceID != code.InstanceID {
		failure(c, 409, errors.New("请先移除现有目标平台；关联任务必须先删除"))
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	bodyBytes, _ := json.Marshal(gin.H{"ticket": code.Ticket, "instance_id": a.instanceID(), "name": q.Name})
	var result struct {
		Token      string `json:"token"`
		InstanceID string `json:"instance_id"`
		Version    string `json:"version"`
	}
	e = peerHTTPRequest(ctx, q.URL, code.Fingerprint, "", "POST", "/pair", strings.NewReader(string(bodyBytes)), &result)
	if e != nil {
		failure(c, 400, e)
		return
	}
	if result.InstanceID != code.InstanceID || result.Version != Version || len(result.Token) < 32 {
		failure(c, 400, errors.New("目标平台身份不匹配"))
		return
	}
	_, credential, e := a.cryptSyncSecret(syncSecret{Password: result.Token}, "")
	if e != nil {
		failure(c, 500, e)
		return
	}
	p := PlatformPeer{ID: old.ID, Name: q.Name, Direction: "outbound", InstanceID: result.InstanceID, URL: strings.TrimRight(q.URL, "/"), Fingerprint: code.Fingerprint, Credential: credential, Enabled: true}
	if e = a.DB.Save(&p).Error; e != nil {
		failure(c, 500, e)
		return
	}
	a.audit(current(c).ID, current(c).Username, "peer.save", fmt.Sprint(p.ID), p.URL, c.ClientIP())
	c.JSON(200, p)
}
func (a *App) peerAuth(c *gin.Context) {
	if c.GetHeader("X-Backup-Center-Version") != Version {
		failure(c, 409, errors.New("两端平台版本必须完全相同"))
		c.Abort()
		return
	}
	token := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
	var p PlatformPeer
	if len(token) < 32 || a.DB.Where("direction = ? AND enabled = ? AND token_hash = ?", "inbound", true, peerHash(token)).First(&p).Error != nil {
		failure(c, 401, errors.New("平台互信凭证无效"))
		c.Abort()
		return
	}
	c.Set("peer", p)
	c.Next()
}
func (a *App) listPeers(c *gin.Context) {
	var rows []PlatformPeer
	a.DB.Order("id").Find(&rows)
	c.JSON(200, rows)
}
func (a *App) deletePeer(c *gin.Context) {
	id, e := paramID(c)
	if e != nil {
		return
	}
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	var p PlatformPeer
	if a.DB.First(&p, id).Error != nil {
		failure(c, 404, errors.New("连接不存在"))
		return
	}
	var n int64
	a.DB.Model(&Replication{}).Where("peer_id = ?", id).Count(&n)
	if n > 0 {
		failure(c, 409, errors.New("请先删除关联的复制任务，远端数据会保留"))
		return
	}
	a.replicaMu.Lock()
	busy := len(a.replicaReceiving) > 0
	a.replicaMu.Unlock()
	if p.Direction == "inbound" && busy {
		failure(c, 409, errors.New("正在接收复制，请稍后撤销信任"))
		return
	}
	a.DB.Delete(&p)
	a.audit(current(c).ID, current(c).Username, "peer.delete", fmt.Sprint(id), "", c.ClientIP())
	c.JSON(200, gin.H{"ok": true})
}
