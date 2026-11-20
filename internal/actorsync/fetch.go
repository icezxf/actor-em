package actorsync

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ErrNotFound 表示数据源查询成功但确实没有这个人。
// 用它区分「查无此人」（可标 no_source）和「网络/限流错误」（要重试）。
var ErrNotFound = errors.New("not found")

// FetchResult 是多源聚合后的中间结果，还没落成 Actor。
//
// 字段来源见 docs/actor-fields.md，这里只强调三条硬规则：
//   - 生日以 JavStash 为准，它缺席时才用 MDCx（并标 BirthDateSource=mdcx）
//   - 三围只认 TPDB，存原样字符串，不换算
//   - 简介只要中文：wiki → theactorbase，不取英文、不机翻
type FetchResult struct {
	NameOriginal string
	Aliases      []string
	Gender       string
	BirthDate    string
	// BirthDateSource 记生日是哪来的：javstash（权威）/ mdcx（兜底）。
	// MDCx 覆盖 65.7% 但约 24% 与 JavStash 冲突，标出来下游才能自己决定信不信。
	BirthDateSource string
	BirthPlace      string
	Country         string
	HeightCm        int
	WeightKg        int
	Measurements    string // "44-25-39"，英寸，胸围-腰围-臀围
	// MeasurementsUnit 区分上面那串是厘米还是英寸。两套数据源单位不同，
	// 不标出来下游没法比较大小。空表示还没有三围。
	MeasurementsUnit string
	CupSize          string
	CareerStart      int
	Tags             []string
	Bio              string
	BioLang          string

	CanonicalName string // 数据源返回的标准名（清单里写 UUID 时用它兜底）
	ChineseName   string // 中文名，Emby 的 Name 用它；没有就退回清单名
	HomePageURL   string // JavStash 演员页
	ImageURL      string
	ImageSite     string
	Images        []string
	ProviderIDs   map[string]string
	Sources       map[string]bool
	SourceErrors  int
}

