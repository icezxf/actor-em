package actorsync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

// Gfriends 是 gfriends/gfriends 的头像库索引。
//
// 目录结构：Content/<类别>/<演员名>.jpg，一个演员可能在多个类别里各有一张，
// 所以「选哪张」需要一个确定的顺序，见 gfriendsDirOrder。
//
// 索引里有两个坑，都是实测出来的：
//
//  1. Filetree.json 的顶层是 {Content, Information}，Information 里是数字，
//     不能直接反序列化成 map[string]map[string]map[string]string。
//
//  2. Content/<类别> 下面 key 和 value 的配对是错的（实测 36.8% 对不上，
//     例如 key 是 桃乃木かな.jpg 而 value 指向 AI-Fix-松嶋真麻.jpg）。
//     但同一个目录里 key 集合和 value 集合完全相同——也就是说
//     「这个目录里存在哪些文件」是可信的，只有「哪个文件对应哪个名字」是坏的。
//     所以这里只拿 value 建「实际存在的文件名」集合，完全不用 key 做映射，
//     既不会张冠李戴，也不会拼出 404。
//
// value 形如 "AI-Fix-桃乃木かな.jpg?t=1607433425"，?t= 只是缓存戳，要去掉。
const (
	gfriendsRawBase = "https://raw.githubusercontent.com/gfriends/gfriends/master/Content/"

	gfriendsFixPrefix = "AI-Fix-"
)

// 索引有 6MB，raw.githubusercontent 国内经常连不上，退到 jsDelivr。
var gfriendsIndexURLs = []string{
	"https://raw.githubusercontent.com/gfriends/gfriends/master/Filetree.json",
	"https://cdn.jsdelivr.net/gh/gfriends/gfriends@master/Filetree.json",
}

type Gfriends struct {
	// 目录 → 演员名(去扩展名) → 该目录里实际存在的文件名（非 AI-Fix 优先）
	index map[string]map[string]string
	// 目录按优先级排好，查找时按这个顺序命中
	dirs []string
	// 条目总数，仅用于启动日志
	count int
}

// 目录优先级。前面是实测过的：
//
//	0-Hand-Storage 精选库，实测 1235x1851
//	y-Minnano      覆盖 58%，实测 1049x1372
//	9-Javrave / 8-Warashi 是 250x300 缩略图，压到后面
//
// 没列到的按条目数从多到少垫底（条目多的目录命中率高）。
var gfriendsDirOrder = []string{
	"0-Hand-Storage",
	"y-Minnano",
	"z-DMM(骑)",
	"z-DMM(步)",
	"y-AVDC",
	"z-Derekhsu",
	"y-无垢",
	"z-ラグジュTV",
	"9-AVDBS",
	"7-Moodyz",
	"7-Madonna",
	"8-Ideapocket",
	"v-Attackers",
	"9-Javrave",
	"8-Warashi",
}

// 只关心图片扩展名；其它类型原样保留，拼 URL 时仍可用。
var gfriendsExts = []string{".jpg", ".jpeg", ".png", ".webp"}

// gfriendsTree 只取 Content，Information 那堆数字直接忽略。
type gfriendsTree struct {
	Content map[string]map[string]string `json:"Content"`
}

func LoadGfriends(ctx context.Context, hc *http.Client) (*Gfriends, error) {
	if hc == nil {
		hc = &http.Client{Timeout: 120 * time.Second}
	}
	var lastErr error
	for _, u := range gfriendsIndexURLs {
		raw, err := fetchBytes(ctx, hc, u, 64<<20)
		if err != nil {
			lastErr = err
			continue
		}
		g, err := parseGfriends(raw)
		if err != nil {
			lastErr = err
			continue
		}
		return g, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("没有可用的索引地址")
	}
	return nil, fmt.Errorf("拉 gfriends 索引失败: %w", lastErr)
}

// LoadGfriendsFile 从本地 Filetree.json 加载，离线调试用。
func LoadGfriendsFile(path string) (*Gfriends, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseGfriends(raw)
}

func fetchBytes(ctx context.Context, hc *http.Client, u string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "actor-em/1.0")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

