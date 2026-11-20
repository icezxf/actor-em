package actorsync

import (
	"encoding/csv"
	"io"
	"os"
	"strconv"
	"strings"
)

// MDCxProfile 是 MDCx actor-profile 里一个演员的档案，只保留我们真正用的字段。
// 原始库是 sqlite，由 scripts/export_profiles.py 导出成 CSV——
// 清洗（简繁归一、剔「海外」、拆标签、解析出道年）都在 Python 侧做掉，
// Go 这边只负责读，避免为了 650KB 的简繁表再引一套依赖。
type MDCxProfile struct {
	Name        string // 日文名
	NameCN      string // 中文名
	Aliases     []string
	BirthPlace  string // 已归一为简体，且剔除了「海外/国外」
	Country     string // 从出生地推出来的两位码
	Height      int
	Cup         string
	CareerStart int      // 出道年
	Tags        []string // 已过滤黑名单、已简繁归一
	BioRaw      string   // 结构化字段串，只用来抽标签，不当简介
	Romaji      string   // 罗马字（"Momonogi Kana"），拼 theactorbase 的 slug 用
	Birthday    string   // YYYY-MM-DD，JavStash 的兜底（约 24% 与 JavStash 冲突）
}

// MDCxDB 是内存索引，支持按日文名、中文名、别名查同一个人。
type MDCxDB struct {
	byKey map[string]*MDCxProfile
	total int
}

func NewMDCxDB() *MDCxDB {
	return &MDCxDB{byKey: map[string]*MDCxProfile{}}
}

// CSV 列：name,name_cn,aliases,birthplace,country,height,cup,career_start,tags,bio,romaji,birthday
// aliases 和 tags 用 | 分隔。
func LoadMDCxCSV(path string) (*MDCxDB, error) {
	db := NewMDCxDB()
	if path == "" {
		return db, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return db, nil
		}
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	first := true
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue
		}
		if first {
			first = false
			if len(rec) > 0 && strings.TrimSpace(rec[0]) == "name" {
				continue
			}
		}
		if len(rec) < 10 {
			continue
		}
		p := &MDCxProfile{
			Name:        strings.TrimSpace(rec[0]),
			NameCN:      strings.TrimSpace(rec[1]),
			Aliases:     splitPipe(rec[2]),
			BirthPlace:  strings.TrimSpace(rec[3]),
			Country:     strings.TrimSpace(rec[4]),
			Height:      atoiOrZero(rec[5]),
			Cup:         strings.TrimSpace(rec[6]),
			CareerStart: atoiOrZero(rec[7]),
			Tags:        splitPipe(rec[8]),
			BioRaw:      strings.TrimSpace(rec[9]),
		}
		if len(rec) >= 11 {
			p.Romaji = strings.TrimSpace(rec[10])
		}
		// 第 12 列是后加的，老 CSV 没有，读不到就当空
		if len(rec) >= 12 {
			p.Birthday = normalizeDate(strings.TrimSpace(rec[11]))
		}
		if p.Name == "" {
			continue
		}
		db.total++
		db.byKey[NormalizeName(p.Name)] = p
		if p.NameCN != "" {
			if _, ok := db.byKey[NormalizeName(p.NameCN)]; !ok {
				db.byKey[NormalizeName(p.NameCN)] = p
			}
		}
		for _, a := range p.Aliases {
			if _, ok := db.byKey[NormalizeName(a)]; !ok {
				db.byKey[NormalizeName(a)] = p
			}
		}
	}
	return db, nil
}

// Lookup 按任意名字查档案，查不到返回 nil。
func (m *MDCxDB) Lookup(name string) *MDCxProfile {
	if m == nil || name == "" {
		return nil
	}
	return m.byKey[NormalizeName(name)]
}

// Size 返回演员数（不是索引键数）。
func (m *MDCxDB) Size() int {
	if m == nil {
		return 0
	}
	return m.total
}

func splitPipe(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, "|")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func atoiOrZero(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}
