package actorsync

import (
	"regexp"
	"strconv"
	"strings"
)

// bioUpgradeRunes 是「简介够不够用」的门槛。低于它说明只是一句话，
// 值得再问下一个数据源要更完整的经历描述。
const bioUpgradeRunes = 150

// 维基百科是「按标题查」，不是「按人查」。拿一个短名字去查，命中率很高，
// 但命中的往往是同名的另一样东西，且看起来完全合法：
//
//	もも   → 桃（學名：Prunus persica）是源自中国，蔷薇科李属的一种植物
//	DA     → Da或DA可能是指以下事物：deviantART 的常用缩写
//	David  → 大衛王是公元前10世紀以色列聯合王國的第二任國王
//	JULIA  → Julia或JULIA可能是指：Julia語言，一种用于科学计算的语言
//
// 这些简介会直接进 Emby 的 Overview，必须在写入前拦掉。

// bioDisambigWords 是维基消歧义页的典型措辞。这类页面在罗列同名的人或物，
// 不是任何一个人的介绍。
var bioDisambigWords = regexp.MustCompile(
	`可能是指|可能是|可能指|可以指|指以下|以下事物|消歧义|消歧義|是源于|可以參考|可以参考`,
)

// bioPerformerWords 是「这确实是 AV / 写真圈的人」的硬证据。
// 不够精确会放进桃树和上古王妃，太精确会误杀 theactorbase 的正常简介，
// 这一组是拿 936 条真实数据试出来的。
var bioPerformerWords = regexp.MustCompile(
	`AV|女优|女優|成人|セクシー|グラビア|ヌード|裸体|裸模|写真偶像|寫真偶像|寫真女星|性感|エロ`,
)

// bioMaleWords 表示简介在讲一个男性。字典里混着男优和同名男艺人，
// 这类简介不该挂到女演员条目上。
var bioMaleWords = regexp.MustCompile(
	`日本男|男性[動动歌演漫聲声]|男優|男优|男歌手|男演員|男演员`,
)

// bioFieldDump 认出 MDCx 那种「身高: 166cm | 罩杯: D | 三围: 84/60/86」属性串。
// 这不是介绍文章，而 Overview 头部那行五项本来就是这些字段，
// 原样贴进去等于同一份资料写两遍。
var bioFieldDump = regexp.MustCompile(
	`^\s*(身高|罩杯|三围|三圍|生涯|出身|血型|标签|標籤|出道|事务所|事務所|年龄|年齡)\s*[:：]`,
)

// bioHeightRe 从简介里抽身高，用于和结构化字段对账。
var bioHeightRe = regexp.MustCompile(`(\d{3})\s*(?:cm|CM|公分|厘米)`)

// ValidBio 判断一段文本能不能当演员简介用。
//
// 三条都要过：不是消歧义页、确实是这个圈子里的人、不是在讲男人。
// 宁可留空——Emby 里空着比写着「桃是蔷薇科植物」好。
func ValidBio(bio string) bool {
	if bio = strings.TrimSpace(bio); len([]rune(bio)) < 20 {
		return false
	}
	if bioFieldDump.MatchString(bio) {
		return false
	}
	if bioDisambigWords.MatchString(bio) {
		return false
	}
	if !bioPerformerWords.MatchString(bio) {
		return false
	}
	if bioMaleWords.MatchString(bio) {
		return false
	}
	return true
}

// BioHeightCm 抽简介里提到的身高，抽不到返回 0。
// 只取第一个匹配：简介开头那句才是这个人的基本资料。
func BioHeightCm(bio string) int {
	m := bioHeightRe.FindStringSubmatch(bio)
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n < 120 || n > 220 {
		return 0
	}
	return n
}

// BioConflictsHeight 判断简介里的身高和结构化字段是否矛盾。
//
// 实测有抓到同名的另一个人：Mio Fukada 的简介写「161cm 出生于 1995-11-12」，
// 但 JavStash 给的是 153cm / 1998-12-27。两条数据不可能都对，
// 而结构化字段有 provider 背书，所以丢简介。
//
// 容差 3cm：不同来源各自四舍五入会有一点差。
func BioConflictsHeight(bio string, heightCm int) bool {
	if heightCm <= 0 {
		return false
	}
	h := BioHeightCm(bio)
	if h == 0 {
		return false
	}
	d := h - heightCm
	if d < 0 {
		d = -d
	}
	return d > 3
}
