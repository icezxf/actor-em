package actorsync

import "testing"

// StashDB 和 JavStash 是同一套 stash-box schema，除了英文名（搜 TPDB 用），
// 整条记录也要能补出生地 / 出道年 / 身高 / 三围。
func TestApplyPerformerStashDBFillsFields(t *testing.T) {
	f := &Fetcher{}
	res := &FetchResult{ProviderIDs: map[string]string{}, Sources: map[string]bool{}}
	p := &Performer{
		Source:      "stashdb",
		ID:          "uuid-sd",
		Name:        "Rio Haregi",
		Country:     "JP",
		Gender:      "FEMALE",
		Height:      158,
		CupSize:     "E",
		Band:        34,
		Waist:       24,
		Hip:         35,
		CareerStart: 2019,
		Aliases:     []string{"はれぎりお"},
	}
	f.applyPerformer(res, p)

	if res.CareerStart != 2019 {
		t.Errorf("出道年 = %d, want 2019", res.CareerStart)
	}
	if res.Country != "JP" || res.BirthPlace != "日本" {
		t.Errorf("出生地 = %q / %q, want JP / 日本", res.Country, res.BirthPlace)
	}
	if res.HeightCm != 158 {
		t.Errorf("身高 = %d", res.HeightCm)
	}
	if res.Measurements != "34E-24-35" || res.MeasurementsUnit != "in" {
		t.Errorf("三围 = %q (%q)", res.Measurements, res.MeasurementsUnit)
	}
	if res.Gender != "female" {
		t.Errorf("性别 = %q", res.Gender)
	}
	if res.ProviderIDs["StashDB"] != "uuid-sd" {
		t.Errorf("provider id = %v", res.ProviderIDs)
	}
	if len(res.Aliases) != 1 {
		t.Errorf("别名 = %v", res.Aliases)
	}
}

// 先应用 JavStash 再应用 StashDB 时，已有值不能被顶掉（JavStash 优先）。
func TestApplyPerformerStashDBKeepsJavStashValues(t *testing.T) {
	f := &Fetcher{}
	res := &FetchResult{ProviderIDs: map[string]string{}, Sources: map[string]bool{}}
	f.applyPerformer(res, &Performer{
		Source: "javstash", BirthDate: "1996-05-01", Country: "JP",
		Height: 160, CareerStart: 2018, CupSize: "F", Band: 34, Waist: 24, Hip: 35,
	})
	f.applyPerformer(res, &Performer{
		Source: "stashdb", BirthDate: "1990-01-01", Country: "US",
		Height: 170, CareerStart: 2005, CupSize: "A", Band: 36, Waist: 26, Hip: 37,
	})

	if res.BirthDate != "1996-05-01" {
		t.Errorf("生日被 StashDB 改成了 %q", res.BirthDate)
	}
	if res.HeightCm != 160 || res.CareerStart != 2018 {
		t.Errorf("身高/出道年被覆盖: %d / %d", res.HeightCm, res.CareerStart)
	}
	if res.Measurements != "34F-24-35" {
		t.Errorf("三围被覆盖: %q", res.Measurements)
	}
	if res.Country != "JP" || res.BirthPlace != "日本" {
		t.Errorf("出生地被覆盖: %q / %q", res.Country, res.BirthPlace)
	}
}
