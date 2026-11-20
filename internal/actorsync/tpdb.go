package actorsync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// TPDBClient 对接 theporndb.net。搜索列表只返回 ID，需要再拉一次详情。
type TPDBClient struct {
	Endpoint string
	APIKey   string
	HTTP     *http.Client
}

func NewTPDBClient(endpoint, apiKey string) *TPDBClient {
	if endpoint == "" {
		endpoint = "https://api.theporndb.net/performers"
	}
	return &TPDBClient{
		Endpoint: endpoint,
		APIKey:   strings.TrimSpace(apiKey),
		HTTP:     &http.Client{Timeout: 30 * time.Second},
	}
}

type tpdbDetail struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Bio     string   `json:"bio"`
	Image   string   `json:"image"`
	Aliases []string `json:"aliases"`
	Extras  struct {
		Birthday       string `json:"birthday"`
		BirthplaceCode string `json:"birthplace_code"`
		Height         string `json:"height"`
		Weight         string `json:"weight"`
		Measurements   string `json:"measurements"`
		CupSize        string `json:"cupsize"`
		Waist          string `json:"waist"`
		Hips           string `json:"hips"`
		CareerStart    int    `json:"career_start_year"`
		Ethnicity      string `json:"ethnicity"`
		Gender         string `json:"gender"`
	} `json:"extras"`
	Links  map[string]string `json:"links"`
	Images []struct {
		URL string `json:"url"`
	} `json:"images"`
}

func (c *TPDBClient) Search(ctx context.Context, term string) (*Performer, error) {
	if c.APIKey == "" {
		return nil, fmt.Errorf("TPDB: API Key 未配置")
	}
	term = strings.TrimSpace(term)
	if term == "" {
		return nil, fmt.Errorf("TPDB: 空关键词")
	}

	isUUID := IsUUID(term)
	id := term

	if !isUUID {
		searchURL := c.Endpoint + "?q=" + url.QueryEscape(term)
		raw, err := c.get(ctx, searchURL)
		if err != nil {
			return nil, err
		}
		var list struct {
			Data []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"data"`
		}
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, fmt.Errorf("TPDB 搜索响应解析失败: %w", err)
		}
		if len(list.Data) == 0 {
			return nil, fmt.Errorf("%w: TPDB 未找到演员", ErrNotFound)
		}
		// 优先取名字匹配的，取不到才退回第一条（并继续用详情页再校验一次）
		id = list.Data[0].ID
		sTerm := NormalizeName(term)
		for _, item := range list.Data {
			if NormalizeName(item.Name) == sTerm {
				id = item.ID
				break
			}
		}
	}

	raw, err := c.get(ctx, c.Endpoint+"/"+id)
	if err != nil {
		return nil, err
	}
	var detail struct {
		Data tpdbDetail `json:"data"`
	}
	if err := json.Unmarshal(raw, &detail); err != nil {
		return nil, fmt.Errorf("TPDB 详情解析失败: %w", err)
	}
	d := detail.Data
	if d.ID == "" {
		return nil, fmt.Errorf("TPDB 返回空数据")
	}

	// 名字校验：防止短名/同名被错误匹配
	if !isUUID {
		if matchScore(term, d.Name, d.Aliases) <= 0 {
			return nil, fmt.Errorf("%w: TPDB 返回了无关演员: %s", ErrNotFound, d.Name)
		}
	}

	p := &Performer{
		Source:      "tpdb",
		ID:          d.ID,
		Name:        d.Name,
		Aliases:     d.Aliases,
		BirthDate:   normalizeDate(d.Extras.Birthday),
		Country:     strings.ToUpper(strings.TrimSpace(d.Extras.BirthplaceCode)),
		Ethnicity:   d.Extras.Ethnicity,
		Gender:      d.Extras.Gender,
		Bio:         strings.TrimSpace(d.Bio),
		CareerStart: d.Extras.CareerStart,
	}
	p.Height = parseHeight(d.Extras.Height)
	p.Weight = parseWeight(d.Extras.Weight)
	p.Measurements = strings.TrimSpace(d.Extras.Measurements)
	// TPDB 的 cupsize 是 "34E"（band+cup），只留字母部分
	p.CupSize = extractCupLetter(d.Extras.CupSize)
	if p.CupSize == "" {
		p.CupSize = cupFromMeasurements(p.Measurements)
	}
	p.Waist, _ = strconv.Atoi(strings.TrimSpace(d.Extras.Waist))
	p.Hip, _ = strconv.Atoi(strings.TrimSpace(d.Extras.Hips))

	if d.Image != "" {
		p.Images = append(p.Images, d.Image)
	}
	for _, img := range d.Images {
		if img.URL != "" && img.URL != d.Image {
			p.Images = append(p.Images, img.URL)
		}
	}
	if len(p.Images) > 0 {
		p.ImageURL = p.Images[0]
	}

	for k, v := range d.Links {
		if v != "" {
			p.URLs = append(p.URLs, v)
		}
		_ = k
	}
	p.URLs = append(p.URLs, "https://theporndb.net/performers/"+d.ID)

	return p, nil
}

func (c *TPDBClient) get(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("User-Agent", "actor-em/1.0")
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("TPDB 请求失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%w: TPDB 404 未找到", ErrNotFound)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("TPDB HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw[:min(len(raw), 300)])))
	}
	return raw, nil
}

// parseHeight 兼容 "160cm" 和 "5'4\""。
func parseHeight(s string) int {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return 0
	}
	if strings.Contains(s, "cm") {
		n, _ := strconv.Atoi(strings.TrimSpace(strings.ReplaceAll(s, "cm", "")))
		return n
	}
	if strings.Contains(s, "'") {
		parts := strings.Split(s, "'")
		feet, _ := strconv.Atoi(strings.TrimSpace(parts[0]))
		inches := 0
		if len(parts) > 1 {
			inches, _ = strconv.Atoi(strings.Trim(strings.TrimSpace(parts[1]), `"`))
		}
		return int(math.Round(float64(feet*12+inches) * 2.54))
	}
	n, _ := strconv.Atoi(s)
	return n
}

