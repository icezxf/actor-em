package actorsync

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
)

// FsItem 是 alist 目录里的一项。
type FsItem struct {
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
}

// List 列目录，自动翻页。
func (c *AlistClient) List(ctx context.Context, dir string) ([]FsItem, error) {
	const perPage = 200
	var out []FsItem

	for page := 1; ; page++ {
		payload := map[string]any{
			"path":     dir,
			"password": "",
			"page":     page,
			"per_page": perPage,
			"refresh":  false,
		}
		body, _ := json.Marshal(payload)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/fs/list", strings.NewReader(string(body)))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", c.token)

		resp, err := c.HTTP.Do(req)
		if err != nil {
			return nil, fmt.Errorf("列目录 %s 失败: %w", dir, err)
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()

		var outResp struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    struct {
				Content []FsItem `json:"content"`
				Total   int      `json:"total"`
			} `json:"data"`
		}
		if err := json.Unmarshal(raw, &outResp); err != nil {
			return nil, fmt.Errorf("解析目录 %s 失败: %w", dir, err)
		}
		if outResp.Code != http.StatusOK {
			return nil, fmt.Errorf("列目录 %s 失败: %d %s", dir, outResp.Code, outResp.Message)
		}

		out = append(out, outResp.Data.Content...)
		if len(outResp.Data.Content) < perPage || len(out) >= outResp.Data.Total {
			break
		}
	}
	return out, nil
}

// ReadFile 读 alist 上的文件内容，限制 4MB。
func (c *AlistClient) ReadFile(ctx context.Context, p string) ([]byte, error) {
	payload := map[string]any{"path": p, "password": ""}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/fs/get", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.token)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()

	var meta struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Name   string `json:"name"`
			Size   int64  `json:"size"`
			RawURL string `json:"raw_url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil, fmt.Errorf("解析 %s 元信息失败: %w", p, err)
	}
	if meta.Code != http.StatusOK || meta.Data.RawURL == "" {
		return nil, fmt.Errorf("取 %s 直链失败: %d %s", p, meta.Code, meta.Message)
	}
	if meta.Data.Size > 4<<20 {
		return nil, fmt.Errorf("%s 超过 4MB，跳过", p)
	}

	rawURL := meta.Data.RawURL
	if !strings.HasPrefix(rawURL, "http") {
		rawURL = strings.TrimRight(c.BaseURL, "/") + "/" + strings.TrimLeft(rawURL, "/")
	}

	freq, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	freq.Header.Set("Authorization", c.token)
	fresp, err := c.HTTP.Do(freq)
	if err != nil {
		return nil, err
	}
	defer fresp.Body.Close()
	if fresp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("下载 %s HTTP %d", p, fresp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(fresp.Body, 4<<20))
}

// nfoRoot 只关心 actor 下的 name，避免误取 <movie><name>（片名）。
type nfoRoot struct {
	XMLName xml.Name
	Actors  []struct {
		Name string `xml:"name"`
	} `xml:"actor"`
}

// ParseNFOActors 从 nfo 内容里取出演员名。
func ParseNFOActors(data []byte) []string {
	var root nfoRoot
	if err := xml.Unmarshal(data, &root); err != nil {
		return nil
	}
	var names []string
	for _, a := range root.Actors {
		n := strings.TrimSpace(a.Name)
		if n != "" {
			names = append(names, n)
		}
	}
	return names
}

// ScanStats 是 NFO 扫描的统计。
type ScanStats struct {
	Dirs      int
	NFOCount  int
	ParseFail int
	Names     int
}

// ScanNFODirectory 递归扫描 alist 目录，从所有 .nfo 里收集演员名。
// maxFiles <= 0 表示不限制（但仍然受 ctx 超时约束）。
func ScanNFODirectory(ctx context.Context, c *AlistClient, root string, maxFiles int, progress func(ScanStats)) ([]string, ScanStats, error) {
	root = "/" + strings.Trim(root, "/")
	seen := map[string]bool{}
	var names []string
	var stats ScanStats

	var walk func(dir string, depth int) error
	walk = func(dir string, depth int) error {
		if depth > 12 {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		items, err := c.List(ctx, dir)
		if err != nil {
			// 单个目录失败不中断整轮
			return nil
		}
		stats.Dirs++

		for _, it := range items {
			if strings.HasPrefix(it.Name, ".") || strings.EqualFold(it.Name, "$RECYCLE.BIN") {
				continue
			}
			p := path.Join(dir, it.Name)
			if it.IsDir {
				if err := walk(p, depth+1); err != nil {
					return err
				}
				continue
			}
			if !strings.HasSuffix(strings.ToLower(it.Name), ".nfo") {
				continue
			}
			if maxFiles > 0 && stats.NFOCount >= maxFiles {
				return nil
			}
			data, err := c.ReadFile(ctx, p)
			if err != nil {
				stats.ParseFail++
				continue
			}
			stats.NFOCount++
			found := ParseNFOActors(data)
			if len(found) == 0 {
				// 有些 nfo 是完整的 xml 但用 <actor><name> 以外的写法，
				// 这里不报错，只统计
				continue
			}
			for _, n := range found {
				key := NormalizeName(n)
				if key == "" || seen[key] {
					continue
				}
				seen[key] = true
				names = append(names, n)
			}
			if progress != nil && stats.NFOCount%50 == 0 {
				progress(stats)
			}
		}
		return nil
	}

	err := walk(root, 0)
	stats.Names = len(names)
	return names, stats, err
}
