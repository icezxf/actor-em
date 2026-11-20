package actorsync

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseFillFields(t *testing.T) {
	all, err := ParseFillFields("")
	if err != nil || len(all) != len(fillSpecs) {
		t.Fatalf("空参数应返回全部字段: %v / %d", err, len(all))
	}

	got, err := ParseFillFields("生日, 三围, bio")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"birthday", "measurements", "bio"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("别名解析: got %v want %v", got, want)
	}

	if _, err := ParseFillFields("不存在的字段"); err == nil {
		t.Fatal("未知字段应该报错")
	}
}

func TestMergeMissingFields(t *testing.T) {
	dst := &Actor{
		Key: "k", Name: "某人", Status: StatusOK, CreatedAt: "t0",
		Bio:         StrPtr("旧简介"),
		Avatar:      StrPtr("https://old/avatar.jpg"),
		Sources:     map[string]bool{"javstash": true},
		ProviderIDs: map[string]string{"JavStash": "1"},
	}
	src := &Actor{
		Bio:         StrPtr("新简介"),
		BirthDate:   StrPtr("2000-01-01"),
		Avatar:      StrPtr("https://new/avatar.jpg"),
		AvatarCN:    StrPtr("https://cdn/new/avatar.jpg"),
		Tags:        []string{"tag"},
		Sources:     map[string]bool{"yinrenlu": true},
		ProviderIDs: map[string]string{"JavStash": "2", "TPDB": "t"},
		ScrapedAt:   "t1",
		Status:      StatusPartial,
		CreatedAt:   "t9",
	}

	merged := cloneActor(dst)
	filled := MergeMissingFields(merged, src, []string{"bio", "birthday", "tags", "avatar"})
	wantFilled := []string{"birthday", "tags"}
	if !reflect.DeepEqual(filled, wantFilled) {
		t.Fatalf("补值列表: got %v want %v", filled, wantFilled)
	}
	if StrVal(merged.Bio) != "旧简介" {
		t.Errorf("旧简介不该被覆盖: %s", StrVal(merged.Bio))
	}
	if StrVal(merged.BirthDate) != "2000-01-01" {
		t.Errorf("空生日应该补上: %s", StrVal(merged.BirthDate))
	}
	if !reflect.DeepEqual(merged.Tags, []string{"tag"}) {
		t.Errorf("空标签应该补上: %v", merged.Tags)
	}
	if StrVal(merged.Avatar) != "https://old/avatar.jpg" {
		t.Errorf("已有头像不该被替换: %s", StrVal(merged.Avatar))
	}
	if merged.Status != StatusOK || merged.CreatedAt != "t0" {
		t.Errorf("状态/创建时间不该变: %s / %s", merged.Status, merged.CreatedAt)
	}
	if !merged.Sources["javstash"] || !merged.Sources["yinrenlu"] {
		t.Errorf("来源应该取并集: %v", merged.Sources)
	}
	if merged.ProviderIDs["JavStash"] != "1" || merged.ProviderIDs["TPDB"] != "t" {
		t.Errorf("已有 ID 保留、新 ID 补上: %v", merged.ProviderIDs)
	}
	// src 的切片不该和 merged 共享底层数组
	merged.Tags[0] = "changed"
	if src.Tags[0] != "tag" {
		t.Error("补值后不应共享 src 的切片")
	}
}

func TestBuildPlanWithFill(t *testing.T) {
	full := &Actor{Name: "完整", Status: StatusOK, Bio: StrPtr("有简介"), Tags: []string{"tag"}}
	one := &Actor{Name: "缺简介", Status: StatusOK, Tags: []string{"tag"}}
	two := &Actor{Name: "缺两项", Status: StatusPartial}
	future := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)
	noSource := &Actor{Name: "无源", Status: StatusNoSource, RetryAfter: &future}
	skipped := &Actor{Name: "跳过", Status: StatusSkipped}
	db := &Db{Actors: []*Actor{full, one, two, noSource, skipped}}

	names := []string{"完整", "缺简介", "缺两项", "无源", "跳过", "新演员"}
	plan := BuildPlanWithFill(db, names, nil, false, []string{"bio", "tags"})
	if len(plan) != 2 {
		t.Fatalf("只该挑 2 条有缺字段的可导出记录，实际 %d: %+v", len(plan), plan)
	}
	if plan[0].Name != "缺两项" || len(plan[0].Missing) != 2 {
		t.Fatalf("缺得最多的排第一: %+v", plan[0])
	}
	if plan[1].Name != "缺简介" || plan[1].Reason != "fill" {
		t.Fatalf("第二条应该是缺简介: %+v", plan[1])
	}

	// 传 nil 必须和老行为一致：只有新演员。
	old := BuildPlanWithFill(db, names, nil, false, nil)
	if len(old) != 1 || old[0].Reason != "new" {
		t.Fatalf("nil fillFields 应退回 BuildPlan: %+v", old)
	}
}

func TestEngineFillKeepsExistingValues(t *testing.T) {
	existing := &Actor{
		Key: "javstash:abc", Name: "小凑四叶", Status: StatusOK, CreatedAt: "t0",
		Bio:         StrPtr("旧简介"),
		Avatar:      StrPtr("https://old/avatar.jpg"),
		Sources:     map[string]bool{"javstash": true},
		ProviderIDs: map[string]string{"JavStash": "abc"},
	}
	e := newTestEngine(&fakeFetcher{res: &FetchResult{
		NameOriginal: "小凑四叶",
		Bio:          "新简介",
		BirthDate:    "1999-01-01",
		ImageURL:     "https://new/avatar.jpg",
		ImageSite:    "javstash",
		ProviderIDs:  map[string]string{"JavStash": "abc"},
		Sources:      map[string]bool{"javstash": true},
	}})
	e.DB.Actors = append(e.DB.Actors, existing)

	out, err := e.Process(context.Background(), "小凑四叶", ProcessOptions{
		FillFields: []string{"bio", "birthday", "avatar"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Kept || out.Added {
		t.Fatalf("应该算补值更新: kept=%v added=%v", out.Kept, out.Added)
	}
	if !strings.Contains(strings.Join(out.Filled, ","), "birthday") {
		t.Fatalf("生日应该补上: %v", out.Filled)
	}
	if StrVal(out.Actor.Bio) != "旧简介" {
		t.Errorf("已有简介不该被覆盖: %s", StrVal(out.Actor.Bio))
	}
	if StrVal(out.Actor.BirthDate) != "1999-01-01" {
		t.Errorf("空生日应该补上: %s", StrVal(out.Actor.BirthDate))
	}
	if StrVal(out.Actor.Avatar) != "https://old/avatar.jpg" {
		t.Errorf("已有头像不该被替换: %s", StrVal(out.Actor.Avatar))
	}
	if out.Actor.Status != StatusOK || out.Actor.CreatedAt != "t0" {
		t.Errorf("状态/创建时间不该变: %s / %s", out.Actor.Status, out.Actor.CreatedAt)
	}
}
