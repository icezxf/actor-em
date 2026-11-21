package actorsync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Performer 是各数据源归一化后的演员信息。
type Performer struct {
	Source       string // javstash / stashdb / tpdb
	ID           string
	Name         string
	Aliases      []string
	BirthDate    string
	Country      string
	Ethnicity    string
	Gender       string
	Height       int
	Weight       int
	Measurements string // 仅 TPDB：英寸串，第一段可能是胸围也可能是「下胸围+杯」
	CupSize      string
	Band         int // 下胸围（英寸），JavStash 有，用来拼 34E-24-35
	Waist        int
	Hip          int
	CareerStart  int
	Bio          string
	ImageURL     string
	Images       []string
	URLs         []string // 外部链接，用于提取 provider ids
}

// StashBoxClient 同时适配 JavStash 和 StashDB（两者都是 stash-box GraphQL）。
type StashBoxClient struct {
	Source   string
	Endpoint string
	APIKey   string
	HTTP     *http.Client
}

func NewStashBoxClient(source, endpoint, apiKey string) *StashBoxClient {
	return &StashBoxClient{
		Source:   source,
		Endpoint: endpoint,
		APIKey:   strings.TrimSpace(apiKey),
		HTTP:     &http.Client{Timeout: 30 * time.Second},
	}
}

// JavStash 和 StashDB 现在是同一套 stash-box schema：三围都是扁平字段
// cup_size(String) / band_size(Int) / waist_size(Int) / hip_size(Int)，
// 旧的嵌套 measurements 和 weight 两个站都已经删了。
// GraphQL 查询里带上任何不存在的字段会整体校验失败，所以只能写当前 schema 的字段。
const stashGraphQLFields = `
	id name aliases birth_date country ethnicity height gender career_start_year
	cup_size waist_size hip_size band_size breast_type
	images { url }
	urls { url }
`

type stashPerformerJSON struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Aliases     []string `json:"aliases"`
	BirthDate   string   `json:"birth_date"`
	Country     string   `json:"country"`
	Ethnicity   string   `json:"ethnicity"`
	Height      int      `json:"height"`
	Weight      int      `json:"weight"`
	Gender      string   `json:"gender"`
	CareerStart int      `json:"career_start_year"`
	// 三围：两个站都是扁平字段（JavStash 页面上的「34E-24-35」就来自这四项）
	CupSize    string `json:"cup_size"`
	WaistSize  int    `json:"waist_size"`
	HipSize    int    `json:"hip_size"`
	BandSize   int    `json:"band_size"`
	BreastType string `json:"breast_type"`
	Images     []struct {
		URL string `json:"url"`
	} `json:"images"`
	URLs []struct {
		URL string `json:"url"`
	} `json:"urls"`
}

// queryFor 按关键词拼 GraphQL 语句：UUID 直查，名字走 searchPerformer。
// 单独拆出来是为了能离线断言字段清单（带错字段会让整条查询失败）。
func (c *StashBoxClient) queryFor(term string) (string, map[string]any) {
	vars := map[string]any{}
	if IsUUID(term) {
		vars["id"] = term
		return fmt.Sprintf(`query Find($id: ID!) { findPerformer(id: $id) { %s } }`, stashGraphQLFields), vars
	}
	vars["term"] = term
	return fmt.Sprintf(`query Search($term: String!) { searchPerformer(term: $term) { %s } }`, stashGraphQLFields), vars
}

