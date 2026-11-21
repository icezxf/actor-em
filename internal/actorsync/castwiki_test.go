package actorsync

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// 搜索的 subject 用「/」分段，逐段精确比对：站内搜一个名字会顺带返回
// 「提到过她」的其他人，按包含匹配就会串号。
func TestCastWikiSubjectMatches(t *testing.T) {
	cases := []struct {
		subject string
		cands   []string
		want    bool
		reason  string
	}{
		{"三上悠亜 / Yua Mikami / 三上悠亚", []string{"三上悠亜"}, true, "日文名一致"},
		{"三上悠亜 / Yua Mikami / 三上悠亚", []string{"三上悠亚"}, true, "中文名一致"},
		{"三上悠亜 / Yua Mikami / 三上悠亚", []string{"Yua Mikami"}, true, "罗马字词序不同也算"},
		{"小野六花 / Rikka Ono", []string{"三上悠亜"}, false, "只在简介里提到不算本人"},
		{"桃乃木かな / Kana Momonogi / 桃乃木香奈", []string{"坂田もも"}, false, "同名别人要拒"},
		{"", []string{"三上悠亜"}, false, "空 subject 不能放行"},
	}
	for _, tc := range cases {
		if got := castWikiSubjectMatches(tc.subject, tc.cands); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.reason, got, tc.want)
		}
	}
}

// 简介开头的属性串有四种写法，剥完都得是正文。
func TestStripCastWikiFields(t *testing.T) {
	cases := []struct {
		in     string
		prefix string // 结果必须以它开头
		reason string
	}{
		{
			"身高: 155cm 罩杯: D 胸围: 84cm 腰围: 57cm 臀围: 82cm 爱好: 料理、マッサージ 三上悠亚，原名鬼头桃菜，1993年8月16日出生于日本爱知县名古屋市，前女子偶像团体SKE48成员。",
			"三上悠亚，原名鬼头桃菜",
			"空格分隔的字段串",
		},
		{
			"出生:1992年11月30日 身高:161cm 三围:82 - 59 - 86 cm 罩杯:C  兴趣是逛街跟喝下午茶，喜欢温柔且技术好的男生，平常看男生第一眼会先注意肌肉线条。",
			"兴趣是逛街跟喝下午茶",
			"双空格分隔 + 三围带空格",
		},
		{
			"相泽南（相沢 みなみ.Minami Aizawa）生日：1996年6月14日 身高／体重：155 厘米 / 43千克 三围：80 - 54 - 82 罩杯：C 出道时间：2016年（2023年6月退役） 相泽南出生于香港，在富裕家庭中成长，因为父亲经商的关系曾在美国洛杉矶居住。",
			"相泽南出生于香港",
			"名号前缀 + 全角冒号",
		},
		{
			"桃乃木かな（日文名：桃乃木 かな，出生于1996年12月24日）是日本知名的AV女优、歌手，隶属于ライフプロモーション经纪公司。",
			"桃乃木かな（日文名：",
			"括号没闭合时标签在正文里，整段保留",
		},
		{
			"身高: 155cm 罩杯: D 胸围: 84cm",
			"",
			"整段都是属性串时要返回空",
		},
	}
	for _, tc := range cases {
		got := stripCastWikiFields(tc.in)
		if tc.prefix == "" {
			if got != "" {
				t.Errorf("%s: 期望空，得到 %q", tc.reason, got)
			}
			continue
		}
		if !strings.HasPrefix(got, tc.prefix) {
			t.Errorf("%s: 期望以 %q 开头，得到 %q", tc.reason, tc.prefix, got)
		}
		if strings.HasPrefix(got, "身高") || strings.HasPrefix(got, "出生") {
			t.Errorf("%s: 属性串没剥干净：%q", tc.reason, got)
		}
	}
}

// 站点给的是英文地名（"Tokyo, Japan"），Emby 里写英文会中英混杂，直接丢。
func TestCastWikiCNPlace(t *testing.T) {
	cases := map[string]string{
		"日本神奈川县":       "神奈川县",
		"東京都":          "东京都",
		"Tokyo, Japan": "",
		"":             "",
		"日本":           "",
	}
	for in, want := range cases {
		if got := castWikiCNPlace(in); got != want {
			t.Errorf("castWikiCNPlace(%q)=%q want %q", in, got, want)
		}
	}
}

