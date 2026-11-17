package actorsync

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBucketOf(t *testing.T) {
	cases := map[string]string{
		"小凑四叶":       "小",
		"あい":         "あ",
		"John Smith": "J",
		"123":        "1",
		"":           "_",
		"🎬actor":     "_",
	}
	for in, want := range cases {
		if got := BucketOf(in); got != want {
			t.Errorf("BucketOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitizeFileName(t *testing.T) {
	cases := map[string]string{
		"A/B:C":     "A_B_C",
		"  name.  ": "name",
		"CON":       "_CON",
		"小凑四叶":      "小凑四叶",
	}
	for in, want := range cases {
		if got := SanitizeFileName(in); got != want {
			t.Errorf("SanitizeFileName(%q) = %q, want %q", in, got, want)
		}
	}
	// 超长按 rune 截断，不能切坏 UTF-8
	long := ""
	for i := 0; i < 100; i++ {
		long += "あ"
	}
	if got := SanitizeFileName(long); len(got) > 80 || len([]rune(got)) == 0 {
		t.Errorf("截断结果异常: %d 字节, %d rune", len(got), len([]rune(got)))
	}
}

func TestBuildKey(t *testing.T) {
	ids := map[string]string{"JavStash": "8F2C", "TPDB": "123"}
	if got := BuildKey(ids, "某人"); got != "javstash:8f2c" {
		t.Errorf("优先 JavStash 失败: %s", got)
	}
	if got := BuildKey(map[string]string{"TPDB": "123"}, "某人"); got != "tpdb:123" {
		t.Errorf("降级 TPDB 失败: %s", got)
	}
	if got := BuildKey(nil, "某人"); got[:5] != "name:" {
		t.Errorf("名字哈希兜底失败: %s", got)
	}
}

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "actors.json")

	db, err := LoadDb(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(db.Actors) != 0 {
		t.Fatal("空库不该有记录")
	}

	a := &Actor{
		Key: "javstash:abc", Name: "小凑四叶", Status: StatusOK,
		EmbyIDs: []string{"local-1"}, CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if added := db.Upsert(a); !added {
		t.Fatal("首次写入应该是新增")
	}
	if err := db.Save(p); err != nil {
		t.Fatal(err)
	}

	db2, err := LoadDb(p)
	if err != nil {
		t.Fatal(err)
	}
	if db2.Count != 1 || db2.Actors[0].Name != "小凑四叶" {
		t.Fatalf("读回数据不对: %+v", db2)
	}

	// 再次 Upsert 同一 key，应该是更新且保留 created_at
	b := &Actor{Key: "javstash:abc", Name: "小凑四叶", Status: StatusNoSource, EmbyIDs: []string{"local-2"}}
	if added := db2.Upsert(b); added {
		t.Fatal("同 key 应该是更新")
	}
	if len(db2.Actors) != 1 {
		t.Fatalf("不该产生重复记录: %d", len(db2.Actors))
	}
	if len(db2.Actors[0].EmbyIDs) != 2 {
		t.Fatalf("EmbyIDs 应该合并: %v", db2.Actors[0].EmbyIDs)
	}
	if db2.Actors[0].CreatedAt == "" {
		t.Fatal("created_at 应该保留")
	}
	if _, err := os.Stat(p + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("临时文件应该被 rename 掉")
	}
}

func TestBuildPlan(t *testing.T) {
	past := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	future := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)

	db := &Db{Actors: []*Actor{
		{Key: "javstash:1", Name: "已存在", EmbyIDs: []string{"e1"}, Status: StatusOK},
		{Key: "javstash:2", Name: "待重试", EmbyIDs: []string{"e2"}, Status: StatusFailed, RetryAfter: &past},
		{Key: "javstash:3", Name: "退避中", EmbyIDs: []string{"e3"}, Status: StatusFailed, RetryAfter: &future},
	}}

	persons := []EmbyPerson{
		{ID: "e1", Name: "已存在"},
		{ID: "e2", Name: "待重试"},
		{ID: "e3", Name: "退避中"},
		{ID: "e4", Name: "新演员"},
	}

	plan := BuildPlan(db, persons, nil, false)
	if len(plan) != 2 {
		t.Fatalf("应该只抓 1 个新增 + 1 个重试，实际 %d: %+v", len(plan), plan)
	}
	if plan[0].Reason != "new" || plan[0].Name != "新演员" {
		t.Errorf("新演员应该排第一: %+v", plan[0])
	}
	if plan[1].Reason != "retry" {
		t.Errorf("第二个应该是重试: %+v", plan[1])
	}

	// --refresh 指定名字
	plan = BuildPlan(db, persons, []string{"已存在"}, false)
	found := false
	for _, it := range plan {
		if it.Reason == "refresh" && it.Name == "已存在" {
			found = true
		}
	}
	if !found {
		t.Fatalf("refresh 应该命中: %+v", plan)
	}
}

func TestOutcome(t *testing.T) {
	r := &FetchResult{Sources: map[string]bool{"javstash": true}, BirthDate: "1996-05-10"}
	if got := r.Outcome(); got != StatusOK {
		t.Errorf("有生日应为 ok，得到 %s", got)
	}
	r = &FetchResult{Sources: map[string]bool{"javstash": true}}
	if got := r.Outcome(); got != StatusPartial {
		t.Errorf("命中但无关键字段应为 partial，得到 %s", got)
	}
	r = &FetchResult{Sources: map[string]bool{"javstash": false}}
	if got := r.Outcome(); got != StatusNoSource {
		t.Errorf("全未命中应为 no_source，得到 %s", got)
	}
	r = &FetchResult{Sources: map[string]bool{"javstash": false}, SourceErrors: 1}
	if got := r.Outcome(); got != StatusFailed {
		t.Errorf("有网络错误应为 failed，得到 %s", got)
	}
}

func TestEncodePath(t *testing.T) {
	got := encodePath("/actors/小/小凑四叶.jpg")
	want := "/actors/%E5%B0%8F/%E5%B0%8F%E5%87%91%E5%9B%9B%E5%8F%B6.jpg"
	if got != want {
		t.Errorf("encodePath = %s, want %s", got, want)
	}
}

func TestNormalizeDate(t *testing.T) {
	cases := map[string]string{
		"1996/5/10":            "1996-05-10",
		"1996-05-10T00:00:00Z": "1996-05-10",
		"":                     "",
		"unknown":              "",
	}
	for in, want := range cases {
		if got := normalizeDate(in); got != want {
			t.Errorf("normalizeDate(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDictMergeOverride(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "main.csv")
	localPath := filepath.Join(dir, "local.csv")

	if err := os.WriteFile(mainPath, []byte("中文名,日文名\n小凑よつ叶,小湊よつ葉\n三上悠亚,三上悠亜\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(localPath, []byte("中文名,日文名\n三上悠亚,三上悠亜改\n小凑四叶,小湊よつ葉\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	d, err := LoadDict(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := d.Lookup("三上悠亚"); got != "三上悠亜" {
		t.Fatalf("主字典应命中三上悠亜，实际 %q", got)
	}
	if got := d.Lookup("小凑四叶"); got != "" {
		t.Fatalf("主字典不应命中 小凑四叶，实际 %q", got)
	}

	l, err := LoadDict(localPath)
	if err != nil {
		t.Fatal(err)
	}
	d.Merge(l)

	if got := d.Lookup("三上悠亚"); got != "三上悠亜改" {
		t.Fatalf("补充字典应覆盖同名条目，实际 %q", got)
	}
	if got := d.Lookup("小凑四叶"); got != "小湊よつ葉" {
		t.Fatalf("补充字典应新增条目，实际 %q", got)
	}
	if got := d.Lookup("小凑よつ叶"); got != "小湊よつ葉" {
		t.Fatalf("原有条目不应丢失，实际 %q", got)
	}
}

func TestDictMissingFile(t *testing.T) {
	d, err := LoadDict(filepath.Join(t.TempDir(), "nope.csv"))
	if err != nil {
		t.Fatalf("文件不存在不应报错: %v", err)
	}
	if d.Size() != 0 {
		t.Fatalf("应为空字典，实际 %d", d.Size())
	}
}
