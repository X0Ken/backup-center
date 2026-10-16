package appliance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
)

type replicaSnapshot struct {
	Name, GUID string
	Txg        uint64
}

func (s replicaSnapshot) shortName() string { return strings.SplitN(s.Name, "@", 2)[1] }
func (a *App) replicaCatalog(ctx context.Context, r Repository) ([]replicaSnapshot, error) {
	out, e := a.zfs(ctx, "list", "-Hp", "-d", "1", "-t", "snapshot", "-o", "name,guid,createtxg", r.Dataset)
	if e != nil {
		return nil, e
	}
	rows := []replicaSnapshot{}
	for _, f := range fields(out) {
		if len(f) != 3 {
			return nil, errors.New("快照目录返回格式无效")
		}
		if !strings.HasPrefix(f[0], r.Dataset+"@bc-") {
			continue
		}
		txg, e := strconv.ParseUint(f[2], 10, 64)
		if e != nil {
			return nil, e
		}
		if _, e = strconv.ParseUint(f[1], 10, 64); e != nil {
			return nil, e
		}
		rows = append(rows, replicaSnapshot{f[0], f[1], txg})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Txg == rows[j].Txg {
			return rows[i].Name < rows[j].Name
		}
		return rows[i].Txg < rows[j].Txg
	})
	return rows, nil
}
func catalogHash(rows []replicaSnapshot) string {
	raw, _ := json.Marshal(rows)
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}
func missingReplicaSnapshots(rows []replicaSnapshot, remote map[string]string) []replicaSnapshot {
	var missing []replicaSnapshot
	for _, s := range rows {
		if remote[s.shortName()] != s.GUID {
			missing = append(missing, s)
		}
	}
	return missing
}
func needsReplicaReseed(rows []replicaSnapshot, remote map[string]string) bool {
	missing := false
	for _, s := range rows {
		if remote[s.shortName()] != s.GUID {
			missing = true
			if v := remote[s.shortName()]; v != "" && v != s.GUID {
				return true
			}
		} else if missing {
			return true
		}
	}
	return false
}