// Fetcher 编排所有数据源。任何一个源挂掉都不影响其他源。
type Fetcher struct {
	HTTP         *http.Client
	TPDB         *TPDBClient
	JavStash     *StashBoxClient
	StashDB      *StashBoxClient
	TheActorBase *TheActorBaseClient
	Gfriends     *Gfriends
	MDCx         *MDCxDB
	Dict         *Dict
	WikiEmail    string
	Verbose      bool
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

// Fetch 抓一个演员。name 用清单里的名字（通常是日文名或中文名）。
func (f *Fetcher) Fetch(ctx context.Context, name string) *FetchResult {
	res := &FetchResult{
		NameOriginal: name,
		ProviderIDs:  map[string]string{},
		Sources:      map[string]bool{},
	}
	isUUID := IsUUID(name)

	// ---------- 1. MDCx 离线档案：结构化字段的地基 ----------
	// 它是本地库，不打网络，先铺一层；后面的在线源只填空位，不覆盖。
	//
	// 生日例外：这里只把 MDCx 的生日暂存起来，等 JavStash 查完再决定用不用。
	// 不能在这一步直接写，因为 applyPerformer 拿到 JavStash 生日是直接赋值的，
	// 顺序反了就会被兜底值顶掉权威值。
	var mdcxBirth string
	if f.MDCx != nil {
		if p := f.MDCx.Lookup(name); p != nil {
			f.mark(res, "mdcx", true)
			f.applyMDCx(res, p)
			mdcxBirth = p.Birthday
			f.logf("[%s] mdcx 命中：%s", name, p.Name)
		} else {
			f.mark(res, "mdcx", false)
		}
	}

	// ---------- 2. 候选名 ----------
	candidates := []string{name}
	lang := DetectLanguage(name)

	// 字典里有「日文名 → 中文名」的话，中文名就是 Emby 的 Name
	if f.Dict != nil {
		if zh := f.Dict.ChineseName(name); zh != "" {
			res.ChineseName = zh
		} else if lang == "zh" {
			// 清单里写的就是中文名，不用再翻
			res.ChineseName = name
		}
	}

	// 字典里的中文→日文映射最直接，放最前面
	if lang == "zh" && f.Dict != nil {
		if jp := f.Dict.Lookup(name); jp != "" {
			candidates = prependUnique(candidates, jp)
			f.logf("[%s] 字典命中：%s → %s", name, name, jp)
		}
	}

	// ---------- 3. 维基：中文简介第一顺位，顺带拿日文名做搜索词 ----------
	// 维基按标题查而不是按人查，短名字极易命中同名物（もも→桃树、DA→deviantART），
	// 所以拿到的正文必须过一遍校验才敢用。
	wikiCtx, wcancel := context.WithTimeout(ctx, 10*time.Second)
	jaTitle, zhBio := fetchWikiBoth(wikiCtx, f.HTTP, name, lang, f.WikiEmail)
	wcancel()
	if zhBio != "" && !ValidBio(zhBio) {
		f.logf("[%s] 维基简介不像本人，丢弃：%s", name, TruncateUTF8(zhBio, 120))
		zhBio = ""
	}
	if zhBio != "" {
		f.mark(res, "wiki", true)
		res.Bio = zhBio
	} else {
		f.mark(res, "wiki", false)
	}
	if jaTitle != "" {
		candidates = prependUnique(candidates, jaTitle)
	}

	// ---------- 4. JavStash：日系演员主力源，生日唯一权威 ----------
	var js *Performer
	if f.JavStash != nil && f.JavStash.APIKey != "" {
		js = f.searchCandidates(ctx, res, f.JavStash, candidates)
	}

	// ---------- 5. StashDB：欧美演员兜底 ----------
	if js == nil && f.StashDB != nil && f.StashDB.APIKey != "" {
		f.searchCandidates(ctx, res, f.StashDB, candidates)
	}

	// ---------- 5.5 生日兜底：只在 JavStash 缺席时用 MDCx ----------
	// JavStash 是唯一权威，它给了就不碰。实测 866 条里 354 条有生日（40.9%），
	// 剩下的 512 条里 MDCx 能补 215 条 → 覆盖率 65.7%。
	// MDCx 生日本身约 76% 与 JavStash 一致（冲突 24%，多发生在短名/常见名上），
	// 但这里是「空着」对「76% 概率对」，补了是净收益。标 source 让下游自己判断。
	if res.BirthDate == "" && mdcxBirth != "" {
		res.BirthDate = mdcxBirth
		res.BirthDateSource = "mdcx"
		f.logf("[%s] 生日兜底用 MDCx：%s", name, mdcxBirth)
	}

	// ---------- 6. TPDB：三围兜底 + 体重 ----------
	if f.TPDB != nil && f.TPDB.APIKey != "" {
		// TPDB 是欧美库，只认罗马字。拿日文名去搜必定 not found，
		// 所以把 MDCx 的 romanization（覆盖 93.9%）排到候选最前面。
		terms := candidates
		if p := f.MDCx.Lookup(name); p != nil && p.Romaji != "" {
			terms = prependUnique(terms, p.Romaji)
		}
		if js != nil && js.Name != "" {
			terms = prependUnique(terms, js.Name)
		}
		if p := f.searchTPDB(ctx, res, terms); p != nil {
			f.applyPerformer(res, p)
		}
	}

	// ---------- 7. theactorbase：中文简介第二顺位 ----------
	// 维基往往只有一句话（「XXX是一位日本AV女优。」），theactorbase 给的是整段经历。
	// 所以拿到短简介时继续往下问，谁长用谁。
	if len([]rune(res.Bio)) < bioUpgradeRunes && f.TheActorBase != nil {
		terms := candidates
		// 站点 slug 用罗马字拼，日文名拼不出来。MDCx 的 romanization 覆盖 93.9%，放最前面。
		if p := f.MDCx.Lookup(name); p != nil && p.Romaji != "" {
			terms = prependUnique(terms, p.Romaji)
		}
		if bio := f.TheActorBase.FetchBio(ctx, terms); bio != "" && ValidBio(bio) {
			f.mark(res, "theactorbase", true)
			if len([]rune(bio)) > len([]rune(res.Bio)) {
				res.Bio = bio
			}
		} else {
			f.mark(res, "theactorbase", false)
		}
	}

	// 简介到 theactorbase 为止。MDCx 那个「身高: 166cm | 罩杯: D | 三围: 84/60/86」
	// 是字段串不是文章，塞进 Overview 只会跟头部那行五项重复，宁可空着。

	res.Bio = strings.TrimSpace(res.Bio)
	// 简介和结构化字段打架时以结构化字段为准：实测抓到过同名的另一个人，
	// 简介写「161cm 出生于 1995-11-12」，而 JavStash 给的是 153cm / 1998-12-27。
	if res.Bio != "" && BioConflictsHeight(res.Bio, res.HeightCm) {
		f.logf("[%s] 简介身高 %dcm 与记录 %dcm 冲突，丢弃简介",
			name, BioHeightCm(res.Bio), res.HeightCm)
		res.Bio = ""
	}
	if res.Bio != "" {
		res.BioLang = DetectLanguage(res.Bio)
	}
	res.Bio = TruncateUTF8(res.Bio, 6000)

	// ---------- 9. 头像：gfriends 首选，退到各数据源 ----------
	f.pickAvatar(ctx, res, candidates, isUUID)

	// ---------- 10. 主页：JavStash 演员页 > TPDB 演员页 ----------
	res.HomePageURL = buildHomePageURL(res.ProviderIDs)

	return res
}

// applyMDCx 把离线档案铺进结果。只填空位，不覆盖已有值。
// 注意：不写生日（JavStash 优先，MDCx 生日留到 Fetch 第 5.5 步兜底），
// 不写简介（简介有单独的优先级链）。三围写在这里，因为 MDCx 的厘米分项
// 覆盖 80%，比 TPDB 的 36% 全，后面的源只补空位。
func (f *Fetcher) applyMDCx(res *FetchResult, p *MDCxProfile) {
	if res.CanonicalName == "" && p.Name != "" {
		res.CanonicalName = p.Name
	}
	if p.BirthPlace != "" && res.BirthPlace == "" {
		res.BirthPlace = p.BirthPlace
	}
	if p.Country != "" && res.Country == "" {
		res.Country = p.Country
	}
	if p.Height > 0 && res.HeightCm == 0 {
		res.HeightCm = p.Height
	}
	if p.Cup != "" && res.CupSize == "" {
		res.CupSize = p.Cup
	}
	if p.CareerStart > 0 && res.CareerStart == 0 {
		res.CareerStart = p.CareerStart
	}
	if len(p.Tags) > 0 && len(res.Tags) == 0 {
		res.Tags = p.Tags
	}
	// 三围：MDCx 三项齐备就用它，换算成英寸并用「底围+罩杯」写法（36J-25-39）。
	// TPDB 那套本来就是英寸，留到第 6 步只在空缺时兜底。
	if res.Measurements == "" {
		if s := MeasurementsInches(p.Bust, p.Waist, p.Hip, p.Cup); s != "" {
			res.Measurements = s
			res.MeasurementsUnit = "in"
		}
	}
	if len(p.Aliases) > 0 {
		res.Aliases = mergeStrings(res.Aliases, p.Aliases)
	}
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
		f.applyPerformer(res, p)
		f.logf("[%s] %s 命中：%s", res.NameOriginal, c.Source, p.Name)
		return p
	}
	return nil
}

