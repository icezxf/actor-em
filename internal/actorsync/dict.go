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
	m            map[string]string   // 别名（规范化）→ 日文名
	zh           map[string]string   // 日文名（规范化）→ 首选中文名（Emby 的 Name）
	aliasesByJP  map[string][]string // 日文名（规范化）→ 该人的全部别名（含所有中文名）
}

func NewDict() *Dict {
	return &Dict{
		m:           map[string]string{},
		zh:          map[string]string{},
		aliasesByJP: map[string][]string{},
	}
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
		jpKey := NormalizeName(jp)
		d.m[NormalizeName(alias)] = jp
		if zh != "" {
			if _, ok := d.zh[jpKey]; !ok {
				d.zh[jpKey] = zh
			}
			// 中文名也要能反查回日文名（它可能只出现在中文名列，不在别名列）。
			// 不覆盖第一列已有的 alias 映射，避免同名冲突时改掉真实别名指向。
			zhKey := NormalizeName(zh)
			if _, ok := d.m[zhKey]; !ok {
				d.m[zhKey] = jp
			}
		}
		// 同一个日文名在字典里可能有多行（不同来源给出不同中文名/别名），
		// 全部收集为别名。zh 只留一个当主名，其余中文名必须进 aliases，
		// 否则 Emby 里用「桥本有菜」这类旧名/异名就匹配不到「新有菜」。
		d.aliasesByJP[jpKey] = appendUnique(d.aliasesByJP[jpKey], alias)
		if zh != "" {
			d.aliasesByJP[jpKey] = appendUnique(d.aliasesByJP[jpKey], zh)
		}
	}
	return d, nil
}

func appendUnique(list []string, s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return list
	}
	n := NormalizeName(s)
	for _, v := range list {
		if NormalizeName(v) == n {
			return list
		}
	}
	return append(list, s)
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

// AllAliases 返回字典里某个日文名对应的全部别名（含所有中文名）。
// 传入的 name 可以是日文名本身，也可以是任一别名/中文名——先解析回日文名再取。
// 返回的切片是副本，调用方可安全修改。
func (d *Dict) AllAliases(name string) []string {
	if d == nil {
		return nil
	}
	jp := d.m[NormalizeName(name)]
	if jp == "" {
		jp = name
	}
	src := d.aliasesByJP[NormalizeName(jp)]
	out := make([]string, len(src))
	copy(out, src)
	return out
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
	for k, list := range other.aliasesByJP {
		for _, s := range list {
			d.aliasesByJP[k] = appendUnique(d.aliasesByJP[k], s)
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
