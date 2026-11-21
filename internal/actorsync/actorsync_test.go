package actorsync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	// 男性即使抓到了全套资料，也不能算 ok
	r = &FetchResult{
		Sources:   map[string]bool{"javstash": true},
		BirthDate: "1990-01-01", Gender: "MALE",
	}
	if got := r.Outcome(); got != StatusSkipped {
		t.Errorf("男性应为 skipped，得到 %s", got)
	}
}

// ExportJSON 只给下游 ok / partial：skipped 是男性、no_source 是查无此人、
// failed 是这轮没抓成的空壳，三者留在 jsonl 真源里备查即可。
func TestExportJSONKeepsOnlyUsable(t *testing.T) {
	dir := t.TempDir()
	jsonl := filepath.Join(dir, "actors.jsonl")
	jsonPath := filepath.Join(dir, "actors.json")

	db := &Db{SchemaVersion: SchemaVersion, Actors: []*Actor{
		{Key: "a", Name: "有数据的", Status: StatusOK},
		{Key: "b", Name: "只抓到一部分", Status: StatusPartial},
		{Key: "c", Name: "男性", Status: StatusSkipped},
		{Key: "d", Name: "查无此人", Status: StatusNoSource},
		{Key: "e", Name: "这轮抓失败", Status: StatusFailed},
		{Key: "f", Name: "Kristina_Rose", Status: StatusPartial}, // 老字典下划线名
	}}
	if err := db.Save(jsonl); err != nil {
		t.Fatalf("写 jsonl 失败: %v", err)
	}
	if err := db.ExportJSON(jsonPath); err != nil {
		t.Fatalf("导出 json 失败: %v", err)
	}

	out, err := LoadDb(jsonPath)
	if err != nil {
		t.Fatalf("读回 json 失败: %v", err)
	}
	if out.Count != 2 || len(out.Actors) != 2 {
		t.Fatalf("actors.json 应只剩 2 条，得到 count=%d len=%d", out.Count, len(out.Actors))
	}
	for _, a := range out.Actors {
		if a.Status != StatusOK && a.Status != StatusPartial {
			t.Errorf("导出了不该有的状态 %s（%s）", a.Status, a.Name)
		}
	}

	// 真源不能跟着缩水，否则下次运行会把 skipped/no_source 当成新人重抓
	src, err := LoadDb(jsonl)
	if err != nil {
		t.Fatalf("读回 jsonl 失败: %v", err)
	}
	if len(src.Actors) != 6 {
		t.Errorf("actors.jsonl 应保留全部 6 条，得到 %d", len(src.Actors))
	}
}

// MDCx 生日是第 12 列，后加的；老格式只有 11 列，读到空串不能崩。
func TestLoadMDCxCSVBirthday(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "p.csv")
	body := "name,name_cn,aliases,birthplace,country,height,cup,career_start,tags,bio,romaji,birthday\n" +
		"桃乃木かな,桃乃木香奈,,东京都,JP,153,E,2015,,,Momonogi Kana,1996-12-24\n" +
		"三上悠亜,三上悠亚,,爱知县,JP,159,F,2015,,,Mikami Yua,1993-08-16\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := LoadMDCxCSV(path)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if got := db.Lookup("桃乃木かな").Birthday; got != "1996-12-24" {
		t.Errorf("Birthday = %q, want 1996-12-24", got)
	}
	// 别名也要能查到同一份档案
	if got := db.Lookup("桃乃木香奈").Birthday; got != "1996-12-24" {
		t.Errorf("按中文名查到生日 %q", got)
	}

	// 老格式：11 列，没有 birthday
	old := filepath.Join(dir, "old.csv")
	body = "name,name_cn,aliases,birthplace,country,height,cup,career_start,tags,bio,romaji\n" +
		"三上悠亜,三上悠亚,,爱知县,JP,159,F,2015,,,Mikami Yua\n"
	if err := os.WriteFile(old, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err = LoadMDCxCSV(old)
	if err != nil {
		t.Fatalf("老格式加载失败: %v", err)
	}
	if p := db.Lookup("三上悠亜"); p == nil || p.Birthday != "" {
		t.Errorf("老格式应解析成功且生日为空，得到 %+v", p)
	}
}

