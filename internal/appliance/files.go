package appliance

import (
	"errors"
	"github.com/gin-gonic/gin"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func (a *App) snapshotRoot(c *gin.Context) (*os.Root, string, error) {
	id, _ := strconv.ParseUint(c.Query("repository_id"), 10, 32)
	r, e := a.repo(uint(id))
	if e != nil {
		return nil, "", e
	}
	if r.Kind != "filesystem" {
		return nil, "", errors.New("块卷不支持文件浏览")
	}
	snap := c.Query("snapshot")
	if e = validSnapshot(r, snap); e != nil {
		return nil, "", e
	}
	browseRepo := r
	browseRepo.Dataset = strings.SplitN(snap, "@", 2)[0]
	d, e := a.dataset(c.Request.Context(), browseRepo)
	if e != nil {
		return nil, "", e
	}
	if !strings.HasPrefix(d.Mountpoint, "/srv/backup-center/repos/") {
		return nil, "", errors.New("挂载路径不在管理范围内")
	}
	root, e := os.OpenRoot(filepath.Join(d.Mountpoint, ".zfs", "snapshot", strings.SplitN(snap, "@", 2)[1]))
	path := c.DefaultQuery("path", ".")
	if filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
		if root != nil {
			root.Close()
		}
		return nil, "", errors.New("文件路径无效")
	}
	return root, path, e
}
func (a *App) browseSnapshot(c *gin.Context) {
	root, path, e := a.snapshotRoot(c)
	if e != nil {
		failure(c, 400, e)
		return
	}
	defer root.Close()
	f, e := root.Open(path)
	if e != nil {
		failure(c, 400, e)
		return
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil || !info.IsDir() {
		failure(c, 400, errors.New("请选择目录"))
		return
	}
	entries, e := f.ReadDir(2001)
	if e != nil && e != io.EOF {
		failure(c, 400, e)
		return
	}
	more := len(entries) > 2000
	if more {
		entries = entries[:2000]
	}
	rows := []gin.H{}
	for _, entry := range entries {
		info, e := entry.Info()
		if e != nil {
			continue
		}
		rows = append(rows, gin.H{"name": entry.Name(), "directory": entry.IsDir(), "symlink": entry.Type()&os.ModeSymlink != 0, "size": info.Size(), "modified_at": info.ModTime()})
	}
	c.JSON(200, gin.H{"path": path, "entries": rows, "truncated": more})
}
func (a *App) downloadFile(c *gin.Context) {
	root, path, e := a.snapshotRoot(c)
	if e != nil {
		failure(c, 400, e)
		return
	}
	defer root.Close()
	f, e := root.Open(path)
	if e != nil {
		failure(c, 400, e)
		return
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() {
		failure(c, 400, errors.New("仅可下载普通文件"))
		return
	}
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": info.Name()}))
	http.ServeContent(c.Writer, c.Request, info.Name(), info.ModTime(), f)
}
