package actorsync

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// DramaFaceClient 抓 dramaface.com（剧面）的中文简介。
//
// 站点是静态页，演员页 slug 规则（实测 2026-11，见下）：
//   - 用罗马字名去掉空格直接拼接，大小写不敏感：
//     "Kana Momonogi" → MomonogiKana / KanaMomonogi（两种词序都在用）
//     桃乃木香奈=MomonogiKana、三上悠亚=YuaMikami、山岸逢花=YamagishiAika、
//     桥本有菜=ArinaHashimoto 均实测命中。
//   - 单个英文名（如 "Aika"）也能命中，但可能是同名别人——
//     所以只作为候选之一，且必须由 ValidBio + 身高校验兜底。
//   - 简介在 <meta name="description"> 里，是整段中文，质量高于维基的一句话。
//
// 注意：站点对无浏览器 UA 的请求返回 403，必须带常规 UA。
// 每个演员最多试 4 个 slug，由外层串行调用，不并发猛冲。
const dramaFaceBase = "https://www.dramaface.com/model/"

const dramaFaceUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36"

var (
	reDramaDesc  = regexp.MustCompile(`(?is)<meta[^>]*name=["']description["'][^>]*content=["']([^"']*)["']`)
	reDramaTitle = regexp.MustCompile(`(?is)<title>([^<]*)</title>`)
	// 简介开头的属性串字段名，用于逐段剥掉（见 stripDramaFields）。
	reDramaFieldLabel = regexp.MustCompile(`^(?:身高|罩杯|胸围|腰围|臀围|三围|三圍|生涯|出身|血型|标签|標籤|出道|事务所|事務所|爱好|愛好|年龄|年齡)\s*[:：]`)
)

type DramaFaceClient struct {
	HTTP *http.Client
	Base string // 允许测试覆盖；空则用默认 dramaFaceBase
}

func NewDramaFaceClient(hc *http.Client) *DramaFaceClient {
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	return &DramaFaceClient{HTTP: hc}
}

func (c *DramaFaceClient) baseURL() string {
	if c.Base != "" {
		return c.Base
	}
	return dramaFaceBase
}

// FetchBio 按候选名试出演员页，返回中文简介；取不到返回空串。
func (c *DramaFaceClient) FetchBio(ctx context.Context, candidates []string) string {
	if c == nil {
		return ""
	}
	tried := map[string]bool{}
	for _, cand := range candidates {
		for _, slug := range dramaSlugs(cand) {
			key := strings.ToLower(slug)
			if key == "" || tried[key] {
				continue
			}
			tried[key] = true
			if len(tried) > 4 {
				return "" // 每个演员最多试 4 个 slug
			}
			bio, ok := c.fetchOne(ctx, slug)
			if ok && bio != "" {
				return bio
			}
		}
	}
	return ""
}

func (c *DramaFaceClient) fetchOne(ctx context.Context, slug string) (string, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL()+slug, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("User-Agent", dramaFaceUA)
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
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
	// 未命中页 title 是「提示信息 - 9K星图」，正文没有简介。
	if m := reDramaTitle.FindSubmatch(body); len(m) > 1 && strings.Contains(string(m[1]), "提示信息") {
		return "", false
	}
	m := reDramaDesc.FindSubmatch(body)
	if len(m) < 2 {
		return "", true // 页面在，但没简介
	}
	desc := strings.TrimSpace(html.UnescapeString(string(m[1])))
	if !ValidBio(desc) {
		// 以属性串开头的（如三上悠亚），剥掉前缀再验一次。
		desc = stripDramaFields(desc)
		if !ValidBio(desc) {
			return "", true
		}
	}
	return desc, true
}

// stripDramaFields 剥掉简介开头的「字段: 值 字段: 值 ...」串，只留正文。
// 属性串里每个字段是「标签:值」或「标签: 值」，值可能含单位（155cm）也可能
// 是中文词（料理、マッサージ）。做法：逐 token 走，遇到字段标签就跳过它和它的值，
// 一旦出现不是字段标签开头的 token，就认为正文开始，从这里截断。
func stripDramaFields(s string) string {
	tokens := strings.Fields(s)
	i := 0
	for i < len(tokens) {
		if !reDramaFieldLabel.MatchString(tokens[i]) {
			break
		}
		i++
		// 跳过值："身高:155cm" / "身高：155cm" 值在同一 token；
		// "身高: 155cm" 值在下一个 token。站点两种冒号都在用，
		// 早先只按半角切割，遇到全角会取 [1] 越界把整轮同步打挂。
		label := tokens[i-1]
		inlineValue := ""
		// 全角冒号占 3 字节，不能用 IndexAny + p+1，否则切出来的是残留字节。
		for _, sep := range []string{":", "："} {
			if p := strings.Index(label, sep); p >= 0 {
				inlineValue = strings.TrimSpace(label[p+len(sep):])
				break
			}
		}
		if inlineValue == "" && i < len(tokens) {
			i++
		}
	}
	return strings.Join(tokens[i:], " ")
}

// dramaSlugs 由罗马字名生成 slug 候选（实测规则，见文件头注释）。
//   - "Kana Momonogi" → ["KanaMomonogi", "MomonogiKana", "Kana"]
//   - "Aika Yamagishi" → ["AikaYamagishi", "YamagishiAika", "Aika"]
//   - "aika" → ["aika"]
//
// 大小写不敏感，两种词序都试；单个词兜底（命中可能是同名别人，靠 ValidBio 过滤）。
func dramaSlugs(romaji string) []string {
	r := strings.TrimSpace(romaji)
	if r == "" {
		return nil
	}
	parts := strings.Fields(r)
	if len(parts) == 1 {
		return []string{parts[0]}
	}
	if len(parts) == 2 {
		return []string{parts[0] + parts[1], parts[1] + parts[0], parts[1]}
	}
	// 三段及以上：按原顺序拼 + 末段兜底
	out := []string{strings.Join(parts, "")}
	out = append(out, parts[len(parts)-1])
	return out
}

func (c *DramaFaceClient) describe() string {
	return fmt.Sprintf("dramaface(%s)", dramaFaceBase)
}
