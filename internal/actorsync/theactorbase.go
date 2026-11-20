package actorsync

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// TheActorBaseClient 抓 theactorbase.com 的中文简介。
//
// 站点是纯静态 HTML，没有 API、没有 sitemap，演员页 slug 规则是
// 「罗马字 姓-名」全小写（如 morisawa-kana），大小写不敏感，反序会 404，
// 但也有例外（桃乃木かな 是 kana-momonogi，名在姓前）。
// 所以只能拿罗马字拼多个候选逐个试，命中即停。
//
// robots.txt 禁止 AI 训练爬虫（ai-train=no），个人媒体库刮削属于 reference 用途，
// 但要注意别并发猛冲——这里由外层串行调用，且每个演员最多试 3 个 slug。
const theActorBaseBase = "https://www.theactorbase.com/people/"

var (
	reStory = regexp.MustCompile(`(?s)<div class="story">(.*?)</div>`)
	reSlug  = regexp.MustCompile(`[^a-z0-9]+`)
)

type TheActorBaseClient struct {
	HTTP *http.Client
}

func NewTheActorBaseClient(hc *http.Client) *TheActorBaseClient {
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	return &TheActorBaseClient{HTTP: hc}
}

// FetchBio 按候选名试出演员页，返回中文简介；取不到返回空串。
func (c *TheActorBaseClient) FetchBio(ctx context.Context, candidates []string) string {
	if c == nil {
		return ""
	}
	tried := map[string]bool{}
	for _, cand := range candidates {
		for _, slug := range slugCandidates(cand) {
			if slug == "" || tried[slug] {
				continue
			}
			tried[slug] = true
			if len(tried) > 3 {
				return "" // 每个演员最多试 3 个候选，别把站点当搜索引擎用
			}
			bio, ok := c.fetchOne(ctx, slug)
			if ok && bio != "" && !isPlaceholderBio(bio) {
				return bio
			}
		}
	}
	return ""
}

func (c *TheActorBaseClient) fetchOne(ctx context.Context, slug string) (string, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, theActorBaseBase+slug, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("User-Agent", "actor-em/1.0 (+https://github.com/icezxf/actor-em)")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "", false
	}
	m := reStory.FindSubmatch(body)
	if len(m) < 2 {
		return "", true // 页面在，但没简介
	}
	return CleanHTML(string(m[1])), true
}

// slugCandidates 由罗马字候选名生成 slug。
//   - "Kana Momonogi" → kana-momonogi（名-姓，比如桃乃木）
//   - "Momonogi Kana" → momonogi-kana（姓-名，绝大多数）
//
// 两种顺序都试，因为站点规则不统一。
func slugCandidates(romaji string) []string {
	r := strings.TrimSpace(strings.ToLower(romaji))
	if r == "" {
		return nil
	}
	parts := strings.Fields(reSlug.ReplaceAllString(r, " "))
	if len(parts) < 2 {
		if len(parts) == 1 {
			return []string{parts[0]}
		}
		return nil
	}
	// 两段名字：姓-名 与 名-姓 都试
	if len(parts) == 2 {
		return []string{
			parts[0] + "-" + parts[1],
			parts[1] + "-" + parts[0],
		}
	}
	// 三段及以上（复姓/中间名），只按原顺序拼
	return []string{strings.Join(parts, "-")}
}

// isPlaceholderBio 过滤「暂无简介」这类占位文案。
// 站点对没写简介的人也返回 200，只是正文是四个字，不滤掉会当成有效简介入档。
func isPlaceholderBio(s string) bool {
	s = strings.TrimSpace(s)
	if len([]rune(s)) < 20 {
		return true
	}
	for _, k := range []string{"暂无简介", "暂未收录", "待补充"} {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

// describe 用于日志。
func (c *TheActorBaseClient) describe() string {
	return fmt.Sprintf("theactorbase(%s)", theActorBaseBase)
}
