package actorsync

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// LoadNamesFile 从纯文本读取待刮削的演员名，一行一个。
//
// 规则：
//   - 空行、以 # 开头的行忽略
//   - 行首行尾空格自动去掉
//   - 同名（按 NormalizeName 规范化后）只保留第一次出现的写法
//
// 返回去重后的名字列表，顺序与文件一致。
func LoadNamesFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	seen := map[string]bool{}
	var out []string

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
		out = append(out, s)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("读取演员清单 %s 失败: %w", path, err)
	}
	return out, nil
}