// searchTPDB 依次尝试候选名，命中即停。
func (f *Fetcher) searchTPDB(ctx context.Context, res *FetchResult, terms []string) *Performer {
	for _, cand := range terms {
		if strings.TrimSpace(cand) == "" {
			continue
		}
		p, err := f.TPDB.Search(ctx, cand)
		if err != nil {
			f.mark(res, "tpdb", false)
			f.countErr(res, err)
			f.logf("[%s] tpdb 搜索 %q 未命中: %v", res.NameOriginal, cand, err)
			continue
		}
		f.mark(res, "tpdb", true)
		f.logf("[%s] tpdb 命中：%s", res.NameOriginal, p.Name)
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

// applyPerformer 把数据源结果合并进结果。只填空位——先到的源赢，
// 所以调用顺序就是优先级（MDCx → JavStash → TPDB），生日是唯一例外。
func (f *Fetcher) applyPerformer(res *FetchResult, p *Performer) {
	if p == nil {
		return
	}
	if res.CanonicalName == "" && p.Name != "" {
		res.CanonicalName = p.Name
	}

	// 生日以 JavStash 为准；其他源的生日一律丢弃（MDCx 走 5.5 步的兜底，TPDB 也偶有偏差）
	if p.Source == "javstash" && p.BirthDate != "" {
		res.BirthDate = p.BirthDate
		res.BirthDateSource = "javstash"
	}

	if p.Country != "" && res.Country == "" {
		res.Country = p.Country
		if res.BirthPlace == "" {
			if cn, ok := countryToChinese[p.Country]; ok {
				res.BirthPlace = cn
			} else {
				res.BirthPlace = p.Country
			}
		}
	}
	if p.Gender != "" && res.Gender == "" {
		res.Gender = strings.ToLower(p.Gender) // StashBox 给的是 FEMALE
	}
	if p.Height > 0 && res.HeightCm == 0 {
		res.HeightCm = p.Height
	}
	if p.Weight > 0 && res.WeightKg == 0 {
		res.WeightKg = p.Weight
	}
	if p.CupSize != "" && res.CupSize == "" {
		res.CupSize = p.CupSize
	}
	// 三围：MDCx 已在第 1 步填过，这里只补空位。
	// TPDB 的第一段可能是胸围（34-24-35），也可能是「底围+杯」（34E-24-35），
	// 两种都原样留着——后者正是欧美标准写法。
	if p.Source == "tpdb" && res.Measurements == "" {
		if first, w, h := p.bwhInches(); first != "" && w > 0 && h > 0 {
			res.Measurements = fmt.Sprintf("%s-%d-%d", first, w, h)
			res.MeasurementsUnit = "in"
		}
	}
	if p.CareerStart > 0 && res.CareerStart == 0 {
		res.CareerStart = p.CareerStart
	}
	if len(p.Aliases) > 0 {
		res.Aliases = mergeStrings(res.Aliases, p.Aliases)
	}

	// 图片：这里只收集候选，最终用哪张由 pickAvatar 决定
	for _, img := range p.Images {
		res.Images = mergeStrings(res.Images, []string{img})
	}
	if p.ImageURL != "" && res.ImageURL == "" {
		res.ImageURL = p.ImageURL
		res.ImageSite = p.Source
	}

	// provider ids
	ids := extractProviderIDs(p.URLs)
	if p.ID != "" {
		switch p.Source {
		case "javstash":
			ids["JavStash"] = p.ID
		case "stashdb":
			ids["StashDB"] = p.ID
		case "tpdb":
			ids["TPDB"] = p.ID
		}
	}
	for k, v := range ids {
		if v != "" {
			res.ProviderIDs[k] = v
		}
	}
}

// pickAvatar 决定最终头像：gfriends 优先，没有就用前面数据源收集到的第一张。
// gfriends 只按候选名精确匹配文件名（索引的 key→value 配对不可信，见 gfriends.go）。
func (f *Fetcher) pickAvatar(ctx context.Context, res *FetchResult, candidates []string, isUUID bool) {
	if f.Gfriends != nil && !isUUID {
		if url := f.Gfriends.Pick(ctx, candidates, res.NameOriginal); url != "" {
			res.ImageURL = url
			res.ImageSite = "gfriends"
			res.Images = mergeStrings([]string{url}, res.Images)
			f.mark(res, "gfriends", true)
			return
		}
		f.mark(res, "gfriends", false)
	}
}

// buildHomePageURL 按 JavStash → TPDB 的顺序拼演员主页地址。
func buildHomePageURL(ids map[string]string) string {
	if v := strings.TrimSpace(ids["JavStash"]); v != "" {
		return "https://javstash.org/performers/" + v
	}
	if v := strings.TrimSpace(ids["TPDB"]); v != "" {
		return "https://theporndb.net/performers/" + v
	}
	return ""
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
	// 先判性别：字典来自 amane 映射表，里面混着男优和同名的男艺人
	// （Panda、Kento Rabbit、れんと…）。这些人抓得到数据，但进了演员库就是错的。
	// 标成 skipped 而不是丢掉，这样下次运行不会重复抓，记录也还留着可查。
	if strings.EqualFold(r.Gender, "male") {
		return StatusSkipped
	}
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
	weight := "-"
	if r.WeightKg > 0 {
		weight = fmt.Sprintf("%d", r.WeightKg)
	}
	// 三围是英寸，日志里带上单位省得和厘米混
	meas := orDash(r.Measurements)
	if meas != "-" && r.MeasurementsUnit != "" {
		meas += r.MeasurementsUnit
	}
	return fmt.Sprintf("生日=%s 身高=%d 体重=%s 三围=%s 出道=%d 标签=%d 简介=%d字 命中[%s] 未命中[%s]",
		birth, r.HeightCm, weight, meas, r.CareerStart, len(r.Tags),
		len([]rune(r.Bio)), strings.Join(hits, ","), strings.Join(misses, ","))
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// cmToInch 把厘米换成英寸（四舍五入）。三围统一走英寸口径，
// 112/64/99cm → 44-25-39in。
func cmToInch(cm int) int {
	if cm <= 0 {
		return 0
	}
	return int(math.Round(float64(cm) / 2.54))
}

// cupDropJP 是日标（JIS L4005）每个罩杯对应的「胸围 − 下胸围」差值，单位 cm。
//
// 反推下胸围必须用日标差值：日本网站标的 cup 就是日标，日标 J 差 32.5cm，
// 而美标 J 只差 25.4cm。用美标差值会把下胸围算大，反推出的 band 系统性偏两码
// （实测 15,028 人里 85% 挤在 28-30，而实际日本女优多是 65-75cm 下胸围）。
var cupDropJP = map[string]float64{
	"AA": 7.5, "A": 10, "B": 12.5, "C": 15, "D": 17.5,
	"E": 20, "F": 22.5, "G": 25, "H": 27.5, "I": 30, "J": 32.5,
	"K": 35, "L": 37.5, "M": 40, "N": 42.5, "O": 45, "P": 47.5,
	"Q": 50, "R": 52.5, "S": 55, "T": 57.5,
}

// BandFromBust 用「胸围 + 罩杯」反推欧美内衣底围（band，英寸偶数）。
//
// 日标下胸围 65/70/75cm 对应美标 30/32/34，换算时补 11cm 的放松量再折英寸取偶。
// 误差约一码：日标罩杯算的是**上胸围**，而网站报的 bust 多是站姿胸围，站姿比
// 上胸围小 2~3cm，反推出的下胸围因此偏大一点。宁可差一码，也好过整条空着。
func BandFromBust(bustCm int, cup string) int {
	drop, ok := cupDropJP[strings.ToUpper(strings.TrimSpace(cup))]
	if !ok || bustCm <= 0 {
		return 0
	}
	under := float64(bustCm) - drop
	// 下胸围落在 55~120cm 之外说明输入有问题（bust 录错或 cup 是别的标准），不猜
	if under < 55 || under > 120 {
		return 0
	}
	band := int(math.Round((under+11)/2.54/2.0) * 2)
	if band < 26 || band > 46 {
		return 0
	}
	return band
}

// MeasurementsInches 把厘米制三围拼成英寸串，第一段优先用「底围+罩杯」写法：
//
//	36J-25-39   有罩杯：第一段是 band+cup，欧美内衣的标法
//	44-25-39    没有罩杯：第一段只能是胸围本身
//
// 抓取路径和离线补值（--backfill）共用这一处，避免两边算出不同的数。
func MeasurementsInches(bust, waist, hip int, cup string) string {
	if bust <= 0 || waist <= 0 || hip <= 0 {
		return ""
	}
	first := strconv.Itoa(cmToInch(bust))
	if band := BandFromBust(bust, cup); band > 0 {
		first = fmt.Sprintf("%d%s", band, strings.ToUpper(strings.TrimSpace(cup)))
	}
	return fmt.Sprintf("%s-%d-%d", first, cmToInch(waist), cmToInch(hip))
}

// bwhInches 从 TPDB 的记录里取出「第一段 + 腰围 + 臀围」，第一段原样返回。
//
// measurements 的第一段有两种形态，两种都是下游要的写法，不必还原：
//
//	34-24-35   第一段就是胸围
//	34E-24-35  第一段是「底围 34 + E 杯」，欧美内衣的标准标法
//
// 旧版把带字母的那种整条丢弃，理由是「34 是下胸围不是胸围」——那没错，但现在
// measurements 的第一段本来就允许是底围+杯，所以原样留着比丢掉有价值。
func (p *Performer) bwhInches() (first string, waist, hip int) {
	waist, hip = p.Waist, p.Hip
	parts := strings.Split(strings.TrimSpace(p.Measurements), "-")
	if len(parts) < 3 {
		return "", waist, hip
	}
	first = strings.ToUpper(strings.TrimSpace(parts[0]))
	if waist == 0 {
		waist, _ = strconv.Atoi(strings.TrimSpace(parts[1]))
	}
	if hip == 0 {
		hip, _ = strconv.Atoi(strings.TrimSpace(parts[2]))
	}
	return first, waist, hip
}