// Search 按名字或 UUID 查询；名字查询时做严格匹配，避免短名误配。
func (c *StashBoxClient) Search(ctx context.Context, term string) (*Performer, error) {
	if c.APIKey == "" {
		return nil, fmt.Errorf("%s: API Key 未配置", c.Source)
	}
	term = strings.TrimSpace(term)
	if term == "" {
		return nil, fmt.Errorf("%s: 空关键词", c.Source)
	}

	query, vars := c.queryFor(term)

	body, _ := json.Marshal(map[string]any{"query": query, "variables": vars})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("ApiKey", c.APIKey)
	req.Header.Set("User-Agent", "actor-em/1.0")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s 请求失败: %w", c.Source, err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s HTTP %d: %s", c.Source, resp.StatusCode, strings.TrimSpace(string(raw[:min(len(raw), 300)])))
	}

	var envelope struct {
		Data struct {
			FindPerformer   *stashPerformerJSON  `json:"findPerformer"`
			SearchPerformer []stashPerformerJSON `json:"searchPerformer"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("%s 响应解析失败: %w", c.Source, err)
	}
	if len(envelope.Errors) > 0 {
		return nil, fmt.Errorf("%s 接口报错: %s", c.Source, envelope.Errors[0].Message)
	}

	var hit *stashPerformerJSON
	if IsUUID(term) {
		hit = envelope.Data.FindPerformer
		if hit == nil {
			return nil, fmt.Errorf("%w: %s 未找到该 ID", ErrNotFound, c.Source)
		}
	} else {
		// 严格匹配：名字或别名必须互相包含，且优先取完全相等的
		best := -1
		for i := range envelope.Data.SearchPerformer {
			p := &envelope.Data.SearchPerformer[i]
			score := matchScore(term, p.Name, p.Aliases)
			if score > best {
				best = score
				hit = p
			}
			if score == 100 {
				break
			}
		}
		if hit == nil || best <= 0 {
			return nil, fmt.Errorf("%w: %s 未找到匹配的演员", ErrNotFound, c.Source)
		}
	}

	return toPerformer(c.Source, hit), nil
}

// matchScore: 100 完全相等，95 词序颠倒后相等，80 互相包含，0 不匹配。
//
// 「词序颠倒」这条不能省：日文名的罗马字常写成「姓 名」，而欧美库（TPDB）写「名 姓」，
// 三上悠亜 就是 MDCx 给 "Mikami Yua"、TPDB 存 "Yua Mikami"，只比字符串会误判成两个人。
func matchScore(term, name string, aliases []string) int {
	nt := NormalizeName(term)
	if NormalizeName(name) == nt {
		return 100
	}
	for _, a := range aliases {
		if NormalizeName(a) == nt {
			return 95
		}
	}
	if wordBag(name) != "" && wordBag(name) == wordBag(term) {
		return 95
	}
	for _, a := range aliases {
		if wordBag(a) != "" && wordBag(a) == wordBag(term) {
			return 90
		}
	}
	n := NormalizeName(name)
	if strings.Contains(n, nt) || strings.Contains(nt, n) {
		return 80
	}
	for _, a := range aliases {
		an := NormalizeName(a)
		if strings.Contains(an, nt) || strings.Contains(nt, an) {
			return 70
		}
	}
	return 0
}

// wordBag 把名字拆成单词后排序，用来做「不看词序」的比较。
func wordBag(s string) string {
	f := strings.Fields(strings.ToLower(strings.TrimSpace(s)))
	if len(f) == 0 {
		return ""
	}
	sort.Strings(f)
	return strings.Join(f, " ")
}

func toPerformer(source string, p *stashPerformerJSON) *Performer {
	out := &Performer{
		Source:      source,
		ID:          p.ID,
		Name:        p.Name,
		Aliases:     p.Aliases,
		BirthDate:   normalizeDate(p.BirthDate),
		Country:     p.Country,
		Ethnicity:   p.Ethnicity,
		Gender:      p.Gender,
		Height:      p.Height,
		Weight:      p.Weight,
		CupSize:     p.CupSize,
		Band:        p.BandSize,
		Waist:       p.WaistSize,
		Hip:         p.HipSize,
		CareerStart: p.CareerStart,
	}
	for _, img := range p.Images {
		out.Images = append(out.Images, img.URL)
	}
	if len(out.Images) > 0 {
		out.ImageURL = out.Images[0]
	}
	for _, u := range p.URLs {
		out.URLs = append(out.URLs, u.URL)
	}
	return out
}
