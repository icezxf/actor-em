package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/icezxf/actor-em/internal/actorsync"
)

func main() {
	cfg := LoadConfig()

	var (
		refresh    string
		refreshAll bool
	)

	flag.IntVar(&cfg.Limit, "limit", cfg.Limit, "本轮最多处理多少个演员（0 = 不限）")
	flag.BoolVar(&cfg.DryRun, "dry-run", cfg.DryRun, "只打印结果，不写任何文件")
	flag.BoolVar(&cfg.Verbose, "verbose", cfg.Verbose, "打印每个数据源的命中情况")
	flag.BoolVar(&cfg.ForceAvatar, "force-avatar", cfg.ForceAvatar, "头像已存在也重新上传")
	flag.BoolVar(&refreshAll, "refresh-all", false, "重抓库里所有演员（耗时最长）")
	flag.StringVar(&refresh, "refresh", "", "重抓指定演员，逗号分隔，例如 --refresh 小凑四叶,三上悠亚")
	flag.StringVar(&cfg.DataFile, "data", cfg.DataFile, "actors.json 路径")
	flag.StringVar(&cfg.ManifestFile, "manifest", cfg.ManifestFile, "manifest 路径")
	flag.Parse()

	logger := log.New(os.Stdout, "", log.LstdFlags)
	start := time.Now()

	if cfg.EmbyURL == "" {
		logger.Fatal("缺少 Emby 地址：设置 EMBY_URL 或在 config.local.go 里填 EmbyURL")
	}
	if cfg.TPDBAPIKey == "" && cfg.JavStashAPIKey == "" && cfg.StashDBAPIKey == "" {
		logger.Fatal("至少要配一个数据源 Key（TPDB / JavStash / StashDB），否则所有演员都会被判成 no_source")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Hour)
	defer cancel()

	// ---------- 1. 读本地库 ----------
	db, err := actorsync.LoadDb(cfg.DataFile)
	if err != nil {
		logger.Fatalf("读取本地库失败: %v", err)
	}
	logger.Printf("本地库：%d 条（%s）", len(db.Actors), cfg.DataFile)

	// ---------- 2. 拉 Emby 人物清单 ----------
	emby := actorsync.NewEmbyClient(cfg.EmbyURL, cfg.EmbyUser, cfg.EmbyPass, cfg.EmbyAPIKey)
	if err := emby.Login(ctx); err != nil {
		logger.Fatalf("Emby 登录失败: %v", err)
	}
	persons, err := emby.ListPersons(ctx)
	if err != nil {
		logger.Fatalf("拉取 Emby 人物清单失败: %v", err)
	}
	logger.Printf("Emby 人物：%d 个", len(persons))

	// ---------- 3. 算本轮待抓名单 ----------
	var refreshNames []string
	for _, n := range strings.Split(refresh, ",") {
		if s := strings.TrimSpace(n); s != "" {
			refreshNames = append(refreshNames, s)
		}
	}
	plan := actorsync.BuildPlan(db, persons, refreshNames, refreshAll)

	stats := actorsync.RunStats{
		StartedAt:   start.UTC().Format(time.RFC3339),
		Picked:      len(plan),
		EmbyPersons: len(persons),
	}

	if cfg.Limit > 0 && len(plan) > cfg.Limit {
		plan = plan[:cfg.Limit]
		stats.Picked = len(plan)
	}
	if len(plan) == 0 {
		logger.Printf("没有需要处理的演员，退出")
		if !cfg.DryRun {
			writeManifest(logger, cfg, db, stats, map[string]map[string]int{}, start)
		}
		return
	}

	newCount, retryCount, refreshCount := 0, 0, 0
	for _, it := range plan {
		switch it.Reason {
		case "new":
			newCount++
		case "retry":
			retryCount++
		default:
			refreshCount++
		}
	}
	logger.Printf("本轮待处理 %d 个（新增 %d / 重试 %d / 刷新 %d）", len(plan), newCount, retryCount, refreshCount)

	// ---------- 4. 准备抓取器和 alist ----------
	hc := &http.Client{Timeout: 30 * time.Second}
	fetcher := actorsync.NewFetcher(hc)
	fetcher.WikiEmail = cfg.WikiEmail
	fetcher.Verbose = cfg.Verbose
	if cfg.TPDBAPIKey != "" {
		fetcher.TPDB = actorsync.NewTPDBClient(cfg.TPDBEndpoint, cfg.TPDBAPIKey)
	}
	if cfg.JavStashAPIKey != "" {
		fetcher.JavStash = actorsync.NewStashBoxClient("javstash", cfg.JavStashEndpoint, cfg.JavStashAPIKey)
	}
	if cfg.StashDBAPIKey != "" {
		fetcher.StashDB = actorsync.NewStashBoxClient("stashdb", cfg.StashDBEndpoint, cfg.StashDBAPIKey)
	}
	fetcher.Translator = actorsync.NewTranslator(cfg.DeepLAPIKey, hc)
	dict, err := actorsync.LoadDict(cfg.DictFile)
	if err != nil {
		logger.Printf("⚠️  读取字典失败（不影响主流程）: %v", err)
	} else if dict.Size() > 0 {
		logger.Printf("中文名字典：%d 条（%s）", dict.Size(), cfg.DictFile)
	} else {
		logger.Printf("提示：未找到中文名字典 %s，中文名可能搜不到日系数据源", cfg.DictFile)
	}
	fetcher.Dict = dict

	var alist *actorsync.AlistClient
	if cfg.AlistURL != "" && cfg.AlistUser != "" {
		alist = actorsync.NewAlistClient(cfg.AlistURL, cfg.AlistPublicBase, cfg.AlistUser, cfg.AlistPass)
		if err := alist.Login(ctx); err != nil {
			logger.Printf("⚠️  alist 登录失败，本轮跳过头像上传: %v", err)
			alist = nil
		} else {
			logger.Printf("alist 已连接：%s%s", cfg.AlistURL, cfg.AlistDir)
		}
	} else {
		logger.Printf("⚠️  未配置 alist，跳过头像上传")
	}

	// ---------- 5. 逐个抓 ----------
	srcStats := map[string]map[string]int{}
	added, updated, failed := 0, 0, 0

	for i, item := range plan {
		prefix := fmt.Sprintf("[%d/%d] %s（%s）", i+1, len(plan), item.Name, item.Reason)

		res := fetcher.Fetch(ctx, item.Name)
		actor := actorsync.BuildActor(item.EmbyID, item.Name, res, item.Existing)

		avatarNote := "无图"
		if res.ImageURL != "" && alist != nil {
			imgData, err := actorsync.DownloadImage(ctx, hc, res.ImageURL)
			if err != nil {
				avatarNote = "图下载失败: " + err.Error()
			} else {
				av, err := alist.UploadAvatar(ctx, cfg.AlistDir, actor.Name, imgData, cfg.ForceAvatar)
				if err != nil {
					avatarNote = "上传失败: " + err.Error()
				} else {
					actor.Avatar = actorsync.StrPtr(av.URL)
					actor.AvatarPath = actorsync.StrPtr(av.Path)
					actor.AvatarDir = actorsync.StrPtr(av.Dir)
					actor.AvatarSHA256 = actorsync.StrPtr(av.SHA256)
					actor.AvatarBytes = &av.Bytes
					actor.AvatarW = actorsync.IntPtr(av.Width)
					actor.AvatarH = actorsync.IntPtr(av.Height)
					if av.Skipped {
						avatarNote = "头像已存在"
					} else {
						avatarNote = "头像已上传"
					}
				}
			}
		} else if res.ImageURL != "" {
			avatarNote = "有图但未配置 alist"
		}

		countSources(srcStats, res.Sources)

		switch actor.Status {
		case actorsync.StatusOK:
		case actorsync.StatusPartial:
		case actorsync.StatusNoSource:
		case actorsync.StatusFailed:
			failed++
		}

		if cfg.DryRun {
			logger.Printf("%s → %s | %s | %s", prefix, actor.Status, res.Summary(), avatarNote)
			continue
		}

		if db.Upsert(actor) {
			added++
		} else {
			updated++
		}

		logger.Printf("%s → %s | %s | %s", prefix, actor.Status, res.Summary(), avatarNote)

		// 每 20 个落盘一次，中途挂掉也不丢进度
		if (i+1)%20 == 0 {
			if err := db.Save(cfg.DataFile); err != nil {
				logger.Printf("⚠️  中途保存失败: %v", err)
			}
		}

		time.Sleep(200 * time.Millisecond) // 对数据源友好一点
	}

	stats.Added = added
	stats.Updated = updated
	stats.Failed = failed

	// ---------- 6. 收尾 ----------
	if cfg.DryRun {
		logger.Printf("dry-run 结束：处理 %d 个，未写文件", len(plan))
		return
	}
	if err := db.Save(cfg.DataFile); err != nil {
		logger.Fatalf("保存 actors.json 失败: %v", err)
	}
	writeManifest(logger, cfg, db, stats, srcStats, start)
	logger.Printf("完成：库内 %d 条，新增 %d，更新 %d，失败 %d，用时 %s",
		len(db.Actors), added, updated, failed, time.Since(start).Round(time.Second))
}

func writeManifest(logger *log.Logger, cfg *Config, db *actorsync.Db, stats actorsync.RunStats, srcStats map[string]map[string]int, start time.Time) {
	stats.DurationSec = int(time.Since(start).Seconds())
	m := actorsync.BuildManifest(db, stats, srcStats)
	if err := actorsync.SaveManifest(cfg.ManifestFile, m); err != nil {
		logger.Printf("⚠️  写入 manifest 失败: %v", err)
		return
	}
	if stats.Picked > 0 {
		logger.Printf("manifest 已更新：%s", cfg.ManifestFile)
	}
}

func countSources(agg map[string]map[string]int, sources map[string]bool) {
	for name, ok := range sources {
		if agg[name] == nil {
			agg[name] = map[string]int{"ok": 0, "miss": 0}
		}
		if ok {
			agg[name]["ok"]++
		} else {
			agg[name]["miss"]++
		}
	}
}
