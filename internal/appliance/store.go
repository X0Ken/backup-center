package appliance

import (
	"context"
	"errors"
	"fmt"
	"github.com/robfig/cron/v3"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

type Config struct {
	Listen, DataDir, Pool, WebDir, ExternalURL string
	SecureCookie                               bool
}
type App struct {
	DB         *gorm.DB
	Run        Runner
	Config     Config
	root       string
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	policyWake chan struct{}
	cron       *cron.Cron
	scheduleMu sync.Mutex
	mutationMu sync.Mutex
	wake       chan struct{}
	lock       *os.File
	loginMu    sync.Mutex
	attempts   map[string][]time.Time
}

func Open(cfg Config, runner Runner) (*App, error) {
	if !datasetRE.MatchString(cfg.Pool) || filepath.Base(cfg.Pool) != cfg.Pool {
		return nil, errors.New("invalid pool")
	}
	if err := os.MkdirAll(cfg.DataDir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(cfg.DataDir, 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(cfg.DataDir, "daemon.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, errors.New("another backupd owns the data directory")
	}
	dbpath := filepath.Join(cfg.DataDir, "backup-center.db")
	db, err := gorm.Open(sqlite.Open(dbpath+"?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=10000&_synchronous=FULL"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		lock.Close()
		return nil, err
	}
	os.Chmod(dbpath, 0600)
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(1)
	if err = db.AutoMigrate(&User{}, &Session{}, &Repository{}, &Policy{}, &Job{}, &Audit{}, &Export{}, &Replication{}, &Alert{}, &Metric{}, &Setting{}); err != nil {
		sqlDB.Close()
		lock.Close()
		return nil, err
	}
	now := time.Now()
	if err = db.Model(&Job{}).Where("status = ?", "running").Updates(map[string]any{"status": "interrupted", "error": "服务重启中断任务；请核对真实资源状态后重新执行", "finished_at": now}).Error; err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	a := &App{DB: db, Run: runner, Config: cfg, root: cfg.Pool + "/backup-center", ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1), policyWake: make(chan struct{}, 1), lock: lock, attempts: map[string][]time.Time{}}
	a.cron = cron.New(cron.WithLocation(time.Local), cron.WithChain(cron.SkipIfStillRunning(cron.DefaultLogger), cron.Recover(cron.DefaultLogger)))
	return a, nil
}
func (a *App) Start() error {
	if err := a.reloadSchedules(); err != nil {
		return err
	}
	a.cron.Start()
	a.wg.Add(3)
	go a.worker()
	go a.monitor()
	go a.policyScheduler()
	a.signal()
	return nil
}
func (a *App) Close() {
	a.cancel()
	a.cron.Stop()
	a.wg.Wait()
	db, _ := a.DB.DB()
	if db != nil {
		db.Close()
	}
	if a.lock != nil {
		syscall.Flock(int(a.lock.Fd()), syscall.LOCK_UN)
		a.lock.Close()
	}
}
func (a *App) signal() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}
func (a *App) setting(key string) string {
	var v Setting
	a.DB.First(&v, "key = ?", key)
	return v.Value
}
func (a *App) set(key, value string) error { return a.DB.Save(&Setting{Key: key, Value: value}).Error }
func (a *App) repo(id uint) (Repository, error) {
	var r Repository
	err := a.DB.First(&r, id).Error
	if err != nil {
		return r, fmt.Errorf("仓库不存在")
	}
	if r.Dataset != a.root+"/repos/"+r.Name {
		return r, errors.New("仓库路径不在平台管理范围")
	}
	return r, nil
}
func (a *App) audit(uid uint, username, action, target, detail, ip string) error {
	return a.DB.Create(&Audit{UserID: uid, Username: username, Action: action, Target: target, Detail: detail, IP: ip}).Error
}
func (a *App) enqueue(kind, target string, payload any, uid uint) (Job, error) {
	b, err := marshal(payload)
	if err != nil {
		return Job{}, err
	}
	j := Job{Kind: kind, Target: target, Payload: string(b), Status: "queued", UserID: uid}
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&Job{}).Where("kind = ? AND target = ? AND status IN ?", kind, target, []string{"queued", "running"}).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return errors.New("同一任务已在队列中")
		}
		return tx.Create(&j).Error
	})
	if err == nil {
		a.signal()
	}
	return j, err
}
func (a *App) worker() {
	defer a.wg.Done()
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-a.wake:
		case <-t.C:
		}
		for {
			if a.ctx.Err() != nil {
				return
			}
			var j Job
			if a.DB.Order("id").First(&j, "status = ?", "queued").Error != nil {
				break
			}
			now := time.Now()
			if err := a.DB.Model(&j).Updates(map[string]any{"status": "running", "started_at": now}).Error; err != nil {
				slog.Error("claim job", "error", err)
				break
			}
			a.mutationMu.Lock()
			result, err := a.execute(j)
			a.mutationMu.Unlock()
			status, msg := "succeeded", ""
			if err != nil {
				status = "failed"
				msg = err.Error()
				if len(msg) > 12000 {
					msg = msg[:12000]
				}
				slog.Error("job failed", "id", j.ID, "error", err)
				if a.ctx.Err() != nil {
					status = "interrupted"
				} else {
					a.raise("job-"+fmt.Sprint(j.ID), "critical", j.Kind+": "+msg)
				}
			}
			finish := time.Now()
			if e := a.DB.Model(&j).Updates(map[string]any{"status": status, "error": msg, "result": result, "finished_at": finish}).Error; e != nil {
				slog.Error("persist job result", "error", e)
			}
			a.audit(j.UserID, "", j.Kind+"."+status, j.Target, msg, "")
		}
	}
}
