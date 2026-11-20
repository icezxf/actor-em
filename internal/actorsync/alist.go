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

// AlistClient 只负责登录和读文件（--scan-nfo 用）。
// 头像不再转存到 alist，上传相关的方法已删除。
type AlistClient struct {
	BaseURL string // https://alist.example.com
	User    string
	Pass    string
	HTTP    *http.Client
	token   string
}

func NewAlistClient(baseURL, user, pass string) *AlistClient {
	return &AlistClient{
		BaseURL: strings.TrimRight(baseURL, "/"),
		User:    user,
		Pass:    pass,
		HTTP:    &http.Client{Timeout: 60 * time.Second},
	}
}

func (c *AlistClient) Login(ctx context.Context) error {
	if c.BaseURL == "" {
		return fmt.Errorf("alist 地址未配置")
	}
	body, _ := json.Marshal(map[string]string{"username": c.User, "password": c.Pass})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/auth/login", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("alist 登录失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"message"`
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("alist 登录响应解析失败: %w", err)
	}
	if out.Data.Token == "" {
		return fmt.Errorf("alist 登录失败: %s", out.Msg)
	}
	c.token = out.Data.Token
	return nil
}

func (c *AlistClient) doJSON(ctx context.Context, method, endpoint string, payload any) error {
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.token)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"message"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("alist 响应解析失败: %w (%s)", err, strings.TrimSpace(string(raw[:min(len(raw), 200)])))
	}
	if out.Code != http.StatusOK {
		return fmt.Errorf("alist %s 失败: %d %s", endpoint, out.Code, out.Msg)
	}
	return nil
}

// DownloadImage 下载原图，限制 12MB 防止被超大图拖死。
func DownloadImage(ctx context.Context, hc *http.Client, imageURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Referer", imageURL)

	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("下载头像 HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 12<<20))
	if err != nil {
		return nil, err
	}
	if len(data) < 512 {
		return nil, fmt.Errorf("图片过小（%d 字节），可能不是有效图片", len(data))
	}
	return data, nil
}

// encodePath 逐段 escape，保留 / 分隔符。
func encodePath(p string) string {
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return "/" + strings.Join(parts, "/")
}