// 搜索词先挑含汉字/假名的候选：站内条目按原名收录，纯罗马字命中率低。
func TestCastWikiQueriesPrefersCJK(t *testing.T) {
	got := castWikiQueries([]string{"Yua Mikami", "三上悠亜", "三上悠亚", "", "三上 悠亜"})
	if len(got) < 2 || got[0] != "三上悠亜" || got[1] != "三上悠亚" {
		t.Fatalf("汉字候选应排前面且去重，得到 %v", got)
	}
	if last := got[len(got)-1]; last != "Yua Mikami" {
		t.Fatalf("拉丁候选应排最后，得到 %q", last)
	}
}

// 端到端：搜索拿到 slug、读详情页解析 JSON-LD，简介剥完后是正文，
// 结构化字段从属性串里抽出来。
func TestCastWikiFetchParsesProfile(t *testing.T) {
	const searchXML = `<?xml version="1.0" encoding="UTF-8" ?>
<xs:result count="2" total="100">
  <doc index="1" percent="100%">
    <slug><![CDATA[yuamikami]]></slug>
    <subject><![CDATA[三上悠亜 / Yua Mikami / 三上悠亚]]></subject>
  </doc>
  <doc index="2" percent="78%">
    <slug><![CDATA[rikkaono]]></slug>
    <subject><![CDATA[小野六花 / Rikka Ono]]></subject>
  </doc>
</xs:result>`

	profile := `<!DOCTYPE html><html><head>
<title>三上悠亜(Yua Mikami,三上悠亚)的个人主页,资料信息,联系方式 - 卡司Wiki</title>
<script type="application/ld+json">{"@context":"https://schema.org","@type":"Person",
"name":"三上悠亜","url":"https://www.castwiki.org/profile/yuamikami",
"alternateName":"Yua Mikami","birthDate":"1993-08-16","gender":"女",
"birthPlace":"Nagoya, Aichi Prefecture, Japan",
"description":"身高: 155cm 罩杯: D 胸围: 84cm 腰围: 57cm 臀围: 82cm 爱好: 料理、マッサージ 三上悠亚，原名鬼头桃菜，1993年8月16日出生于日本爱知县名古屋市，前女子偶像团体SKE48 Team S的成员，AV演员。 演艺经历 2006年7月参加早安家族试听会。出道时间：2014年"}
</script></head><body></body></html>`

	var gotSearch, gotProfile int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/search.php"):
			gotSearch++
			w.Header().Set("Content-Type", "application/xml")
			fmt.Fprint(w, searchXML)
		case strings.Contains(r.URL.Path, "/profile/"):
			gotProfile++
			fmt.Fprint(w, profile)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewCastWikiClient(srv.Client())
	c.Base = srv.URL + "/profile/"
	c.Search = srv.URL + "/search.php"

	p, matched := c.Fetch(context.Background(), []string{"三上悠亜", "Yua Mikami"})
	if p == nil {
		t.Fatalf("应该命中（搜索 %d 次、详情 %d 次）", gotSearch, gotProfile)
	}
	if matched != "三上悠亜 / Yua Mikami / 三上悠亚" {
		t.Errorf("matched=%q", matched)
	}
	if !strings.HasPrefix(p.Bio, "三上悠亚，原名鬼头桃菜") {
		t.Errorf("简介没剥干净：%q", p.Bio)
	}
	if strings.Contains(p.Bio, "罩杯") {
		t.Errorf("简介里不该残留属性串：%q", p.Bio)
	}
	if p.BirthDate != "1993-08-16" {
		t.Errorf("生日=%q", p.BirthDate)
	}
	if p.HeightCm != 155 || p.Cup != "D" || p.Bust != 84 || p.Waist != 57 || p.Hip != 82 {
		t.Errorf("三围/身高/罩杯解析错：%d %s %d %d %d", p.HeightCm, p.Cup, p.Bust, p.Waist, p.Hip)
	}
	if p.CareerStart != 2014 {
		t.Errorf("出道年=%d", p.CareerStart)
	}
	if p.BirthPlace != "" {
		t.Errorf("英文出生地应丢弃，得到 %q", p.BirthPlace)
	}
	if p.ChineseName != "三上悠亚" {
		t.Errorf("中文名=%q", p.ChineseName)
	}
}

