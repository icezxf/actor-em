package actorsync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// EmbyClient 只做三件事：登录、拉人物清单、拿人物详情（可选）。
type EmbyClient struct {
	BaseURL string
	User    string
	Pass    string
	APIKey  string
	HTTP    *http.Client

	token string
}

func NewEmbyClient(baseURL, user, pass, apiKey string) *EmbyClient {
	return &EmbyClient{
		BaseURL: strings.TrimRight(baseURL, "/"),
		User:    user,
		Pass:    pass,
		APIKey:  apiKey,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *EmbyClient) authHeader() string {
	return `MediaBrowser Client="actor-em", Device="cli", DeviceId="actor-em-cli", Version="1.0.0"`
}

// Login 用用户名密码换 AccessToken。配了 APIKey 且登录失败时降级用 Key。
func (c *EmbyClient) Login(ctx context.Context) error {
	if c.APIKey != "" && (c.User == "" || c.Pass == "") {
		return nil // 只配了 APIKey，跳过登录
	}

	body, _ := json.Marshal(map[string]string{"Username": c.User, "Pw": c.Pass})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/emby/Users/AuthenticateByName", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Emby-Authorization", c.authHeader())

	resp, err := c.HTTP.Do(req)
	if err != nil {
		if c.APIKey != "" {
			return nil // 网络问题，让 APIKey 兜底
		}
		return fmt.Errorf("Emby 登录请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if c.APIKey != "" {
			return nil // 2FA 账号等场景，降级到 APIKey
		}
		return fmt.Errorf("Emby 登录失败 HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	var out struct {
		AccessToken string `json:"AccessToken"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fmt.Errorf("解析登录响应失败: %w", err)
	}
	if out.AccessToken == "" {
		return fmt.Errorf("Emby 登录成功但没有返回 AccessToken")
	}
	c.token = out.AccessToken
	return nil
}

func (c *EmbyClient) credential() string {
	if c.token != "" {
		return c.token
	}
	return c.APIKey
}

// EmbyPerson 是 /emby/Persons 返回的条目。
type EmbyPerson struct {
	ID           string            `json:"Id"`
	Name         string            `json:"Name"`
	SortName     string            `json:"SortName"`
	Overview     string            `json:"Overview"`
	PremiereDate string            `json:"PremiereDate"`
	EndDate      string            `json:"EndDate"`
	ProviderIDs  map[string]string `json:"ProviderIds"`
}

// ListPersons 分页拉全量人物。
// 关键点：Fields 里带 Overview/ProviderIds/PremiereDate，一次就拿到 Emby 当前状态，
// 增量判断不用再逐个人物请求详情。
func (c *EmbyClient) ListPersons(ctx context.Context) ([]EmbyPerson, error) {
	const pageSize = 500
	fields := "ProviderIds,Overview,PremiereDate,EndDate,SortName"

	var all []EmbyPerson
	start := 0
	for {
		q := url.Values{}
		q.Set("Recursive", "true")
		q.Set("Fields", fields)
		q.Set("StartIndex", fmt.Sprint(start))
		q.Set("Limit", fmt.Sprint(pageSize))
		endpoint := c.BaseURL + "/emby/Persons?" + q.Encode()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("X-Emby-Token", c.credential())
		req.Header.Set("Accept", "application/json")

		resp, err := c.HTTP.Do(req)
		if err != nil {
			return nil, fmt.Errorf("拉取人物清单失败: %w", err)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("拉取人物清单 HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body[:min(len(body), 300)])))
		}

		var page struct {
			Items            []EmbyPerson `json:"Items"`
			TotalRecordCount int          `json:"TotalRecordCount"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("解析人物清单失败: %w", err)
		}

		all = append(all, page.Items...)
		start += len(page.Items)

		if len(page.Items) == 0 || start >= page.TotalRecordCount {
			break
		}
	}
	return all, nil
}
