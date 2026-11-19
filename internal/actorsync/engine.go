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
	Alist   *AlistClient
	Choices *Choices
	HTTP    *http.Client

	DataFile    string
	AlistDir    string
	ForceAvatar bool

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

// Process 抓一个演员并写入内存中的 DB（是否落盘由调用方决定）。
func (e *Engine) Process(ctx context.Context, name string, opts ProcessOptions) (*ProcessResult, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("演员名为空")
	}
	if e.Fetcher == nil {
		return nil, fmt.Errorf("未初始化数据源")
	}
	if e.DB == nil {
		return nil, fmt.Errorf("未初始化数据库")
	}

	existing := e.DB.FindByName(name)
	res := e.Fetcher.Fetch(ctx, name)

	key := BuildKey(res.ProviderIDs, name)
	if existing != nil {
		key = existing.Key
	}

	forceAvatar := e.ForceAvatar
	manualPick := ""
	if e.Choices != nil {
		if url, ok := e.Choices.Get(key); ok {
			manualPick = url
			if res.ImageURL != url {
				res.ImageURL = url
				res.ImageSite = "manual"
			}
			// 换图必须重传：force=false 时 alist 上还是旧文件，直链也不变，
			// 表现出来就是「选了但没生效」。
			if existing == nil || StrVal(existing.AvatarSource) != url {
				forceAvatar = true
			}
		}
	}

	out := &ProcessResult{Key: key, Res: res, Manual: manualPick != ""}

	// 抓取彻底失败、库里又有记录：保留旧数据。
	// 否则一次数据源抖动就会把好好的 ok 记录改成 no_source，元数据全丢。
	if opts.KeepOnFailure && manualPick == "" {
		switch res.Outcome() {
		case StatusOK, StatusPartial:
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
			out.Candidates = OrderCandidates(res.Images, StrVal(existing.AvatarSource))
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
	case e.Alist == nil:
		avatarNote = "有图但未配置 alist"
	default:
		imgData, err := DownloadImage(ctx, e.httpClient(), res.ImageURL)
		if err != nil {
			avatarNote = "图下载失败: " + err.Error()
		} else {
			av, err := e.Alist.UploadAvatar(ctx, e.AlistDir, actor.Name, imgData, forceAvatar)
			if err != nil {
				avatarNote = "上传失败: " + err.Error()
			} else {
				applyAvatarResult(actor, av, res.ImageURL, res.ImageSite)
				if av.Skipped {
					avatarNote = "头像已存在"
				} else {
					avatarNote = "头像已上传"
				}
			}
		}
	}

	out.AvatarNote = avatarNote
	out.Candidates = OrderCandidates(res.Images, res.ImageURL)
	if !opts.NoDBWrite {
		out.Added = e.DB.Upsert(actor)
	}
	return out, nil
}

// ApplyChoice 把用户选中的图下载后传到 alist，并就地更新记录。
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
	if e.Alist == nil {
		return actor, "未配置 alist，选择已记录（下次同步时生效）", nil
	}

	data, err := DownloadImage(ctx, e.httpClient(), imageURL)
	if err != nil {
		return nil, "", fmt.Errorf("下载选中图失败: %w", err)
	}
	// 换了图一定要 force，否则 alist 上还是旧文件、直链对不上新内容
	av, err := e.Alist.UploadAvatar(ctx, e.AlistDir, actor.Name, data, true)
	if err != nil {
		return nil, "", fmt.Errorf("上传头像失败: %w", err)
	}

	applyAvatarResult(actor, av, imageURL, "manual")
	return actor, "已上传到 alist", nil
}

// Save 把内存中的库落盘。
func (e *Engine) Save() error {
	if e.DB == nil || e.DataFile == "" {
		return nil
	}
	return e.DB.Save(e.DataFile)
}

// applyAvatarResult 把上传结果写回记录的头像字段。
func applyAvatarResult(actor *Actor, av *AvatarResult, sourceURL, sourceSite string) {
	actor.Avatar = StrPtr(av.URL)
	actor.AvatarPath = StrPtr(av.Path)
	actor.AvatarDir = StrPtr(av.Dir)
	actor.AvatarSource = StrPtr(sourceURL)
	actor.AvatarSourceSite = StrPtr(sourceSite)
	actor.AvatarSHA256 = StrPtr(av.SHA256)
	actor.AvatarBytes = &av.Bytes
	actor.AvatarW = IntPtr(av.Width)
	actor.AvatarH = IntPtr(av.Height)
}
