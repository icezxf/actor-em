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
// 字段来源见 docs/actor-fields.md，这里只强调四条硬规则：
//   - 生日以 JavStash 为准，它缺席时才用 MDCx（并标 BirthDateSource=mdcx）
//   - 三围 MDCx 打底（厘米反推成「底围+杯」英寸写法），TPDB 兜底
//   - 简介只要中文：wiki → dramaface → theactorbase → yinrenlu → cinewho → jav-idol-db → castwiki，不取英文、不机翻
//   - 中文人物站能按原名精确对账，除简介外还补生日/身高/出道年（只填空位）
//   - 搜 TPDB 必须用 StashDB 英文名（英文站），从 JavStash 的 stashdb 链接 UUID 反查
type FetchResult struct {
	NameOriginal string
	Aliases      []string
	Gender       string
	BirthDate    string
	BirthYear    int  // 只有年份的生日（StashDB 常见 "2004"），无法构成完整日期时存这里
	// BirthDateSource 记生日是哪来的：javstash（权威）/ stashdb / tpdb / mdcx（兜底）。
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
	// BioEnFallback：TPDB 的英文简介，单独暂存。中文简介链全部落空时才用它兜底，
	// 避免长英文把后来的短中文挤掉（中文优先口径不变）。
	BioEnFallback string

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
	DramaFace    *DramaFaceClient
	Yinrenlu     *YinrenluClient
	Cinewho      *CinewhoClient
	IdolDB       *IdolDBClient
	CastWiki     *CastWikiClient
	WikiEN       *WikiENClient // 英文维基，欧美演员简介兜底（严格身份对账）
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
	"TW": "中国台湾", "HK": "中国香港", "MO": "中国澳门", "CA": "加拿大",
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

	// 字典里指向同一人的全部别名（含改名前后的各种中文名）都要带上。
	// Emby 侧按 name 精确匹配，用户库里的演员名常是旧名（如「桥本有菜」），
	// 而主名取了新名（「新有菜」）——旧名若不进 aliases 就永远匹配不上。
	if f.Dict != nil {
		if dictAliases := f.Dict.AllAliases(name); len(dictAliases) > 0 {
			// 主名（中文名）/原名不重复塞进 aliases
			selfNames := map[string]bool{NormalizeName(name): true}
			if res.ChineseName != "" {
				selfNames[NormalizeName(res.ChineseName)] = true
			}
			filtered := dictAliases[:0]
			for _, v := range dictAliases {
				if !selfNames[NormalizeName(v)] {
					filtered = append(filtered, v)
				}
			}
			res.Aliases = mergeStrings(res.Aliases, filtered)
			f.logf("[%s] 字典别名 %d 个：%v", name, len(filtered), filtered)
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
	// UUID 清单模式（StashDB 欧美演员）：清单名是 StashDB 的 ID，JavStash 永远查不到，
	// 白烧一次网络请求还可能 TLS 超时拖慢整轮——直接跳过。
	var js *Performer
	if !IsUUID(name) && f.JavStash != nil && f.JavStash.APIKey != "" {
		js = f.searchCandidates(ctx, res, f.JavStash, candidates)
	}

	// ---------- 5. StashDB：出生地/出道年/三围的第二个 stash-box 源 ----------
	// 用户硬性要求：搜 TPDB 必须用 StashDB 的标准英文名（TPDB 是英文站）。
	// 两条路径拿这个英文名：
	//   (a) JavStash 命中 → 它的 urls 里常直接带 stashdb.org/performers/<uuid>
	//       （实测 12432 个 JavStash 命中里 3817 个带，31%），用 UUID 精确反查，
	//       这样日本演员也有 StashDB 英文名可搜 TPDB。
	//   (b) JavStash 没命中（欧美演员）→ 按候选名查 StashDB。
	//   (c) 清单里直接写 UUID（StashDB 欧美清单模式）→ findPerformer 精确直查。
	// 拿到的 performer.Name 供第 6 步 tpdbTerms 排第一；
	// 整条记录也一并应用（见下面的 applyPerformer）。
	var sd *Performer
	if f.StashDB != nil && f.StashDB.APIKey != "" {
		sdUUID := ""
		if IsUUID(name) {
			// (c) 清单名本身就是 StashDB UUID：精确直查，不走 searchPerformer
			// （searchPerformer 不认 UUID，会用 UUID 当关键词乱搜）。
			sdUUID = name
		} else if js != nil {
			if ids := extractProviderIDs(js.URLs); ids["StashDB"] != "" {
				sdUUID = ids["StashDB"]
			}
		}
		if sdUUID != "" {
			if p, err := f.StashDB.Search(ctx, sdUUID); err == nil && p != nil {
				sd = p
				f.logf("[%s] StashDB 按 UUID 命中：%s", name, p.Name)
			} else {
				f.mark(res, "stashdb", false)
				f.countErr(res, err)
			}
		}
		// (b) 走 searchCandidates 时它内部已经应用过整条记录，不用再来一次
		applied := false
		if sd == nil && js == nil && !IsUUID(name) {
			sd = f.searchCandidates(ctx, res, f.StashDB, candidates)
			applied = sd != nil
		}
		// StashDB 的整条记录都要用上，不能只取那个英文名：它和 JavStash 同一套
		// schema，同样有 country（→ 出生地）、career_start_year、身高、三围、别名、图。
		// JavStash 在前面已经应用过，所以已有值不会被顶掉（applyPerformer 只补空位），
		// 但 JavStash 没填的空字段、以及 JavStash 缺席的欧美演员都能吃上。
		if sd != nil && !applied {
			f.mark(res, "stashdb", true)
			f.applyPerformer(res, sd)
			f.logf("[%s] StashDB 记录并入：出生地/出道年/身高/三围按空位补齐", name)
		}
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

	// ---------- 5.6 UUID 模式：用 StashDB 英文名回查 JavStash 补权威生日 ----------
	// 部分欧美演员在 JavStash 也有条目（尤其拍过日本作品的），生日以它为权威。
	// UUID 是 StashDB 的 ID，JavStash 不认，所以用 StashDB 返回的英文名查。
	// best-effort：JavStash 常触发 Cloudflare 403/超时，失败就静默跳过，绝不阻塞主流程。
	// 只在连 StashDB/MDCx 都没给出完整生日时才试，减少无谓请求。
	if IsUUID(name) && js == nil && res.BirthDate == "" &&
		f.JavStash != nil && f.JavStash.APIKey != "" && sd != nil && sd.Name != "" {
		jctx, jcancel := context.WithTimeout(ctx, 8*time.Second)
		if p, err := f.JavStash.Search(jctx, sd.Name); err == nil && p != nil {
			if p.BirthDate != "" {
				res.BirthDate = p.BirthDate
				res.BirthDateSource = "javstash"
				res.BirthYear = 0 // 精确生日盖掉年份占位
				f.mark(res, "javstash", true)
				f.logf("[%s] JavStash 按英文名补权威生日：%s → %s", name, sd.Name, p.BirthDate)
			}
		}
		jcancel()
	}

	// ---------- 6. TPDB：三围兜底 + 体重 ----------
	if f.TPDB != nil && f.TPDB.APIKey != "" {
		terms := tpdbTerms(candidates, f.MDCx, name, js, sd)
		if p := f.searchTPDB(ctx, res, terms); p != nil {
			f.applyPerformer(res, p)
		}
	}

	// ---------- 7. 中文简介补充 ----------
	// 维基往往只有一句话（「XXX是一位日本AV女优。」），下面几站给的是整段经历。
	// 猜 slug 的站（dramaface / theactorbase）只认罗马字，MDCx 覆盖 93.9% 放最前面；
	// 按名字搜详情页的中文站（yinrenlu / cinewho / jav-idol-db / castwiki）也在同一组词里找。
	// 数据返回的标准英文名也是可靠 slug（如 StashDB 的 "Kana Momonogi"）。
	terms := candidates
	if p := f.MDCx.Lookup(name); p != nil && p.Romaji != "" {
		terms = prependUnique(terms, p.Romaji)
	}
	if js != nil && js.Name != "" {
		terms = prependUnique(terms, js.Name)
	}
	if sd != nil && sd.Name != "" {
		terms = prependUnique(terms, sd.Name)
	}

	// 短简介才去猜 slug：这些站没有身份对账能力，命中同名别人的代价高。
	if len([]rune(res.Bio)) < bioUpgradeRunes {
		if f.DramaFace != nil {
			if bio := f.DramaFace.FetchBio(ctx, terms); bio != "" && ValidBio(bio) {
				f.mark(res, "dramaface", true)
				if len([]rune(bio)) > len([]rune(res.Bio)) {
					res.Bio = bio
					res.BioLang = "zh"
				}
			} else {
				f.mark(res, "dramaface", false)
			}
		}
		if f.TheActorBase != nil {
			if bio := f.TheActorBase.FetchBio(ctx, terms); bio != "" && ValidBio(bio) {
				f.mark(res, "theactorbase", true)
				if len([]rune(bio)) > len([]rune(res.Bio)) {
					res.Bio = bio
					res.BioLang = "zh"
				}
			} else {
				f.mark(res, "theactorbase", false)
			}
		}
	}

	// 中文人物站无论简介长短都跑：它们能用页面上的原名精确对账，
	// 除了更长的中文简介，还能补生日/身高/出道年这些结构性空位。
	f.fetchCNProfiles(ctx, res, terms)

	// 卡司Wiki 单独跑：它的身份核对比别的源更严——站方主名必须与清单名一致。
	// 统一的 CNProfile 接口只给候选名列表，而字典会把不同演员的艺名混进同一份
	// 候选里（实测一个人 15 个别名，其中含别的演员正名），拿候选核身份会串号。
	if f.CastWiki != nil {
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		p, matched := f.CastWiki.FetchFor(cctx, terms, res.NameOriginal)
		cancel()
		if p != nil && p.hasContent() {
			f.mark(res, "castwiki", true)
			f.logf("[%s] castwiki 命中：%s（简介 %d 字）", res.NameOriginal, matched, len([]rune(p.Bio)))
			f.applyCNProfile(res, "castwiki", p)
		} else {
			f.mark(res, "castwiki", false)
		}
	}

	// 简介到中文人物站为止。MDCx 那个「身高: 166cm | 罩杯: D | 三围: 84/60/86」
	// 是字段串不是文章，塞进 Overview 只会跟头部那行五项重复，宁可空着。

	res.Bio = strings.TrimSpace(res.Bio)
	// 简介和结构化字段打架时以结构化字段为准：实测抓到过同名的另一个人，
	// 简介写「161cm 出生于 1995-11-12」，而 JavStash 给的是 153cm / 1998-12-27。
	if res.Bio != "" && BioConflictsHeight(res.Bio, res.HeightCm) {
		f.logf("[%s] 简介身高 %dcm 与记录 %dcm 冲突，丢弃简介",
			name, BioHeightCm(res.Bio), res.HeightCm)
		res.Bio = ""
	}
	// 中文简介链全部落空时，依次用英文维基（严格身份对账）、TPDB 英文简介兜底。
	// 都标 bio_lang=en，下游可选择只显示中文；保证"中文优先"不被长英文抢占。
	if res.Bio == "" && f.WikiEN != nil {
		// UUID 模式下 name 是 StashDB ID，用数据源返回的英文名搜维基
		enName := name
		if IsUUID(name) && res.CanonicalName != "" {
			enName = res.CanonicalName
		}
		wy := wantBirthYear(res.BirthDate, res.BirthYear)
		wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
		if enBio := f.WikiEN.FetchBio(wctx, enName, wy); enBio != "" {
			res.Bio = enBio
			res.BioLang = "en"
			f.mark(res, "wiki_en", true)
			f.logf("[%s] 简介用英文维基兜底（%d 字，对账年份 %d）", name, len([]rune(enBio)), wy)
		} else {
			f.mark(res, "wiki_en", false)
		}
		wcancel()
	}
	if res.Bio == "" && res.BioEnFallback != "" {
		res.Bio = res.BioEnFallback
		res.BioLang = "en" // 明确标英文，不靠 DetectLanguage 猜（弯引号等会让它判 unknown）
		f.logf("[%s] 简介用 TPDB 英文兜底（%d 字）", name, len([]rune(res.Bio)))
	} else if res.Bio != "" {
		if res.BioLang == "" {
			res.BioLang = DetectLanguage(res.Bio)
		}
	}
	res.Bio = TruncateUTF8(res.Bio, 6000)

	// ---------- 9. 头像：gfriends 首选，退到各数据源 ----------
	f.pickAvatar(ctx, res, candidates, isUUID)

	// ---------- 10. 主页：JavStash 演员页 > TPDB 演员页 ----------
	res.HomePageURL = buildHomePageURL(res.ProviderIDs)

	// ---------- 11. 出生地补国家 ----------
	// 下游拿到的是一条 Emby ProductionLocations，只有「东京都」看不出国家，
	// 统一成「日本·东京都」；本来就只有国家（「日本」）的原样留着。
	res.finalizeBirthPlace()

	// ---------- 12. 罩杯定稿 + 进标签 ----------
	// 没有 cup_size 的从三围首段反推（34E-24-35 → E），再补成「E罩杯」标签，
	// Emby 的标签栏能直接按罩杯筛人。
	res.finalizeCup()

	return res
}

// fetchCNProfiles 跑「先搜索/按名取档、再读详情」的中文人物站。
// 每个源单独 12 秒超时，任何一个挂掉都不拖累其他源，也不影响已抓到的字段。
func (f *Fetcher) fetchCNProfiles(ctx context.Context, res *FetchResult, terms []string) {
	type cnSource struct {
		name  string
		fetch func(context.Context, []string) (*CNProfile, string)
	}
	var sources []cnSource
	if f.Yinrenlu != nil {
		sources = append(sources, cnSource{"yinrenlu", f.Yinrenlu.Fetch})
	}
	if f.Cinewho != nil {
		sources = append(sources, cnSource{"cinewho", f.Cinewho.Fetch})
	}
	if f.IdolDB != nil {
		sources = append(sources, cnSource{"idoldb", f.IdolDB.Fetch})
	}
	for _, s := range sources {
		cctx, cancel := context.WithTimeout(ctx, 12*time.Second)
		p, matched := s.fetch(cctx, terms)
		cancel()
		if p == nil || !p.hasContent() {
			f.mark(res, s.name, false)
			continue
		}
		f.mark(res, s.name, true)
		f.logf("[%s] %s 命中：%s（简介 %d 字）", res.NameOriginal, s.name,
			matched, len([]rune(p.Bio)))
		f.applyCNProfile(res, s.name, p)
	}
}

// applyCNProfile 只填空位；简介谁长用谁。
// 生日例外：这三站能按原名对账，JavStash/MDCx 都没给时它们的值可用，
// 但要在 BirthDateSource 上标出来，别冒充 JavStash 权威值。
func (f *Fetcher) applyCNProfile(res *FetchResult, source string, p *CNProfile) {
	if bio := strings.TrimSpace(p.Bio); validCNBio(bio) {
		if res.Bio == "" || len([]rune(bio)) > len([]rune(res.Bio)) {
			res.Bio = bio
			res.BioLang = "zh"
		}
	}
	if res.ChineseName == "" && p.ChineseName != "" {
		res.ChineseName = p.ChineseName
	}
	if p.Romaji != "" {
		res.Aliases = mergeStrings(res.Aliases, []string{p.Romaji})
	}
	if len(p.Aliases) > 0 {
		res.Aliases = mergeStrings(res.Aliases, p.Aliases)
	}
	if res.BirthDate == "" && p.BirthDate != "" {
		res.BirthDate = p.BirthDate
		res.BirthDateSource = source
	}
	if res.HeightCm == 0 && p.HeightCm > 0 {
		res.HeightCm = p.HeightCm
	}
	if res.Gender == "" && p.Gender != "" {
		res.Gender = p.Gender
	}
	if res.BirthPlace == "" && p.BirthPlace != "" {
		res.BirthPlace = p.BirthPlace
	}
	if res.CareerStart == 0 && p.CareerStart > 0 {
		res.CareerStart = p.CareerStart
	}
	if res.Measurements == "" {
		if s := MeasurementsInches(p.Bust, p.Waist, p.Hip, p.Cup); s != "" {
			res.Measurements = s
			res.MeasurementsUnit = "in"
		}
	}
	if p.ImageURL != "" {
		res.Images = mergeStrings(res.Images, []string{p.ImageURL})
	}
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
	// TPDB 英文简介单独暂存兜底：中文简介链（wiki/中文站/theactorbase）优先，
	// 全落空时才在收尾用这段英文，标 bio_lang=en。
	if p.Source == "tpdb" && res.BioEnFallback == "" {
		if b := strings.TrimSpace(p.Bio); len([]rune(b)) >= 20 {
			res.BioEnFallback = b
		}
	}

	// 生日来源优先级：
	//   - javstash 是日系唯一权威，命中即写（覆盖空位）
	//   - stashdb 是欧美主力源，只在还没有生日时兜底，标 stashdb（JavStash 命中时绝不顶掉）
	//   - 其余源（MDCx / 中文人物站）走各自的兜底分支，同样只填空位
	switch {
	case p.Source == "javstash" && p.BirthDate != "":
		res.BirthDate = p.BirthDate
		res.BirthDateSource = "javstash"
	case p.Source == "stashdb" && p.BirthDate != "" && res.BirthDate == "":
		res.BirthDate = p.BirthDate
		res.BirthDateSource = "stashdb"
	case p.Source == "tpdb" && p.BirthDate != "" && res.BirthDate == "":
		// TPDB 能给部分欧美演员补精确生日（StashDB 缺席时）
		res.BirthDate = p.BirthDate
		res.BirthDateSource = "tpdb"
	}
	// 只有年份、没有完整日期时记到 BirthYear（优先 stashdb，其次 tpdb）
	// 一旦后面拿到精确生日，就把年份占位清掉，避免同一人两个字段打架。
	if res.BirthDate != "" {
		res.BirthYear = 0
	} else if p.BirthYear > 0 && res.BirthYear == 0 {
		res.BirthYear = p.BirthYear
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
	// JavStash/StashDB 给的是「底围+杯 + 腰 + 臀」（英寸，band_size/cup_size/waist/hip），
	// 正好是下游要的 34E-24-35 写法，排在 TPDB 的原文之前。
	if res.Measurements == "" {
		if s := bandCupMeasurements(p.Band, p.CupSize, p.Waist, p.Hip); s != "" {
			res.Measurements = s
			res.MeasurementsUnit = "in"
		}
	}
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

// tpdbTerms 组装 TPDB 的搜索词，优先级从高到低：
//
//	StashDB 英文名 > JavStash 名 > MDCx 罗马字 > 清单原名/字典日文名…
//
// TPDB 是欧美英文站，拿中文名或日文名去搜必定 not found。StashDB 也是英文站
// （stash-box），它命中的 performer.Name 是标准英文名，是最可靠的关键词；
// 但 StashDB 只在 JavStash 缺席时才查，所以命中日本演员时退而用 JavStash 名
// （也是罗马字），再退用 MDCx 的 romanization（覆盖 93.9%）。
func tpdbTerms(candidates []string, mdcx *MDCxDB, name string, js, sd *Performer) []string {
	terms := candidates
	if mdcx != nil {
		if p := mdcx.Lookup(name); p != nil && p.Romaji != "" {
			terms = prependUnique(terms, p.Romaji)
		}
	}
	if js != nil && js.Name != "" {
		terms = prependUnique(terms, js.Name)
	}
	if sd != nil && sd.Name != "" {
		terms = prependUnique(terms, sd.Name)
	}
	return terms
}

// buildHomePageURL 按 JavStash → StashDB → TPDB 的顺序拼演员主页地址。
func buildHomePageURL(ids map[string]string) string {
	if v := strings.TrimSpace(ids["JavStash"]); v != "" {
		return "https://javstash.org/performers/" + v
	}
	if v := strings.TrimSpace(ids["StashDB"]); v != "" {
		return "https://stashdb.org/performers/" + v
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
