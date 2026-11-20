package actorsync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// IdolDBClient 读 MagicBude/jav-idol-db 仓库里的女优 profile.json。
//
// 这个项目的价值不在数量（实测 114 份档案），而在结构规整：
//   - 目录名就是日文原名，按名字精确取文件，几乎不会串人
//   - 16 份有中文简介，格式是完整的经历描述
//   - name_zh / aliases_zh / roman_name / birthdate / 三围 / 出道 / status 都能补空位
//
// 数据以公开元数据为主、图片只做热链；这里只把 profile 当补充源，
// 头像仍由 gfriends 优先决定，生日仍以 JavStash 为准。
const idolDBRawBase = "https://raw.githubusercontent.com/MagicBude/jav-idol-db/main/data/actresses/"

type IdolDBClient struct {
	HTTP *http.Client
	Base string // 测试可覆盖，需以 / 结尾
}

func NewIdolDBClient(hc *http.Client) *IdolDBClient {
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	return &IdolDBClient{HTTP: hc}
}

func (c *IdolDBClient) baseURL() string {
	if c.Base != "" {
		return c.Base
	}
	return idolDBRawBase
}

type idolDBProfile struct {
	Name       string   `json:"name"`
	NameZH     string   `json:"name_zh"`
	RomanName  string   `json:"roman_name"`
	Aliases    []string `json:"aliases"`
	AliasesZH  []string `json:"aliases_zh"`
	Birthdate  string   `json:"birthdate"`
	Birthplace string   `json:"birthplace"`
	Height     int      `json:"height"`
	Bust       int      `json:"bust"`
	Waist      int      `json:"waist"`
	Hips       int      `json:"hips"`
	Cup        string   `json:"cup"`
	DebutDate  string   `json:"debut_date"`
	Career     string   `json:"career_periods"`
	Bio        string   `json:"bio"`
	Avatar     string   `json:"avatar"`
	PhotoURL   string   `json:"photo_url"`
	Status     string   `json:"status"`
	StatusSrc  string   `json:"status_source"`
}

// Fetch 按候选名试 profile.json，最多试 3 个，命中本人即返回。
func (c *IdolDBClient) Fetch(ctx context.Context, candidates []string) (*CNProfile, string) {
	if c == nil {
		return nil, ""
	}
	tried := map[string]bool{}
	for _, name := range candidates {
		name = strings.TrimSpace(name)
		key := NormalizeName(name)
		if key == "" || tried[key] || IsUUID(name) || strings.ContainsAny(name, "/\\") {
			continue
		}
		tried[key] = true
		if len(tried) > 3 {
			break
		}
		raw, ok := c.fetchOne(ctx, name)
		if !ok {
			continue
		}
		var p idolDBProfile
		if err := json.Unmarshal(raw, &p); err != nil {
			continue
		}
		siteNames := append([]string{p.Name, p.NameZH, p.RomanName}, p.Aliases...)
		siteNames = append(siteNames, p.AliasesZH...)
		if !cnNameHit(siteNames, candidates) {
			continue
		}
		out := &CNProfile{
			Bio:         strings.TrimSpace(p.Bio),
			ChineseName: strings.TrimSpace(p.NameZH),
			Romaji:      strings.TrimSpace(p.RomanName),
			Aliases:     mergeStrings(p.Aliases, p.AliasesZH),
			BirthDate:   cnParseDate(p.Birthdate),
			BirthPlace:  strings.TrimSpace(p.Birthplace),
			HeightCm:    p.Height,
			CareerStart: cnParseYear(p.DebutDate),
			Bust:        p.Bust,
			Waist:       p.Waist,
			Hip:         p.Hips,
			Cup:         strings.ToUpper(strings.TrimSpace(p.Cup)),
			ImageURL:    firstNonEmpty(p.Avatar, p.PhotoURL),
			SourceURL:   c.baseURL() + url.PathEscape(name) + "/profile.json",
		}
		if out.CareerStart == 0 {
			out.CareerStart = cnParseYear(p.Career)
		}
		// status / status_source 只用来判断档案质量，不写进 Emby 的状态字段。
		return out, name
	}
	return nil, ""
}

func (c *IdolDBClient) fetchOne(ctx context.Context, name string) ([]byte, bool) {
	u := c.baseURL() + url.PathEscape(name) + "/profile.json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("User-Agent", "actor-em/1.0 (+actor profile enrichment)")
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	if err != nil {
		return nil, false
	}
	return body, true
}

func (c *IdolDBClient) describe() string {
	return fmt.Sprintf("jav-idol-db(%s)", idolDBRawBase)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
