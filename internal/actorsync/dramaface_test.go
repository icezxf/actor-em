package actorsync

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStripDramaFields(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "半角冒号",
			in:   "身高: 155cm 罩杯: D 三围: 84/60/86 她是一位日本女优。",
			want: "她是一位日本女优。",
		},
		{
			name: "全角冒号",
			in:   "身高：155cm 罩杯：D 三围：84/60/86 她是一位日本女优。",
			want: "她是一位日本女优。",
		},
		{
			name: "混用冒号且值在下一 token",
			in:   "身高: 155cm 罩杯： D 她是一位日本女优。",
			want: "她是一位日本女优。",
		},
		{
			name: "标签后没有值也不越界",
			in:   "身高：",
			want: "",
		},
		{
			name: "本来就是正文",
			in:   "她是一位日本女优，2008 年出道。",
			want: "她是一位日本女优，2008 年出道。",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripDramaFields(tc.in); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

// 回归：线上 panic 就是全角冒号触发的，这里走完整 fetchOne 链路再验一次。
func TestDramaFaceFullWidthColon(t *testing.T) {
	desc := "身高：155cm 罩杯：D 三围：84/60/86 她是一位日本女优，2008 年出道，曾参与多部写真与电视节目录制。"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `<html><head><title>测试演员 (テスト俳優,TestActor) 角色解析、出演作品 - 剧面</title><meta name="description" content="%s"></head><body></body></html>`, desc)
	}))
	defer srv.Close()

	c := &DramaFaceClient{HTTP: srv.Client(), Base: srv.URL + "/"}
	got, ok := c.fetchOne(context.Background(), "TestActor", []string{"测试演员"})
	if !ok {
		t.Fatal("页面有 description，应该算命中")
	}
	if !strings.HasPrefix(got, "她是一位日本女优") {
		t.Fatalf("前缀属性串应被剥掉，实际 %q", got)
	}
}

// 回归：短 slug 撞到同名别人。
// 实测「坂田もも」按末段 slug "Momo" 抓到了「市井桃（ＭＯ☆ＭＯ）」的简介，
// 同一段简介最终挂到了 19 个名字带「もも/桃」的人身上。
func TestDramaFaceRejectsWrongPerson(t *testing.T) {
	desc := "市井桃（ＭＯ☆ＭＯ，1986年8月12日－）是日本的前AV女优，兴趣是美甲艺术。"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `<html><head><title>市井桃 (ＭＯ☆ＭＯ,MOMO) 角色解析、出演作品、人物照片 -  剧面</title><meta name="description" content="%s"></head><body></body></html>`, desc)
	}))
	defer srv.Close()

	c := &DramaFaceClient{HTTP: srv.Client(), Base: srv.URL + "/"}
	if bio, ok := c.fetchOne(context.Background(), "Momo", []string{"坂田もも", "Momo Sakata"}); ok || bio != "" {
		t.Fatalf("短 slug 撞到别人时应拒绝，得到 %q", bio)
	}
	// 本人（名字就是 Momo）应当放行：候选名能在标题里找到
	if _, ok := c.fetchOne(context.Background(), "Momo", []string{"市井桃"}); !ok {
		t.Error("同名本人的页面不该被拒")
	}
}

func TestDramaPageMatches(t *testing.T) {
	cases := []struct {
		title  string
		slug   string
		cands  []string
		want   bool
		reason string
	}{
		{"桃乃木香奈 (桃乃木かな,Kana Momonogi) 角色解析、出演作品 -  剧面", "MomonogiKana", []string{"桃乃木かな"}, true, "候选名出现在标题"},
		{"桃乃木香奈 (桃乃木かな,Kana Momonogi) 角色解析、出演作品 -  剧面", "KanaMomonogi", nil, true, "长 slug 出现在标题"},
		{"市井桃 (ＭＯ☆ＭＯ,MOMO) 角色解析、出演作品、人物照片 -  剧面", "Momo", []string{"坂田もも"}, false, "短 slug + 候选名不符"},
		{"市井桃 (ＭＯ☆ＭＯ,MOMO) 角色解析、出演作品、人物照片 -  剧面", "Momo", []string{"市井桃"}, true, "同名本人"},
		{"", "MomonogiKana", []string{"桃乃木かな"}, false, "没有标题不能放行"},
	}
	for _, tc := range cases {
		if got := dramaPageMatches(tc.title, tc.slug, tc.cands); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.reason, got, tc.want)
		}
	}
}
