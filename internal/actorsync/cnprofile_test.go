package actorsync

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCNParseHelpers(t *testing.T) {
	if got := cnParseDate("1993 年 8 月 16 日"); got != "1993-08-16" {
		t.Fatalf("cnParseDate(zh) = %q", got)
	}
	if got := cnParseDate("2002-06-16"); got != "2002-06-16" {
		t.Fatalf("cnParseDate(iso) = %q", got)
	}
	if got := cnParseDate("1993"); got != "" {
		t.Fatalf("cnParseDate(year only) = %q", got)
	}
	if got := cnParseYear("2024出道年龄"); got != 2024 {
		t.Fatalf("cnParseYear = %d", got)
	}
	if got := cnParseHeight("164 CM"); got != 164 {
		t.Fatalf("cnParseHeight = %d", got)
	}
	b, w, h, cup := cnParseMeasurements("B92(Hcup)・W59・H89")
	if b != 92 || w != 59 || h != 89 || cup != "H" {
		t.Fatalf("cnParseMeasurements = %d/%d/%d %q", b, w, h, cup)
	}
	if cnNameHit([]string{"彩月七緒"}, []string{"彩月七绪"}) {
		t.Fatal("简繁不同字不该算命中")
	}
}

func TestYinrenluFetch(t *testing.T) {
	search := `<div class="people-grid"><article class="person-card with-portrait">
	<a href="/people/q22128111" class="person-portrait"></a><div><p class="kicker">日本</p>
	<h3><a href="/people/q22128111">三上悠亚</a></h3><p class="original-name">三上悠亜</p>
	</div></article></div>`
	detail := `<h1 id="person-title">三上悠亚</h1><p class="person-native">三上悠亜</p>
	<section class="profile-panel" id="overview"><p class="kicker">PROFILE SNAPSHOT</p><h2>个人简介</h2>
	<p>三上悠亚（三上悠亜），出生于 1993 年。公开人物资料记录了其成人影视表演职业经历。</p>
	<p>职业活动结束年份记录为 2023 年；姓名另有 Yua Mikami 等公开写法，可用于对照条目。</p>
	</section>
	<dl class="profile-table">
	<div><dt>中文艺名</dt><dd>三上悠亚</dd></div>
	<div><dt>原文艺名</dt><dd>三上悠亜</dd></div>
	<div><dt>罗马字写法</dt><dd>Yua Mikami</dd></div>
	<div><dt>其他艺名</dt><dd>三上悠亜、Yua Mikami、三上悠亞</dd></div>
	<div><dt>出生日期</dt><dd>1993 年 8 月 16 日</dd></div>
	<div><dt>出道年份</dt><dd>2015 年</dd></div>
	</dl>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/search"):
			fmt.Fprint(w, search)
		case strings.HasPrefix(r.URL.Path, "/people/"):
			fmt.Fprint(w, detail)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := &YinrenluClient{HTTP: srv.Client(), Base: srv.URL}
	p, matched := c.Fetch(context.Background(), []string{"三上悠亚", "三上悠亜"})
	if p == nil || matched != "三上悠亚" {
		t.Fatalf("Fetch = %+v, %q", p, matched)
	}
	if p.BirthDate != "1993-08-16" || p.CareerStart != 2015 {
		t.Fatalf("parsed fields: %+v", p)
	}
	if p.ChineseName != "三上悠亚" || p.Romaji != "Yua Mikami" {
		t.Fatalf("names: %+v", p)
	}
	if !strings.Contains(p.Bio, "职业活动结束年份") {
		t.Fatalf("bio: %q", p.Bio)
	}
}

func TestCinewhoFetch(t *testing.T) {
	search := `<div class="r-item"><div class="r-text"><dt>
	<a href="model/satsukinao"><h4>1. <em>彩月</em><em>七緒</em>-彩月七绪-Satsuki Nao <small>[100%]</small></h4></a>
	</dt></div></div>`
	detail := `<script type="application/ld+json">
	{"@type":"Person","name":"彩月七緒","gender":"Female","birthDate":"2002-06-16",
	 "nationality":"日本","height":"164 cm","image":"https://example.com/a.jpg",
	 "description":"21岁（2024出道年龄）的彩月七绪（Satsuki Nao），以她柔和的笑容、H罩杯和纤细身材成为AV界的超级新星。"}
	</script>
	<div class="profile-info"><div class="details-items">
	<div class="info-item"><strong>生日:</strong>2002-06-16  (24岁)</div>
	<div class="info-item"><strong>身高:</strong>164 CM</div>
	<div class="info-item"><strong>三围:</strong>B92(Hcup)・W59・H89</div>
	<div class="info-item"><strong>中文名:</strong>彩月七绪</div>
	<div class="info-item"><strong>英文名:</strong>Satsuki Nao</div>
	</div></div>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/search.php"):
			fmt.Fprint(w, search)
		case strings.HasPrefix(r.URL.Path, "/cine/"):
			fmt.Fprint(w, detail)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := &CinewhoClient{HTTP: srv.Client(), Base: srv.URL}
	p, matched := c.Fetch(context.Background(), []string{"彩月七緒"})
	if p == nil || matched != "彩月七緒" {
		t.Fatalf("Fetch = %+v, %q", p, matched)
	}
	if p.BirthDate != "2002-06-16" || p.HeightCm != 164 || p.Cup != "H" {
		t.Fatalf("parsed fields: %+v", p)
	}
	if p.Gender != "female" || p.ChineseName != "彩月七绪" {
		t.Fatalf("gender/name: %+v", p)
	}
	if !validCNBio(p.Bio) {
		t.Fatalf("bio rejected: %q", p.Bio)
	}
}

