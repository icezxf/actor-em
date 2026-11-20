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

// FetchWiki 查维基百科简介。
// targetLang 非空时顺带取跨语言标题（用于「中文名 → 日文名 → 再搜 JavStash」这条链路）。
func FetchWiki(ctx context.Context, hc *http.Client, lang, title, targetLang, email string) (langTitle, actualTitle, bio string) {
	prop := "extracts"
	if targetLang != "" {
		prop = "langlinks|extracts"
	}
	u := fmt.Sprintf(
		"https://%s.wikipedia.org/w/api.php?action=query&titles=%s&prop=%s&exintro=1&explaintext=1&format=json&redirects=1&formatversion=2",
		lang, url.QueryEscape(title), prop,
	)
	if targetLang != "" {
		u += "&lllang=" + targetLang
	}

	if email == "" {
		email = "actor-em@users.noreply.github.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", "", ""
	}
	req.Header.Set("User-Agent", fmt.Sprintf("actor-em/1.0 (https://github.com/icezxf/actor-em; %s)", email))

	resp, err := hc.Do(req)
	if err != nil {
		return "", "", ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", ""
	}

	var result struct {
		Query struct {
			Pages []struct {
				Title     string `json:"title"`
				Extract   string `json:"extract"`
				LangLinks []struct {
					Title string `json:"title"`
				} `json:"langlinks"`
			} `json:"pages"`
		} `json:"query"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || len(result.Query.Pages) == 0 {
		return "", "", ""
	}

	p := result.Query.Pages[0]
	actualTitle = p.Title
	bio = strings.TrimSpace(p.Extract)
	if len(p.LangLinks) > 0 {
		langTitle = regexp.MustCompile(`\s*\(.*?\)`).ReplaceAllString(p.LangLinks[0].Title, "")
		langTitle = strings.TrimSpace(langTitle)
	}
	return langTitle, actualTitle, bio
}

// fetchWikiBoth 一次拿到「日文标题 + 中文简介」。
//
// 目标是中文简介（英文/日文都不要），但同时需要日文标题去搜 JavStash：
//   - 输入是中文：查中文维基，正文即简介，跨语言链接给日文标题
//   - 输入是日文：查日文维基拿中文标题，再查中文维基拿简介
func fetchWikiBoth(ctx context.Context, hc *http.Client, name, lang, email string) (jaTitle, zhBio string) {
	switch lang {
	case "zh":
		ja, _, bio := FetchWiki(ctx, hc, "zh", name, "ja", email)
		return ja, bio
	case "ja":
		zhTitle, _, _ := FetchWiki(ctx, hc, "ja", name, "zh", email)
		if zhTitle != "" {
			if _, _, bio := FetchWiki(ctx, hc, "zh", zhTitle, "", email); bio != "" {
				return name, bio
			}
		}
		return name, ""
	default:
		_, _, bio := FetchWiki(ctx, hc, "zh", name, "ja", email)
		return "", bio
	}
}