// 搜索第一条是别人（站内搜一个名字会返回一堆提到她的人）时不能串号。
func TestCastWikiRejectsOtherPerson(t *testing.T) {
	const searchXML = `<?xml version="1.0" encoding="UTF-8" ?>
<result>
  <doc index="1" percent="100%"><slug><![CDATA[rikkaono]]></slug>
    <subject><![CDATA[小野六花 / Rikka Ono]]></subject></doc>
</result>`
	var profileHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/search.php") {
			fmt.Fprint(w, searchXML)
			return
		}
		profileHits++
		fmt.Fprint(w, `<html><head><title>小野六花(Rikka Ono)的个人主页</title></head><body></body></html>`)
	}))
	defer srv.Close()

	c := NewCastWikiClient(srv.Client())
	c.Base = srv.URL + "/profile/"
	c.Search = srv.URL + "/search.php"

	if p, _ := c.Fetch(context.Background(), []string{"三上悠亜"}); p != nil {
		t.Fatalf("不应该命中别人，得到 %+v", p)
	}
	if profileHits != 0 {
		t.Errorf("名字对不上时不该去读详情页，实际读了 %d 次", profileHits)
	}
}

// 真实站点冒烟：默认跳过，设 CASTWIKI_LIVE=1 才跑。
// 只用来人工核对解析有没有跟着站点改版走样，不参与 CI。
func TestCastWikiLive(t *testing.T) {
	if os.Getenv("CASTWIKI_LIVE") == "" {
		t.Skip("设 CASTWIKI_LIVE=1 才跑真实站点")
	}
	c := NewCastWikiClient(nil)
	for _, names := range [][]string{
		{"三上悠亜", "Yua Mikami"},
		{"桃乃木かな", "Kana Momonogi"},
		{"相沢みなみ", "Minami Aizawa"},
		{"山岸あや花", "Ayaka Yamagishi"},
	} {
		p, matched := c.Fetch(context.Background(), names)
		if p == nil {
			t.Logf("%v → 未命中", names)
			continue
		}
		bio := []rune(p.Bio)
		head := string(bio)
		if len(bio) > 60 {
			head = string(bio[:60])
		}
		t.Logf("%v → %s | bio %d 字 | %s | %s | %dcm %s", names, matched, len(bio),
			p.BirthDate, p.BirthPlace, p.HeightCm, head)
	}
}

// 站方主名对不上候选就不是本人：别名里出现我们手上的名字也不算。
// 实测搜「今井まい」返回的主体是「陽木かれん」，搜「矢沢飛鳥」返回的是「雪奈真冬」，
// 都是字典把不同演员的别名混在一起，按别名放行会写进别人的经历。
func TestCastWikiPageMatchesPrimaryNameOnly(t *testing.T) {
	page := []byte(`<html><head><title>陽木かれん(Karen Aoki,Mai Imai)的个人主页,资料 - 卡司Wiki</title></head><body></body></html>`)
	person := &castWikiPerson{Type: "Person", Name: "陽木かれん", AlternateName: "Mai Imai"}

	if castWikiPageMatches(person, page, []string{"今井まい", "Mai Imai"}) {
		t.Error("主体是别人时，别名一致也不能放行")
	}
	if !castWikiPageMatches(person, page, []string{"陽木かれん"}) {
		t.Error("主名一致应该放行")
	}
	// 标题兜底：JSON-LD 缺失时用括号前那段当主名
	if castWikiPageMatches(nil, page, []string{"今井まい"}) {
		t.Error("JSON-LD 缺失时也不能按别名放行")
	}
	if !castWikiPageMatches(nil, page, []string{"陽木かれん"}) {
		t.Error("JSON-LD 缺失时应退回标题主名")
	}
}

