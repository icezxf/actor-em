package actorsync

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 标题里的中文名或罗马字要对得上才认；对不上说明 slug 撞了同名别人。
func TestTabPageMatches(t *testing.T) {
	cases := []struct {
		title  string
		cands  []string
		want   bool
		reason string
	}{
		{"三上悠亜（三上悠亚 / Mikami Yua）资料_作品 | TheActorBase 演员资料库",
			[]string{"三上悠亜", "Mikami Yua"}, true, "中文名一致"},
		{"三上悠亜（三上悠亚 / Mikami Yua）资料_作品 | TheActorBase 演员资料库",
			[]string{"Yua Mikami"}, true, "罗马字一致（词序不同也算）"},
		{"松咲那奈（Matsuzaki Nana）资料_作品 | TheActorBase 演员资料库",
			[]string{"松咲那奈"}, true, "括号里只有罗马字时也能核"},
		{"市井桃（ＭＯ☆ＭＯ / MOMO）资料_作品 | TheActorBase 演员资料库",
			[]string{"坂田もも", "Momo Sakata"}, false, "同名别人要拒"},
		{"", []string{"三上悠亜"}, false, "没标题不能放行"},
	}
	for _, tc := range cases {
		if got := tabPageMatches(tc.title, tc.cands); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.reason, got, tc.want)
		}
	}
}

// 冒烟：抓到的页面是别人时不能把简介拿走。
func TestTheActorBaseRejectsWrongPage(t *testing.T) {
	const story = "市井桃（ＭＯ☆ＭＯ），日本前AV女优，出生于东京都，兴趣是美甲艺术，二零一零年正式引退，此后转行从事美容相关工作。"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `<html><head><title>市井桃（ＭＯ☆ＭＯ / MOMO）资料_作品</title></head>`+
			`<body><div class="story">%s</div></body></html>`, story)
	}))
	defer srv.Close()

	c := NewTheActorBaseClient(srv.Client())
	c.Base = srv.URL + "/"
	if bio, _ := c.fetchOne(context.Background(), "momo", []string{"坂田もも", "Momo Sakata"}); bio != "" {
		t.Fatalf("同名别人的简介应被拒，得到 %q", bio)
	}
	if bio, ok := c.fetchOne(context.Background(), "momo", []string{"市井桃"}); !ok || bio == "" {
		t.Fatalf("本人页面应通过，得到 ok=%v bio=%q", ok, bio)
	}
}

// slug 只从拉丁候选拼，中文候选直接跳过（拼出来必然 404）。
func TestSlugCandidates(t *testing.T) {
	got := slugCandidates("Mikami Yua")
	if len(got) != 2 || got[0] != "mikami-yua" || got[1] != "yua-mikami" {
		t.Fatalf("两种词序都要试，得到 %v", got)
	}
	if hasLatinLetter("三上悠亜") {
		t.Error("中文不该被当成 slug 候选")
	}
}
