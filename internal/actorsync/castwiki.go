package actorsync

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// CastWikiClient 抓 castwiki.org（卡司Wiki）的中文简介。
//
// 站点是一个带站内搜索的人物维基，走 xunsearch：
//
//	/search.php?q=三上悠亚&f=_all&s=relevance&xml=yes
//	→ <doc percent="100%"><slug>yuamikami</slug>
//	    <subject><![CDATA[三上悠亜 / Yua Mikami / 三上悠亚]]></subject></doc>
//
// slug **不能靠猜**：三上悠亜 → yuamikami 是罗马字去空格，但桃乃木かな → busycy
// 是站方自己起的短名，还有一批干脆是随机串。所以只走站内搜索，不猜。
//
// 详情页里有 JSON-LD Person（2026-11 实测）：
//
//	{"@type":"Person","name":"相沢みなみ","alternateName":"Minami Aizawa",
//	 "birthDate":"1996-06-14","gender":"女","birthPlace":"Tokyo, Japan",
//	 "description":"相泽南（相沢 みなみ.Minami Aizawa）\r\n生日：…\r\n…"}
//
// description 就是整段中文简介，动辄上千字（维基通常只有一句话），是这个源最值钱的地方。
// 页面上另有一份 <p class="person-profile-intro">，内容与 description 相同，
// 万一哪天 JSON-LD 没了用它兜底。
//
// robots.txt 只限制 AI 训练（ai-train=no），个人媒体库刮削属于 reference 用途；
// 外层串行调用、每人最多搜 2 次 + 读 1 页，不并发猛冲。
const (
	castWikiBase      = "https://www.castwiki.org/profile/"
	castWikiSearchURL = "https://www.castwiki.org/search.php"
	castWikiUA        = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)

var (
	reCastWikiLD    = regexp.MustCompile(`(?is)<script[^>]*type=["']application/ld\+json["'][^>]*>(.*?)</script>`)
	reCastWikiIntro = regexp.MustCompile(`(?is)<p class="person-profile-intro">(.*?)</p>`)
	reCastWikiTitle = regexp.MustCompile(`(?is)<title>([^<]*)</title>`)
	// 页面/简介开头的属性串字段名，用于逐段剥掉（见 stripCastWikiFields）。
	reCastWikiFieldLabel = regexp.MustCompile(`(出生日期|出生|生日|身高／体重|身高/体重|身高|体重|三围|三圍|罩杯|胸围|腰围|臀围|爱好|興趣|兴趣|出道时间|出道|血型|星座|籍贯|出生地|国籍|职业|職業|事务所|事務所|日文名)\s*[:：]`)
	reCastWikiHeight     = regexp.MustCompile(`身高(?:／体重|/体重)?\s*[:：]\s*(\d{3})`)
	reCastWikiBWH        = regexp.MustCompile(`三围\s*[:：]\s*(\d{2,3})\s*-\s*(\d{2,3})\s*-\s*(\d{2,3})`)
	reCastWikiBust       = regexp.MustCompile(`胸围\s*[:：]\s*(\d{2,3})`)
	reCastWikiWaist      = regexp.MustCompile(`腰围\s*[:：]\s*(\d{2,3})`)
	reCastWikiHip        = regexp.MustCompile(`臀围\s*[:：]\s*(\d{2,3})`)
	reCastWikiCup        = regexp.MustCompile(`罩杯\s*[:：]\s*([A-K]{1,3})`)
	reCastWikiDebut      = regexp.MustCompile(`出道(?:时间|年份|日期)?\s*[:：]\s*((?:19|20)\d{2})`)
)

type CastWikiClient struct {
	HTTP   *http.Client
	Base   string // 允许测试覆盖；空则用默认 castWikiBase
	Search string // 允许测试覆盖；空则用默认 castWikiSearchURL
}

func NewCastWikiClient(hc *http.Client) *CastWikiClient {
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	return &CastWikiClient{HTTP: hc}
}

func (c *CastWikiClient) baseURL() string {
	if c.Base != "" {
		return c.Base
	}
	return castWikiBase
}