// 改名演员的旧名要能被别名索引命中。新导出用 | 分隔别名，旧名必须单独可查。
// 罗马字名里的空格不能被误拆成两段。
func TestMDCxAliasLookupRenamed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "alias.csv")
	body := "name,name_cn,aliases,birthplace,country,height,cup,career_start,tags,bio,romaji,birthday,bust,waist,hip\n" +
		"新ありな,,上乃木まな(デリヘル)|岩谷志季(撮影会)|橋本ありな|Arina Hashimoto,东京都,JP,167,C,2016,,,Arata Arina,1996-12-15,84,56,83\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := LoadMDCxCSV(path)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	// 别名里的旧名「橋本ありな」应单独命中本人
	if p := db.Lookup("橋本ありな"); p == nil || p.Name != "新ありな" {
		t.Fatalf("旧名 橋本ありな 应按别名命中 新ありな，得到 %+v", p)
	}
	// 罗马字别名内的空格要保留，整名可查
	if p := db.Lookup("Arina Hashimoto"); p == nil || p.Name != "新ありな" {
		t.Fatalf("罗马字别名 Arina Hashimoto 应命中，得到 %+v", p)
	}
	if p := db.Lookup("橋本ありな"); p != nil && p.Birthday != "1996-12-15" {
		t.Errorf("命中档案生日错误: %q", p.Birthday)
	}
	// splitPipe 对历史换行粘连也能切开（CSV 里用引号包裹内嵌换行的情况）
	if got := splitPipe("上乃木まな\n岩谷志季\n橋本ありな"); len(got) != 3 || got[2] != "橋本ありな" {
		t.Errorf("换行粘连切分错误: %#v", got)
	}
}

// MDCx 三围是厘米分项，三项齐备才用；英寸值要挡掉，不能当厘米存。
func TestMDCxMeasurements(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "m.csv")
	body := "name,name_cn,aliases,birthplace,country,height,cup,career_start,tags,bio,romaji,birthday,bust,waist,hip\n" +
		"橘めぐみ,橘惠,,东京都,JP,165,J,2021,巨乳,,,1998-01-01,112,64,99\n" + // 齐备
		"柏浦千晶,,,,,160,D,2022,,,,-,87,57,\n" + // 缺臀围
		"欧美派,,,,,170,C,,,,,-,34,24,35\n" // 英寸制，应全丢
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := LoadMDCxCSV(path)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	p := db.Lookup("橘めぐみ")
	if p == nil || p.Bust != 112 || p.Waist != 64 || p.Hip != 99 {
		t.Fatalf("三围解析错误: %+v", p)
	}
	if p := db.Lookup("柏浦千晶"); p == nil || p.Hip != 0 {
		t.Errorf("缺臀围时应为 0，得到 %+v", p)
	}
	if p := db.Lookup("欧美派"); p == nil || p.Bust != 0 || p.Waist != 0 || p.Hip != 0 {
		t.Errorf("英寸值应被丢弃，得到 %+v", p)
	}
}

// 三围统一英寸：厘米换算四舍五入，三项缺一不可。
func TestMeasurementsInches(t *testing.T) {
	cases := []struct {
		b, w, h int
		cup     string
		want    string
	}{
		// 有罩杯：第一段是「底围+杯」。112/64/99cm J 杯 → 下胸围 79.5 → band 36
		{112, 64, 99, "J", "36J-25-39"},
		{87, 57, 85, "D", "32D-22-33"},
		// 没有罩杯：第一段只能是胸围本身
		{82, 63, 83, "", "32-25-33"},
		// 罩杯不在日标表里（或写错），退回纯胸围，不瞎猜
		{82, 63, 83, "ZZ", "32-25-33"},
		// 缺任一围就整条不写
		{0, 64, 99, "C", ""},
		{82, 0, 83, "C", ""},
	}
	for _, c := range cases {
		if got := MeasurementsInches(c.b, c.w, c.h, c.cup); got != c.want {
			t.Errorf("MeasurementsInches(%d,%d,%d,%q) = %q, want %q", c.b, c.w, c.h, c.cup, got, c.want)
		}
	}
}

