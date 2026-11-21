package actorsync

import (
	"sort"
	"strings"
)

// latinWordKey 把「罗马字名字」压成一个与词序无关的键：
// 只留拉丁字母，按词排序后拼接。
//
//	"Mikami Yua"  → "mikamiyua"
//	"Yua Mikami"  → "mikamiyua"
//
// 站点之间词序不统一（theactorbase 同一个站就两种都有：mikami-yua / kana-momonogi），
// 直接比字符串会把本人当成别人。
func latinWordKey(s string) string {
	var words []string
	for _, f := range strings.Fields(strings.ToLower(s)) {
		letters := make([]rune, 0, len(f))
		for _, r := range f {
			if r >= 'a' && r <= 'z' {
				letters = append(letters, r)
			}
		}
		if len(letters) > 0 {
			words = append(words, string(letters))
		}
	}
	if len(words) == 0 {
		return ""
	}
	sort.Strings(words)
	return strings.Join(words, "")
}

// sameRomanizedName 判断两个名字是不是同一个人的罗马字写法：
// 先比归一化后的整串，再比与词序无关的键。
func sameRomanizedName(a, b string) bool {
	na, nb := normalizeForMatch(a), normalizeForMatch(b)
	if na != "" && na == nb {
		return true
	}
	ka, kb := latinWordKey(a), latinWordKey(b)
	return ka != "" && ka == kb
}