func (c *CastWikiClient) searchURL() string {
	if c.Search != "" {
		return c.Search
	}
	return castWikiSearchURL
}

// castWikiDoc 是 /search.php?xml=yes 的 <doc>。
type castWikiDoc struct {
	Percent string `xml:"percent,attr"`
	Slug    string `xml:"slug"`
	Subject string `xml:"subject"`
}

type castWikiReply struct {
	XMLName xml.Name      `xml:"result"`
	Docs    []castWikiDoc `xml:"doc"`
}

// castWikiPerson 是详情页 JSON-LD 里我们关心的部分。
type castWikiPerson struct {
	Type          string `json:"@type"`
	Name          string `json:"name"`
	AlternateName string `json:"alternateName"`
	URL           string `json:"url"`
	BirthDate     string `json:"birthDate"`
	Gender        string `json:"gender"`
	BirthPlace    string `json:"birthPlace"`
	Description   string `json:"description"`
}

// Fetch 先站内搜索、再读详情页，返回可并入记录的 CNProfile；取不到返回 nil。
func (c *CastWikiClient) Fetch(ctx context.Context, candidates []string) (*CNProfile, string) {
	if c == nil {
		return nil, ""
	}
	// 每人最多搜 2 个词：站内一次请求就能给出准确 slug，
	// 比按名字挨个试省得多，但候选词再往上翻命中率提升很有限。
	for i, q := range castWikiQueries(candidates) {
		if i >= 2 {
			break
		}
		slug, subject, ok := c.searchSlug(ctx, q, candidates)
		if !ok {
			continue
		}
		if p := c.fetchProfile(ctx, slug, candidates); p != nil {
			return p, subject
		}
	}
	return nil, ""
}

// searchSlug 搜一次，返回 subject 确实指向本人的那条 doc 的 slug。
func (c *CastWikiClient) searchSlug(ctx context.Context, q string, candidates []string) (string, string, bool) {
	u := fmt.Sprintf("%s?q=%s&f=_all&m=&s=relevance&o=0&n=10&xml=yes",
		c.searchURL(), url.QueryEscape(q))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", "", false
	}
	req.Header.Set("User-Agent", castWikiUA)
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", false
	}
	var reply castWikiReply
	if err := xml.Unmarshal(body, &reply); err != nil {
		return "", "", false
	}
	for _, doc := range reply.Docs {
		slug := strings.TrimSpace(doc.Slug)
		subject := strings.TrimSpace(doc.Subject)
		if slug == "" || subject == "" {
			continue
		}
		if !castWikiSubjectMatches(subject, candidates) {
			continue
		}
		return slug, subject, true
	}
	return "", "", false
}

// castWikiSubjectMatches 判断搜索结果的 subject 是否指向本人。
//
// subject 形如「三上悠亜 / Yua Mikami / 三上悠亚」，按「/」切开逐段精确比对。
// 不做包含匹配：站内搜「三上悠亚」会顺带返回小野六花、桃乃木かな这些
// 「提到过她」的人，按包含判断就会串号。
func castWikiSubjectMatches(subject string, candidates []string) bool {
	names := castWikiSubjectNames(subject)
	if len(names) == 0 {
		return false
	}
	if cnNameHit(names, candidates) {
		return true
	}
	for _, n := range names {
		for _, cand := range candidates {
			if sameRomanizedName(n, cand) {
				return true
			}
		}
	}
	return false
}

// castWikiSubjectNames 把 subject 拆成名字列表。
func castWikiSubjectNames(subject string) []string {
	var out []string
	for _, part := range strings.Split(subject, "/") {
		if s := strings.TrimSpace(part); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// castWikiQueries 挑搜索词：站内条目按日文原名 / 中文名收录，含汉字或假名的候选优先，
// 纯拉丁名（罗马字）放后面兜底。去重并保持组内原顺序。
func castWikiQueries(candidates []string) []string {
	var cjk, latin []string
	seen := map[string]bool{}
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if c == "" || IsUUID(c) {
			continue
		}
		k := NormalizeName(c)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		if hasCJK(c) {
			cjk = append(cjk, c)
		} else if hasLatinLetter(c) {
			latin = append(latin, c)
		}
	}
	return append(cjk, latin...)
}

// hasCJK 判断是否含汉字或日文假名。
func hasCJK(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) || (r >= 0x3040 && r <= 0x30FF) {
			return true
		}
	}
	return false
}

