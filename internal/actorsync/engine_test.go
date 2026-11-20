package actorsync

import (
	"context"
	"testing"
)

// fakeFetcher 免掉真实网络，让 Engine 的分支逻辑可测。
type fakeFetcher struct {
	res   *FetchResult
	calls int
}

func (f *fakeFetcher) Fetch(_ context.Context, name string) *FetchResult {
	f.calls++
	if f.res == nil {
		return &FetchResult{
			NameOriginal: name,
			ProviderIDs:  map[string]string{},
			Sources:      map[string]bool{},
		}
	}
	return f.res
}

func okResult(name string) *FetchResult {
	return &FetchResult{
		NameOriginal: name,
		BirthDate:    "1999-01-01",
		HeightCm:     160,
		ImageURL:     "https://cdn.example.com/a.jpg",
		ImageSite:    "javstash",
		Images:       []string{"https://cdn.example.com/a.jpg", "https://cdn.example.com/b.jpg"},
		ProviderIDs:  map[string]string{"JavStash": "abc"},
		Sources:      map[string]bool{"javstash": true},
	}
}

func newTestEngine(f NameFetcher) *Engine {
	return &Engine{
		DB:      &Db{SchemaVersion: SchemaVersion, Actors: []*Actor{}},
		Fetcher: f,
	}
}

