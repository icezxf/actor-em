package actorsync

import (
	"fmt"
	"regexp"
	"strings"
)

// 罩杯既写 cup_size，也进 tags。Emby 的标签栏能直接按「E罩杯」筛人，
// 否则罩杯只藏在详情字段里，看不出规律。
var (
	// 三围首段的「34E」「42DDD」：底围+杯（杯 1~3 字母）
	reBandCup = regexp.MustCompile(`^\s*(\d{2,3})\s*([A-Za-z]{1,3})`)
	// 单独的罩杯：「E」「J」「DDD」
	reCupOnly = regexp.MustCompile(`^\s*([A-Za-z]{1,3})\s*(?:cup|カップ|罩杯|杯)?\s*$`)
)

// CupTag 把罩杯转成标签文字：「E」→「E罩杯」。认不出来返回空串。
func CupTag(cup string) string {
	c := normalizeCup(cup)
	if c == "" {
		return ""
	}
	return c + "罩杯"
}

// normalizeCup 接受 A~O 的罩杯。日系是单字母（A~O）；欧美常见多字母重复
// （AA/DD/JJ/DDD），也接受——但只允许同一个字母重复 2~3 次（AA/DD/JJ/DDD），
// 不认 ABC/DDD 之外的乱组合（如 AB/CD/DDD 中的 DDD 合法是因为三字母全同）。
func normalizeCup(cup string) string {
	s := strings.TrimSpace(cup)
	if m := reCupOnly.FindStringSubmatch(s); len(m) > 1 {
		s = m[1]
	}
	s = strings.ToUpper(strings.TrimSpace(s))
	if len(s) < 1 || len(s) > 3 {
		return ""
	}
	for _, r := range s {
		if r < 'A' || r > 'O' {
			return ""
		}
	}
	// 多字母必须全部相同（AA/DD/JJ/DDD），否则不认。
	for i := 1; i < len(s); i++ {
		if s[i] != s[0] {
			return ""
		}
	}
	return s
}

// DeriveCup 从三围首段反推罩杯：「34E-24-35」→「E」，纯胸围「44-25-39」→ 空。
func DeriveCup(measurements string) string {
	m := reBandCup.FindStringSubmatch(strings.TrimSpace(measurements))
	if len(m) < 3 {
		return ""
	}
	return normalizeCup(m[2])
}

// bandCupMeasurements 用「底围+杯」拼三围，统一成英寸写法：34E-24-35。
// 只认英寸区间（底围 24~50、腰 18~45、臀 28~55），防止把厘米值当英寸拼进去。
func bandCupMeasurements(band int, cup string, waist, hip int) string {
	c := normalizeCup(cup)
	if c == "" || band < 24 || band > 50 || waist < 18 || waist > 45 || hip < 28 || hip > 55 {
		return ""
	}
	return fmt.Sprintf("%d%s-%d-%d", band, c, waist, hip)
}

// AddCupTag 在标签里补「E罩杯」，已有就不再追加。新加的放最前面：
// 下游推 Emby 时按个数截断（只留前 8 个），罩杯放最后会被切掉。
func AddCupTag(tags []string, cup string) []string {
	tag := CupTag(cup)
	if tag == "" {
		return tags
	}
	for _, t := range tags {
		if strings.EqualFold(strings.TrimSpace(t), tag) {
			return tags
		}
	}
	return append([]string{tag}, tags...)
}

// finalizeCup 在抓取收尾时定稿罩杯：没有 cup_size 就从三围首段反推，
// 然后把罩杯补进标签。
func (r *FetchResult) finalizeCup() {
	if normalizeCup(r.CupSize) == "" {
		if c := DeriveCup(r.Measurements); c != "" {
			r.CupSize = c
		}
	}
	if c := normalizeCup(r.CupSize); c != "" {
		r.CupSize = c
	}
	r.Tags = AddCupTag(r.Tags, r.CupSize)
}

// EnsureActorCupTag 给已落库的记录补罩杯标签（以及从三围反推的 cup_size）。
// 幂等，返回是否改动了记录。
func EnsureActorCupTag(a *Actor) bool {
	if a == nil {
		return false
	}
	changed := false
	cup := normalizeCup(StrVal(a.CupSize))
	if cup == "" {
		if c := DeriveCup(StrVal(a.Measurements)); c != "" {
			cup = c
			a.CupSize = StrPtr(c)
			changed = true
		}
	} else if cup != StrVal(a.CupSize) {
		a.CupSize = StrPtr(cup)
		changed = true
	}
	if tag := CupTag(cup); tag != "" {
		before := len(a.Tags)
		a.Tags = AddCupTag(a.Tags, cup)
		if len(a.Tags) != before {
			changed = true
		}
	}
	return changed
}
