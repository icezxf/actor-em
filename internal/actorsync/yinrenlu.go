package actorsync

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// YinrenluClient 抓 yinrenlu.com（映人录）的中文人物档案。
//
// 站点是服务端渲染的静态 HTML，两步走：
//  1. /search?q=<名字> 返回 person-card 列表，卡片上有显示名和「原文艺名」
//  2. /people/<id> 详情页有 <section id="overview"> 的中文简介，以及
//     <dl class="profile-table"> 里的中文艺名/罗马字/其他艺名/生日/出道年
//
// 只有卡片的显示名或原文名与候选名完全一致才进详情页；详情页再对一次
// 原名和罗马字，防止「搜索命中同姓的人」串档。实测 10 个样本里 9 个有档案。
const yinrenluBase = "https://yinrenlu.com"

type YinrenluClient struct {
	HTTP *http.Client
	Base string
}

func NewYinrenluClient(hc *http.Client) *YinrenluClient {
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	return &YinrenluClient{HTTP: hc}
}

func (c *YinrenluClient) baseURL() string {
	if c.Base != "" {
		return strings.TrimRight(c.Base, "/")
	}
	return yinrenluBase
}

var (
	reYinCard     = regexp.MustCompile(`(?is)<article class="person-card[^"]*"(.*?)</article>`)
	reYinHref     = regexp.MustCompile(`(?is)<h3><a href="(/people/[^"]+)">([^<]+)</a></h3>`)
	reYinNative   = regexp.MustCompile(`(?is)<p class="original-name">([^<]*)</p>`)
	reYinTitle    = regexp.MustCompile(`(?is)<h1 id="person-title">([^<]*)</h1>`)
	reYinRoman    = regexp.MustCompile(`(?is)<p class="person-native">([^<]*)</p>`)
	reYinOverview = regexp.MustCompile(`(?is)<section class="profile-panel" id="overview">(.*?)</section>`)
	reYinP        = regexp.MustCompile(`(?is)<p>(.*?)</p>`)
	reYinField    = regexp.MustCompile(`(?is)<dt>([^<]+)</dt><dd>(.*?)</dd>`)
	reYinImage    = regexp.MustCompile(`(?is)<figure class="person-hero-photo"><img[^>]*src="([^"]+)"`)
)

type yinCard struct {
	path     string
	name     string
	original string
}

// Fetch 依次用候选名搜索，返回第一个身份对得上的档案。
func (c *YinrenluClient) Fetch(ctx context.Context, candidates []string) (*CNProfile, string) {
	if c == nil {
		return nil, ""
	}
	tried := map[string]bool{}
	for _, cand := range candidates {
		cand = strings.TrimSpace(cand)
		key := NormalizeName(cand)
		if key == "" || tried[key] || IsUUID(cand) {
			continue
		}
		tried[key] = true
		if len(tried) > 3 {
			break
		}
		searchURL := c.baseURL() + "/search?q=" + url.QueryEscape(cand)
		body, err := c.get(ctx, searchURL)
		if err != nil {
			continue
		}
		for _, card := range parseYinCards(string(body)) {
			if !cnNameHit([]string{card.name, card.original}, []string{cand}) {
				continue
			}
			detailURL := c.baseURL() + card.path
			detail, err := c.get(ctx, detailURL)
			if err != nil {
				continue
			}
			if p := parseYinDetail(string(detail), detailURL); p != nil &&
				cnNameHit(append([]string{p.ChineseName, p.Romaji}, p.Aliases...), candidates) {
				return p, cand
			}
		}
	}
	return nil, ""
}

func (c *YinrenluClient) get(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", yinrenluUA)
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 2<<20))
}

const yinrenluUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

func parseYinCards(page string) []yinCard {
	var out []yinCard
	for _, m := range reYinCard.FindAllStringSubmatch(page, -1) {
		chunk := m[1]
		hm := reYinHref.FindStringSubmatch(chunk)
		if len(hm) < 3 {
			continue
		}
		card := yinCard{path: hm[1], name: cnCleanText(hm[2])}
		if nm := reYinNative.FindStringSubmatch(chunk); len(nm) > 1 {
			card.original = cnCleanText(nm[1])
		}
		out = append(out, card)
	}
	return out
}

func parseYinDetail(page, sourceURL string) *CNProfile {
	title := ""
	if m := reYinTitle.FindStringSubmatch(page); len(m) > 1 {
		title = cnCleanText(m[1])
	}
	native := ""
	if m := reYinRoman.FindStringSubmatch(page); len(m) > 1 {
		native = cnCleanText(m[1])
	}

	bio := ""
	if m := reYinOverview.FindStringSubmatch(page); len(m) > 1 {
		var paras []string
		for _, p := range reYinP.FindAllStringSubmatch(m[1], -1) {
			if s := cnCleanText(p[1]); s != "" {
				paras = append(paras, s)
			}
		}
		bio = strings.Join(paras, "\n\n")
	}

	fields := map[string]string{}
	for _, m := range reYinField.FindAllStringSubmatch(page, -1) {
		fields[cnCleanText(m[1])] = cnCleanText(m[2])
	}

	aliases := []string{}
	if v := fields["其他艺名"]; v != "" {
		for _, a := range strings.Split(v, "、") {
			if a = strings.TrimSpace(a); a != "" {
				aliases = append(aliases, a)
			}
		}
	}
	image := ""
	if m := reYinImage.FindStringSubmatch(page); len(m) > 1 {
		image = html.UnescapeString(m[1])
	}

	return &CNProfile{
		Bio:         bio,
		ChineseName: firstNonEmpty(fields["中文艺名"], title),
		Romaji:      fields["罗马字写法"],
		Aliases:     mergeStrings(aliases, []string{native, title}),
		BirthDate:   cnParseDate(fields["出生日期"]),
		BirthPlace:  fields["出生地"],
		CareerStart: cnParseYear(fields["出道年份"]),
		ImageURL:    image,
		SourceURL:   sourceURL,
	}
}
