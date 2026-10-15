package appliance

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Runner interface {
	Run(context.Context, string, ...string) (string, error)
}
type ExecRunner struct{}
type cappedBuffer struct {
	buf       bytes.Buffer
	mu        sync.Mutex
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if b.buf.Len()+n > 16<<20 {
		b.truncated = true
	}
	if b.buf.Len() < 16<<20 {
		left := (16 << 20) - b.buf.Len()
		if len(p) > left {
			p = p[:left]
		}
		b.buf.Write(p)
	}
	return n, nil
}
func (b *cappedBuffer) Truncated() bool { b.mu.Lock(); defer b.mu.Unlock(); return b.truncated }
func (b *cappedBuffer) String() string  { b.mu.Lock(); defer b.mu.Unlock(); return b.buf.String() }
func command(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C", "HOME=/root"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	return cmd
}
func (ExecRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := command(ctx, name, args...)
	var b cappedBuffer
	cmd.Stdout = &b
	cmd.Stderr = &b
	err := cmd.Run()
	if err == nil && b.Truncated() {
		return "", fmt.Errorf("%s output exceeds 16 MiB; refusing truncated state", name)
	}
	if err != nil {
		return b.String(), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, b.String())
	}
	return b.String(), nil
}

var snapshotRE = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_.:-]{0,180}$`)
var nameRE = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,62}$`)
var datasetRE = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_.:-]*(/[a-zA-Z0-9_][a-zA-Z0-9_.:-]*)*$`)
var iqnRE = regexp.MustCompile(`^iqn\.[0-9]{4}-[0-9]{2}\.[a-z0-9][a-z0-9.:-]{2,180}$`)
var hostRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]{0,252}$`)
var userRE = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

func validName(s string) error {
	if !nameRE.MatchString(s) {
		return fmt.Errorf("名称须以字母开头，仅含字母、数字、下划线和连字符，最长 63 字符")
	}
	return nil
}
func number(s string) uint64 { var n uint64; fmt.Sscan(s, &n); return n }
func fields(s string) [][]string {
	var rows [][]string
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if l != "" {
			rows = append(rows, strings.Split(l, "\t"))
		}
	}
	return rows
}
func copyStream(dst io.Writer, src io.Reader) (int64, error) {
	return io.CopyBuffer(dst, src, make([]byte, 1024*1024))
}
