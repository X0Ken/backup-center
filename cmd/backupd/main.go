package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/X0Ken/backup-center/internal/appliance"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	if e := run(); e != nil {
		slog.Error("backupd stopped", "error", e)
		os.Exit(1)
	}
}
func run() error {
	var cfg appliance.Config
	var initAdmin, passwordFile, restoreConfig string
	flag.StringVar(&cfg.Listen, "listen", "127.0.0.1:8080", "API listen address")
	flag.StringVar(&cfg.DataDir, "data-dir", "/var/lib/backup-center", "private state directory")
	flag.StringVar(&cfg.Pool, "pool", "backup-pool", "existing ZFS storage pool")
	flag.StringVar(&cfg.WebDir, "web-dir", "/opt/backup-center/web", "built SPA directory")
	flag.StringVar(&cfg.ExternalURL, "external-url", "https://192.0.2.10", "public origin")
	flag.BoolVar(&cfg.SecureCookie, "secure-cookie", true, "require HTTPS session cookies")
	flag.StringVar(&initAdmin, "init-admin", "", "initialize first administrator and exit")
	flag.StringVar(&passwordFile, "password-file", "", "read initial password from a protected file")
	flag.StringVar(&restoreConfig, "restore-config", "", "restore encrypted configuration to a fresh data directory and exit")
	flag.Parse()
	app, err := appliance.Open(cfg, appliance.ExecRunner{})
	if err != nil {
		return err
	}
	defer app.Close()
	if restoreConfig != "" {
		if passwordFile == "" {
			return fmt.Errorf("--password-file required")
		}
		info, e := os.Stat(passwordFile)
		if e != nil {
			return e
		}
		if info.Mode().Perm()&0077 != 0 {
			return fmt.Errorf("password file permissions must be 0600")
		}
		password, e := os.ReadFile(passwordFile)
		if e != nil {
			return e
		}
		data, e := os.ReadFile(restoreConfig)
		if e != nil {
			return e
		}
		return app.RestoreConfig(data, strings.TrimSpace(string(password)))
	}
	if initAdmin != "" {
		if passwordFile == "" {
			return fmt.Errorf("--password-file required")
		}
		info, e := os.Stat(passwordFile)
		if e != nil {
			return e
		}
		if info.Mode().Perm()&0077 != 0 {
			return fmt.Errorf("password file must not be readable by group or other users")
		}
		b, e := os.ReadFile(passwordFile)
		if e != nil {
			return e
		}
		return app.InitAdmin(initAdmin, strings.TrimSpace(string(b)))
	}
	if err = app.Start(); err != nil {
		return err
	}
	srv := &http.Server{Addr: cfg.Listen, Handler: app.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 32 << 10}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sig)
	done := make(chan error, 1)
	go func() {
		slog.Info("backupd listening", "address", cfg.Listen, "version", appliance.Version)
		done <- srv.ListenAndServe()
	}()
	select {
	case err = <-done:
		if err != http.ErrServerClosed {
			return err
		}
	case <-sig:
		app.BeginShutdown()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err = srv.Shutdown(ctx); err != nil {
			return err
		}
	}
	return nil
}
