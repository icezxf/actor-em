package actorsync

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestBotEndToEnd 用假 Telegram API 跑完整链路：
// 发名字 → 刮削入库 → 发相册候选 → 点第 2 张 → 写 choices + 更新头像字段。
func TestBotEndToEnd(t *testing.T) {
	var (
		mu       sync.Mutex
		queue    []string
		sentText []string
	)
	gotAlbum := make(chan struct{}, 4)

	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/img/"):
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte(strings.Repeat("x", 600))) // 发相册时 DownloadImage 要求 ≥512 字节

		// ---- Telegram ----
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			mu.Lock()
			var batch string
			if len(queue) > 0 {
				batch, queue = queue[0], queue[1:]
			}
			mu.Unlock()
			if batch == "" {
				// 留一点间隔，别把 CPU 打满
				time.Sleep(30 * time.Millisecond)
				_, _ = w.Write([]byte(`{"ok":true,"result":[]}`))
				return
			}
			_, _ = w.Write([]byte(batch))

		case strings.HasSuffix(r.URL.Path, "/sendMediaGroup"):
			_, _ = w.Write([]byte(`{"ok":true,"result":[{"message_id":100}]}`))
			select {
			case gotAlbum <- struct{}{}:
			default:
			}

		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			var p map[string]any
			_ = json.NewDecoder(r.Body).Decode(&p)
			mu.Lock()
			sentText = append(sentText, fmt.Sprint(p["text"]))
			mu.Unlock()
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))

		default:
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		}
	}

	srv := httptest.NewServer(http.HandlerFunc(handler))
	defer srv.Close()

	mu.Lock()
	queue = append(queue, `{"ok":true,"result":[
		{"update_id":1,"message":{"message_id":10,"chat":{"id":42},"text":"小凑四叶"}}
	]}`)
	mu.Unlock()

	tg := NewTelegramClient("tok", "42")
	tg.APIBase = srv.URL
	tg.HTTP = srv.Client()

	dir := t.TempDir()
	engine := newTestEngine(&fakeFetcher{res: &FetchResult{
		NameOriginal: "小凑四叶",
		BirthDate:    "1999-01-01",
		HeightCm:     160,
		ImageURL:     srv.URL + "/img/a.jpg",
		ImageSite:    "javstash",
		Images:       []string{srv.URL + "/img/a.jpg", srv.URL + "/img/b.jpg"},
		ProviderIDs:  map[string]string{"JavStash": "abc"},
		Sources:      map[string]bool{"javstash": true},
	}})
	engine.DataFile = dir + "/actors.json"
	engine.HTTP = srv.Client()

	choices := NewChoices(dir + "/choices.json")
	state := NewReviewState(dir + "/review.json")

	bot := &Bot{
		TG:          tg,
		Engine:      engine,
		State:       state,
		Choices:     choices,
		ChatID:      "42",
		MaxNames:    3,
		PollTimeout: 1,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = bot.Run(ctx)
		close(done)
	}()

	// 等候选相册发出来，再喂一次「点第 2 张」
	select {
	case <-gotAlbum:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("5 秒内没发出候选图")
	}

	mu.Lock()
	queue = append(queue, `{"ok":true,"result":[
		{"update_id":2,"callback_query":{"id":"cb1","data":"r1:1","message":{"message_id":100,"chat":{"id":42}}}}
	]}`)
	mu.Unlock()

	// 等它处理完点击
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(sentText)
		var last string
		if n > 0 {
			last = sentText[n-1]
		}
		mu.Unlock()
		if strings.Contains(last, "已换成第 2 张") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	cancel()
	<-done

	// Run 已经返回，之后读共享状态是安全的
	if len(engine.DB.Actors) != 1 {
		t.Fatalf("应该有 1 条记录，实际 %d", len(engine.DB.Actors))
	}
	wantURL := srv.URL + "/img/b.jpg"
	actor := engine.DB.Actors[0]
	if StrVal(actor.Avatar) != wantURL {
		t.Fatalf("头像应是第 2 张的网络地址，实际 %q", StrVal(actor.Avatar))
	}
	if StrVal(actor.AvatarSource) != wantURL {
		t.Fatalf("头像来源应为第 2 张，实际 %q", StrVal(actor.AvatarSource))
	}
	if actor.AvatarPath != nil {
		t.Fatal("不该残留 alist 路径")
	}

	url, ok := choices.Get(actor.Key)
	if !ok {
		t.Fatal("choices 应该记录了这次选择")
	}
	if url != wantURL {
		t.Fatalf("choices 里应是第 2 张，实际 %q", url)
	}
	if state.HasKey(actor.Key) {
		t.Fatal("选完之后不该还留着待选记录")
	}

	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(sentText, "\n")
	if !strings.Contains(joined, "小凑四叶") {
		t.Fatalf("应该回过带名字的消息，实际：%s", joined)
	}
}
