package actorsync

import (
	"reflect"
	"testing"
)

func TestCupTag(t *testing.T) {
	cases := map[string]string{
		"E":       "E罩杯",
		"e":       "E罩杯",
		" J ":     "J罩杯",
		"Eカップ":    "E罩杯",
		"E cup":   "E罩杯",
		"":        "",
		"AA":      "",
		"1":       "",
		"unknown": "",
	}
	for in, want := range cases {
		if got := CupTag(in); got != want {
			t.Errorf("CupTag(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDeriveCup(t *testing.T) {
	cases := map[string]string{
		"34E-24-35": "E",
		"36J-25-39": "J",
		"44-25-39":  "",
		"":          "",
		"30f-23-34": "F",
	}
	for in, want := range cases {
		if got := DeriveCup(in); got != want {
			t.Errorf("DeriveCup(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBandCupMeasurements(t *testing.T) {
	if got := bandCupMeasurements(34, "E", 24, 35); got != "34E-24-35" {
		t.Errorf("got %q", got)
	}
	// 单位不对（厘米）或缺失，一律不拼
	if got := bandCupMeasurements(0, "E", 24, 35); got != "" {
		t.Errorf("band=0 got %q", got)
	}
	if got := bandCupMeasurements(34, "", 24, 35); got != "" {
		t.Errorf("cup empty got %q", got)
	}
	if got := bandCupMeasurements(86, "E", 24, 35); got != "" {
		t.Errorf("cm band got %q", got)
	}
	if got := bandCupMeasurements(34, "E", 60, 35); got != "" {
		t.Errorf("cm waist got %q", got)
	}
}

func TestAddCupTag(t *testing.T) {
	got := AddCupTag([]string{"巨乳"}, "E")
	if !reflect.DeepEqual(got, []string{"E罩杯", "巨乳"}) {
		t.Errorf("got %v", got)
	}
	// 已有就不重复（大小写/空格不敏感）
	got = AddCupTag([]string{"巨乳", "e罩杯"}, "E")
	if !reflect.DeepEqual(got, []string{"巨乳", "e罩杯"}) {
		t.Errorf("dedupe got %v", got)
	}
	// 没有罩杯不动
	got = AddCupTag([]string{"巨乳"}, "")
	if !reflect.DeepEqual(got, []string{"巨乳"}) {
		t.Errorf("no cup got %v", got)
	}
}

func TestFinalizeCup(t *testing.T) {
	// 只有三围、没有 cup_size：反推出罩杯并进标签
	r := &FetchResult{Measurements: "34E-24-35", Tags: []string{"熟女"}}
	r.finalizeCup()
	if r.CupSize != "E" {
		t.Fatalf("cup=%q", r.CupSize)
	}
	if !reflect.DeepEqual(r.Tags, []string{"E罩杯", "熟女"}) {
		t.Fatalf("tags=%v", r.Tags)
	}
	// 有 cup_size：直接用，且小写归一
	r2 := &FetchResult{CupSize: "j"}
	r2.finalizeCup()
	if r2.CupSize != "J" || !reflect.DeepEqual(r2.Tags, []string{"J罩杯"}) {
		t.Fatalf("cup=%q tags=%v", r2.CupSize, r2.Tags)
	}
	// 什么都没有：不动
	r3 := &FetchResult{}
	r3.finalizeCup()
	if r3.CupSize != "" || len(r3.Tags) != 0 {
		t.Fatalf("cup=%q tags=%v", r3.CupSize, r3.Tags)
	}
}

func TestEnsureActorCupTag(t *testing.T) {
	a := &Actor{Name: "x", Measurements: StrPtr("34E-24-35")}
	if !EnsureActorCupTag(a) {
		t.Fatal("should change")
	}
	if StrVal(a.CupSize) != "E" || !reflect.DeepEqual(a.Tags, []string{"E罩杯"}) {
		t.Fatalf("cup=%q tags=%v", StrVal(a.CupSize), a.Tags)
	}
	// 幂等
	if EnsureActorCupTag(a) {
		t.Fatal("second run should be no-op")
	}
	// 没罩杯也没三围：不动
	b := &Actor{Name: "y"}
	if EnsureActorCupTag(b) {
		t.Fatal("empty should be no-op")
	}
}