// fetchProfile 读详情页，解析 JSON-LD（缺失时退回 HTML 里的 intro 段）。
func (c *CastWikiClient) fetchProfile(ctx context.Context, slug string, candidates []string) *CNProfile {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL()+slug, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", castWikiUA)
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil
	}

	person := parseCastWikiLD(body)
	var raw string
	if person != nil {
		raw = person.Description
	}
	// JSON-LD 缺 description 时用页面上的 intro 段兜底，两者内容相同。
	if strings.TrimSpace(raw) == "" {
		if m := reCastWikiIntro.FindSubmatch(body); len(m) > 1 {
			raw = CleanHTML(string(m[1]))
		}
	}
	// 身份再核一遍：搜索给的 subject 已经对过，但详情页可能被站方合并/重定向到别人。
	if !castWikiPageMatches(person, body, candidates) {
		return nil
	}

	out := &CNProfile{
		SourceURL: c.baseURL() + slug,
	}
	if person != nil {
		out.Romaji = strings.TrimSpace(person.AlternateName)
		out.BirthDate = cnParseDate(person.BirthDate)
		out.Gender = castWikiGender(person.Gender)
		out.BirthPlace = castWikiCNPlace(person.BirthPlace)
		out.ChineseName = castWikiChineseName(body, candidates)
	}
	// 简介：先剥掉开头的「身高: / 罩杯: / 生日: …」属性串，剩下的才是正文。
	// 属性串原样塞进 Overview 会跟 Emby 头部那行五项重复。
	bio := stripCastWikiFields(raw)
	// 没写简介的页面，intro 是一段英文模板（"A curated record of the person behind
	// the screen…"），站上大约一半命中都是它；另有相当一部分页面只有英文简介。
	// 这类一律不收——但生日/身高/三围仍从属性串里抽，那部分不受影响。
	if isCastWikiTemplate(raw) || !LooksChineseBio(bio) {
		bio = ""
	}
	out.Bio = bio

	// 结构化字段都从属性串里抽，抽不到就留空，让别的源去补。
	head := raw
	if len([]rune(head)) > 400 {
		head = string([]rune(head)[:400]) // 属性串只在开头，不必全文扫
	}
	if m := reCastWikiHeight.FindStringSubmatch(head); len(m) > 1 {
		out.HeightCm = castWikiAtoi(m[1])
	}
	if m := reCastWikiBWH.FindStringSubmatch(head); len(m) > 3 {
		out.Bust = castWikiAtoi(m[1])
		out.Waist = castWikiAtoi(m[2])
		out.Hip = castWikiAtoi(m[3])
	} else {
		if m := reCastWikiBust.FindStringSubmatch(head); len(m) > 1 {
			out.Bust = castWikiAtoi(m[1])
		}
		if m := reCastWikiWaist.FindStringSubmatch(head); len(m) > 1 {
			out.Waist = castWikiAtoi(m[1])
		}
		if m := reCastWikiHip.FindStringSubmatch(head); len(m) > 1 {
			out.Hip = castWikiAtoi(m[1])
		}
	}
	if m := reCastWikiCup.FindStringSubmatch(head); len(m) > 1 {
		out.Cup = strings.ToUpper(m[1])
	}
	if m := reCastWikiDebut.FindStringSubmatch(head); len(m) > 1 {
		out.CareerStart = castWikiAtoi(m[1])
	}
	return out
}

// parseCastWikiLD 从页面里取出第一个 Person 类型 JSON-LD。
func parseCastWikiLD(body []byte) *castWikiPerson {
	for _, m := range reCastWikiLD.FindAllSubmatch(body, -1) {
		blob := strings.TrimSpace(string(m[1]))
		if blob == "" {
			continue
		}
		var p castWikiPerson
		if err := json.Unmarshal([]byte(blob), &p); err != nil {
			continue
		}
		if p.Type != "" && !strings.Contains(p.Type, "Person") {
			continue
		}
		return &p
	}
	return nil
}

