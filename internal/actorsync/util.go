package actorsync

import (
	"crypto/sha1"
	"encoding/hex"
	"regexp"
	"strings"
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
func BuildKey(providerIDs map[string]string, name string) string {
	for _, k := range []string{"JavStash", "StashDB", "TPDB"} {
		if v := strings.TrimSpace(providerIDs[k]); v != "" {
			return strings.ToLower(k) + ":" + strings.ToLower(strings.TrimSpace(v))
		}
	}
	return NameHashKey(name)
}

// ---------- 文本截断 ----------

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

// ---------- 头像地址 ----------

// rawToJSDelivr 把 gfriends 的 raw 直链换成 jsDelivr 镜像，国内访问更稳。
const rawPrefix = "https://raw.githubusercontent.com/"
const cdnPrefix = "https://cdn.jsdelivr.net/gh/"

// MirrorCN 返回国内可访问的镜像地址；没有镜像时返回空串。
// 目前只有 gfriends 的 raw.githubusercontent.com 地址有确定性的镜像映射。
func MirrorCN(rawURL string) string {
	u := strings.TrimSpace(rawURL)
	if !strings.HasPrefix(u, rawPrefix) {
		return ""
	}
	rest := strings.TrimPrefix(u, rawPrefix)
	// raw 形式：owner/repo/branch/path → gh/owner/repo@branch/path
	parts := strings.SplitN(rest, "/", 4)
	if len(parts) != 4 {
		return ""
	}
	owner, repo, branch, path := parts[0], parts[1], parts[2], parts[3]
	return cdnPrefix + owner + "/" + repo + "@" + branch + "/" + path
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

// IsUUID 判断是否是 36 位 UUID（清单里可以直接写数据源 ID）。
func IsUUID(s string) bool {
	return len(s) == 36 && strings.Count(s, "-") == 4
}

// CleanHTML 去掉所有标签，压缩空白。
func CleanHTML(s string) string {
	s = regexp.MustCompile(`<[^>]+>`).ReplaceAllString(s, " ")
	return strings.Join(strings.Fields(s), " ")
}
