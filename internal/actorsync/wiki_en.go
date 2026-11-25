package actorsync

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// wikiEN 是英文维基百科简介源，专门给欧美演员用。
//
// 与日系的中文维基不同，英文维基按英文名查极易撞同名（"Bára" 命中捷克作家、
// "Kiara" 命中消歧义页），所以这一路刻意保守，宁缺毋滥：
//   - 消歧义页 / 简介过短 → 丢弃
//   - 记录有出生年（birth_date / birth_year）→ 条目 extract 里出现的年份必须包含它，否则丢弃
//   - 记录没有出生年 → 要求条目是"人物"且分类里含演员/成人行业相关词，且简介够长
//
// 命中后由调用方写进 Bio，标 bio_lang=en。
type WikiENClient struct {
	HTTP  *http.Client
	Email string
}

func NewWikiENClient(hc *http.Client, email string) *WikiENClient {
	if hc == nil {
		hc = &http.Client{}
	}
	return &WikiENClient{HTTP: hc, Email: email}
}

var (
	reDisambig = regexp.MustCompile(`(?i)may refer to|disambiguation`)
	// 抓 "born" 后 100 字内的所有年份（"born 1984 or 1985" 要能抓到两个）
	reBornTail = regexp.MustCompile(`(?i)born`)
	reYear     = regexp.MustCompile(`\b((?:19|20)\d{2})\b`)
)

// FetchBio 按英文名查英文维基，返回通过身份对账的英文简介；拿不到返回空串。
// wantYear: 记录里已知的出生年（birth_date 前 4 位或 birth_year），0 表示未知。
func (c *WikiENClient) FetchBio(ctx context.Context, name string, wantYear int) string {
	if strings.TrimSpace(name) == "" {
		return ""
	}
	u := "https://en.wikipedia.org/w/api.php?" + url.Values{
		"action":        {"query"},
		"format":        {"json"},
		"prop":          {"extracts|categories"},
		"exintro":       {"1"},
		"explaintext":   {"1"},
		"redirects":     {"1"},
		"cllimit":       {"max"},
		"titles":        {name},
		"formatversion": {"2"},
	}.Encode()

	email := strings.TrimSpace(c.Email)
	if email == "" {
		email = "actor-em@users.noreply.github.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", fmt.Sprintf("actor-em/1.0 (https://github.com/icezxf/actor-em; %s)", email))

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}

	var result struct {
		Query struct {
			Pages []struct {
				Title      string `json:"title"`
				Missing    bool   `json:"missing"`
				Extract    string `json:"extract"`
				Categories []struct {
					Title string `json:"title"`
				} `json:"categories"`
			} `json:"pages"`
		} `json:"query"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || len(result.Query.Pages) == 0 {
		return ""
	}
	p := result.Query.Pages[0]
	if p.Missing {
		return ""
	}
	extract := strings.TrimSpace(p.Extract)
	joined := strings.Join([]string{p.Title, extract}, " ")
	if reDisambig.MatchString(joined) {
		return ""
	}
	// 简介太短没价值（消歧义、红字往往 <100 字）
	if len([]rune(extract)) < 100 {
		return ""
	}

	cats := make([]string, 0, len(p.Categories))
	for _, c := range p.Categories {
		cats = append(cats, strings.ToLower(strings.TrimPrefix(c.Title, "Category:")))
	}
	isPerson := false
	isPerformer := false
	for _, c := range cats {
		if strings.Contains(c, "births") || strings.Contains(c, "living people") {
			isPerson = true
		}
		if strings.Contains(c, "actress") || strings.Contains(c, "actor") ||
			strings.Contains(c, "pornographic") || strings.Contains(c, "adult film") ||
			strings.Contains(c, "model") {
			isPerformer = true
		}
	}

	if wantYear > 0 {
		// 有出生年：extract 里 "born" 附近的年份必须包含 wantYear。
		// 维基常写 "born 1984 or 1985"，所以抓 born 后一段里的所有年份再比。
		loc := reBornTail.FindStringIndex(extract)
		if loc == nil {
			return ""
		}
		tail := extract[loc[1]:]
		if len(tail) > 120 {
			tail = tail[:120]
		}
		found := false
		for _, y := range reYear.FindAllStringSubmatch(tail, -1) {
			if len(y) > 1 && y[1] == fmt.Sprintf("%04d", wantYear) {
				found = true
				break
			}
		}
		if !found {
			return ""
		}
	} else {
		// 无出生年：必须是人物，且分类显示演员/模特相关，简介够长。
		if !isPerson || !isPerformer || len([]rune(extract)) < 150 {
			return ""
		}
	}
	return extract
}

// wantBirthYear 从一条记录的出生字段取用于维基对账的年份。
func wantBirthYear(birthDate string, birthYear int) int {
	if birthYear > 0 {
		return birthYear
	}
	if len(birthDate) >= 4 {
		var y int
		if _, err := fmt.Sscanf(birthDate[:4], "%d", &y); err == nil && y >= 1900 && y <= 2100 {
			return y
		}
	}
	return 0
}
