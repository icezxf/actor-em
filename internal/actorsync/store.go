package actorsync

import (
	"encoding/json"
	"os"
	"strings"
	"time"
)

// 读写（LoadDb / Save / SaveJSONL / ExportJSON）都在 store_jsonl.go，
// 因为真源是 actors.jsonl。这个文件只放内存索引和派生逻辑。

// dbIndex 是「名字 / 别名 / 原名 / 站点 ID → 记录」的倒排索引。
// 六万条时 FindByName 若逐条线性比对，--refresh-stored 会退化成 O(n²)（十几分钟），
// 建一次索引就变成一次哈希查找。
type dbIndex struct {
	byKey      map[string]*Actor
	byNorm     map[string]*Actor // 规范化名字 / 原名 / 别名
	byProvider map[string]*Actor // "site\x00id"（全小写）
	size       int               // 建索引时的 Actors 长度，用来发现切片被整体替换
}

func (db *Db) buildIndex() *dbIndex {
	ix := &dbIndex{
		byKey:      make(map[string]*Actor, len(db.Actors)),
		byNorm:     make(map[string]*Actor, len(db.Actors)*3),
		byProvider: make(map[string]*Actor, len(db.Actors)),
		size:       len(db.Actors),
	}
	for _, a := range db.Actors {
		if a == nil {
			continue
		}
		if a.Key != "" {
			ix.byKey[a.Key] = a
		}
		put := func(s string) {
			if n := NormalizeName(s); n != "" {
				if _, exists := ix.byNorm[n]; !exists {
					ix.byNorm[n] = a
				}
			}
		}
		put(a.Name)
		if a.NameOriginal != nil {
			put(*a.NameOriginal)
		}
		for _, al := range a.Aliases {
			put(al)
		}
		for site, pid := range a.ProviderIDs {
			pid = strings.TrimSpace(pid)
			if pid == "" {
				continue
			}
			k := strings.ToLower(site) + "\x00" + strings.ToLower(pid)
			if _, exists := ix.byProvider[k]; !exists {
				ix.byProvider[k] = a
			}
		}
	}
	return ix
}

// getIndex 按需建索引；切片被整体替换过（长度变化）或显式失效时重建。
func (db *Db) getIndex() *dbIndex {
	if db.idx == nil || db.idx.size != len(db.Actors) {
		db.idx = db.buildIndex()
	}
	return db.idx
}

// InvalidateIndex 在直接改动 db.Actors 之后调用（比如去重替换了整个切片）。
func (db *Db) InvalidateIndex() { db.idx = nil }

// FindByKey 按主键查。
func (db *Db) FindByKey(key string) *Actor {
	if db == nil {
		return nil
	}
	return db.getIndex().byKey[key]
}

// FindByName 按规范化名字查（清单里改名或写别名时靠它接上旧记录）。
// UUID 清单特殊处理：按 provider_ids 里的站点 ID 反查，避免同一 UUID 被当新人反复抓。
func (db *Db) FindByName(name string) *Actor {
	if db == nil {
		return nil
	}
	ix := db.getIndex()
	if IsUUID(name) {
		for _, site := range []string{"StashDB", "JavStash", "TPDB"} {
			if a := ix.byProvider[strings.ToLower(site)+"\x00"+strings.ToLower(strings.TrimSpace(name))]; a != nil {
				return a
			}
		}
	}
	return ix.byNorm[NormalizeName(name)]
}

// Upsert 按主键写入，已存在则保留 created_at。
func (db *Db) Upsert(a *Actor) (added bool) {
	if old := db.FindByKey(a.Key); old != nil {
		if old.CreatedAt != "" {
			a.CreatedAt = old.CreatedAt
		}
		*old = *a
		db.InvalidateIndex() // 名字/别名/ID 可能变了，索引作废
		return false
	}
	db.Actors = append(db.Actors, a)
	db.InvalidateIndex()
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
