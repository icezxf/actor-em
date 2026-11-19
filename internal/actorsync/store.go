package actorsync

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// LoadDb 读取 actors.json；文件不存在时返回一个空库。
func LoadDb(path string) (*Db, error) {
	db := &Db{SchemaVersion: SchemaVersion, Actors: []*Actor{}}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return db, nil
		}
		return nil, fmt.Errorf("读取 %s 失败: %w", path, err)
	}
	if len(data) == 0 {
		return db, nil
	}
	if err := json.Unmarshal(data, db); err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", path, err)
	}
	if db.Actors == nil {
		db.Actors = []*Actor{}
	}
	// 旧版本（1）升级到 2：emby_ids 会被忽略，保存时自然丢弃
	if db.SchemaVersion < SchemaVersion {
		db.SchemaVersion = SchemaVersion
	}
	return db, nil
}

// Save 按 key 排序后原子写盘（先写 .tmp 再 rename，避免中途崩溃写坏文件）。
func (db *Db) Save(path string) error {
	sort.Slice(db.Actors, func(i, j int) bool { return db.Actors[i].Key < db.Actors[j].Key })
	db.Count = len(db.Actors)
	db.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	if db.Actors == nil {
		db.Actors = []*Actor{}
	}

	data, err := json.MarshalIndent(db, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

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
func (db *Db) FindByName(name string) *Actor {
	n := NormalizeName(name)
	for _, a := range db.Actors {
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