// castWikiPageMatches 判断详情页是不是本人，**只认站方的主名**。
//
// 主名取 JSON-LD 的 name（如「相沢みなみ」），没有就用标题括号前那段。
// 站方主名对不上候选就是别人的页面，别名里出现我们手上的名字也不算数：
// 实测搜「今井まい」返回的页面主体是「陽木かれん」，搜「矢沢飛鳥」返回的是
// 「雪奈真冬」——这两条都是字典把不同演员的别名混在一起造成的，
// 按别名放行就会把别人的整段经历写进库里。
func castWikiPageMatches(person *castWikiPerson, body []byte, candidates []string) bool {
	var primary string
	if person != nil {
		primary = strings.TrimSpace(person.Name)
	}
	if primary == "" {
		if m := reCastWikiTitle.FindSubmatch(body); len(m) > 1 {
			title := html.UnescapeString(strings.TrimSpace(string(m[1])))
			if i := strings.Index(title, "的个人主页"); i > 0 {
				title = title[:i]
			}
			if l := strings.Index(title, "("); l > 0 {
				primary = strings.TrimSpace(title[:l])
			}
		}
	}
	if primary == "" {
		return false
	}
	if cnNameHit([]string{primary}, candidates) {
		return true
	}
	for _, cand := range candidates {
		if sameRomanizedName(primary, cand) {
			return true
		}
	}
	return false
}

// isCastWikiTemplate 认出「没写简介也给你一段话」的模板文案。
// 它不是任何人的介绍，收进去等于给所有人挂同一句英文。
func isCastWikiTemplate(s string) bool {
	return strings.Contains(s, "A curated record of the person behind") ||
		strings.Contains(s, "CastWiki's people archive")
}

// castWikiChineseName 从标题里取中文艺名。
// 「相沢みなみ(Minami Aizawa,相泽南)」→「相泽南」：括号里逗号后那段是中文写法。
// 取不到就返回空，让 applyCNProfile 保留记录里已有的名字。
func castWikiChineseName(body []byte, candidates []string) string {
	m := reCastWikiTitle.FindSubmatch(body)
	if len(m) < 2 {
		return ""
	}
	title := html.UnescapeString(strings.TrimSpace(string(m[1])))
	l := strings.Index(title, "(")
	r := strings.LastIndex(title, ")")
	if l < 0 || r <= l {
		return ""
	}
	for _, part := range strings.Split(title[l+1:r], ",") {
		part = strings.TrimSpace(part)
		// 汉字名：≥2 个汉字且不含拉丁字母（去掉罗马字那段）。
		if hasLatinLetter(part) || !hasCJK(part) {
			continue
		}
		if len([]rune(part)) >= 2 && len([]rune(part)) <= 8 {
			return part
		}
	}
	return ""
}

// castWikiGender 把站点给的中文性别转成记录里用的写法。
func castWikiGender(s string) string {
	switch strings.TrimSpace(s) {
	case "女", "女性", "female", "Female":
		return "女"
	case "男", "男性", "male", "Male":
		return "男"
	}
	return ""
}

// castWikiCNPlace 只收中文的都道府县，英文地名（"Tokyo, Japan"）一律丢掉。
// Emby 的 ProductionLocations 里写英文会把中英混在一起，宁可空着等别的源补。
func castWikiCNPlace(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.TrimPrefix(s, "日本·")
	s = strings.TrimPrefix(s, "日本")
	s = strings.TrimSpace(s)
	if hasLatinLetter(s) {
		return ""
	}
	s = normalizeJPPlace(s)
	if !isJPPrefecture(s) {
		return ""
	}
	return s
}

