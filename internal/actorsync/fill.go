package actorsync

import (
	"fmt"
	"strings"
)

// 缺字段重抓（--fill-missing）支持的可补字段。
//
// 这里只列「能从在线源重新抓到」的字段。status / key / created_at / fail_count
// 这类控制字段永远保留旧值，不参与补值；否则一次补值就可能把 ok 记录降级。
type fillSpec struct {
	Name    string
	Aliases []string
	Missing func(*Actor) bool
	Copy    func(dst, src *Actor)
}

var fillSpecs = []fillSpec{
	{
		Name: "bio", Aliases: []string{"简介", "overview"},
		Missing: func(a *Actor) bool { return StrVal(a.Bio) == "" },
		Copy: func(dst, src *Actor) {
			dst.Bio = src.Bio
			dst.BioLang = src.BioLang
		},
	},
	{
		Name: "birthday", Aliases: []string{"birth_date", "生日"},
		Missing: func(a *Actor) bool { return StrVal(a.BirthDate) == "" },
		Copy: func(dst, src *Actor) {
			dst.BirthDate = src.BirthDate
			dst.BirthDateSource = src.BirthDateSource
		},
	},
	{
		Name: "birth_place", Aliases: []string{"出生地"},
		Missing: func(a *Actor) bool { return StrVal(a.BirthPlace) == "" },
		Copy:    func(dst, src *Actor) { dst.BirthPlace = src.BirthPlace },
	},
	{
		Name: "gender", Aliases: []string{"性别"},
		Missing: func(a *Actor) bool { return StrVal(a.Gender) == "" },
		Copy:    func(dst, src *Actor) { dst.Gender = src.Gender },
	},
	{
		Name: "country", Aliases: []string{"国家"},
		Missing: func(a *Actor) bool { return StrVal(a.Country) == "" },
		Copy:    func(dst, src *Actor) { dst.Country = src.Country },
	},
	{
		Name: "height", Aliases: []string{"height_cm", "身高"},
		Missing: func(a *Actor) bool { return IntVal(a.HeightCm) == 0 },
		Copy:    func(dst, src *Actor) { dst.HeightCm = src.HeightCm },
	},
	{
		Name: "weight", Aliases: []string{"weight_kg", "体重"},
		Missing: func(a *Actor) bool { return IntVal(a.WeightKg) == 0 },
		Copy:    func(dst, src *Actor) { dst.WeightKg = src.WeightKg },
	},
	{
		Name: "measurements", Aliases: []string{"三围", "bust"},
		Missing: func(a *Actor) bool { return StrVal(a.Measurements) == "" },
		Copy: func(dst, src *Actor) {
			dst.Measurements = src.Measurements
			dst.MeasurementsUnit = src.MeasurementsUnit
		},
	},
	{
		Name: "cup", Aliases: []string{"cup_size", "罩杯"},
		Missing: func(a *Actor) bool { return StrVal(a.CupSize) == "" },
		Copy:    func(dst, src *Actor) { dst.CupSize = src.CupSize },
	},
	{
		Name: "career_start", Aliases: []string{"career", "出道年"},
		Missing: func(a *Actor) bool { return IntVal(a.CareerStart) == 0 },
		Copy:    func(dst, src *Actor) { dst.CareerStart = src.CareerStart },
	},
	{
		Name: "tags", Aliases: []string{"标签"},
		Missing: func(a *Actor) bool { return len(a.Tags) == 0 },
		Copy:    func(dst, src *Actor) { dst.Tags = append([]string{}, src.Tags...) },
	},
	{
		Name: "homepage", Aliases: []string{"homepage_url", "主页"},
		Missing: func(a *Actor) bool { return StrVal(a.HomePageURL) == "" },
		Copy:    func(dst, src *Actor) { dst.HomePageURL = src.HomePageURL },
	},
	{
		Name: "avatar", Aliases: []string{"头像", "image"},
		Missing: func(a *Actor) bool { return StrVal(a.Avatar) == "" },
		Copy: func(dst, src *Actor) {
			dst.Avatar = src.Avatar
			dst.AvatarCN = src.AvatarCN
			dst.AvatarSourceSite = src.AvatarSourceSite
		},
	},
	{
		Name: "aliases", Aliases: []string{"别名"},
		Missing: func(a *Actor) bool { return len(a.Aliases) == 0 },
		Copy:    func(dst, src *Actor) { dst.Aliases = append([]string{}, src.Aliases...) },
	},
	{
		Name: "name_original", Aliases: []string{"original", "原名"},
		Missing: func(a *Actor) bool { return StrVal(a.NameOriginal) == "" },
		Copy:    func(dst, src *Actor) { dst.NameOriginal = src.NameOriginal },
	},
}

