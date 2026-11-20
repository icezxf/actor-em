package actorsync

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// 存储格式：actors.jsonl 是唯一真源，一行一个演员，append-friendly、diff 干净。
// actors.json 是从 jsonl 派生出来的完整对象，给不认 jsonl 的下游用（比如旧的 tg 机器人）。

// IsJSONLPath 判断这个路径该按 JSONL 读写。
func IsJSONLPath(path string) bool {
	return strings.HasSuffix(strings.ToLower(path), ".jsonl")
}

// LoadDb 读库。按扩展名分派：.jsonl 逐行读，其他按旧的单一 JSON 对象读。
// 文件不存在时返回空库，不报错。
func LoadDb(path string) (*Db, error) {
	if IsJSONLPath(path) {
		return LoadJSONL(path)
	}
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
	// 兼容：文件名是 .json 但内容其实是 JSONL（手工改名的情况）
	if err := json.Unmarshal(data, db); err != nil {
		if db2, err2 := LoadJSONL(path); err2 == nil {
			return db2, nil
		}
		return nil, fmt.Errorf("解析 %s 失败: %w", path, err)
	}
	if db.Actors == nil {
		db.Actors = []*Actor{}
	}
	if db.SchemaVersion < SchemaVersion {
		db.SchemaVersion = SchemaVersion
	}
	return db, nil
}

// LoadJSONL 逐行读。坏行跳过并计数，不让一行脏数据毁掉整个库。
func LoadJSONL(path string) (*Db, error) {
	db := &Db{SchemaVersion: SchemaVersion, Actors: []*Actor{}}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return db, nil
		}
		return nil, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 8<<20) // 单行最大 8MB，简介再长也够
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		a := &Actor{}
		if err := json.Unmarshal(line, a); err != nil {
			continue
		}
		if a.Key == "" {
			continue
		}
		db.Actors = append(db.Actors, a)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return db, nil
}

// Save 按扩展名分派：.jsonl 写 JSONL，否则写旧的单一 JSON 对象。
func (db *Db) Save(path string) error {
	if IsJSONLPath(path) {
		return db.SaveJSONL(path)
	}
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
	return writeFileAtomic(path, data)
}

// SaveJSONL 排序后一行一条写盘。排序是为了让 git diff 稳定。
func (db *Db) SaveJSONL(path string) error {
	sort.Slice(db.Actors, func(i, j int) bool { return db.Actors[i].Key < db.Actors[j].Key })

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	for _, a := range db.Actors {
		if a == nil || a.Key == "" {
			continue
		}
		if err := enc.Encode(a); err != nil { // Encode 自带换行，正好是 JSONL
			return err
		}
	}
	return writeFileAtomic(path, buf.Bytes())
}

// ExportJSON 从内存库派生一份 actors.json（完整对象），供只认 json 的下游使用。
//
// skipped 的记录不导出：那些是男性或非目标演员，留在 actors.jsonl 里只是为了
// 下次不再重复抓，下游没有理由看到它们。
func (db *Db) ExportJSON(path string) error {
	var kept []*Actor
	for _, a := range db.Actors {
		if a.Status == StatusSkipped {
			continue
		}
		kept = append(kept, a)
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].Key < kept[j].Key })
	out := &Db{
		SchemaVersion: SchemaVersion,
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		Count:         len(kept),
		Actors:        kept,
	}
	if out.Actors == nil {
		out.Actors = []*Actor{}
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeFileAtomic(path, data)
}

func writeFileAtomic(path string, data []byte) error {
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