func TestEngineProcessNewActor(t *testing.T) {
	e := newTestEngine(&fakeFetcher{res: okResult("小凑四叶")})

	out, err := e.Process(context.Background(), "小凑四叶", ProcessOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Added {
		t.Fatal("新名字应该入库")
	}
	if len(e.DB.Actors) != 1 {
		t.Fatalf("库里应有 1 条，实际 %d", len(e.DB.Actors))
	}
	if out.Actor.Status != StatusOK {
		t.Fatalf("状态应为 ok，实际 %s", out.Actor.Status)
	}
	// 候选：数据源给的两张，没有当前图时保持原序
	if len(out.Candidates) != 2 || out.Candidates[0] != "https://cdn.example.com/a.jpg" {
		t.Fatalf("候选不对: %v", out.Candidates)
	}
	// 不转存：有图就直接用网络地址当 avatar
	if got := StrVal(out.Actor.Avatar); got != "https://cdn.example.com/a.jpg" {
		t.Fatalf("avatar 应是网络地址，实际 %q", got)
	}
	if StrVal(out.Actor.AvatarSourceSite) != "javstash" {
		t.Fatalf("avatar_source_site 应是 javstash，实际 %q", StrVal(out.Actor.AvatarSourceSite))
	}
	if out.Actor.AvatarCN != nil {
		t.Fatalf("非 gfriends 的图没有国内镜像，应为空，实际 %q", StrVal(out.Actor.AvatarCN))
	}
	if out.AvatarNote == "" {
		t.Fatal("应该说明头像处理结果")
	}
}

func TestEngineNoDBWrite(t *testing.T) {
	e := newTestEngine(&fakeFetcher{res: okResult("某人")})
	if _, err := e.Process(context.Background(), "某人", ProcessOptions{NoDBWrite: true}); err != nil {
		t.Fatal(err)
	}
	if len(e.DB.Actors) != 0 {
		t.Fatal("dry-run 不该写库")
	}
}

func TestEngineUnfoundDoesNotPolluteDB(t *testing.T) {
	e := newTestEngine(&fakeFetcher{}) // 全空结果

	out, err := e.Process(context.Background(), "拼错的名字", ProcessOptions{KeepOnFailure: true})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Kept {
		t.Fatal("应该标记为 Kept")
	}
	if out.Actor != nil {
		t.Fatalf("库里没有这条，不该凭空造记录: %+v", out.Actor)
	}
	if len(e.DB.Actors) != 0 {
		t.Fatalf("抓不到又没有旧记录时不该入库，实际 %d 条", len(e.DB.Actors))
	}
}

func TestEngineKeepsOldRecordOnFailure(t *testing.T) {
	e := newTestEngine(&fakeFetcher{})
	old := &Actor{
		Key:         "javstash:abc",
		Name:        "小凑四叶",
		BirthDate:   StrPtr("1999-01-01"),
		Status:      StatusOK,
		CreatedAt:   "2026-01-01T00:00:00Z",
		ProviderIDs: map[string]string{"JavStash": "abc"},
		Avatar:      StrPtr("https://alist.example.com/d/actors/小/小凑四叶.jpg"),
	}
	e.DB.Actors = append(e.DB.Actors, old)

	out, err := e.Process(context.Background(), "小凑四叶", ProcessOptions{KeepOnFailure: true})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Kept {
		t.Fatal("应该保留旧记录")
	}
	if out.Actor != old {
		t.Fatal("应原样返回旧记录")
	}
	if out.Actor.Status != StatusOK {
		t.Fatalf("不该把 ok 降级成 %s", out.Actor.Status)
	}
	if StrVal(out.Actor.BirthDate) != "1999-01-01" {
		t.Fatal("旧元数据不该丢")
	}
}

func TestEngineFailureWithoutKeepDowngrades(t *testing.T) {
	// 批量同步的语义：清单驱动，抓不到就记 no_source（保持原有行为）
	e := newTestEngine(&fakeFetcher{})
	old := &Actor{Key: "javstash:abc", Name: "小凑四叶", Status: StatusOK}
	e.DB.Actors = append(e.DB.Actors, old)

	out, err := e.Process(context.Background(), "小凑四叶", ProcessOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Kept {
		t.Fatal("KeepOnFailure=false 时不该走保留分支")
	}
	if out.Actor.Status != StatusNoSource {
		t.Fatalf("应降级为 no_source，实际 %s", out.Actor.Status)
	}
	if len(e.DB.Actors) != 1 {
		t.Fatalf("不该新增记录，实际 %d 条", len(e.DB.Actors))
	}
}

func TestEngineUsesManualChoice(t *testing.T) {
	e := newTestEngine(&fakeFetcher{res: okResult("小凑四叶")})
	e.Choices = NewChoices("")
	e.Choices.Set("javstash:abc", "https://cdn.example.com/picked.jpg", "telegram")

	out, err := e.Process(context.Background(), "小凑四叶", ProcessOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Manual {
		t.Fatal("应该识别到人工选图")
	}
	if out.Res.ImageURL != "https://cdn.example.com/picked.jpg" {
		t.Fatalf("人工选择的图应覆盖自动抓的图，实际 %q", out.Res.ImageURL)
	}
	if out.Res.ImageSite != "manual" {
		t.Fatalf("来源应标成 manual，实际 %q", out.Res.ImageSite)
	}
	// 选中图排第一，方便下次对照
	if len(out.Candidates) == 0 || out.Candidates[0] != "https://cdn.example.com/picked.jpg" {
		t.Fatalf("候选第 1 张应是选中的图: %v", out.Candidates)
	}
}

func TestApplyChoiceMissingRecord(t *testing.T) {
	e := newTestEngine(&fakeFetcher{})
	if _, _, err := e.ApplyChoice(context.Background(), "nope:1", "https://x/y.jpg"); err == nil {
		t.Fatal("库里没有记录时应该报错")
	}
}

func TestApplyChoiceUsesNetworkURL(t *testing.T) {
	e := newTestEngine(&fakeFetcher{})
	actor := &Actor{
		Key:    "javstash:abc",
		Name:   "小凑四叶",
		Avatar: StrPtr("https://cdn.example.com/old.jpg"),
	}
	e.DB.Actors = append(e.DB.Actors, actor)

	got, note, err := e.ApplyChoice(context.Background(), "javstash:abc", "https://x/y.jpg")
	if err != nil {
		t.Fatalf("用网络地址不该报错: %v", err)
	}
	if got != actor {
		t.Fatal("应返回原记录")
	}
	if StrVal(actor.Avatar) != "https://x/y.jpg" {
		t.Fatalf("avatar 应更新为选中地址: %q", StrVal(actor.Avatar))
	}
	if StrVal(actor.AvatarSourceSite) != "manual" {
		t.Fatalf("来源应标 manual，实际 %q", StrVal(actor.AvatarSourceSite))
	}
	if note == "" {
		t.Fatal("应该说明处理结果")
	}
}
