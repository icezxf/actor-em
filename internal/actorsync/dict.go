package actorsync

import (
	"encoding/csv"
	"io"
	"os"
	"strings"
)

// Dict 是中文名 → 日文名的映射表。
// 清单里的演员名多为中文，而 JavStash / TPDB 只认日文或罗马字，
// 没有这层映射，日系演员基本搜不到。
type Dict struct {
	m map[string]string
}

func NewDict() *Dict {
	return &Dict{m: map[string]string{}}
}

// LoadDict 读 CSV（两列：中文名,日文名）。文件不存在时返回空字典，不报错。
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
		zh := strings.TrimSpace(rec[0])
		jp := strings.TrimSpace(rec[1])
		if zh == "" || jp == "" {
			continue
		}
		if first && (strings.Contains(zh, "中文") || strings.EqualFold(zh, "zh_name")) {
			first = false
			continue // 跳过表头
		}
		first = false
		d.m[NormalizeName(zh)] = jp
	}
	return d, nil
}

// Lookup 按中文名查日文名，查不到返回空串。
func (d *Dict) Lookup(name string) string {
	if d == nil {
		return ""
	}
	return d.m[NormalizeName(name)]
}

// Merge 用另一份字典覆盖当前字典，用于手工补充条目。
// 主字典从 amane 自动生成、会被覆盖，手工维护请写在补充字典里。
func (d *Dict) Merge(other *Dict) {
	if d == nil || other == nil {
		return
	}
	for k, v := range other.m {
		if v != "" {
			d.m[k] = v
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
