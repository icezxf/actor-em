package actorsync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

// AlistClient 负责登录、建目录、判存在、上传。
type AlistClient struct {
	BaseURL    string // https://alist.example.com
	PublicBase string // 直链前缀，默认等于 BaseURL
	User       string
	Pass       string
	HTTP       *http.Client
	token      string
}

func NewAlistClient(baseURL, publicBase, user, pass string) *AlistClient {
	baseURL = strings.TrimRight(baseURL, "/")
	if publicBase == "" {
		publicBase = baseURL
	}
	return &AlistClient{
		BaseURL:    baseURL,
		PublicBase: strings.TrimRight(publicBase, "/"),
		User:       user,
		Pass:       pass,
		HTTP:       &http.Client{Timeout: 60 * time.Second},
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

func (c *AlistClient) mkdir(ctx context.Context, dir string) error {
	err := c.doJSON(ctx, http.MethodPost, "/api/fs/mkdir", map[string]string{"path": dir})
	if err != nil && strings.Contains(err.Error(), "exist") {
		return nil // 已存在
	}
	return err
}

// exists 判断文件是否已存在。
func (c *AlistClient) exists(ctx context.Context, p string) bool {
	body, _ := json.Marshal(map[string]string{"path": p})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/fs/get", bytes.NewReader(body))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.token)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var out struct {
		Code int `json:"code"`
		Data struct {
			Name  string `json:"name"`
			IsDir bool   `json:"is_dir"`
			Size  int64  `json:"size"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return false
	}
	return out.Code == http.StatusOK && out.Data.Name != "" && !out.Data.IsDir
}

func (c *AlistClient) put(ctx context.Context, p string, data []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.BaseURL+"/api/fs/put", bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", c.token)
	req.Header.Set("File-Path", encodePath(p))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = int64(len(data))

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("alist 上传 HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw[:min(len(raw), 200)])))
	}
	return nil
}

// AvatarResult 是一次头像上传的结果。
type AvatarResult struct {
	Path    string // /actors/小/小凑四叶.jpg
	Dir     string // /actors/小
	URL     string // 完整直链（已 encode）
	SHA256  string
	Bytes   int64
	Width   int
	Height  int
	Skipped bool // 已存在，跳过上传
}

// UploadAvatar 把图片存到 {baseDir}/{首字符}/{演员名}.jpg，返回各种路径。
// force=false 时已存在就跳过（幂等，省流量）。
//
// Deprecated: 头像已改为直接用网络地址，主链路不再调用这里。
// 保留仅为兼容可能仍在用它的外部工具；AlistClient 本身仍供 --scan-nfo 使用。
func (c *AlistClient) UploadAvatar(ctx context.Context, baseDir, actorName string, data []byte, force bool) (*AvatarResult, error) {
	baseDir = "/" + strings.Trim(baseDir, "/")
	bucket := BucketOf(actorName)
	dir := path.Join(baseDir, bucket)
	filename := SanitizeFileName(actorName) + ".jpg"
	fullPath := path.Join(dir, filename)

	res := &AvatarResult{
		Path:   fullPath,
		Dir:    dir,
		URL:    c.PublicBase + "/d" + encodePath(fullPath),
		SHA256: fmt.Sprintf("%x", sha256.Sum256(data)),
		Bytes:  int64(len(data)),
	}
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		res.Width, res.Height = cfg.Width, cfg.Height
	}

	if !force && c.exists(ctx, fullPath) {
		res.Skipped = true
		return res, nil
	}
	if err := c.mkdir(ctx, dir); err != nil {
		return nil, fmt.Errorf("创建目录 %s 失败: %w", dir, err)
	}
	if err := c.put(ctx, fullPath, data); err != nil {
		return nil, err
	}
	return res, nil
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
