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
		fmt.Fprintf(w, `<html><head><title>测试页面</title><meta name="description" content="%s"></head><body></body></html>`, desc)
	}))
	defer srv.Close()

	c := &DramaFaceClient{HTTP: srv.Client(), Base: srv.URL + "/"}
	got, ok := c.fetchOne(context.Background(), "TestActor")
	if !ok {
		t.Fatal("页面有 description，应该算命中")
	}
	if !strings.HasPrefix(got, "她是一位日本女优") {
		t.Fatalf("前缀属性串应被剥掉，实际 %q", got)
	}
}
