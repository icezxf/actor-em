package actorsync

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"
)

// ErrNotFound 表示数据源查询成功但确实没有这个人。
// 用它区分「查无此人」（可标 no_source）和「网络/限流错误」（要重试）。
var ErrNotFound = errors.New("not found")

// FetchResult 是四源聚合后的中间结果，还没落成 Actor。
type FetchResult struct {
	NameOriginal string
	Aliases      []string
	Gender       string
	BirthDate    string
	BirthPlace   string
	Country      string
	HeightCm     int
	WeightKg     int
	CupSize      string
	WaistCm      int
	HipCm        int
	CareerStart  int
	Bio          string
	BioLang      string

	CanonicalName string // 数据源返回的标准名（清单里写 UUID 时用它兜底）
	ImageURL      string
	ImageSite     string
	Images        []string
	ProviderIDs   map[string]string
	Sources       map[string]bool
	SourceErrors  int
}

// Fetcher 编排所有数据源。任何一个源挂掉都不影响其他源。
type Fetcher struct {
	HTTP       *http.Client
	TPDB       *TPDBClient
	JavStash   *StashBoxClient
	StashDB    *StashBoxClient
	Translator *Translator
	Dict       *Dict
	WikiEmail  string
	Verbose    bool
}

func NewFetcher(hc *http.Client) *Fetcher {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Fetcher{HTTP: hc}
}

func (f *Fetcher) logf(format string, args ...any) {
	if f.Verbose {
		log.Printf(format, args...)
	}
}

var countryToChinese = map[string]string{
	"JP": "日本", "US": "美国", "KR": "韩国", "CN": "中国",
	"TW": "中国台湾", "HK": "中国香港", "CA": "加拿大",
	"UK": "英国", "GB": "英国", "AU": "澳大利亚", "FR": "法国",
	"DE": "德国", "RU": "俄罗斯", "IT": "意大利", "ES": "西班牙",
	"BR": "巴西",
}

