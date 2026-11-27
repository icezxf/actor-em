package actorsync

import (
	"path/filepath"
	"testing"
)

func TestChoicesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "avatar_choices.json")

	ch := NewChoices(p)
	if _, ok := ch.Get("nope"); ok {
		t.Fatal("空 choices 不该命中")
	}
	ch.Set("javstash:1", "https://cdn/x.jpg", "pages")
	ch.Set("javstash:2", "  ", "pages") // 空 URL 视为未选
	if err := ch.Save(p); err != nil {
		t.Fatal(err)
	}

	again, err := LoadChoices(p)
	if err != nil {
		t.Fatal(err)
	}
	url, ok := again.Get("javstash:1")
	if !ok || url != "https://cdn/x.jpg" {
		t.Fatalf("选择没读回来: %q %v", url, ok)
	}
	if _, ok := again.Get("javstash:2"); ok {
		t.Fatal("空 URL 不应命中")
	}
	if again.Choices["javstash:1"].Source != "pages" {
		t.Fatalf("来源应记录为 pages: %+v", again.Choices["javstash:1"])
	}
}

func TestOrderCandidates(t *testing.T) {
	// 当前的图不在列表里时应补到第一位
	got := OrderCandidates([]string{"a", "b", "b"}, "x")
	if len(got) != 3 || got[0] != "x" {
		t.Fatalf("当前图应排第一: %v", got)
	}
	// 去重 + 已有的排前
	got = OrderCandidates([]string{"a", "b", "c", "a"}, "b")
	want := []string{"b", "a", "c"}
	if len(got) != len(want) {
		t.Fatalf("去重失败: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("顺序不对，期望 %v，实际 %v", want, got)
		}
	}
	// 没有当前图时保持原序
	got = OrderCandidates([]string{"a", "b"}, "")
	if len(got) != 2 || got[0] != "a" {
		t.Fatalf("空 current 不该改动顺序: %v", got)
	}
}