// 没写简介的页面给的是英文模板文案，不能当成简介收下；生日/三围仍要知道。
func TestCastWikiSkipsTemplateBio(t *testing.T) {
	const searchXML = `<?xml version="1.0" encoding="UTF-8" ?>
<result>
  <doc index="1" percent="100%"><slug><![CDATA[serizawayuzu]]></slug>
    <subject><![CDATA[Serizawa Yuzu]]></subject></doc>
</result>`
	profile := `<html><head><title>芹沢ゆず(Serizawa Yuzu)的个人主页 - 卡司Wiki</title>
<script type="application/ld+json">{"@type":"Person","name":"芹沢ゆず",
"alternateName":"Serizawa Yuzu","birthDate":"1998-03-04","birthPlace":"北海道"}</script>
</head><body><p class="person-profile-intro">A curated record of the person behind the screen, assembled from CastWiki's people archive.</p></body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/search.php") {
			fmt.Fprint(w, searchXML)
			return
		}
		fmt.Fprint(w, profile)
	}))
	defer srv.Close()

	c := NewCastWikiClient(srv.Client())
	c.Base = srv.URL + "/profile/"
	c.Search = srv.URL + "/search.php"

	p, _ := c.Fetch(context.Background(), []string{"芹沢ゆず", "Serizawa Yuzu"})
	if p == nil {
		t.Fatal("应命中本人")
	}
	if p.Bio != "" {
		t.Errorf("模板文案不该当简介：%q", p.Bio)
	}
	if p.BirthDate != "1998-03-04" || p.BirthPlace != "北海道" {
		t.Errorf("结构化字段应照收：%q %q", p.BirthDate, p.BirthPlace)
	}
}

// 别名污染：字典把「矢沢飛鳥 / 西山楓 / 雪奈真冬」归成一个人，候选列表里
// 三个名字都在。不带清单名时，「矢沢飛鳥」会因为候选里有「雪奈真冬」而认下
// 雪奈真冬的页面；传清单名就切断这条路。
func TestCastWikiFetchForUsesPrimaryName(t *testing.T) {
	const searchXML = `<?xml version="1.0" encoding="UTF-8" ?>
<result>
  <doc index="1" percent="100%"><slug><![CDATA[yukinamafuyu]]></slug>
    <subject><![CDATA[Yukina Mafuyu]]></subject></doc>
</result>`
	profile := `<html><head><title>雪奈真冬(Yukina Mafuyu)的个人主页 - 卡司Wiki</title>
<script type="application/ld+json">{"@type":"Person","name":"雪奈真冬",
"alternateName":"Yukina Mafuyu","birthDate":"1995-02-14","birthPlace":"东京都",
"description":"雪奈真冬，1995年出生于东京都，2016年以专属身份出道，擅长舞蹈，"
+"后来转战职业摔角，成为 Fighting Girls International 的一员。"}</script></head><body></body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/search.php") {
			fmt.Fprint(w, searchXML)
			return
		}
		fmt.Fprint(w, profile)
	}))
	defer srv.Close()

	c := NewCastWikiClient(srv.Client())
	c.Base = srv.URL + "/profile/"
	c.Search = srv.URL + "/search.php"

	// 字典里「矢沢飛鳥」的别名含「雪奈真冬」，不带清单名时会被认成同一个人
	if p, _ := c.Fetch(context.Background(), []string{"矢沢飛鳥", "雪奈真冬", "Yukina Mafuyu"}); p == nil {
		t.Fatal("不带清单名时应按候选名命中（这是已知的别名污染路径）")
	}
	// 带清单名后必须拒
	if p, _ := c.FetchFor(context.Background(), []string{"矢沢飛鳥", "雪奈真冬", "Yukina Mafuyu"}, "矢沢飛鳥"); p != nil {
		t.Fatalf("清单名与站方主名不符应拒绝，得到 %+v", p)
	}
	// 本人仍然能命中
	if p, _ := c.FetchFor(context.Background(), []string{"雪奈真冬", "Yukina Mafuyu"}, "雪奈真冬"); p == nil {
		t.Fatal("清单名一致时应命中")
	}
}