// Fetch 抓一个演员。name 用清单里的名字。
func (f *Fetcher) Fetch(ctx context.Context, name string) *FetchResult {
	res := &FetchResult{
		NameOriginal: name,
		ProviderIDs:  map[string]string{},
		Sources:      map[string]bool{},
	}
	isUUID := IsUUID(name)

	// ---------- 1. Ouxiang：中文简介的主要来源 ----------
	var oux *OuxiangData
	if !isUUID {
		oxCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
		d, err := FetchOuxiang(oxCtx, f.HTTP, name)
		cancel()
		if err == nil {
			oux = d
			f.mark(res, "ouxiang", true)
			if d.Bio != "" {
				res.Bio = d.Bio
			}
			if d.Weight > 0 {
				res.WeightKg = d.Weight
			}
			if d.URL != "" {
				res.ProviderIDs["Ouxiang"] = d.URL
			}
			f.logf("[%s] ouxiang 命中：中文名=%s 日文名=%s", name, d.ZhName, d.JpName)
		} else {
			// Ouxiang 只是附加源，挂了不算失败，否则会把整条记录误判成 failed
			f.mark(res, "ouxiang", false)
			f.logf("[%s] ouxiang 未命中: %v", name, err)
		}
	}

	// ---------- 2. TPDB：生日、三围、体重、图 ----------
	var tp *Performer
	if f.TPDB != nil && f.TPDB.APIKey != "" {
		p, err := f.TPDB.Search(ctx, name)
		if err != nil {
			f.mark(res, "tpdb", false)
			f.countErr(res, err)
		} else {
			tp = p
			f.mark(res, "tpdb", true)
			f.applyPerformer(res, p, true)
			f.logf("[%s] tpdb 命中：%s", name, p.Name)
		}
	}

	// ---------- 3. 维基百科：补简介、拿日文名做搜索词 ----------
	candidates := []string{name}
	lang := DetectLanguage(name)

	switch lang {
	case "zh":
		wctx, wcancel := context.WithTimeout(ctx, 8*time.Second)
		jaTitle, _, zhBio := FetchWiki(wctx, f.HTTP, "zh", name, "ja", f.WikiEmail)
		wcancel()
		if zhBio != "" {
			f.mark(res, "wiki", true)
			if res.Bio == "" {
				res.Bio = zhBio
			}
		}
		if jaTitle != "" {
			candidates = prependUnique(candidates, jaTitle)
			wctx2, wcancel2 := context.WithTimeout(ctx, 8*time.Second)
			_, _, jaBio := FetchWiki(wctx2, f.HTTP, "ja", jaTitle, "", f.WikiEmail)
			wcancel2()
			if jaBio != "" && res.Bio == "" {
				res.Bio = jaBio
			}
		}
	case "ja":
		wctx, wcancel := context.WithTimeout(ctx, 8*time.Second)
		zhTitle, _, jaBio := FetchWiki(wctx, f.HTTP, "ja", name, "zh", f.WikiEmail)
		wcancel()
		if jaBio != "" {
			f.mark(res, "wiki", true)
			if res.Bio == "" {
				res.Bio = jaBio
			}
		}
		if zhTitle != "" {
			candidates = prependUnique(candidates, zhTitle)
			wctx2, wcancel2 := context.WithTimeout(ctx, 8*time.Second)
			_, _, zhBio := FetchWiki(wctx2, f.HTTP, "zh", zhTitle, "", f.WikiEmail)
			wcancel2()
			if zhBio != "" && res.Bio == "" {
				res.Bio = zhBio
			}
		}
	}

	// 字典里的中文→日文映射最直接，放最前面
	if lang == "zh" && f.Dict != nil {
		if jp := f.Dict.Lookup(name); jp != "" {
			candidates = prependUnique(candidates, jp)
			f.logf("[%s] 字典命中：%s → %s", name, name, jp)
		}
	}
	// Ouxiang 查到的日文名也很可靠
	if oux != nil && oux.JpName != "" {
		candidates = prependUnique(candidates, oux.JpName)
	}

	// ---------- 4. JavStash：日系演员的主力源 ----------
	var js *Performer
	if f.JavStash != nil && f.JavStash.APIKey != "" {
		js = f.searchCandidates(ctx, res, f.JavStash, candidates)
	}

	// ---------- 5. StashDB：欧美演员兜底 ----------
	if js == nil && f.StashDB != nil && f.StashDB.APIKey != "" {
		f.searchCandidates(ctx, res, f.StashDB, candidates)
	}

	// ---------- 6. TPDB 二次补三围（JavStash 有更准的罗马字名） ----------
	needExtra := res.HeightCm == 0 || res.WaistCm == 0 || res.HipCm == 0 || res.CupSize == ""
	if needExtra && tp == nil && js != nil && js.Name != "" && f.TPDB != nil && f.TPDB.APIKey != "" {
		if p, err := f.TPDB.Search(ctx, js.Name); err == nil {
			f.mark(res, "tpdb", true)
			f.applyPerformer(res, p, false)
		}
	}

	// 体重最后兜底
	if res.WeightKg == 0 && oux != nil && oux.Weight > 0 {
		res.WeightKg = oux.Weight
	}

	// ---------- 7. 简介翻译 ----------
	res.Bio = strings.TrimSpace(res.Bio)
	if LooksEnglish(res.Bio) && f.Translator != nil && f.Translator.APIKey != "" {
		if zh, err := f.Translator.TranslateEN2ZH(ctx, res.Bio); err == nil && zh != "" {
			res.Bio = zh
			f.logf("[%s] 简介已翻译为中文", name)
		}
	}
	if res.Bio != "" {
		res.BioLang = DetectLanguage(res.Bio)
		if res.BioLang == "unknown" {
			res.BioLang = "zh"
		}
	}
	res.Bio = TruncateUTF8(res.Bio, 6000)

	return res
}

// searchCandidates 依次尝试候选名，命中即停。
func (f *Fetcher) searchCandidates(ctx context.Context, res *FetchResult, c *StashBoxClient, candidates []string) *Performer {
	for _, cand := range candidates {
		if strings.TrimSpace(cand) == "" {
			continue
		}
		p, err := c.Search(ctx, cand)
		if err != nil {
			f.mark(res, c.Source, false)
			f.countErr(res, err)
			f.logf("[%s] %s 搜索 %q 未命中: %v", res.NameOriginal, c.Source, cand, err)
			continue
		}
		f.mark(res, c.Source, true)
		f.applyPerformer(res, p, true)
		f.logf("[%s] %s 命中：%s", res.NameOriginal, c.Source, p.Name)
		return p
	}
	return nil
}

func (f *Fetcher) mark(res *FetchResult, source string, ok bool) {
	if ok {
		res.Sources[source] = true
		return
	}
	// false 不覆盖已有的 true
	if _, exists := res.Sources[source]; !exists {
		res.Sources[source] = false
	}
}

func (f *Fetcher) countErr(res *FetchResult, err error) {
	if err == nil || errors.Is(err, ErrNotFound) {
		return
	}
	res.SourceErrors++
}