func TestIdolDBFetch(t *testing.T) {
	profile := `{"name":"波多野結衣","name_zh":"波多野结衣","roman_name":"Hatano Yui",
	"aliases":["リン・チーリン"],"birthdate":"1988-05-24","height":163,
	"bust":88,"waist":59,"hips":94,"cup":"G","debut_date":"2008-06-03",
	"bio":"2008年以MOODYZ专属身份出道，隶属T-POWERS事务所，以清纯治愈形象与读书、料理等爱好为人知。"}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/profile.json") {
			fmt.Fprint(w, profile)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c := &IdolDBClient{HTTP: srv.Client(), Base: srv.URL + "/actresses/"}
	p, matched := c.Fetch(context.Background(), []string{"波多野結衣"})
	if p == nil || matched != "波多野結衣" {
		t.Fatalf("Fetch = %+v, %q", p, matched)
	}
	if p.BirthDate != "1988-05-24" || p.CareerStart != 2008 || p.ChineseName != "波多野结衣" {
		t.Fatalf("parsed fields: %+v", p)
	}
	if p.Cup != "G" || p.Bust != 88 {
		t.Fatalf("measurements: %+v", p)
	}
}

func TestFetcherApplyCNProfileOnlyFillsBlanks(t *testing.T) {
	f := NewFetcher(nil)
	res := &FetchResult{
		BirthDate:       "1996-12-24",
		BirthDateSource: "javstash",
		HeightCm:        160,
		Bio:             "维基给的一句话简介",
	}
	f.applyCNProfile(res, "yinrenlu", &CNProfile{
		Bio:         "这是一段足够长的中文人物简介，描述了她从出道到引退的完整经历，超过二十个字。",
		BirthDate:   "1993-08-16",
		HeightCm:    155,
		ChineseName: "三上悠亚",
		CareerStart: 2015,
	})
	if res.BirthDate != "1996-12-24" || res.BirthDateSource != "javstash" {
		t.Fatalf("不应覆盖 JavStash 生日：%+v", res)
	}
	if res.HeightCm != 160 {
		t.Fatalf("不应覆盖身高：%d", res.HeightCm)
	}
	if res.CareerStart != 2015 || res.ChineseName != "三上悠亚" {
		t.Fatalf("空位没补上：%+v", res)
	}
	if !strings.Contains(res.Bio, "完整经历") {
		t.Fatalf("更长的简介应替换短简介：%q", res.Bio)
	}
}
