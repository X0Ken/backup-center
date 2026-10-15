package appliance

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	_, err := rand.Read(b)
	return hex.EncodeToString(b), err
}
func hashToken(s string) string { x := sha256.Sum256([]byte(s)); return hex.EncodeToString(x[:]) }
func passwordHash(p string) (string, error) {
	if len(p) < 14 || len(p) > 72 {
		return "", errors.New("密码长度应为 14 至 72 字节")
	}
	b, e := bcrypt.GenerateFromPassword([]byte(p), 12)
	return string(b), e
}
func jsonDecode(s string, v any) error {
	d := json.NewDecoder(strings.NewReader(s))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return e
	}
	var extra any
	if e := d.Decode(&extra); e != io.EOF {
		return errors.New("仅允许一个 JSON 对象")
	}
	return nil
}
func body(c *gin.Context, v any) bool {
	b, e := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20))
	if e == nil {
		e = jsonDecode(string(b), v)
	}
	if e != nil {
		failure(c, 400, e)
		return false
	}
	return true
}
func failure(c *gin.Context, status int, e error) {
	c.AbortWithStatusJSON(status, gin.H{"error": e.Error()})
}
func (a *App) InitAdmin(name, password string) error {
	if err := validName(name); err != nil {
		return err
	}
	var count int64
	if err := a.DB.Model(&User{}).Count(&count).Error; err != nil {
		return err
	}
	if count != 0 {
		return errors.New("用户已存在，禁止重复初始化")
	}
	hash, err := passwordHash(password)
	if err != nil {
		return err
	}
	return a.DB.Create(&User{Username: name, Password: hash, Role: "admin", MustChange: true}).Error
}
func (a *App) login(c *gin.Context) {
	var q struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !body(c, &q) {
		return
	}
	key := c.ClientIP()
	a.loginMu.Lock()
	now := time.Now()
	recent := []time.Time{}
	for _, t := range a.attempts[key] {
		if now.Sub(t) < 15*time.Minute {
			recent = append(recent, t)
		}
	}
	allowed := len(recent) < 10
	if len(a.attempts) > 10000 {
		for k, v := range a.attempts {
			if len(v) == 0 || now.Sub(v[len(v)-1]) > 15*time.Minute {
				delete(a.attempts, k)
			}
		}
	}
	if allowed {
		recent = append(recent, now)
	}
	a.attempts[key] = recent
	a.loginMu.Unlock()
	if !allowed {
		failure(c, 429, errors.New("登录尝试过多，请 15 分钟后重试"))
		return
	}
	var u User
	err := a.DB.First(&u, "username = ?", q.Username).Error
	if err != nil || u.Disabled || bcrypt.CompareHashAndPassword([]byte(u.Password), []byte(q.Password)) != nil {
		a.audit(0, q.Username, "auth.login.failed", "", "", key)
		failure(c, 401, errors.New("用户名或密码错误"))
		return
	}
	token, e := randomToken(32)
	if e != nil {
		failure(c, 500, e)
		return
	}
	csrf, e := randomToken(32)
	if e != nil {
		failure(c, 500, e)
		return
	}
	if e = a.DB.Create(&Session{ID: hashToken(token), UserID: u.ID, CSRF: csrf, ExpiresAt: now.Add(12 * time.Hour)}).Error; e != nil {
		failure(c, 500, e)
		return
	}
	http.SetCookie(c.Writer, &http.Cookie{Name: "bc_session", Value: token, Path: "/", HttpOnly: true, Secure: a.Config.SecureCookie, SameSite: http.SameSiteStrictMode, MaxAge: 43200})
	a.audit(u.ID, u.Username, "auth.login", "", "", key)
	c.JSON(200, gin.H{"user": u, "csrf": csrf})
}
func (a *App) auth(c *gin.Context) {
	token, e := c.Cookie("bc_session")
	if e != nil || len(token) != 64 {
		failure(c, 401, errors.New("请先登录"))
		return
	}
	var s Session
	if a.DB.First(&s, "id = ? AND expires_at > ?", hashToken(token), time.Now()).Error != nil {
		failure(c, 401, errors.New("会话已过期"))
		return
	}
	var u User
	if a.DB.First(&u, s.UserID).Error != nil || u.Disabled {
		failure(c, 401, errors.New("账号不可用"))
		return
	}
	if c.Request.Method != "GET" && c.Request.Method != "HEAD" {
		if subtle.ConstantTimeCompare([]byte(s.CSRF), []byte(c.GetHeader("X-CSRF-Token"))) != 1 {
			failure(c, 403, errors.New("CSRF 校验失败"))
			return
		}
		if origin := c.GetHeader("Origin"); origin != "" {
			o, err := url.Parse(origin)
			if err != nil || o.Host != c.Request.Host {
				failure(c, 403, errors.New("禁止跨站请求"))
				return
			}
		}
	}
	if u.MustChange && c.Request.Method != "GET" && c.FullPath() != "/api/auth/password" && c.FullPath() != "/api/auth/logout" {
		failure(c, 403, errors.New("首次登录必须先修改密码"))
		return
	}
	c.Set("user", u)
	c.Set("session", s)
	c.Next()
}
func current(c *gin.Context) User { return c.MustGet("user").(User) }
func admin(c *gin.Context) {
	if current(c).Role != "admin" {
		failure(c, 403, errors.New("需要管理员权限"))
		return
	}
	c.Next()
}
func (a *App) password(c *gin.Context) {
	var q struct {
		Current  string `json:"current_password"`
		Password string `json:"new_password"`
	}
	if !body(c, &q) {
		return
	}
	u := current(c)
	if bcrypt.CompareHashAndPassword([]byte(u.Password), []byte(q.Current)) != nil {
		failure(c, 403, errors.New("当前密码错误"))
		return
	}
	hash, err := passwordHash(q.Password)
	if err != nil {
		failure(c, 400, err)
		return
	}
	if err = a.DB.Model(&u).Updates(map[string]any{"password": hash, "must_change": false}).Error; err != nil {
		failure(c, 500, err)
		return
	}
	s := c.MustGet("session").(Session)
	a.DB.Where("user_id = ? AND id <> ?", u.ID, s.ID).Delete(&Session{})
	a.audit(u.ID, u.Username, "user.password", u.Username, "", c.ClientIP())
	c.JSON(200, gin.H{"ok": true})
}
