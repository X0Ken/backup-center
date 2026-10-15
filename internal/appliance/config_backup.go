package appliance

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/scrypt"
	"gorm.io/gorm"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const backupHeader = "BCBACKUP1"

type backupUser struct {
	ID                       uint
	Username, Password, Role string
	Disabled, MustChange     bool
	CreatedAt                time.Time
}
type configBundle struct {
	Schema       int
	Pool         string
	CreatedAt    time.Time
	Users        []backupUser
	Repositories []Repository
	Policies     []Policy
	Exports      []Export
	Replications []Replication
	Settings     []Setting
	Files        map[string][]byte
}

func encryptBundle(plain []byte, password string) ([]byte, error) {
	if len(password) < 14 || len(password) > 1024 {
		return nil, errors.New("备份口令至少 14 字节，最多 1024 字节")
	}
	salt := make([]byte, 16)
	if _, e := rand.Read(salt); e != nil {
		return nil, e
	}
	key, e := scrypt.Key([]byte(password), salt, 32768, 8, 1, 32)
	if e != nil {
		return nil, e
	}
	block, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	gcm, e := cipher.NewGCM(block)
	if e != nil {
		return nil, e
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, e = rand.Read(nonce); e != nil {
		return nil, e
	}
	out := append([]byte(backupHeader), salt...)
	out = append(out, nonce...)
	return append(out, gcm.Seal(nil, nonce, plain, []byte(backupHeader))...), nil
}
func decryptBundle(data []byte, password string) ([]byte, error) {
	if len(data) < len(backupHeader)+16+12+16 || string(data[:len(backupHeader)]) != backupHeader {
		return nil, errors.New("备份格式无效")
	}
	salt := data[len(backupHeader) : len(backupHeader)+16]
	key, e := scrypt.Key([]byte(password), salt, 32768, 8, 1, 32)
	if e != nil {
		return nil, e
	}
	block, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	gcm, e := cipher.NewGCM(block)
	if e != nil {
		return nil, e
	}
	offset := len(backupHeader) + 16
	plain, e := gcm.Open(nil, data[offset:offset+12], data[offset+12:], []byte(backupHeader))
	if e != nil {
		return nil, errors.New("备份口令错误或文件已损坏")
	}
	return plain, nil
}
func (a *App) backupBundle(password string) ([]byte, error) {
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	b := configBundle{Schema: 1, Pool: a.Config.Pool, CreatedAt: time.Now(), Files: map[string][]byte{}}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		var users []User
		if e := tx.Find(&users).Error; e != nil {
			return e
		}
		for _, u := range users {
			b.Users = append(b.Users, backupUser{u.ID, u.Username, u.Password, u.Role, u.Disabled, u.MustChange, u.CreatedAt})
		}
		for _, v := range []any{&b.Repositories, &b.Policies, &b.Exports, &b.Replications, &b.Settings} {
			if e := tx.Find(v).Error; e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, dir := range []string{"keys", "ssh"} {
		entries, e := os.ReadDir(filepath.Join(a.Config.DataDir, dir))
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return nil, e
		}
		for _, entry := range entries {
			if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			name := dir + "/" + entry.Name()
			if !validBackupFile(name) {
				continue
			}
			v, e := os.ReadFile(filepath.Join(a.Config.DataDir, name))
			if e != nil {
				return nil, e
			}
			b.Files[name] = v
		}
	}
	plain, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	return encryptBundle(plain, password)
}
func validBackupFile(name string) bool {
	if strings.HasPrefix(name, "keys/") {
		return strings.HasSuffix(name, ".key") && validName(strings.TrimSuffix(strings.TrimPrefix(name, "keys/"), ".key")) == nil
	}
	return name == "ssh/id_ed25519" || name == "ssh/id_ed25519.pub" || name == "ssh/known_hosts"
}
func (a *App) configBackup(c *gin.Context) {
	var q struct {
		Passphrase string `json:"passphrase"`
	}
	if !body(c, &q) {
		return
	}
	out, e := a.backupBundle(q.Passphrase)
	if e != nil {
		failure(c, 400, e)
		return
	}
	a.audit(current(c).ID, current(c).Username, "config.backup", "", "encrypted bundle", c.ClientIP())
	c.Header("Content-Disposition", "attachment; filename=backup-center-"+time.Now().UTC().Format("20060102T150405")+".bcbackup")
	c.Data(200, "application/octet-stream", out)
}
func (a *App) RestoreConfig(data []byte, password string) error {
	var count int64
	if e := a.DB.Model(&User{}).Count(&count).Error; e != nil {
		return e
	}
	if count != 0 {
		return errors.New("仅允许向未初始化的新数据目录恢复配置")
	}
	plain, e := decryptBundle(data, password)
	if e != nil {
		return e
	}
	var b configBundle
	if e = json.Unmarshal(plain, &b); e != nil {
		return e
	}
	if b.Schema != 1 || b.Pool != a.Config.Pool {
		return errors.New("备份版本或存储池名称不匹配")
	}
	for _, r := range b.Repositories {
		if validName(r.Name) != nil || r.Dataset != a.root+"/repos/"+r.Name {
			return errors.New("备份包含非法仓库路径")
		}
	}
	for name := range b.Files {
		if !validBackupFile(name) {
			return fmt.Errorf("拒绝恢复非法文件 %s", name)
		}
	}
	for name, data := range b.Files {
		if e := os.MkdirAll(filepath.Dir(filepath.Join(a.Config.DataDir, name)), 0700); e != nil {
			return e
		}
		if e := atomicFile(filepath.Join(a.Config.DataDir, name), data, 0600); e != nil {
			return e
		}
	}
	return a.DB.Transaction(func(tx *gorm.DB) error {
		for _, u := range b.Users {
			if validName(u.Username) != nil || !contains([]string{"admin", "operator"}, u.Role) {
				return errors.New("备份含非法用户")
			}
			if e := tx.Create(&User{ID: u.ID, Username: u.Username, Password: u.Password, Role: u.Role, Disabled: u.Disabled, MustChange: u.MustChange, CreatedAt: u.CreatedAt}).Error; e != nil {
				return e
			}
		}
		for _, v := range []any{b.Repositories, b.Policies, b.Exports, b.Replications, b.Settings} {
			switch rows := v.(type) {
			case []Repository:
				if len(rows) > 0 {
					if e := tx.Create(&rows).Error; e != nil {
						return e
					}
				}
			case []Policy:
				if len(rows) > 0 {
					if e := tx.Create(&rows).Error; e != nil {
						return e
					}
				}
			case []Export:
				if len(rows) > 0 {
					if e := tx.Create(&rows).Error; e != nil {
						return e
					}
				}
			case []Replication:
				if len(rows) > 0 {
					if e := tx.Create(&rows).Error; e != nil {
						return e
					}
				}
			case []Setting:
				if len(rows) > 0 {
					if e := tx.Create(&rows).Error; e != nil {
						return e
					}
				}
			}
		}
		return nil
	})
}
