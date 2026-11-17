package actorsync

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"
)

// LoadNamesFile 从纯文本读取待刮削的演员名，一行一个。
//
// 规则：
//   - 空行、以 # 开头的行忽略
//   - 行首行尾空格自动去掉
//   - 同名（按 NormalizeName 规范化后）只保留第一次出现的写法
//
// 返回的 EmbyPerson 只有 Name，ID 为空 —— 清单不再依赖 Emby。
func LoadNamesFile(path string) ([]EmbyPerson, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	seen := map[string]bool{}
	var out []EmbyPerson

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		s := strings.TrimSpace(sc.Text())
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		key := NormalizeName(s)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, EmbyPerson{Name: s})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("读取演员清单 %s 失败: %w", path, err)
	}
	return out, nil
}

// SaveNamesFile 写回演员清单，每行一个名字，按名字排序，UTF-8 无 BOM。
// 先写 .tmp 再 rename，避免中途出错把原文件写坏。
func SaveNamesFile(path string, names []string) error {
	clean := make([]string, 0, len(names))
	seen := map[string]bool{}
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		key := NormalizeName(n)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		clean = append(clean, n)
	}
	sort.Slice(clean, func(i, j int) bool {
		return NormalizeName(clean[i]) < NormalizeName(clean[j])
	})

	var b strings.Builder
	b.WriteString("# 待刮削演员清单，一行一个；# 开头的行会被忽略\n")
	b.WriteString("# 由 scripts 或 nfo 扫描自动生成，也可以手工增删\n")
	for _, n := range clean {
		b.WriteString(n)
		b.WriteByte('\n')
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
