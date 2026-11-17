package actorsync

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// OuxiangData 来自 ouxiangdanku.com，主要价值是中文简介和中文名。
type OuxiangData struct {
	JpName string
	ZhName string
	Bio    string
	Weight int
	URL    string
}

var (
	ouxIDRe      = regexp.MustCompile(`href="(/idol/[^"]+)"`)
	ouxH1Re      = regexp.MustCompile(`<h1>([^<]+)</h1>`)
	ouxIntroRe   = regexp.MustCompile(`(?s)<section id="intro"><p>(.*?)</p>`)
	ouxCnNameRe  = regexp.MustCompile(`(?s)<tr[^>]*>\s*<td[^>]*>\s*中文名\s*</td>\s*<td[^>]*>\s*([^<]+?)\s*</td>`)
	ouxWeightRe  = regexp.MustCompile(`体重[^\d]*(\d+)`)
	ouxSplitRe   = regexp.MustCompile(`[，,\n\r]`)
	ouxBrRe      = regexp.MustCompile(`(?i)<br\s*/?>`)
	reKanaFilter = regexp.MustCompile(`[\p{Hiragana}\p{Katakana}]`)
)

// FetchOuxiang 搜名字 → 进详情页 → 严格匹配后取简介/中文名/体重。
// 站点结构随时可能改，失败就返回 error，调用方跳过即可。
func FetchOuxiang(ctx context.Context, hc *http.Client, keyword string) (*OuxiangData, error) {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return nil, fmt.Errorf("空关键词")
	}
	normKeyword := NormalizeName(keyword)

	searchURL := "https://www.ouxiangdanku.com/search/" + url.PathEscape(keyword)
	body, err := httpGet(ctx, hc, searchURL)
	if err != nil {
		return nil, err
	}

	matches := ouxIDRe.FindAllStringSubmatch(body, 5)
	if len(matches) == 0 {
		return nil, fmt.Errorf("%w: 未搜到结果", ErrNotFound)
	}

	for _, m := range matches {
		detailURL := "https://www.ouxiangdanku.com" + m[1]
		html, err := httpGet(ctx, hc, detailURL)
		if err != nil {
			continue
		}

		h1 := ""
		if hm := ouxH1Re.FindStringSubmatch(html); len(hm) >= 2 {
			h1 = strings.TrimSpace(hm[1])
		}
		if h1 == "" {
			continue
		}

		// 详情页把字段塞在表格里，先切成候选词池，再做严格完全匹配
		clean := ouxBrRe.ReplaceAllString(html, ",")
		clean = strings.ReplaceAll(clean, "</td>", ",")
		clean = CleanHTML(clean)

		candidates := []string{h1}
		for _, part := range ouxSplitRe.Split(clean, -1) {
			p := strings.TrimSpace(part)
			if len([]rune(p)) >= 2 && len([]rune(p)) <= 20 &&
				!strings.ContainsAny(p, "0123456789年月日") &&
				!strings.ContainsAny(p, "：:") {
				candidates = append(candidates, p)
			}
		}

		matched := false
		for _, c := range candidates {
			if NormalizeName(c) == normKeyword {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}

		data := &OuxiangData{JpName: h1, URL: detailURL}

		if im := ouxIntroRe.FindStringSubmatch(html); len(im) >= 2 {
			data.Bio = strings.TrimSpace(CleanHTML(im[1]))
		}
		if m := ouxCnNameRe.FindStringSubmatch(html); len(m) >= 2 {
			c := strings.TrimSpace(CleanHTML(m[1]))
			if IsChineseName(c) {
				data.ZhName = c
			}
		}
		if data.ZhName == "" {
			for _, c := range candidates {
				if IsChineseName(c) {
					data.ZhName = c
					break
				}
			}
		}
		if data.ZhName == "" {
			data.ZhName = h1
		}
		if wm := ouxWeightRe.FindStringSubmatch(CleanHTML(html)); len(wm) >= 2 {
			data.Weight, _ = strconv.Atoi(wm[1])
		}
		return data, nil
	}
	return nil, fmt.Errorf("%w: 未找到完全匹配的详情页", ErrNotFound)
}

// IsChineseName 判断是否纯中文（不含假名/拉丁字母）。
func IsChineseName(s string) bool {
	hasHan := false
	for _, r := range s {
		if reKanaFilter.MatchString(string(r)) {
			return false
		}
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
			return false
		}
		if r >= 0x4E00 && r <= 0x9FFF {
			hasHan = true
		}
	}
	return hasHan
}

func httpGet(ctx context.Context, hc *http.Client, u string) (string, error) {
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,ja;q=0.8")

	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	return string(b), nil
}
