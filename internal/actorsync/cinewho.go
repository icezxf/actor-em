package actorsync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// CinewhoClient 抓 cinewho.com（影中人）的中文演员档案。
//
// 和 yinrenlu 一样是「搜索 → 详情」两步，但详情页里有一份完整的
// <script type="application/ld+json"> Person 结构（description / birthDate /
// height / nationality / image），比从正文扒 div 稳定得多：
//
//	https://www.cinewho.com/cine/satsukinao
//	→ name=彩月七緒, description=21岁（2024出道年龄）的彩月七绪…
//
// 搜索结果里的链接是 model/<slug>，但 /cine/<slug> 也能打开，两者同页。
// 用搜索结果给出的 slug，不自己猜拼写（实测 slug 就是罗马字去掉空格，
// 但词序/大小写偶有例外，猜错会命中同名别人）。
const cinewhoBase = "https://www.cinewho.com"

type CinewhoClient struct {
	HTTP *http.Client
	Base string
}

func NewCinewhoClient(hc *http.Client) *CinewhoClient {
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	return &CinewhoClient{HTTP: hc}
}

func (c *CinewhoClient) baseURL() string {
	if c.Base != "" {
		return strings.TrimRight(c.Base, "/")
	}
	return cinewhoBase
}

var (
	reCineResult = regexp.MustCompile(`(?is)<a[^>]+href="(?:model|cine)/([^"?/]+)"[^>]*>\s*<h4>(.*?)</h4>`)
	reCineLD     = regexp.MustCompile(`(?is)<script type="application/ld\+json">(.*?)</script>`)
	reCineInfo   = regexp.MustCompile(`(?is)<div class="info-item"><strong>([^<:：]+)[:：]?</strong>(.*?)</div>`)
)

type cineLD struct {
	Type        string   `json:"@type"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	BirthDate   string   `json:"birthDate"`
	Nationality string   `json:"nationality"`
	Height      string   `json:"height"`
	Gender      string   `json:"gender"`
	Image       string   `json:"image"`
	SameAs      []string `json:"sameAs"`
}

// Fetch 依次用候选名搜索，最多试 3 个；命中本人即停。
func (c *CinewhoClient) Fetch(ctx context.Context, candidates []string) (*CNProfile, string) {
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
		q := url.Values{"f": {"_all"}, "m": {"yes"}, "s": {"relevance"}, "q": {cand}}
		body, err := c.get(ctx, c.baseURL()+"/search.php?"+q.Encode())
		if err != nil {
			continue
		}
		slugs := parseCineSlugs(string(body))
		for i, hit := range slugs {
			if i >= 3 {
				break
			}
			// 搜索结果标题要含候选名，才值得再发一次详情请求。
			if !cnNameHit([]string{hit.title}, []string{cand}) && !strings.Contains(NormalizeName(hit.title), key) {
				continue
			}
			detailURL := c.baseURL() + "/cine/" + hit.slug
			detail, err := c.get(ctx, detailURL)
			if err != nil {
				continue
			}
			if p := parseCineDetail(string(detail), detailURL); p != nil &&
				cnNameHit(append([]string{p.ChineseName, p.Romaji}, p.Aliases...), candidates) {
				return p, cand
			}
		}
	}
	return nil, ""
}

func (c *CinewhoClient) get(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", cinewhoUA)
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

const cinewhoUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

type cineHit struct {
	slug  string
	title string
}

func parseCineSlugs(page string) []cineHit {
	var out []cineHit
	seen := map[string]bool{}
	for _, m := range reCineResult.FindAllStringSubmatch(page, -1) {
		slug := strings.TrimSpace(m[1])
		if slug == "" || seen[slug] {
			continue
		}
		seen[slug] = true
		out = append(out, cineHit{slug: slug, title: cnCleanText(m[2])})
	}
	return out
}

func parseCineDetail(page, sourceURL string) *CNProfile {
	var ld *cineLD
	for _, m := range reCineLD.FindAllStringSubmatch(page, -1) {
		var one cineLD
		if err := json.Unmarshal([]byte(strings.TrimSpace(m[1])), &one); err != nil {
			continue
		}
		if strings.EqualFold(one.Type, "Person") && one.Name != "" {
			ld = &one
			break
		}
	}
	if ld == nil {
		return nil
	}

	fields := map[string]string{}
	for _, m := range reCineInfo.FindAllStringSubmatch(page, -1) {
		fields[cnCleanText(m[1])] = cnCleanText(m[2])
	}

	aliases := []string{}
	for _, k := range []string{"中文名", "英文名", "原文名"} {
		if v := fields[k]; v != "" {
			aliases = append(aliases, v)
		}
	}
	aliases = mergeStrings(aliases, []string{ld.Name})
	gender := ""
	if strings.EqualFold(ld.Gender, "Female") || strings.Contains(fields["性别"], "女") {
		gender = "female"
	}

	bust, waist, hip, cup := cnParseMeasurements(fields["三围"])
	return &CNProfile{
		Bio:         strings.TrimSpace(ld.Description),
		ChineseName: firstNonEmpty(fields["中文名"]),
		Romaji:      firstNonEmpty(fields["英文名"]),
		Aliases:     aliases,
		BirthDate:   cnParseDate(firstNonEmpty(ld.BirthDate, fields["生日"])),
		Gender:      gender,
		HeightCm:    cnParseHeight(firstNonEmpty(ld.Height, fields["身高"])),
		Bust:        bust,
		Waist:       waist,
		Hip:         hip,
		Cup:         cup,
		ImageURL:    ld.Image,
		SourceURL:   sourceURL,
	}
}

func (c *CinewhoClient) describe() string {
	return fmt.Sprintf("cinewho(%s)", cinewhoBase)
}
