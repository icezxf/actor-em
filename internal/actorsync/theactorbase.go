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
// 站点是纯静态 HTML，没有 API、没有 sitemap，站内搜索也是前端 JS（服务端只返回空壳），
// 所以只能靠 slug 直接拼。实测（2026-11）slug 就是罗马字，全小写、用连字符，
// 但**词序不统一**：
//
//	三上悠亜  Mikami Yua  → /people/mikami-yua   （姓-名）
//	桃乃木かな Kana Momonogi → /people/kana-momonogi（名-姓）
//	松咲那奈  Matsuzaki Nana → /people/matsuzaki-nana（姓-名）
//
// 所以两种词序都要试。页面标题形如「三上悠亜（三上悠亚 / Mikami Yua）资料_作品」，
// 中文名和罗马字都在里面，正好用来核身份——以前不核，撞到同名的另一个人时
// 会把别人的简介写进来。
//
// robots.txt 只限制 AI 训练（ai-train=no），个人媒体库刮削属于 reference 用途；
// 由外层串行调用、每人最多试 4 个 slug，不并发猛冲。
const theActorBaseBase = "https://www.theactorbase.com/people/"

var (
	reStory      = regexp.MustCompile(`(?s)<div class="story">(.*?)</div>`)
	reSlug       = regexp.MustCompile(`[^a-z0-9]+`)
	reTabTitle   = regexp.MustCompile(`(?is)<title>([^<]*)</title>`)
	reTabChinese = regexp.MustCompile(`^([^（(]+)[（(]`)
	reTabRomanji = regexp.MustCompile(`[/／]\s*([A-Za-z][A-Za-z .'\-]*?)\s*[）)]`)
)

type TheActorBaseClient struct {
	HTTP *http.Client
	Base string // 允许测试覆盖；空则用默认 theActorBaseBase（"https://www.theactorbase.com/people/"）
}

func (c *TheActorBaseClient) baseURL() string {
	if c.Base != "" {
		return c.Base
	}
	return theActorBaseBase
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
		// 中文候选拼不出 slug（站内 slug 是罗马字），只会白丢请求。
		if !hasLatinLetter(cand) {
			continue
		}
		for _, slug := range slugCandidates(cand) {
			if slug == "" || tried[slug] {
				continue
			}
			tried[slug] = true
			if len(tried) > 4 {
				return "" // 每个演员最多试 4 个候选，别把站点当搜索引擎用
			}
			bio, ok := c.fetchOne(ctx, slug, candidates)
			if ok && bio != "" && !isPlaceholderBio(bio) {
				return bio
			}
		}
	}
	return ""
}

func (c *TheActorBaseClient) fetchOne(ctx context.Context, slug string, candidates []string) (string, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL()+slug, nil)
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
	// 页面标题带中文名和罗马字，先核身份再取简介：
	// slug 靠猜，撞到同名的另一个人时不能把别人的经历写进库里。
	title := ""
	if m := reTabTitle.FindSubmatch(body); len(m) > 1 {
		title = strings.TrimSpace(string(m[1]))
	}
	if !tabPageMatches(title, candidates) {
		return "", false
	}
	m := reStory.FindSubmatch(body)
	if len(m) < 2 {
		return "", true // 页面在，但没简介
	}
	bio := CleanHTML(string(m[1]))
	if !LooksChineseBio(bio) {
		return "", true // 非中文（英文/日文条目）不收
	}
	return bio, true
}

// tabPageMatches 用页面标题核身份。
//
// 标题形如「三上悠亜（三上悠亚 / Mikami Yua）资料_作品」，括号前是中文名，
// 括号里是「别名 / 罗马字」。三种命中任一即算本人：
//   - 标题里的中文名与候选名一致；
//   - 括号里的罗马字（去掉空格/连字符）与候选罗马字一致；
//   - 候选名（≥4 字）作为整体出现在标题里。
//
// 标题解析不出来时一律拒绝——猜出来的 slug 没有别的证据，宁可不要这条简介。
func tabPageMatches(title string, candidates []string) bool {
	if strings.TrimSpace(title) == "" {
		return false
	}
	var titleNames []string
	if m := reTabChinese.FindStringSubmatch(title); len(m) > 1 {
		titleNames = append(titleNames, strings.TrimSpace(m[1]))
	}
	if m := reTabRomanji.FindStringSubmatch(title); len(m) > 1 {
		titleNames = append(titleNames, strings.TrimSpace(m[1]))
	}
	if len(titleNames) == 0 {
		return false
	}
	normTitle := normalizeForMatch(title)
	for _, tn := range titleNames {
		nt := normalizeForMatch(tn)
		if nt == "" {
			continue
		}
		for _, cand := range candidates {
			if nt == normalizeForMatch(cand) || sameRomanizedName(tn, cand) {
				return true
			}
		}
	}
	for _, cand := range candidates {
		nc := normalizeForMatch(cand)
		if len([]rune(nc)) >= 4 && strings.Contains(normTitle, nc) {
			return true
		}
	}
	return false
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