// applyPerformer 把数据源结果合并进结果；override=true 表示这个源优先级更高。
func (f *Fetcher) applyPerformer(res *FetchResult, p *Performer, override bool) {
	if p == nil {
		return
	}
	if res.CanonicalName == "" && p.Name != "" {
		res.CanonicalName = p.Name
	}
	if override || res.BirthDate == "" {
		if p.BirthDate != "" {
			res.BirthDate = p.BirthDate
		}
	}
	if p.Country != "" && (override || res.Country == "") {
		res.Country = p.Country
		if cn, ok := countryToChinese[p.Country]; ok {
			res.BirthPlace = cn
		} else {
			res.BirthPlace = p.Country
		}
	}
	if p.Gender != "" && res.Gender == "" {
		res.Gender = p.Gender
	}
	if p.Height > 0 && (override || res.HeightCm == 0) {
		res.HeightCm = p.Height
	}
	if p.Weight > 0 && (override || res.WeightKg == 0) {
		res.WeightKg = p.Weight
	}
	if p.CupSize != "" && (res.CupSize == "" || override) {
		res.CupSize = p.CupSize
	}
	if p.Waist > 0 && (res.WaistCm == 0 || override) {
		res.WaistCm = p.Waist
	}
	if p.Hip > 0 && (res.HipCm == 0 || override) {
		res.HipCm = p.Hip
	}
	if p.CareerStart > 0 && res.CareerStart == 0 {
		res.CareerStart = p.CareerStart
	}
	if p.Bio != "" && res.Bio == "" {
		res.Bio = p.Bio
	}
	if len(p.Aliases) > 0 {
		res.Aliases = mergeStrings(res.Aliases, p.Aliases)
	}

	// 图片：JavStash/StashDB 优先（日系演员的图通常更全），TPDB 兜底
	if p.ImageURL != "" {
		if res.ImageURL == "" || p.Source != "tpdb" {
			res.ImageURL = p.ImageURL
			res.ImageSite = p.Source
		}
	}
	for _, img := range p.Images {
		res.Images = mergeStrings(res.Images, []string{img})
	}

	// provider ids
	ids := extractProviderIDs(p.URLs)
	if p.Source == "javstash" && p.ID != "" {
		ids["JavStash"] = p.ID
	}
	if p.Source == "stashdb" && p.ID != "" {
		ids["StashDB"] = p.ID
	}
	if p.Source == "tpdb" && p.ID != "" {
		ids["TPDB"] = p.ID
	}
	for k, v := range ids {
		if v != "" {
			res.ProviderIDs[k] = v
		}
	}
}

// extractProviderIDs 从外部链接里提取各种 ID。
func extractProviderIDs(urls []string) map[string]string {
	ids := map[string]string{}
	for _, u := range urls {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		switch {
		case strings.Contains(u, "themoviedb.org/person/"):
			ids["Tmdb"] = strings.Split(strings.Split(u, "/person/")[1], "-")[0]
		case strings.Contains(u, "imdb.com/name/"):
			ids["Imdb"] = strings.Trim(strings.Split(u, "/name/")[1], "/")
		case strings.Contains(u, "wikidata.org/wiki/"):
			ids["Wikidata"] = strings.Trim(strings.Split(u, "/wiki/")[1], "/")
		case strings.Contains(u, "wikipedia.org/wiki/"):
			parts := strings.SplitN(u, "//", 2)
			if len(parts) == 2 {
				ids["Wikipedia"] = strings.TrimSuffix(parts[1], "/")
			}
		case strings.Contains(u, "stashdb.org/performers/"):
			ids["StashDB"] = strings.Trim(strings.Split(u, "/performers/")[1], "/")
		case strings.Contains(u, "javstash.org/performers/"):
			ids["JavStash"] = strings.Trim(strings.Split(u, "/performers/")[1], "/")
		case strings.Contains(u, "theporndb.net/performers/"):
			ids["TPDB"] = strings.Trim(strings.Split(u, "/performers/")[1], "/")
		}
	}
	return ids
}

func prependUnique(list []string, v string) []string {
	v = strings.TrimSpace(v)
	if v == "" {
		return list
	}
	for _, s := range list {
		if NormalizeName(s) == NormalizeName(v) {
			return list
		}
	}
	return append([]string{v}, list...)
}

// Outcome 把抓取结果折算成 status。
func (r *FetchResult) Outcome() string {
	anyHit := false
	for _, ok := range r.Sources {
		if ok {
			anyHit = true
			break
		}
	}
	switch {
	case anyHit:
		if r.BirthDate == "" && r.Bio == "" {
			return StatusPartial
		}
		return StatusOK
	case r.SourceErrors > 0:
		return StatusFailed
	default:
		return StatusNoSource
	}
}

func (r *FetchResult) Summary() string {
	var hits, misses []string
	for k, ok := range r.Sources {
		if ok {
			hits = append(hits, k)
		} else {
			misses = append(misses, k)
		}
	}
	sort.Strings(hits)
	sort.Strings(misses)
	birth := r.BirthDate
	if birth == "" {
		birth = "-"
	}
	return fmt.Sprintf("生日=%s 身高=%d 简介=%d字 命中[%s] 未命中[%s]",
		birth, r.HeightCm, len([]rune(r.Bio)), strings.Join(hits, ","), strings.Join(misses, ","))
}
