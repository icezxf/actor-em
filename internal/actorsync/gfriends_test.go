package actorsync

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// decodeURL 把转义过的路径还原，方便断言中文文件名。
func decodeURL(t *testing.T, s string) string {
	t.Helper()
	out, err := url.PathUnescape(s)
	if err != nil {
		t.Fatalf("URL 解码失败 %q: %v", s, err)
	}
	return out
}

// 这份 fixture 复刻了真实的 Filetree.json 形状，两个坑都在里面：
//   - 顶层除了 Content 还有 Information（值是数字），不能整体当三层 map 解析
//   - z-DMM(骑) 里 key 和 value 是错配的（桃乃木かな 指向 松嶋真麻 的图）
const gfriendsFixture = `{
  "Content": {
    "y-Minnano": {
      "桃乃木かな.jpg": "桃乃木かな.jpg?t=1658331555",
      "三上悠亜.jpg": "AI-Fix-三上悠亜.jpg?t=1658331539"
    },
    "z-DMM(骑)": {
      "桃乃木かな.jpg": "AI-Fix-松嶋真麻.jpg?t=1607433710",
      "松嶋真麻.jpg": "桃乃木かな.jpg?t=1607433711"
    },
    "0-Hand-Storage": {
      "三上悠亜.png": "三上悠亜.png?t=1637650805"
    }
  },
  "Information": {
    "TotalNum": 104709,
    "TotalSize": 10345417891,
    "Timestamp": 1699999999
  }
}`

func TestParseGfriendsIgnoresInformationNumbers(t *testing.T) {
	g, err := parseGfriends([]byte(gfriendsFixture))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if g.Dirs() != 3 {
		t.Fatalf("应有 3 个目录，实际 %d", g.Dirs())
	}
	if g.Size() != 5 {
		t.Fatalf("应有 5 个条目，实际 %d", g.Size())
	}
}

// 错配的条目不能张冠李戴：桃乃木かな 在 z-DMM(骑) 里绝不能被解析成 松嶋真麻。
func TestGfriendsDoesNotTrustScrambledPairing(t *testing.T) {
	g, err := parseGfriends([]byte(gfriendsFixture))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	// 松嶋真麻 只在 z-DMM(骑) 出现，且该目录里没有以她命名的真实文件，
	// 所以只能靠 value 里的 AI-Fix-松嶋真麻.jpg 命中。
	got := decodeURL(t, g.pickOne("松嶋真麻"))
	if !strings.Contains(got, "AI-Fix-") || !strings.Contains(got, "松嶋真麻") {
		t.Fatalf("松嶋真麻 应命中自己那张 AI-Fix 图，实际 %q", got)
	}
	// key 里那条 AI-Fix-松嶋真麻 也被 松嶋真麻 这条 value 覆盖过，
	// 但绝不能因为 z-DMM(骑) 有 桃乃木かな.jpg 这个 key 就返回松嶋真麻。
	momo := decodeURL(t, g.pickOne("桃乃木かな"))
	if !strings.Contains(momo, "桃乃木かな") || strings.Contains(momo, "松嶋真麻") {
		t.Fatalf("桃乃木かな 拿到别人的图: %q", momo)
	}
}

func TestGfriendsDirPriority(t *testing.T) {
	g, err := parseGfriends([]byte(gfriendsFixture))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	// 三上悠亜 在 y-Minnano 和 0-Hand-Storage 都有；0-Hand-Storage 优先级更高
	got := decodeURL(t, g.pickOne("三上悠亜"))
	if !strings.Contains(got, "0-Hand-Storage") {
		t.Fatalf("应命中优先级最高的 0-Hand-Storage，实际 %q", got)
	}
	// 该目录只有原图 png，不能因为别处有 AI-Fix 版本就串过去
	if !strings.HasSuffix(got, "三上悠亜.png") {
		t.Fatalf("应命中 0-Hand-Storage 的原图 png，实际 %q", got)
	}
}

func TestGfriendsPickFallsBackToAlias(t *testing.T) {
	g, err := parseGfriends([]byte(gfriendsFixture))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	// 第一个候选没图，应退到第二个
	got := g.Pick(nil, nil, "")
	if got != "" {
		t.Fatalf("空候选应返回空串，实际 %q", got)
	}
	got = g.Pick(nil, []string{"查无此人", "三上悠亜"}, "")
	if got == "" {
		t.Fatal("应退到第二个候选命中")
	}
}

func TestGfriendsBaseName(t *testing.T) {
	cases := []struct {
		in    string
		base  string
		isFix bool
	}{
		{"桃乃木かな.jpg", "桃乃木かな", false},
		{"AI-Fix-桃乃木かな.jpg", "桃乃木かな", true},
		{"AI-Fix-三上悠亜.PNG", "三上悠亜", true},
		{"梅塔ル.jpg", "梅塔ル", false},
		{"无扩展名", "", false},
	}
	for _, c := range cases {
		base, fix := gfriendsBaseName(c.in)
		if base != c.base || fix != c.isFix {
			t.Fatalf("%q → (%q,%v)，期望 (%q,%v)", c.in, base, fix, c.base, c.isFix)
		}
	}
}

func TestEscapePathKeepsSeparator(t *testing.T) {
	got := escapePath("z-DMM(骑)/桃乃木かな.jpg")
	if strings.Count(got, "/") != 1 {
		t.Fatalf("分隔符应保留: %q", got)
	}
	if strings.Contains(got, "(") {
		t.Fatalf("括号应被转义: %q", got)
	}
}

// TestGfriendsRealIndex 用真实 Filetree.json 跑一遍，平时跳过。
//
//	GFRIENDS_INDEX=/path/Filetree.json go test ./internal/actorsync -run RealIndex -v
func TestGfriendsRealIndex(t *testing.T) {
	path := os.Getenv("GFRIENDS_INDEX")
	if path == "" {
		t.Skip("未设置 GFRIENDS_INDEX，跳过")
	}
	g, err := LoadGfriendsFile(path)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	t.Logf("索引 %d 张图 / %d 个目录", g.Size(), g.Dirs())
	if g.Size() < 1000 {
		t.Fatalf("条目太少，索引可能构建错了: %d", g.Size())
	}
	names := []string{"桃乃木かな", "三上悠亜", "深田えいみ", "小湊よつ葉", "松嶋真麻"}
	for _, n := range names {
		u := g.Pick(context.Background(), []string{n}, "")
		if u == "" {
			t.Errorf("%s 没选到头像", n)
			continue
		}
		got := decodeURL(t, u)
		// 拼出来的 URL 里必须含演员本人名字，否则就是张冠李戴
		if !strings.Contains(filepath.Base(got), n) {
			t.Errorf("%s 拿到了别人的图: %s", n, got)
		}
		t.Logf("%-10s %s", n, got)
	}
}