// 反推底围必须用日标杯差：日标 J 差 32.5cm，美标 J 只差 25.4cm，
// 用错标准会把 band 算小两码（实测 1.5 万人里 85% 挤到 28-30）。
func TestBandFromBust(t *testing.T) {
	cases := []struct {
		bust int
		cup  string
		want int
	}{
		{112, "J", 36}, // 下胸围 79.5cm ≈ 日标 80 → 美标 36
		{85, "C", 32},  // 下胸围 70cm
		{87, "D", 32},  // 下胸围 69.5cm
		{95, "F", 32},  // 下胸围 72.5cm
		{0, "C", 0},    // 缺胸围
		{85, "", 0},    // 缺罩杯
		{85, "ZZ", 0},  // 罩杯不在表里
	}
	for _, c := range cases {
		if got := BandFromBust(c.bust, c.cup); got != c.want {
			t.Errorf("BandFromBust(%d,%q) = %d, want %d", c.bust, c.cup, got, c.want)
		}
	}
	// bust 太小配大杯 → 下胸围低于 55cm，输入明显录错，拒绝而不是瞎算
	if got := BandFromBust(60, "J"); got != 0 {
		t.Errorf("异常输入应返回 0，得到 %d", got)
	}
}

// TPDB 的第一段有两种形态，两种都原样返回：34E 是「底围+杯」，34 是胸围。
func TestBWHInches(t *testing.T) {
	// 34E-24-35：第一段是「底围 34 + E 杯」，正是欧美标准写法，直接留用
	p := &Performer{Measurements: "34E-24-35", Waist: 24, Hip: 35}
	if f, w, h := p.bwhInches(); f != "34E" || w != 24 || h != 35 {
		t.Errorf("带杯型的串应原样取第一段，得到 %q/%d/%d", f, w, h)
	}
	// 34-24-35：第一段就是胸围
	p = &Performer{Measurements: "34-24-35"}
	if f, w, h := p.bwhInches(); f != "34" || w != 24 || h != 35 {
		t.Errorf("纯数字串应三段全取，得到 %q/%d/%d", f, w, h)
	}
	// 只有两段，不构成三围
	p = &Performer{Measurements: "34E-24"}
	if f, _, _ := p.bwhInches(); f != "" {
		t.Errorf("不足三段应返回空，得到 %q", f)
	}
	// 没有 measurements，只有独立字段
	p = &Performer{Waist: 24, Hip: 35}
	if f, w, h := p.bwhInches(); f != "" || w != 24 || h != 35 {
		t.Errorf("缺 measurements 时应从独立字段取腰臀，得到 %q/%d/%d", f, w, h)
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

// 同一个日文名在字典里有多行、多个中文名时，全部别名都要留下。
// 典型：橋本ありな 在 amane 源写「桥本有菜」、mdcx 源写「新有菜」，
// 主名取了「新有菜」，但 Emby 库里存的是旧名「桥本有菜」——旧名必须进 aliases。
func TestDictAllAliasesMultipleChineseNames(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dict.csv")
	body := "别名,日文名,中文名,来源\n" +
		"Arina Hashimoto,橋本ありな,新有菜,mdcx\n" +
		"新ありな,橋本ありな,桥本有菜,amane\n" +
		"桥本有菜,橋本ありな,桥本有菜,amane\n" +
		"上乃木まな,橋本ありな,桥本有菜,amane\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := LoadDict(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := d.ChineseName("橋本ありな"); got != "新有菜" {
		t.Errorf("首选中文名应为新有菜，实际 %q", got)
	}
	for _, name := range []string{"橋本ありな", "新有菜", "桥本有菜", "新ありな", "Arina Hashimoto"} {
		aliases := d.AllAliases(name)
		joined := strings.Join(aliases, "|")
		for _, want := range []string{"桥本有菜", "新有菜", "新ありな", "上乃木まな"} {
			if !strings.Contains(joined, want) {
				t.Errorf("AllAliases(%q)=%v 缺少 %q", name, aliases, want)
			}
		}
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

// TPDB 是英文站，必须用英文名搜。StashDB（stash-box 英文站）命中的
// performer.Name 是最可靠的关键词，必须排在搜索词最前面。
func TestTPDBTermsStashDBNameFirst(t *testing.T) {
	candidates := []string{"山岸逢花", "山岸あや花"}
	js := &Performer{Source: "javstash", Name: "Aika Yamagishi"}
	sd := &Performer{Source: "stashdb", Name: "Aika Yamagishi"}

	got := tpdbTerms(candidates, nil, "山岸逢花", nil, sd)
	if len(got) == 0 || got[0] != "Aika Yamagishi" {
		t.Fatalf("StashDB 英文名应排第一，得到 %v", got)
	}

	// StashDB 与 JavStash 同时给名字（正常不会同时命中，但要保证 StashDB 优先）
	got = tpdbTerms(candidates, nil, "山岸逢花", js, sd)
	if got[0] != sd.Name {
		t.Fatalf("StashDB 名应压过 JavStash 名，得到 %v", got)
	}
}

// JavStash 命中时 StashDB 不查，此时用 JavStash 的罗马字名搜 TPDB。
func TestTPDBTermsJavStashFallback(t *testing.T) {
	candidates := []string{"山岸逢花"}
	js := &Performer{Source: "javstash", Name: "Aika Yamagishi"}
	got := tpdbTerms(candidates, nil, "山岸逢花", js, nil)
	if len(got) == 0 || got[0] != "Aika Yamagishi" {
		t.Fatalf("JavStash 名应排第一（StashDB 缺席），得到 %v", got)
	}
}

// 两个在线源都没命中时，退回 MDCx 罗马字，再退回清单原名。
func TestTPDBTermsMDCxAndCandidatesFallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mdcx.csv")
	body := "name,name_cn,aliases,birthplace,country,height,cup,career_start,tags,bio,romaji,birthday,bust,waist,hip\n" +
		"山岸あや花,,山岸逢花,东京都,JP,163,F,2014,,,Yamagishi Ayaka,1996-01-01,88,58,86\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	mdcx, err := LoadMDCxCSV(path)
	if err != nil {
		t.Fatal(err)
	}
	candidates := []string{"山岸逢花"}
	got := tpdbTerms(candidates, mdcx, "山岸逢花", nil, nil)
	if len(got) == 0 || got[0] != "Yamagishi Ayaka" {
		t.Fatalf("MDCx 罗马字应排第一，得到 %v", got)
	}
	if got[len(got)-1] != "山岸逢花" {
		t.Fatalf("清单原名应保留在候选末尾，得到 %v", got)
	}

	// MDCx 也没罗马字时，只剩清单原名
	got = tpdbTerms(candidates, nil, "山岸逢花", nil, nil)
	if len(got) != 1 || got[0] != "山岸逢花" {
		t.Fatalf("应只剩清单原名，得到 %v", got)
	}
}

// 硬规定：搜 TPDB 必须用 StashDB 英文名。JavStash 命中时，从它的 urls 里
// 提取 StashDB UUID 精确反查（findPerformer），拿到标准英文名。
func TestStashDBLookupByJavStashUUID(t *testing.T) {
	const uuid = "00f5bcc6-f85e-444d-a6d8-7b1b52fb4717"
	var gotQuery, gotID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotQuery = req.Query
		gotID, _ = req.Variables["id"].(string)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"findPerformer":{"id":"` + uuid + `","name":"Iori Ohashi","aliases":["大桥依织"],"gender":"FEMALE","urls":[]}}}`))
	}))
	defer srv.Close()

	c := NewStashBoxClient("stashdb", srv.URL, "test-key")
	p, err := c.Search(context.Background(), uuid)
	if err != nil {
		t.Fatalf("按 UUID 查询失败: %v", err)
	}
	if p.Name != "Iori Ohashi" {
		t.Fatalf("英文名错误: %q", p.Name)
	}
	if !strings.Contains(gotQuery, "findPerformer") || gotID != uuid {
		t.Fatalf("应走 findPerformer(id) 路径，query=%q id=%q", gotQuery, gotID)
	}
	// 反查到的英文名必须能被 tpdbTerms 排到第一位
	terms := tpdbTerms([]string{"大桥依织"}, nil, "大桥依织", nil, p)
	if len(terms) == 0 || terms[0] != "Iori Ohashi" {
		t.Fatalf("StashDB 英文名应排 TPDB 搜索词第一，得到 %v", terms)
	}
}

// dramaface slug 实测规则：全名拼接（两种词序）+ 末段兜底，大小写保留。
func TestDramaSlugs(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"Kana Momonogi", []string{"KanaMomonogi", "MomonogiKana", "Momonogi"}},
		{"Aika Yamagishi", []string{"AikaYamagishi", "YamagishiAika", "Yamagishi"}},
		{"aika", []string{"aika"}},
		{"", nil},
	}
	for _, c := range cases {
		got := dramaSlugs(c.in)
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("dramaSlugs(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// dramaface 简介：meta description 里取正文，剥掉开头属性串；未命中页返回空。
func TestDramaFaceFetchBio(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/model/MomonogiKana":
			_, _ = w.Write([]byte(`<html><head><title>桃乃木香奈 (桃乃木かな,Kana Momonogi) 角色解析</title><meta name="description" content="桃乃木かな（日文名：桃乃木 かな，出生于1996年12月24日）是日本知名的AV女优、歌手，隶属于事务所。她来自东京都，2015年进入AV行业并于同年10月出道，随后崭露头角获得多项大奖。"></head></html>`))
		case "/model/YuaMikami":
			// 开头带属性串，需剥掉后才是正文
			_, _ = w.Write([]byte(`<html><head><title>三上悠亚 (三上悠亜,Yua Mikami) 角色解析</title><meta name="description" content="身高: 155cm 罩杯: D 胸围: 84cm 三上悠亚，原名鬼头桃菜，1993年出生于日本爱知县名古屋市，是前女子偶像团体SKE48的成员，后成为AV演员，演艺经历丰富。"></head></html>`))
		case "/model/Kana":
			_, _ = w.Write([]byte(`<html><head><title>提示信息 -  9K星图</title><meta name="description" content=",9K星图"></head></html>`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := &DramaFaceClient{HTTP: srv.Client(), Base: srv.URL + "/model/"}

	// 正常简介：meta description 直接返回
	bio := c.FetchBio(context.Background(), []string{"Kana Momonogi"})
	if !strings.HasPrefix(bio, "桃乃木かな") {
		t.Fatalf("应取到桃乃木简介，得到 %q", bio)
	}

	// 属性串开头：剥掉前缀后返回正文
	bio = c.FetchBio(context.Background(), []string{"Yua Mikami"})
	if !strings.HasPrefix(bio, "三上悠亚") {
		t.Fatalf("应剥掉属性串返回正文，得到 %q", bio)
	}
	if strings.Contains(bio, "身高: 155cm") {
		t.Fatalf("属性串不应残留：%q", bio)
	}

	// 未命中页（提示信息）跳过，最后一个不存在的 slug 返回空
	bio = c.FetchBio(context.Background(), []string{"Kana"})
	if bio != "" {
		t.Fatalf("未命中页应返回空，得到 %q", bio)
	}

	// 直接验证属性串剥离函数
	desc := "身高: 155cm 罩杯: D 胸围: 84cm 三上悠亚，原名鬼头桃菜，1993年出生于日本爱知县名古屋市，是前女子偶像团体SKE48的成员，后成为AV演员。"
	if ValidBio(desc) {
		t.Fatal("以属性串开头的文本不应通过 ValidBio")
	}
	cleaned := stripDramaFields(desc)
	if !ValidBio(cleaned) || !strings.HasPrefix(cleaned, "三上悠亚") {
		t.Fatalf("剥离属性串后应是正文，得到 %q", cleaned)
	}
}