func parseGfriends(raw []byte) (*Gfriends, error) {
	var tree gfriendsTree
	if err := json.Unmarshal(raw, &tree); err != nil {
		return nil, fmt.Errorf("解析 gfriends 索引失败: %w", err)
	}
	if len(tree.Content) == 0 {
		return nil, fmt.Errorf("gfriends 索引里没有 Content 节点")
	}

	g := &Gfriends{index: make(map[string]map[string]string, len(tree.Content))}
	size := make(map[string]int, len(tree.Content))
	for dir, files := range tree.Content {
		m := make(map[string]string, len(files))
		for _, v := range files {
			// 只信 value：它才是磁盘上的真实文件名
			name := strings.TrimSpace(strings.SplitN(v, "?t=", 2)[0])
			if name == "" {
				continue
			}
			base, _ := gfriendsBaseName(name)
			if base == "" {
				continue
			}
			// 同一演员在 AI-Fix 和原图都有的情况下优先原图
			if old, ok := m[base]; ok && !strings.HasPrefix(old, gfriendsFixPrefix) {
				continue
			}
			m[base] = name
			g.count++
		}
		if len(m) == 0 {
			continue
		}
		g.index[dir] = m
		size[dir] = len(m)
	}

	g.dirs = sortGfriendsDirs(g.index, size)
	return g, nil
}

// gfriendsBaseName 把 "AI-Fix-桃乃木かな.jpg" 拆成 ("桃乃木かな", true)。
// 没有已知图片扩展名的（极少）按整名处理，扩展名当空。
func gfriendsBaseName(file string) (string, bool) {
	isFix := strings.HasPrefix(file, gfriendsFixPrefix)
	if isFix {
		file = strings.TrimPrefix(file, gfriendsFixPrefix)
	}
	lower := strings.ToLower(file)
	for _, ext := range gfriendsExts {
		if strings.HasSuffix(lower, ext) {
			return file[:len(file)-len(ext)], isFix
		}
	}
	return "", isFix
}

// sortGfriendsDirs 先按 gfriendsDirOrder，未列出的按条目数降序垫底。
func sortGfriendsDirs(index map[string]map[string]string, size map[string]int) []string {
	rank := make(map[string]int, len(gfriendsDirOrder))
	for i, d := range gfriendsDirOrder {
		rank[d] = i
	}
	dirs := make([]string, 0, len(index))
	for d := range index {
		dirs = append(dirs, d)
	}
	sort.SliceStable(dirs, func(i, j int) bool {
		ri, iok := rank[dirs[i]]
		rj, jok := rank[dirs[j]]
		switch {
		case iok && jok:
			return ri < rj
		case iok != jok:
			// 白名单目录永远排前面
			return iok
		}
		if size[dirs[i]] != size[dirs[j]] {
			return size[dirs[i]] > size[dirs[j]]
		}
		return dirs[i] < dirs[j]
	})
	return dirs
}

// Pick 在候选名里找一个有头像的，返回原图直链；找不到返回空串。
// candidates 按优先级排列（日文名在前），第一个命中的名字即采用。
func (g *Gfriends) Pick(ctx context.Context, candidates []string, fallback string) string {
	if g == nil {
		return ""
	}
	list := candidates
	if len(list) == 0 && fallback != "" {
		list = []string{fallback}
	}
	for _, cand := range list {
		name := strings.TrimSpace(cand)
		if name == "" {
			continue
		}
		if u := g.pickOne(name); u != "" {
			return u
		}
	}
	return ""
}

// pickOne 按目录优先级找一个演员的头像，命中第一个优先目录即返回。
func (g *Gfriends) pickOne(name string) string {
	base := gfriendsLookupKey(name)
	if base == "" {
		return ""
	}
	for _, dir := range g.dirs {
		file, ok := g.index[dir][base]
		if !ok {
			continue
		}
		return gfriendsRawBase + escapePath(dir) + "/" + escapePath(file)
	}
	return ""
}

// gfriendsLookupKey 把候选名归一成索引键。
// 候选名通常是裸名（"桃乃木かな"），但也可能带扩展名或 AI-Fix 前缀，一并去掉。
func gfriendsLookupKey(name string) string {
	s := strings.TrimSpace(name)
	s = strings.TrimPrefix(s, gfriendsFixPrefix)
	if s == "" {
		return ""
	}
	if base, _ := gfriendsBaseName(s); base != "" {
		return base
	}
	return s
}

// escapePath 逐段做 URL 转义，保留分隔符。
func escapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// Size 返回索引条目数，用于启动日志。
func (g *Gfriends) Size() int {
	if g == nil {
		return 0
	}
	return g.count
}

// Dirs 返回索引里的目录数，用于启动日志。
func (g *Gfriends) Dirs() int {
	if g == nil {
		return 0
	}
	return len(g.dirs)
}
