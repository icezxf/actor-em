package actorsync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Translator 封装 DeepL，带内存缓存避免同一段文本重复翻译。
type Translator struct {
	APIKey string
	HTTP   *http.Client
	cache  map[string]string
}

func NewTranslator(apiKey string, hc *http.Client) *Translator {
	return &Translator{APIKey: strings.TrimSpace(apiKey), HTTP: hc, cache: map[string]string{}}
}

// TranslateEN2ZH 把英文译成简体中文。Key 为空或失败时返回原文。
func (t *Translator) TranslateEN2ZH(ctx context.Context, text string) (string, error) {
	if t == nil || t.APIKey == "" {
		return text, fmt.Errorf("未配置 DeepL Key")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", nil
	}
	if v, ok := t.cache[text]; ok {
		return v, nil
	}

	endpoint := "https://api.deepl.com/v2/translate"
	if strings.HasSuffix(t.APIKey, ":fx") {
		endpoint = "https://api-free.deepl.com/v2/translate"
	}

	form := url.Values{}
	form.Set("text", text)
	form.Set("source_lang", "EN")
	form.Set("target_lang", "ZH")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return text, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "DeepL-Auth-Key "+t.APIKey)

	resp, err := t.HTTP.Do(req)
	if err != nil {
		return text, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return text, fmt.Errorf("DeepL HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	var out struct {
		Translations []struct {
			Text string `json:"text"`
		} `json:"translations"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return text, err
	}
	if len(out.Translations) == 0 || out.Translations[0].Text == "" {
		return text, fmt.Errorf("DeepL 返回空译文")
	}

	t.cache[text] = out.Translations[0].Text
	return out.Translations[0].Text, nil
}

// LooksEnglish 判断简介是否"纯英文、没有日文假名"（日文汉字混排的不该送翻译）。
func LooksEnglish(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	hasLatin := false
	for _, r := range s {
		if reKana.MatchString(string(r)) {
			return false
		}
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
			hasLatin = true
		}
		if r >= 0x4E00 && r <= 0x9FFF {
			return false
		}
	}
	return hasLatin
}
