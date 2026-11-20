package actorsync

import (
	"html"
	"regexp"
	"strconv"
	"strings"
)

// CNProfile 是三个中文人物站的统一返回结构。
//
// 这些站都是「先按名字查到本人，再读详情页」，和 dramaface 那种猜 slug 不同，
// 身份可以靠页面上的原名/罗马字直接对账，所以敢收结构化字段（生日/身高/出道年）。
// 抓取方必须先确认 cnNameHit 命中，才允许把内容并进 FetchResult。
type CNProfile struct {
	Bio         string
	ChineseName string
	Romaji      string
	Aliases     []string
	BirthDate   string // YYYY-MM-DD
	BirthPlace  string
	Gender      string
	HeightCm    int
	CareerStart int
	Bust        int // 厘米，三围分项（可选）
	Waist       int
	Hip         int
	Cup         string
	ImageURL    string
	SourceURL   string
}

func (p *CNProfile) hasContent() bool {
	if p == nil {
		return false
	}
	return p.Bio != "" || p.ChineseName != "" || p.BirthDate != "" ||
		p.HeightCm > 0 || p.CareerStart > 0 || p.Romaji != "" || len(p.Aliases) > 0
}

// cnNameHit 要求「站方给出的任一名号」与「我们手上的任一候选名」归一化后完全相同。
// 不搞模糊包含：cinewho/yinrenlu 搜索页会返回一堆同姓或同名的其他人，
// 只要名字不是精确一致就当没命中，宁可空着也不能串人。
func cnNameHit(siteNames, candidates []string) bool {
	for _, a := range siteNames {
		na := NormalizeName(a)
		if na == "" {
			continue
		}
		for _, b := range candidates {
			if na == NormalizeName(b) {
				return true
			}
		}
	}
	return false
}

// validCNBio 是中文人物站简介的门槛。这些站身份已用原名对过账，
// 所以不再要求简介里必须出现「AV/女优」等职业词（idoldb 的简介很多是
// 「2019年以MOODYZ专属身份出道…」，不含那些词但确实是本人）。
// 消歧义页、男性简介、字段串这三类仍然一律拒掉。
func validCNBio(bio string) bool {
	bio = strings.TrimSpace(bio)
	if len([]rune(bio)) < 20 || len([]rune(bio)) > 6000 {
		return false
	}
	if bioDisambigWords.MatchString(bio) || bioMaleWords.MatchString(bio) || bioFieldDump.MatchString(bio) {
		return false
	}
	return true
}

var (
	cnYearRe   = regexp.MustCompile(`(19|20)\d{2}`)
	cnHeightRe = regexp.MustCompile(`(1[2-9]\d)\s*(?:cm|CM|公分|厘米)`)
	cnDateRe   = regexp.MustCompile(`((?:19|20)\d{2})\s*(?:[-/.年])\s*(\d{1,2})?\s*(?:[-/.月])\s*(\d{1,2})?`)
	// 兼容 B92(Hcup) / B92(H) / B92Hcup 三种写法。
	// 每个杯位单独一组，避免 [A-Z]{1,2} 把 "HC" 整体吞掉。
	cnBustRe  = regexp.MustCompile(`(?i)B\s*(\d{2,3})\s*(?:\(\s*([A-Z])\s*cup\s*\)|\(\s*([A-Z])\s*\)|([A-Z])\s*cup)?`)
	cnWaistRe = regexp.MustCompile(`(?i)W\s*(\d{2,3})`)
	cnHipRe   = regexp.MustCompile(`(?i)H\s*(\d{2,3})`)
)

// cnParseDate 兼容 2002-06-16 / 1993 年 8 月 16 日 / 1993 三种写法。
// 只有年月日齐全才算生日；年份单独出现时留给 CareerStart，不假装是生日。
func cnParseDate(s string) string {
	m := cnDateRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil || m[2] == "" || m[3] == "" {
		return ""
	}
	month, err1 := strconv.Atoi(m[2])
	day, err2 := strconv.Atoi(m[3])
	if err1 != nil || err2 != nil || month < 1 || month > 12 || day < 1 || day > 31 {
		return ""
	}
	return m[1] + "-" + twoDigits(month) + "-" + twoDigits(day)
}

func cnParseYear(s string) int {
	m := cnYearRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(m[0]))
	if err != nil || n < 1900 || n > 2100 {
		return 0
	}
	return n
}

func cnParseHeight(s string) int {
	m := cnHeightRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n < 120 || n > 220 {
		return 0
	}
	return n
}

// cnParseMeasurements 解析 B92(Hcup)・W59・H89 这类日站写法，单位厘米。
func cnParseMeasurements(s string) (bust, waist, hip int, cup string) {
	if m := cnBustRe.FindStringSubmatch(s); len(m) > 1 {
		bust, _ = strconv.Atoi(m[1])
		cup = strings.ToUpper(m[2] + m[3] + m[4])
	}
	if m := cnWaistRe.FindStringSubmatch(s); len(m) > 1 {
		waist, _ = strconv.Atoi(m[1])
	}
	if m := cnHipRe.FindStringSubmatch(s); len(m) > 1 {
		hip, _ = strconv.Atoi(m[1])
	}
	return
}

func twoDigits(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

// cnCleanText 剥掉搜索结果里用于高亮的 <em>/<small> 等标签，并解开 HTML 实体。
func cnCleanText(s string) string {
	s = reCnTag.ReplaceAllString(s, " ")
	s = strings.TrimSpace(html.UnescapeString(s))
	return strings.Join(strings.Fields(s), " ")
}

var reCnTag = regexp.MustCompile(`<[^>]+>`)