// parseWeight 兼容 "45kg" 和 "99lb"。
func parseWeight(s string) int {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return 0
	}
	if strings.Contains(s, "kg") {
		n, _ := strconv.Atoi(strings.TrimSpace(strings.ReplaceAll(s, "kg", "")))
		return n
	}
	if strings.Contains(s, "lb") {
		n, _ := strconv.Atoi(strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "lbs", ""), "lb", "")))
		return int(math.Round(float64(n) * 0.453592))
	}
	n, _ := strconv.Atoi(s)
	return n
}

// extractCupLetter 从 "34E" / "E" 这类值里取出罩杯字母。
// TPDB 的 cupsize 是「下胸围+罩杯」的写法，直接存会在库里出现 "34E"。
func extractCupLetter(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if r >= 'A' && r <= 'P' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// cupFromMeasurements 从 "34E-21-31" 里取第二段 E。
func cupFromMeasurements(m string) string {
	parts := strings.Split(strings.TrimSpace(m), "-")
	if len(parts) < 2 {
		return ""
	}
	return extractCupLetter(parts[1])
}

// normalizeDate 把 1996/5/10、1996-5-10、1996-05-10T00:00:00 统一成 1996-05-10。
func normalizeDate(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if i := strings.IndexAny(s, "T "); i > 0 {
		s = s[:i]
	}
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, ".", "-")
	parts := strings.Split(s, "-")
	if len(parts) != 3 {
		return ""
	}
	y, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	d, err3 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || err3 != nil || y < 1800 || m < 1 || m > 12 || d < 1 || d > 31 {
		return ""
	}
	return fmt.Sprintf("%04d-%02d-%02d", y, m, d)
}
