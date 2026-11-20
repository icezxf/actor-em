package actorsync

import (
	"encoding/csv"
	"io"
	"os"
	"sort"
	"strings"
)

// Dict 是「别名 → 日文名」映射表，外加「日文名 → 中文名」反查。
//
// 清单里的演员名多为中文，而 JavStash / TPDB 只认日文或罗马字，
// 没有这层映射，日系演员基本搜不到。
//
// 文件由 scripts/build_base_db.py（工作流 1）生成，四列：
//
//	别名,日文名,中文名,来源
//
// 也兼容旧的两列格式（中文名,日文名）。
type Dict struct {
	m  map[string]string // 别名（规范化）→ 日文名
	zh map[string]string // 日文名（规范化）→ 中文名
}

func NewDict() *Dict {
	return &Dict{m: map[string]string{}, zh: map[string]string{}}
}

// LoadDict 读 CSV。文件不存在时返回空字典，不报错。
func LoadDict(path string) (*Dict, error) {
	d := NewDict()
	if path == "" {
		return d, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return d, nil
		}
		return d, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	r.TrimLeadingSpace = true
	first := true
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue
		}
		if len(rec) < 2 {
			continue
		}
		alias := strings.TrimSpace(rec[0])
		jp := strings.TrimSpace(rec[1])
		zh := ""
		if len(rec) >= 3 {
			zh = strings.TrimSpace(rec[2])
		}
		if first {
			first = false
			if isDictHeader(alias) {
				continue // 跳过表头
			}
		}
		if alias == "" || jp == "" {
			continue
		}
		d.m[NormalizeName(alias)] = jp
		if zh != "" {
			if _, ok := d.zh[NormalizeName(jp)]; !ok {
				d.zh[NormalizeName(jp)] = zh
			}
		}
	}
	return d, nil
}

func isDictHeader(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "别名", "中文名", "zh_name", "alias", "name":
		return true
	}
	return strings.Contains(s, "中文") || strings.Contains(s, "别名")
}

// Lookup 按别名查日文名，查不到返回空串。
func (d *Dict) Lookup(name string) string {
	if d == nil {
		return ""
	}
	return d.m[NormalizeName(name)]
}

// ChineseName 按日文名反查中文名，查不到返回空串。
func (d *Dict) ChineseName(jp string) string {
	if d == nil {
		return ""
	}
	return d.zh[NormalizeName(jp)]
}

// Merge 用另一份字典覆盖当前字典，用于手工补充条目。
// 主字典会被工作流 1 覆盖，手工维护请写在补充字典里。
func (d *Dict) Merge(other *Dict) {
	if d == nil || other == nil {
		return
	}
	for k, v := range other.m {
		if v != "" {
			d.m[k] = v
		}
	}
	for k, v := range other.zh {
		if v != "" {
			d.zh[k] = v
		}
	}
}

// Size 用于启动日志。
func (d *Dict) Size() int {
	if d == nil {
		return 0
	}
	return len(d.m)
}

// Actors 返回字典里所有唯一的日文名，按名字排序。
// 工作流 2 用它作为待抓清单——字典就是演员总表。
func (d *Dict) Actors() []string {
	if d == nil {
		return nil
	}
	seen := make(map[string]bool, len(d.m))
	out := make([]string, 0, len(d.m))
	for _, jp := range d.m {
		if jp != "" && !seen[jp] {
			seen[jp] = true
			out = append(out, jp)
		}
	}
	sort.Strings(out)
	return out
}
