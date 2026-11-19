package actorsync

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParsePickData(t *testing.T) {
	cases := []struct {
		in  string
		id  string
		idx int
		ok  bool
	}{
		{"r7:0", "r7", 0, true},
		{"r12:9", "r12", 9, true},
		{"", "", 0, false},
		{"r7", "", 0, false},
		{"r7:", "", 0, false},
		{":3", "", 0, false},
		{"r7:x", "", 0, false},
		{"r7:-1", "", 0, false},
	}
	for _, c := range cases {
		id, idx, ok := parsePickData(c.in)
		if ok != c.ok || id != c.id || idx != c.idx {
			t.Errorf("parsePickData(%q) = (%q,%d,%v)，期望 (%q,%d,%v)", c.in, id, idx, ok, c.id, c.idx, c.ok)
		}
	}
}

// callback_data 有 64 字节上限，用短 reviewID 才能保证不超。
func TestPickKeyboardFitsCallbackLimit(t *testing.T) {
	kb := PickKeyboard("r999", 10)
	rows, ok := kb["inline_keyboard"].([][]map[string]string)
	if !ok || len(rows) != 1 || len(rows[0]) != 10 {
		t.Fatalf("键盘结构不对: %#v", kb)
	}
	for i, btn := range rows[0] {
		if len(btn["callback_data"]) > 64 {
			t.Fatalf("第 %d 个按钮 callback_data 超长: %q", i, btn["callback_data"])
		}
		if btn["text"] != strings.TrimSpace(btn["text"]) || btn["text"] == "" {
			t.Fatalf("按钮文案异常: %q", btn["text"])
		}
	}
}

func TestReviewStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "avatar_review.json")

	st := LoadReviewStateOrFail(t, p)
	st.UpdateOffset = 500
	id := st.NextID()
	if id != "r1" {
		t.Fatalf("首个短 id 应为 r1，实际 %s", id)
	}
	st.Add(ReviewPending{ID: id, Key: "javstash:1", Name: "某人", Messages: []int{7}, Images: []string{"u1", "u2"}})
	if err := st.Save(p); err != nil {
		t.Fatal(err)
	}

	again, err := LoadReviewState(p)
	if err != nil {
		t.Fatal(err)
	}
	if again.UpdateOffset != 500 || len(again.Pending) != 1 {
		t.Fatalf("状态没读回来: offset=%d pending=%d", again.UpdateOffset, len(again.Pending))
	}
	if !again.HasKey("javstash:1") {
		t.Fatal("HasKey 应该命中")
	}
	if p2 := again.FindByID("r1"); p2 == nil || p2.Name != "某人" {
		t.Fatalf("FindByID 没找到: %+v", p2)
	}

	again.RemoveByKey("javstash:1")
	if len(again.Pending) != 0 {
		t.Fatalf("移除后应为空: %+v", again.Pending)
	}
}

func TestReviewStatePruneExpired(t *testing.T) {
	st := NewReviewState("")
	old := ReviewPending{ID: "r1", Key: "k1", Name: "老的", Images: []string{"a", "b"}}
	old.SentAt = time.Now().UTC().Add(-30 * 24 * time.Hour).Format(time.RFC3339)
	fresh := ReviewPending{ID: "r2", Key: "k2", Name: "新的", Images: []string{"a", "b"}}
	fresh.SentAt = time.Now().UTC().Format(time.RFC3339)
	st.Pending = []ReviewPending{old, fresh}

	if dropped := st.PruneExpired(7*24*time.Hour, time.Now().UTC()); dropped != 1 {
		t.Fatalf("应该丢弃 1 条，实际 %d", dropped)
	}
	if len(st.Pending) != 1 || st.Pending[0].Key != "k2" {
		t.Fatalf("保留结果不对: %+v", st.Pending)
	}
}

func TestChoicesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "avatar_choices.json")

	ch := NewChoices(p)
	if _, ok := ch.Get("nope"); ok {
		t.Fatal("空 choices 不该命中")
	}
	ch.Set("javstash:1", "https://cdn/x.jpg", "telegram")
	ch.Set("javstash:2", "  ", "telegram") // 空 URL 视为未选
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
	if again.Choices["javstash:1"].Source != "telegram" {
		t.Fatalf("来源应记录为 telegram: %+v", again.Choices["javstash:1"])
	}
}

// CollectPicks 要把「第 N 个按钮」映射回对应的图片 URL，并推进 offset。
func TestCollectPicksAppliesChoice(t *testing.T) {
	var seenOffset float64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/getUpdates") {
			var body struct {
				Offset float64 `json:"offset"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			seenOffset = body.Offset
			io.WriteString(w, `{"ok":true,"result":[
				{"update_id":101,"callback_query":{"id":"cb1","data":"r1:1","message":{"message_id":9,"chat":{"id":1}}}},
				{"update_id":102,"callback_query":{"id":"cb2","data":"r999:0","message":{"message_id":10,"chat":{"id":1}}}}
			]}`)
			return
		}
		io.WriteString(w, `{"ok":true,"result":true}`)
	}))
	defer srv.Close()

	tg := NewTelegramClient("token", "chat")
	tg.APIBase = srv.URL

	st := NewReviewState("")
	st.UpdateOffset = 100
	st.Add(ReviewPending{ID: "r1", Key: "javstash:abc", Name: "某人", Images: []string{"u0", "u1", "u2"}})
	ch := NewChoices("")

	n, names, err := CollectPicks(context.Background(), tg, st, ch)
	if err != nil {
		t.Fatal(err)
	}
	if seenOffset != 100 {
		t.Fatalf("应该用已有的 offset 拉取，实际 %v", seenOffset)
	}
	if n != 1 {
		t.Fatalf("应该只确认 1 个（r999 是未知记录），实际 %d", n)
	}
	if len(names) != 1 || !strings.Contains(names[0], "第 2 张") {
		t.Fatalf("回执文案不对: %v", names)
	}
	url, ok := ch.Get("javstash:abc")
	if !ok || url != "u1" {
		t.Fatalf("应该选中第 2 张 u1，实际 %q %v", url, ok)
	}
	if len(st.Pending) != 0 {
		t.Fatalf("选完应移除待选记录: %+v", st.Pending)
	}
	if st.UpdateOffset != 103 {
		t.Fatalf("offset 应为最大 update_id+1 = 103，实际 %d", st.UpdateOffset)
	}
}

func LoadReviewStateOrFail(t *testing.T, path string) *ReviewState {
	t.Helper()
	st, err := LoadReviewState(path)
	if err != nil {
		t.Fatal(err)
	}
	return st
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
