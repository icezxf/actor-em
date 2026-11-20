package actorsync

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// NameFetcher 是 Engine 依赖的最小抓取接口。*Fetcher 实现了它；
// 抽成接口是为了让「抓取失败 / 人工选图 / 不入库」这些分支能在测试里覆盖到，
// 否则每跑一次单测都要真的去请求五个数据源。
type NameFetcher interface {
	Fetch(ctx context.Context, name string) *FetchResult
}

// Engine 封装「抓一个演员 → 生成记录 → 传头像 → 落库」这条链路。
// 批量同步（cmd/actorsync/main.go）和 Telegram 交互服务都走它，
// 避免两条路径各写一份逻辑、久了行为漂移。
type Engine struct {
	DB      *Db
	Fetcher NameFetcher
	Choices *Choices
	HTTP    *http.Client

	DataFile string

	Log func(format string, args ...any)
}

func (e *Engine) logf(format string, args ...any) {
	if e.Log != nil {
		e.Log(format, args...)
	}
}

func (e *Engine) httpClient() *http.Client {
	if e.HTTP != nil {
		return e.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// ProcessOptions 控制单次处理的副作用范围。
type ProcessOptions struct {
	// KeepOnFailure：抓取完全失败且库里已有记录时保留旧记录，不降级成 no_source。
	// 交互式查询默认开启——用户只是问一句，不该把好数据改坏。
	KeepOnFailure bool
	// SkipAvatar：只补元数据，不下载/上传头像。
	SkipAvatar bool
	// NoDBWrite：不写回内存中的库（--dry-run 用）。
	NoDBWrite bool
}

// ProcessResult 是处理一个演员的完整结果。
type ProcessResult struct {
	Key        string
	Name       string
	Actor      *Actor
	Res        *FetchResult
	Candidates []string // 已去重，第 1 张是当前正在用的图
	AvatarNote string
	Manual     bool // 用了人工选过的图
	Kept       bool // 抓取失败，保留了库里旧记录
	Added      bool // 本次新入库
}

// FetchPhase 只做网络抓取，不碰 DB，可以安全地在多个 goroutine 里并发跑。
func (e *Engine) FetchPhase(ctx context.Context, name string) *FetchResult {
	if e.Fetcher == nil {
		return &FetchResult{NameOriginal: name, ProviderIDs: map[string]string{}, Sources: map[string]bool{}}
	}
	return e.Fetcher.Fetch(ctx, name)
}

// ApplyPhase 把抓取结果落成记录。它会读写 DB，**必须串行调用**。
// 拆成两段是为了让批量同步能并发抓、串行写。
func (e *Engine) ApplyPhase(ctx context.Context, name string, res *FetchResult, opts ProcessOptions) (*ProcessResult, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("演员名为空")
	}
	if e.DB == nil {
		return nil, fmt.Errorf("未初始化数据库")
	}

	existing := e.DB.FindByName(name)

	key := BuildKey(res.ProviderIDs, name)
	if existing != nil {
		key = existing.Key
	}

	manualPick := ""
	if e.Choices != nil {
		if url, ok := e.Choices.Get(key); ok {
			manualPick = url
			if res.ImageURL != url {
				res.ImageURL = url
				res.ImageSite = "manual"
			}
		}
	}

	out := &ProcessResult{Key: key, Res: res, Manual: manualPick != ""}

	// 抓取彻底失败、库里又有记录：保留旧数据。
	// 否则一次数据源抖动就会把好好的 ok 记录改成 no_source，元数据全丢。
	if opts.KeepOnFailure && manualPick == "" {
		switch res.Outcome() {
		// skipped 也要落盘：它是「判定过、不是目标演员」的结论，
		// 不写进去下次还会重新抓一遍，白费 900 次请求。
		case StatusOK, StatusPartial, StatusSkipped:
		default:
			if existing == nil {
				// 库里没有、这次也没抓到：不入库。
				// 交互模式下名字是手打的，拼错一次就留一条 no_source 只会污染库。
				out.Name = name
				out.Kept = true
				out.AvatarNote = "所有源都没找到，未入库"
				out.Candidates = res.Images
				return out, nil
			}
			out.Actor = existing
			out.Name = existing.Name
			out.Kept = true
			out.AvatarNote = "本次未抓到，保留原数据"
			out.Candidates = OrderCandidates(res.Images, StrVal(existing.Avatar))
			return out, nil
		}
	}

	actor := BuildActor(name, res, existing)
	out.Actor = actor
	out.Name = actor.Name

	avatarNote := "无图"
	if manualPick != "" {
		avatarNote = "人工选图"
	}
	switch {
	case opts.SkipAvatar:
		if res.ImageURL != "" {
			avatarNote = "已跳过头像处理"
		}
	case res.ImageURL == "":
		// 保持「无图」
	default:
		// 不转存：直接用数据源给的网络地址当头像。
		applyAvatarURL(actor, res.ImageURL, res.ImageSite)
		avatarNote = "已用网络头像"
	}

	out.AvatarNote = avatarNote
	out.Candidates = OrderCandidates(res.Images, res.ImageURL)
	if !opts.NoDBWrite {
		out.Added = e.DB.Upsert(actor)
	}
	return out, nil
}

// Process 抓一个演员并写入 DB，是 FetchPhase + ApplyPhase 的组合。
// 交互式（Telegram）场景单人处理，用这个就行；批量同步要并发，走拆分后的两段。
func (e *Engine) Process(ctx context.Context, name string, opts ProcessOptions) (*ProcessResult, error) {
	if e.Fetcher == nil {
		return nil, fmt.Errorf("未初始化数据源")
	}
	res := e.FetchPhase(ctx, name)
	return e.ApplyPhase(ctx, name, res, opts)
}

// ApplyChoice 把用户选中的图片网络地址写进记录，就地更新。
// 不下载、不转存：avatar 直接就是这张图的网络地址。
// 返回更新后的记录与一句人类可读的说明。
func (e *Engine) ApplyChoice(ctx context.Context, key, imageURL string) (*Actor, string, error) {
	actor := e.DB.FindByKey(key)
	if actor == nil {
		return nil, "", fmt.Errorf("库里没有记录 %s", key)
	}
	imageURL = strings.TrimSpace(imageURL)
	if imageURL == "" {
		return nil, "", fmt.Errorf("图片地址为空")
	}

	applyAvatarURL(actor, imageURL, "manual")
	return actor, "已用这张图的网络地址作头像", nil
}

// Save 把内存中的库落盘。
func (e *Engine) Save() error {
	if e.DB == nil || e.DataFile == "" {
		return nil
	}
	return e.DB.Save(e.DataFile)
}

// applyAvatarURL 把图片的网络地址直接写进头像字段，不经过任何转存。
// avatar 是原图直链，avatar_cn 是它的国内镜像（只有 gfriends 的 raw 地址有镜像，
// 其他源返回空串，下游按「没有镜像就用 avatar」处理）。
func applyAvatarURL(actor *Actor, sourceURL, sourceSite string) {
	actor.Avatar = StrPtr(sourceURL)
	actor.AvatarSourceSite = StrPtr(sourceSite)
	actor.AvatarCN = StrPtr(MirrorCN(sourceURL))
}
