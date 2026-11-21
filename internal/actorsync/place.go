package actorsync

import "strings"

// 出生地最终写进 Emby 的 ProductionLocations，光有「东京都」看不出是哪国，
// 所以落库前统一补成「日本·东京都」。国家只到国家级（「日本」「美国」）时原样留着。
//
// 优先级：Country 码（MDCx 从出生地推的、或 JavStash/TPDB 给的）→ 认得出的日本都道府县。
const birthPlaceSep = "·"

// jpPrefectures 是日本 47 个都道府县的简体写法。只用来做「没有国家码也能补日本」，
// 以及给 Country 反推 JP。
var jpPrefectures = map[string]bool{
	"北海道": true, "青森县": true, "岩手县": true, "宫城县": true, "秋田县": true,
	"山形县": true, "福岛县": true, "茨城县": true, "栃木县": true, "群马县": true,
	"埼玉县": true, "千叶县": true, "东京都": true, "神奈川县": true, "新潟县": true,
	"富山县": true, "石川县": true, "福井县": true, "山梨县": true, "长野县": true,
	"岐阜县": true, "静冈县": true, "爱知县": true, "三重县": true, "滋贺县": true,
	"京都府": true, "大阪府": true, "兵库县": true, "奈良县": true, "和歌山县": true,
	"鸟取县": true, "岛根县": true, "冈山县": true, "广岛县": true, "山口县": true,
	"德岛县": true, "香川县": true, "爱媛县": true, "高知县": true, "福冈县": true,
	"佐贺县": true, "长崎县": true, "熊本县": true, "大分县": true, "宫崎县": true,
	"鹿儿岛县": true, "冲绳县": true,
}

// jpPlaceVariants 把繁体/日文新字体压成简体，再交给 jpPrefectures 比对。
// 中文人物站给的出生地偶尔是繁体（東京都）或半日文写法（愛知県）。
var jpPlaceVariants = strings.NewReplacer(
	"東", "东", "縣", "县", "県", "县", "廣", "广", "島", "岛", "岡", "冈", "靜", "静",
	"愛", "爱", "濱", "滨", "﨑", "崎", "驛", "驿", "櫻", "樱", "櫪", "栃",
	"沖", "冲", "縄", "绳",
)

// jpPlaceAlias 是常见简称。只做「等于」映射，避免把「东京都新宿区」这类
// 已经带都道府县的写法再替换一次。
var jpPlaceAlias = map[string]string{
	"东京": "东京都", "大阪": "大阪府", "京都": "京都府", "北海道": "北海道",
}

// normalizeJPPlace 把繁体/日文新字体压成简体，并把「东京」「大阪」这类简称
// 补成「东京都」「大阪府」，保证同一个人不会出现两种写法。
func normalizeJPPlace(place string) string {
	p := jpPlaceVariants.Replace(strings.TrimSpace(place))
	if full, ok := jpPlaceAlias[p]; ok {
		return full
	}
	return p
}

// isJPPrefecture 判断出生地是不是日本都道府县级（含「东京都新宿区」这类更细的写法）。
func isJPPrefecture(place string) bool {
	p := normalizeJPPlace(place)
	if p == "" {
		return false
	}
	for pref := range jpPrefectures {
		if strings.HasPrefix(p, pref) {
			return true
		}
	}
	return false
}

// QualifyBirthPlace 给出生地补上国家名，输出形如「日本·东京都」。
// 已经是国家级（「日本」）或已经带国名的不重复拼；国家码认不出来时，
// 认得出是日本都道府县就补日本，否则原样返回。
func QualifyBirthPlace(place, country string) string {
	place = normalizeJPPlace(place)
	if place == "" {
		return ""
	}
	name := strings.TrimSpace(countryToChinese[strings.ToUpper(strings.TrimSpace(country))])
	if name == "" && isJPPrefecture(place) {
		name = "日本"
	}
	if name == "" {
		return place
	}
	if place == name || strings.Contains(place, name) {
		return place
	}
	// 「中国台湾 / 中国香港 / 中国澳门」的国家名本身已经是省份层级，
	// 再拼一次会变成「中国台湾·台湾省」这种废话。
	if regionOnly(place) && strings.HasPrefix(name, "中国") {
		return name
	}
	return name + birthPlaceSep + place
}

// regionOnly 判断出生地是不是只有省/地区名，没有更细的层级。
func regionOnly(place string) bool {
	switch strings.TrimSpace(place) {
	case "台湾", "台湾省", "台灣", "香港", "香港特别行政区", "澳门", "澳門", "澳门特别行政区":
		return true
	}
	return false
}

// finalizeBirthPlace 在抓取结束时定稿出生地，并把缺的国家码补上。
func (r *FetchResult) finalizeBirthPlace() {
	place := strings.TrimSpace(r.BirthPlace)
	if place == "" {
		return
	}
	if r.Country == "" && isJPPrefecture(place) {
		r.Country = "JP"
	}
	r.BirthPlace = QualifyBirthPlace(place, r.Country)
}

// QualifyActorBirthPlace 把已落库记录的出生地补成「国家·都道府县」，
// 顺手补上缺的国家码。返回是否真的改了值（幂等，可反复跑）。
//
// 给 --backfill 用：老库里存的是「东京都」，为了这一个字段重抓两万条不值当。
func QualifyActorBirthPlace(a *Actor) bool {
	if a == nil {
		return false
	}
	place := StrVal(a.BirthPlace)
	if place == "" {
		return false
	}
	country := StrVal(a.Country)
	if country == "" && isJPPrefecture(place) {
		country = "JP"
		a.Country = StrPtr(country)
	}
	fixed := QualifyBirthPlace(place, country)
	if fixed == place {
		return false
	}
	a.BirthPlace = StrPtr(fixed)
	return true
}
