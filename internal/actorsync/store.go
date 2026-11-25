package actorsync

import (
	"encoding/json"
	"os"
	"strings"
	"time"
)

// 读写（LoadDb / Save / SaveJSONL / ExportJSON）都在 store_jsonl.go，
// 因为真源是 actors.jsonl。这个文件只放内存索引和派生逻辑。

// FindByKey 按主键查。
func (db *Db) FindByKey(key string) *Actor {
	for _, a := range db.Actors {
		if a.Key == key {
			return a
		}
	}
	return nil
}

// FindByName 按规范化名字查（清单里改名或写别名时靠它接上旧记录）。
// UUID 清单特殊处理：按 provider_ids 里的站点 ID 反查，避免同一 UUID 被当新人反复抓。
func (db *Db) FindByName(name string) *Actor {
	n := NormalizeName(name)
	isID := IsUUID(name)
	for _, a := range db.Actors {
		// UUID 清单：按各站点的 performer ID 精确匹配
		if isID {
			for _, site := range []string{"StashDB", "JavStash", "TPDB"} {
				if strings.EqualFold(strings.TrimSpace(a.ProviderIDs[site]), name) {
					return a
				}
			}
		}
		if NormalizeName(a.Name) == n {
			return a
		}
		if a.NameOriginal != nil && NormalizeName(*a.NameOriginal) == n {
			return a
		}
		for _, alias := range a.Aliases {
			if NormalizeName(alias) == n {
				return a
			}
		}
	}
	return nil
}

// Upsert 按主键写入，已存在则保留 created_at。
func (db *Db) Upsert(a *Actor) (added bool) {
	if old := db.FindByKey(a.Key); old != nil {
		if old.CreatedAt != "" {
			a.CreatedAt = old.CreatedAt
		}
		*old = *a
		return false
	}
	db.Actors = append(db.Actors, a)
	return true
}

func mergeStrings(a, b []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(a)+len(b))
	for _, s := range append(append([]string{}, a...), b...) {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// BuildManifest 生成健康度快照。
func BuildManifest(db *Db, stats RunStats, srcStats map[string]map[string]int) *Manifest {
	m := &Manifest{
		SchemaVersion: SchemaVersion,
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		Count:         len(db.Actors),
		ByStatus:      map[string]int{},
		LastRun:       stats,
		Sources:       srcStats,
	}
	for _, a := range db.Actors {
		m.ByStatus[a.Status]++
	}
	if m.Sources == nil {
		m.Sources = map[string]map[string]int{}
	}
	return m
}

// SaveManifest 写 actors.manifest.json。
func SaveManifest(path string, m *Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}