// stripCastWikiFields 剥掉简介开头的「字段: 值 字段: 值 …」属性串，只留正文。
//
// 站上的 intro 有四种开头，都要照顾到：
//
//	身高: 155cm 罩杯: D 胸围: 84cm 腰围: 57cm 臀围: 82cm 爱好: 料理、マッサージ 三上悠亚，原名…
//	出生:1992年11月30日 身高:161cm 三围:82 - 59 - 86 cm 罩杯:C  兴趣是逛街…
//	相泽南（相沢 みなみ.Minami Aizawa）生日：1996年6月14日 身高／体重：155 厘米 / 43千克 …
//	桃乃木かな（日文名：桃乃木 かな，出生于1996年12月24日）是日本知名的AV女优…   ← 直接正文
//
// 做法：反复找「字段标签+冒号」的位置。标签在开头就跳过它和它的值（值到下一个标签为止）；
// 一旦出现别的内容就认为正文开始。带「名字（罗马字）」前缀的，剥掉前缀继续。
func stripCastWikiFields(s string) string {
	s = strings.TrimSpace(s)
	for i := 0; i < 32; i++ { // 属性串一般不超过 10 段，32 是防呆上限
		loc := reCastWikiFieldLabel.FindStringIndex(s)
		if loc == nil {
			return strings.TrimSpace(s)
		}
		prefix := strings.TrimSpace(s[:loc[0]])
		if prefix != "" {
			// 「相泽南（相沢 みなみ.Minami Aizawa）生日：…」：名号前缀带上右括号，
			// 后面跟的是属性串，剥掉它继续。括号没闭合的（"桃乃木かな（日文名：…"）
			// 说明标签就在正文句子里，整段保留。
			if strings.HasSuffix(prefix, "）") || strings.HasSuffix(prefix, ")") {
				s = s[loc[0]:]
				continue
			}
			return strings.TrimSpace(s)
		}
		rest := s[loc[1]:]
		if next := reCastWikiFieldLabel.FindStringIndex(rest); next != nil {
			val := strings.TrimSpace(rest[:next[0]])
			// 这一段值是整句话就说明标签落在正文里（「…，出道时间：2014年」），
			// 不能再往下剥——再剥就把正文吃掉了。
			if looksLikeProse(val) {
				if tail := castWikiBioTail(val); tail != "" {
					return tail
				}
				return val
			}
			s = rest[next[0]:]
			continue
		}
		// 最后一个字段：它的值很短（"C" / "2016年（2023年6月退役）"），
		// 正文用空格隔在后面。
		return castWikiBioTail(rest)
	}
	return strings.TrimSpace(s)
}

// looksLikeProse 判断一段文本是不是整句话而不是属性值。
// 属性值都很短（"155cm" / "C" / "2016年（2023年6月退役）"），够长且带句读的只能是正文。
func looksLikeProse(s string) bool {
	return len([]rune(s)) >= 24 && strings.ContainsAny(s, "，。")
}

// castWikiBioTail 在「最后一个字段值 + 正文」这段里切出正文。
//
// 站上的值和正文之间用空格或换行（\r\n）隔开，逐切点试：
// 切出来的尾巴够长、带中文句读，且**切点前的值很短**，才算正文起点。
// 最后一个条件不能少：正文里也有空格（「所属于“Fourty Four Management”」），
// 只看「后面够长」会把正文当成字段值丢掉，切出来只剩半句。
func castWikiBioTail(s string) string {
	s = strings.TrimSpace(s)
	for i, r := range s {
		if r != ' ' && r != '\u3000' && r != '\n' && r != '\r' {
			continue
		}
		head := strings.TrimSpace(s[:i])
		tail := strings.TrimSpace(s[i:])
		if len([]rune(tail)) < 24 || !looksLikeProse(tail) {
			continue
		}
		if len([]rune(head)) > 40 {
			return s // 切点前已经是一整段：正文从开头就开始了，不切
		}
		return tail
	}
	return ""
}

// castWikiAtoi 只用于解析页面上的数字串，解析不出来就当没有。
func castWikiAtoi(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}

func (c *CastWikiClient) describe() string {
	return fmt.Sprintf("castwiki(%s)", castWikiBase)
}
