package actorsync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// TestBotHandleUpdate 模拟 CF Worker 转发的一条 webhook update：
// 不经过 getUpdates，直接交给 Bot 处理，发名字后能收到候选图。
func TestBotHandleUpdate(t *testing.T) {
	var (
		mu       sync.Mutex
		sentText []string
		albums   int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/sendMediaGroup"):
			mu.Lock()
			albums++
			mu.Unlock()
			_, _ = w.Write([]byte(`{"ok":true,"result":[{"message_id":100}]}`))
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			var p map[string]any
			_ = json.NewDecoder(r.Body).Decode(&p)
			mu.Lock()
			sentText = append(sentText, fmtValue(p["text"]))
			mu.Unlock()
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
		default:
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		}
	}))
	defer srv.Close()

	tg := NewTelegramClient("tok", "42")
	tg.APIBase = srv.URL
	tg.HTTP = srv.Client()

	res := okResult("小凑四叶")
	res.ImageURL = srv.URL + "/img/a.jpg"
	res.Images = []string{srv.URL + "/img/a.jpg", srv.URL + "/img/b.jpg"}
	engine := newTestEngine(&fakeFetcher{res: res})
	engine.DataFile = t.TempDir() + "/actors.json"

	dir := t.TempDir()
	state := NewReviewState(dir + "/review.json")
	bot := &Bot{
		TG:       tg,
		Engine:   engine,
		State:    state,
		Choices:  NewChoices(dir + "/choices.json"),
		ChatID:   "42",
		MaxNames: 3,
	}

	raw := []byte(`{"update_id":7,"message":{"message_id":1,"chat":{"id":42},"text":"小凑四叶"}}`)
	if err := bot.HandleUpdate(context.Background(), raw); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(sentText) == 0 || !strings.Contains(strings.Join(sentText, "\n"), "收到 1 个名字") {
		t.Fatalf("应该回一条收到消息，实际：%v", sentText)
	}
	if albums != 1 {
		t.Fatalf("应该发出 1 组候选图，实际 %d", albums)
	}
	if len(engine.DB.Actors) != 1 || len(state.Pending) != 1 {
		t.Fatalf("应该入库并留下待选记录：actors=%d pending=%d", len(engine.DB.Actors), len(state.Pending))
	}
}

// TestBotDeriveChat 验证 webhook 模式：没预置 chat id 时，从 update 里取来源会话，
// 回消息发到这个会话；同时确认 chatIDOf 对 callback_query 也生效。
func TestBotDeriveChat(t *testing.T) {
	chatIDOfTests := []struct {
		name string
		raw  string
		want string
	}{
		{"message", `{"update_id":1,"message":{"chat":{"id":98765},"text":"hi"}}`, "98765"},
		{"callback", `{"update_id":2,"callback_query":{"id":"x","data":"r7:2","message":{"chat":{"id":98765}}}}`, "98765"},
		{"empty", `{"update_id":3}`, ""},
	}
	for _, tc := range chatIDOfTests {
		var u tgUpdate
		if err := json.Unmarshal([]byte(tc.raw), &u); err != nil {
			t.Fatal(err)
		}
		if got := chatIDOf(&u); got != tc.want {
			t.Fatalf("chatIDOf(%s) = %q, want %q", tc.name, got, tc.want)
		}
	}

	// 端到端：TG 初始 chat id 为空，DeriveChat 打开后应被设为 update 的来源。
	var mu sync.Mutex
	var gotChat string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			var p map[string]any
			_ = json.NewDecoder(r.Body).Decode(&p)
			mu.Lock()
			gotChat = fmtValue(p["chat_id"])
			mu.Unlock()
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	defer srv.Close()

	tg := NewTelegramClient("tok", "") // 没有预置 chat id
	tg.APIBase = srv.URL
	tg.HTTP = srv.Client()

	res := okResult("小凑四叶")
	res.ImageURL = srv.URL + "/img/a.jpg"
	res.Images = []string{srv.URL + "/img/a.jpg", srv.URL + "/img/b.jpg"}
	engine := newTestEngine(&fakeFetcher{res: res})
	engine.DataFile = t.TempDir() + "/actors.json"
	dir := t.TempDir()
	bot := &Bot{
		TG:         tg,
		Engine:     engine,
		State:      NewReviewState(dir + "/review.json"),
		Choices:    NewChoices(dir + "/choices.json"),
		DeriveChat: true,
		MaxNames:   3,
	}

	raw := []byte(`{"update_id":7,"message":{"message_id":1,"chat":{"id":98765},"text":"/help"}}`)
	if err := bot.HandleUpdate(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotChat != "98765" {
		t.Fatalf("回复应发到来源会话 98765，实际 chat_id=%q", gotChat)
	}
}

func fmtValue(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}
