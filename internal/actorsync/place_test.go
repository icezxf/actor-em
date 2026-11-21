package actorsync

import "testing"

func TestQualifyBirthPlace(t *testing.T) {
	cases := []struct {
		place, country, want string
	}{
		// 都道府县 → 补国家
		{"东京都", "JP", "日本·东京都"},
		{"神奈川县", "JP", "日本·神奈川县"},
		{"北海道", "JP", "日本·北海道"},
		// 已经没有国家码，但认得出是日本
		{"大阪府", "", "日本·大阪府"},
		{"東京都", "", "日本·东京都"},
		{"爱知県", "", "日本·爱知县"},
		{"东京", "", "日本·东京都"},
		// 更细的写法
		{"东京都新宿区", "JP", "日本·东京都新宿区"},
		// 本来就只到国家，不重复
		{"日本", "JP", "日本"},
		{"美国", "US", "美国"},
		// 已经带国名，幂等
		{"日本·东京都", "JP", "日本·东京都"},
		{"日本东京都", "", "日本东京都"},
		// 非日本
		{"加利福尼亚州", "US", "美国·加利福尼亚州"},
		{"首尔", "KR", "韩国·首尔"},
		{"巴黎", "FR", "法国·巴黎"},
		// 认不出国家也认不出都道府县：原样
		{"某地", "", "某地"},
		{"", "JP", ""},
		{"  ", "JP", ""},
		// 港澳台：国家名本身已是省级，不再叠一层
		{"台湾省", "TW", "中国台湾"},
		{"香港", "HK", "中国香港"},
		{"澳门", "MO", "中国澳门"},
	}
	for _, c := range cases {
		if got := QualifyBirthPlace(c.place, c.country); got != c.want {
			t.Errorf("QualifyBirthPlace(%q, %q) = %q, want %q", c.place, c.country, got, c.want)
		}
	}
}

func TestIsJPPrefecture(t *testing.T) {
	yes := []string{"东京都", "東京都", "大阪府", "京都府", "北海道", "冲绳县", "沖縄県", "东京都新宿区"}
	for _, p := range yes {
		if !isJPPrefecture(p) {
			t.Errorf("isJPPrefecture(%q) = false, want true", p)
		}
	}
	no := []string{"", "日本", "首尔", "加利福尼亚州", "京都市"}
	for _, p := range no {
		if isJPPrefecture(p) {
			t.Errorf("isJPPrefecture(%q) = true, want false", p)
		}
	}
}

func TestQualifyActorBirthPlace(t *testing.T) {
	// 老库存的是「东京都」，补国家并顺手补 Country
	a := &Actor{Name: "x", BirthPlace: StrPtr("东京都")}
	if !QualifyActorBirthPlace(a) {
		t.Fatal("should change")
	}
	if StrVal(a.BirthPlace) != "日本·东京都" || StrVal(a.Country) != "JP" {
		t.Fatalf("got place=%q country=%q", StrVal(a.BirthPlace), StrVal(a.Country))
	}
	// 幂等
	if QualifyActorBirthPlace(a) {
		t.Fatal("second run should be no-op")
	}
	// 只有国家的记录不动
	b := &Actor{BirthPlace: StrPtr("日本"), Country: StrPtr("JP")}
	if QualifyActorBirthPlace(b) {
		t.Fatal("country-level place should be no-op")
	}
	// 空值不动
	if QualifyActorBirthPlace(&Actor{}) {
		t.Fatal("empty should be no-op")
	}
}

func TestFinalizeBirthPlace(t *testing.T) {
	// 有都道府县但缺国家码：顺手把 Country 补成 JP
	r := &FetchResult{BirthPlace: "神奈川县"}
	r.finalizeBirthPlace()
	if r.BirthPlace != "日本·神奈川县" || r.Country != "JP" {
		t.Fatalf("got place=%q country=%q", r.BirthPlace, r.Country)
	}
	// 空出生地：Country 不动
	r2 := &FetchResult{}
	r2.finalizeBirthPlace()
	if r2.BirthPlace != "" || r2.Country != "" {
		t.Fatalf("got place=%q country=%q", r2.BirthPlace, r2.Country)
	}
	// 只有国家：幂等
	r3 := &FetchResult{BirthPlace: "日本", Country: "JP"}
	r3.finalizeBirthPlace()
	if r3.BirthPlace != "日本" {
		t.Fatalf("got place=%q", r3.BirthPlace)
	}
}
