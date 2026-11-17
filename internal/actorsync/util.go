package actorsync

import (
	"crypto/sha1"
	"encoding/hex"
	"regexp"
	"strings"
	"unicode"
)

// ---------- 指针小工具 ----------

func StrPtr(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	s = strings.TrimSpace(s)
	return &s
}

func StrVal(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func IntPtr(n int) *int {
	if n <= 0 {
		return nil
	}
	return &n
}

func IntVal(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// ---------- 名字与主键 ----------

// NormalizeName 用于比对：去空白、转小写。
func NormalizeName(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "　", "")
	return strings.ToLower(s)
}

// NameHashKey 是最后的降级主键：name:<sha1(归一化名)>。
func NameHashKey(name string) string {
	sum := sha1.Sum([]byte(NormalizeName(name)))
	return "name:" + hex.EncodeToString(sum[:])
}

// BuildKey 按 javstash → stashdb → tpdb → 名字哈希 的优先级生成主键。
// Emby 的 local-1234 会随服务器变化，绝不能用。
func BuildKey(providerIDs map[string]string, name string) string {
	for _, k := range []string{"JavStash", "StashDB", "TPDB"} {
		if v := strings.TrimSpace(providerIDs[k]); v != "" {
			return strings.ToLower(k) + ":" + strings.ToLower(strings.TrimSpace(v))
		}
	}
	return NameHashKey(name)
}

// ---------- 文件名 ----------

var (
	illegalFileChars = regexp.MustCompile(`[\\/:*?"<>|\x00-\x1f]`)
	reservedNames    = map[string]bool{
		"CON": true, "PRN": true, "AUX": true, "NUL": true,
		"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
		"COM6": true, "COM7": true, "COM8": true, "COM9": true,
		"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
		"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
	}
)

// SanitizeFileName 只做必要处理：非法字符换 _、去首尾空白与尾部点、超长按 rune 截断。
// 不做翻译/转写——Emby 里叫什么就是什么。
func SanitizeFileName(name string) string {
	s := illegalFileChars.ReplaceAllString(name, "_")
	s = strings.TrimSpace(s)
	s = strings.TrimRight(s, ".")
	if s == "" {
		s = "_"
	}
	// 按 rune 截断到 80 字节，避免切坏 UTF-8
	s = TruncateUTF8(s, 80)
	if reservedNames[strings.ToUpper(s)] {
		s = "_" + s
	}
	return s
}

// TruncateUTF8 按字节上限截断，但不切断 rune。
func TruncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if b.Len()+len(string(r)) > maxBytes {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

// BucketOf 返回头像分桶目录名：取名字首字符。
// 中日韩字符用原字符，拉丁字母转大写，数字原样，其余归到 _。
func BucketOf(name string) string {
	r := []rune(strings.TrimSpace(name))
	if len(r) == 0 {
		return "_"
	}
	c := r[0]
	switch {
	case c >= 'a' && c <= 'z':
		return strings.ToUpper(string(c))
	case c >= 'A' && c <= 'Z':
		return string(c)
	case c >= '0' && c <= '9':
		return string(c)
	case unicode.Is(unicode.Han, c),
		unicode.Is(unicode.Hiragana, c),
		unicode.Is(unicode.Katakana, c),
		unicode.Is(unicode.Hangul, c):
		return string(c)
	}
	return "_"
}

// ---------- 文本 ----------

var (
	reEnglishOnly = regexp.MustCompile(`^[a-zA-Z\s\-\.0-9']+$`)
	reHan         = regexp.MustCompile(`\p{Han}`)
	reKana        = regexp.MustCompile(`[\p{Hiragana}\p{Katakana}]`)
)

// DetectLanguage 粗略判断名字/文本语种：en / zh / ja / unknown。
func DetectLanguage(text string) string {
	if reEnglishOnly.MatchString(text) {
		return "en"
	}
	if reKana.MatchString(text) {
		return "ja"
	}
	if reHan.MatchString(text) {
		return "zh"
	}
	return "unknown"
}

// IsUUID 判断是否是 36 位 UUID（Emby 里有些演员名直接存了 UUID）。
func IsUUID(s string) bool {
	return len(s) == 36 && strings.Count(s, "-") == 4
}

// CleanHTML 去掉所有标签，压缩空白。
func CleanHTML(s string) string {
	s = regexp.MustCompile(`<[^>]+>`).ReplaceAllString(s, " ")
	return strings.Join(strings.Fields(s), " ")
}
