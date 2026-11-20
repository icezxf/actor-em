package actorsync

import (
	"os"
	"path/filepath"
	"strings"
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
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
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
	b := &Actor{Key: "javstash:abc", Name: "小凑四叶", Status: StatusNoSource}
	if added := db2.Upsert(b); added {
		t.Fatal("同 key 应该是更新")
	}
	if len(db2.Actors) != 1 {
		t.Fatalf("不该产生重复记录: %d", len(db2.Actors))
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
		{Key: "javstash:1", Name: "已存在", Status: StatusOK},
		{Key: "javstash:2", Name: "待重试", Status: StatusFailed, RetryAfter: &past},
		{Key: "javstash:3", Name: "退避中", Status: StatusFailed, RetryAfter: &future},
		{Key: "name:4", Name: "查无此人", Status: StatusNoSource, RetryAfter: &past},
	}}

	names := []string{"已存在", "待重试", "退避中", "查无此人", "新演员"}

	plan := BuildPlan(db, names, nil, false)
	if len(plan) != 3 {
		t.Fatalf("应该抓 1 个新增 + 2 个到期重试，实际 %d: %+v", len(plan), plan)
	}
	if plan[0].Reason != "new" || plan[0].Name != "新演员" {
		t.Errorf("新演员应该排第一: %+v", plan[0])
	}
	for _, it := range plan[1:] {
		if it.Reason != "retry" {
			t.Errorf("后面应该是重试: %+v", it)
		}
	}

	// --refresh 指定名字
	plan = BuildPlan(db, names, []string{"已存在"}, false)
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

func TestLoadNamesFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "actors.txt")
	content := "# 注释行\n小凑四叶\n\n  三上悠亚  \n小湊よつ葉\n小凑四叶\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadNamesFile(p)
	if err != nil {
		t.Fatal(err)
	}
	// 空行/注释跳过，重复（规范化后）只留一次
	if len(got) != 3 {
		t.Fatalf("期望 3 条，实际 %d: %+v", len(got), got)
	}
	if got[1] != "三上悠亚" {
		t.Fatalf("应去掉首尾空格，实际 %q", got[1])
	}
}

func TestSaveNamesFileRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "actors.txt")
	if err := SaveNamesFile(p, []string{"b", "a", "a", " "}); err != nil {
		t.Fatal(err)
	}
	got, err := LoadNamesFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("应去重并排序，实际 %+v", got)
	}
}

func TestParseNFOActors(t *testing.T) {
	nfo := []byte(`<?xml version="1.0" encoding="utf-8"?>
<movie>
  <title>某部作品</title>
  <name>片名不应被当成演员</name>
  <actor>
    <name>小湊よつ葉</name>
    <role>主演</role>
  </actor>
  <actor><name> 三上悠亜 </name></actor>
  <actor><name></name></actor>
</movie>`)
	got := ParseNFOActors(nfo)
	if len(got) != 2 {
		t.Fatalf("期望 2 个演员，实际 %d: %v", len(got), got)
	}
	if got[0] != "小湊よつ葉" || got[1] != "三上悠亜" {
		t.Fatalf("解析结果不对: %v", got)
	}
}

func TestParseNFOInvalid(t *testing.T) {
	if got := ParseNFOActors([]byte("not xml at all")); got != nil {
		t.Fatalf("非 xml 应返回空，实际 %v", got)
	}
}

func TestExportXLSX(t *testing.T) {
	db := &Db{SchemaVersion: 1}
	db.Actors = []*Actor{
		{
			Key: "javstash:abc", Name: "小凑四叶", NameOriginal: StrPtr("小湊よつ葉"),
			Aliases: []string{"こみなとよつば"}, Status: StatusOK, BirthDate: StrPtr("1996-05-10"),
			HeightCm: IntPtr(160), ProviderIDs: map[string]string{"JavStash": "abc"},
			Sources:   map[string]bool{"javstash": true, "tpdb": false},
			Avatar:    StrPtr("https://alist.example.com/d/actors/x.jpg"),
			ScrapedAt: "2026-01-01T00:00:00Z",
		},
	}
	out := filepath.Join(t.TempDir(), "actors.xlsx")
	if err := ExportXLSX(out, db); err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	fi, err := os.Stat(out)
	if err != nil || fi.Size() < 2000 {
		t.Fatalf("导出文件异常: %v size=%v", err, fi)
	}
}

func TestLoadDbLegacyEmbyIDs(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "actors.json")
	legacy := `{"schema_version":1,"count":1,"actors":[{"key":"javstash:1","name":"旧记录","emby_ids":["local-1"],"status":"ok"}]}`
	if err := os.WriteFile(p, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	db, err := LoadDb(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(db.Actors) != 1 || db.Actors[0].Name != "旧记录" {
		t.Fatalf("旧数据应能正常读取: %+v", db.Actors)
	}
	if db.SchemaVersion != SchemaVersion {
		t.Fatalf("schema_version 应升到 %d，实际 %d", SchemaVersion, db.SchemaVersion)
	}

	if err := db.Save(p); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "emby_ids") {
		t.Fatal("保存后不应再包含 emby_ids")
	}
}

func TestBuildActorKeepsAvatarWhenNewImageFails(t *testing.T) {
	old := &Actor{
		Key:              "javstash:1",
		Name:             "某人",
		Avatar:           StrPtr("https://cdn.example.com/old.jpg"),
		AvatarSourceSite: StrPtr("javstash"),
	}
	res := &FetchResult{
		NameOriginal: "某人",
		ImageURL:     "", // 这次没抓到图
		ProviderIDs:  map[string]string{"JavStash": "1"},
		Sources:      map[string]bool{"javstash": true},
		BirthDate:    "1990-01-01",
	}

	a := BuildActor("某人", res, old)
	if StrVal(a.Avatar) != "https://cdn.example.com/old.jpg" {
		t.Fatalf("没抓到新图时不应清空旧头像: %q", StrVal(a.Avatar))
	}
	if StrVal(a.AvatarSourceSite) != "javstash" {
		t.Fatalf("没抓到新图时来源应保持: %q", StrVal(a.AvatarSourceSite))
	}
}

func TestBuildActorUsesGfriendsAvatarAndMirror(t *testing.T) {
	raw := "https://raw.githubusercontent.com/gfriends/gfriends/master/Content/y-Minnano/x.jpg"
	res := &FetchResult{
		NameOriginal: "某人",
		ImageURL:     raw,
		ImageSite:    "gfriends",
		ProviderIDs:  map[string]string{"JavStash": "1"},
		Sources:      map[string]bool{"javstash": true, "gfriends": true},
	}

	a := BuildActor("某人", res, nil)
	if StrVal(a.Avatar) != raw {
		t.Fatalf("avatar 应为 gfriends 原图: %q", StrVal(a.Avatar))
	}
	wantCN := "https://cdn.jsdelivr.net/gh/gfriends/gfriends@master/Content/y-Minnano/x.jpg"
	if StrVal(a.AvatarCN) != wantCN {
		t.Fatalf("avatar_cn 应为 jsdelivr 镜像: %q", StrVal(a.AvatarCN))
	}
	if StrVal(a.AvatarSourceSite) != "gfriends" {
		t.Fatalf("来源应为 gfriends: %q", StrVal(a.AvatarSourceSite))
	}
}