// ParseFillFields 把 --fill-fields 的参数解析成规范字段名。
// 空字符串 = 上面全部字段；未知字段直接报错，避免拼错后静默不补。
func ParseFillFields(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		out := make([]string, 0, len(fillSpecs))
		for _, s := range fillSpecs {
			out = append(out, s.Name)
		}
		return out, nil
	}

	valid := map[string]string{}
	for _, s := range fillSpecs {
		valid[s.Name] = s.Name
		for _, alias := range s.Aliases {
			valid[strings.ToLower(alias)] = s.Name
		}
	}

	seen := map[string]bool{}
	var out []string
	for _, tok := range strings.Split(raw, ",") {
		tok = strings.ToLower(strings.TrimSpace(tok))
		if tok == "" {
			continue
		}
		name, ok := valid[tok]
		if !ok {
			names := make([]string, 0, len(fillSpecs))
			for _, s := range fillSpecs {
				names = append(names, s.Name)
			}
			return nil, fmt.Errorf("未知补值字段 %q，可选：%s", tok, strings.Join(names, ", "))
		}
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("--fill-fields 至少指定一个字段")
	}
	return out, nil
}

// MissingFields 返回这条记录里属于 fields 的空字段，顺序固定，便于日志对比。
func MissingFields(a *Actor, fields []string) []string {
	if a == nil {
		return nil
	}
	want := map[string]bool{}
	for _, f := range fields {
		want[f] = true
	}
	var out []string
	for _, s := range fillSpecs {
		if want[s.Name] && s.Missing(a) {
			out = append(out, s.Name)
		}
	}
	return out
}

// cloneActor 复制一条记录，切片和 map 也复制一份，避免补值时改到 DB 里的原对象。
func cloneActor(a *Actor) *Actor {
	if a == nil {
		return nil
	}
	out := *a
	out.Aliases = append([]string{}, a.Aliases...)
	out.Tags = append([]string{}, a.Tags...)
	out.ProviderIDs = map[string]string{}
	for k, v := range a.ProviderIDs {
		out.ProviderIDs[k] = v
	}
	out.Sources = map[string]bool{}
	for k, v := range a.Sources {
		out.Sources[k] = v
	}
	return &out
}

// MergeMissingFields 只把 src 里非空的值填进 dst 的空位，返回本次实际补上的字段。
//
// 控制字段（status / key / created_at / fail_count / retry_after）一律保留 dst，
// 这样补值失败或只抓到一半时，也不会把原本 ok/partial 的记录改坏。
// sources 和 provider_ids 取并集：多认出一个 ID 对下游只有好处。
func MergeMissingFields(dst, src *Actor, fields []string) []string {
	if dst == nil || src == nil {
		return nil
	}
	want := map[string]bool{}
	for _, f := range fields {
		want[f] = true
	}

	var filled []string
	// 按 fillSpecs 的固定顺序补，保证日志和 MissingFields 的顺序一致。
	for _, s := range fillSpecs {
		if !want[s.Name] || !s.Missing(dst) || s.Missing(src) {
			continue
		}
		s.Copy(dst, src)
		filled = append(filled, s.Name)
	}

	for k, v := range src.Sources {
		if v {
			dst.Sources[k] = true
		}
	}
	for k, v := range src.ProviderIDs {
		if _, ok := dst.ProviderIDs[k]; !ok && v != "" {
			dst.ProviderIDs[k] = v
		}
	}
	// 记录「补过」的时间，但不动 created_at / status。
	if src.ScrapedAt != "" {
		dst.ScrapedAt = src.ScrapedAt
	}
	return filled
}
