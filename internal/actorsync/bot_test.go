package actorsync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSplitNames(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"小凑四叶", []string{"小凑四叶"}},
		{"小凑四叶，三上悠亚", []string{"小凑四叶", "三上悠亚"}},
		{"a\nb\nc", []string{"a", "b", "c"}},
		{"A、B；C", []string{"A", "B", "C"}},
		{"  x  \n\n  y  ", []string{"x", "y"}},
		{"# 这行是注释\n真名", []string{"真名"}},
		{"Yua Mikami", []string{"Yua Mikami"}}, // 罗马字名里有空格，不能拆
		{"小凑四叶，小凑四叶", []string{"小凑四叶"}},
		{"", nil},
		{"，、；\n", nil},
	}
	for _, c := range cases {
		got := SplitNames(c.in)
		if len(got) != len(c.want) {
			t.Fatalf("SplitNames(%q) = %v，期望 %v", c.in, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("SplitNames(%q) = %v，期望 %v", c.in, got, c.want)
			}
		}
	}
}

func TestSplitCommand(t *testing.T) {
	cases := []struct{ in, cmd, args string }{
		{"/status 某人", "/status", "某人"},
		{"/help", "/help", ""},
		{"/list@my_bot 关键词", "/list", "关键词"},
		{"/status\t某人", "/status", "某人"},
		{"随便说点", "", "随便说点"},
		{"/REPICK a,b", "/repick", "a,b"},
	}
	for _, c := range cases {
		cmd, args := splitCommand(c.in)
		if cmd != c.cmd || args != c.args {
			t.Fatalf("splitCommand(%q) = (%q, %q)，期望 (%q, %q)", c.in, cmd, args, c.cmd, c.args)
		}
	}
}

func TestClampText(t *testing.T) {
	if got := clampText("abcdef", 3); got != "ab…" {
		t.Fatalf("ASCII 截断不对: %q", got)
	}
	// 按 rune 而不是字节算，中文不能被截成半个
	if got := clampText("一二三四五", 3); got != "一二…" {
		t.Fatalf("中文截断不对: %q", got)
	}
	if got := clampText("短", 10); got != "短" {
		t.Fatalf("不该改动未超长的文本: %q", got)
	}
}

func TestStatusLabelAndSummary(t *testing.T) {
	for _, s := range []string{StatusOK, StatusPartial, StatusNoSource, StatusFailed, "unknown"} {
		if statusLabel(s) == "" {
			t.Fatalf("%s 没有标签", s)
		}
	}
	if actorSummary(nil) != "" {
		t.Fatal("nil 应该返回空串")
	}
	if actorSummary(&Actor{}) == "" {
		t.Fatal("空记录也该有一句提示，而不是空白消息")
	}
}

// newTestTG 起一个假的 Telegram API，按 handler 返回内容。
func newTestTG(t *testing.T, handler http.HandlerFunc) (*TelegramClient, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	tg := NewTelegramClient("test-token", "42")
	tg.APIBase = srv.URL
	tg.HTTP = srv.Client()
	return tg, srv
}

func TestPollUpdatesParsesMessageAndCallback(t *testing.T) {
	var gotOffset float64
	tg, _ := newTestTG(t, func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		gotOffset, _ = payload["offset"].(float64)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":[
			{"update_id":10,"message":{"message_id":5,"chat":{"id":42},"from":{"id":42},"text":"/help"}},
			{"update_id":11,"callback_query":{"id":"cb1","data":"r1:2","message":{"message_id":7,"chat":{"id":42}}}}
		]}`))
	})

	ups, offset, err := tg.PollUpdates(context.Background(), 9, 0, "message", "callback_query")
	if err != nil {
		t.Fatal(err)
	}
	if gotOffset != 9 {
		t.Fatalf("offset 没传对: %v", gotOffset)
	}
	if offset != 12 {
		t.Fatalf("新 offset 应为最大 update_id+1 = 12，实际 %d", offset)
	}
	if len(ups) != 2 {
		t.Fatalf("应解析出 2 条 update，实际 %d", len(ups))
	}
	if ups[0].Message == nil || ups[0].Message.Text != "/help" || ups[0].Message.Chat.ID != 42 {
		t.Fatalf("message 解析不对: %+v", ups[0].Message)
	}
	if ups[1].CallbackQuery == nil || ups[1].CallbackQuery.ID != "cb1" {
		t.Fatalf("callback 解析不对: %+v", ups[1].CallbackQuery)
	}
}

func TestPollPicksUsesSameParser(t *testing.T) {
	var answered int
	tg, _ := newTestTG(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/answerCallbackQuery") {
			answered++
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":[
			{"update_id":3,"callback_query":{"id":"cb9","data":"r2:1","message":{"message_id":8,"chat":{"id":42}}}}
		]}`))
	})

	picks, offset, err := tg.PollPicks(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if offset != 4 || len(picks) != 1 {
		t.Fatalf("offset=%d picks=%v", offset, picks)
	}
	if picks[0].ReviewID != "r2" || picks[0].Index != 1 || picks[0].MessageID != 8 {
		t.Fatalf("点击解析不对: %+v", picks[0])
	}
	if answered != 1 {
		t.Fatalf("应该回执一次，实际 %d", answered)
	}
}

func TestBotIgnoresUnknownChat(t *testing.T) {
	var hits int
	tg, _ := newTestTG(t, func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	})

	bot := &Bot{TG: tg, ChatID: "42"}
	bot.handleMessage(context.Background(), &tgMessage{Chat: tgChat{ID: 999}, Text: "/help"})
	if hits != 0 {
		t.Fatalf("陌生会话不该收到任何回复，实际发了 %d 个请求", hits)
	}

	// /id 是唯一的例外：方便第一次配置时自报家门
	bot.handleMessage(context.Background(), &tgMessage{Chat: tgChat{ID: 999}, Text: "/id"})
	if hits != 1 {
		t.Fatalf("/id 应该回一条，实际 %d", hits)
	}
}

func TestBotHelpDoesNotTouchEngine(t *testing.T) {
	tg, _ := newTestTG(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	})
	// Engine 为 nil：/help 不应该走到刮削流程，否则会 panic
	bot := &Bot{TG: tg, ChatID: "42"}
	bot.handleMessage(context.Background(), &tgMessage{Chat: tgChat{ID: 42}, Text: "/help"})
}
