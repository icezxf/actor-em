package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/icezxf/actor-em/internal/actorsync"
)

func main() {
	cfg := LoadConfig()

	var (
		refresh     string
		refreshAll  bool
		fillMissing bool
		fillFields  string
		scanNFO     bool
		noXlsx      bool
		serveBot    bool
		tgOnce      string
		backfill    bool
	)

	flag.IntVar(&cfg.Limit, "limit", cfg.Limit, "本轮最多处理多少个演员（0 = 不限）")
	flag.BoolVar(&cfg.DryRun, "dry-run", cfg.DryRun, "只打印结果，不写本地文件")
	flag.BoolVar(&cfg.Verbose, "verbose", cfg.Verbose, "打印每个数据源的命中情况")
	flag.BoolVar(&refreshAll, "refresh-all", false, "重抓库里所有演员（耗时最长；配合 --limit 0 才是全量）")
	flag.StringVar(&refresh, "refresh", "", "重抓指定演员，逗号分隔，例如 --refresh 小凑四叶,三上悠亚")
	flag.BoolVar(&fillMissing, "fill-missing", false, "只重抓有缺字段的已有演员，只填空位不覆盖已有值")
	flag.StringVar(&fillFields, "fill-fields", "", "配合 --fill-missing：要补的字段，逗号分隔；留空=全部可补字段")
	flag.StringVar(&cfg.DataFile, "data", cfg.DataFile, "数据文件路径，默认 actors.jsonl")
	flag.StringVar(&cfg.ManifestFile, "manifest", cfg.ManifestFile, "manifest 路径")
	flag.StringVar(&cfg.NamesFile, "names", cfg.NamesFile, "演员清单 txt 路径，一行一个名字")
	flag.StringVar(&cfg.XlsxFile, "xlsx", cfg.XlsxFile, "导出的 Excel 路径")
	flag.BoolVar(&noXlsx, "no-xlsx", false, "不导出 Excel")
	flag.BoolVar(&scanNFO, "scan-nfo", false, "只扫描 alist 里的 nfo 生成演员清单，不刮削")
	flag.BoolVar(&backfill, "backfill", false, "只用 MDCx 离线档案补空字段（生日/三围/标签/身高/罩杯/出道年），不联网、不需要 API Key")
	// 老名字保留，脚本里还在用
	flag.BoolVar(&backfill, "backfill-birthday", false, "同 --backfill（旧名）")
	flag.StringVar(&cfg.NFODir, "nfo-dir", cfg.NFODir, "nfo 所在的 alist 目录")
	flag.IntVar(&cfg.NFOMaxFiles, "nfo-max", cfg.NFOMaxFiles, "最多读取多少个 nfo（0 = 不限）")
	flag.IntVar(&cfg.ReviewMax, "review-max", cfg.ReviewMax, "每轮最多发几个人的候选图到 Telegram 等选择（0 = 关闭）")
	flag.BoolVar(&serveBot, "tg-serve", false, "常驻运行 Telegram 交互服务：发名字即时刮削并回候选图")
	flag.StringVar(&tgOnce, "tg-once", "", "处理一条 Telegram update JSON 后退出（供 CF Worker + Actions 使用）")
	flag.Parse()

	logger := log.New(os.Stdout, "", log.LstdFlags)
	start := time.Now()

	// 四种改写模式互斥：同时开会让「谁覆盖谁」变得不可预测。
	modes := 0
	for _, on := range []bool{backfill, refreshAll, refresh != "", fillMissing} {
		if on {
			modes++
		}
	}
	if modes > 1 {
		logger.Fatal("--backfill / --refresh-all / --refresh / --fill-missing 只能选一个")
	}
	var fillFieldList []string
	if fillMissing {
		var err error
		fillFieldList, err = actorsync.ParseFillFields(fillFields)
		if err != nil {
			logger.Fatalf("解析 --fill-fields 失败: %v", err)
		}
		logger.Printf("缺字段补值模式：%s", strings.Join(fillFieldList, ", "))
	}

	// 交互服务要一直挂着，5 小时超时只适合一次批量同步
	var (
		ctx    context.Context
		cancel context.CancelFunc
	)
	if serveBot {
		ctx, cancel = signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	} else {
		ctx, cancel = context.WithTimeout(context.Background(), 5*time.Hour)
	}
	defer cancel()

	// 只扫描 nfo 生成清单，不抓数据
	if scanNFO {
		if err := runScanNFO(ctx, cfg, logger); err != nil {
			logger.Fatalf("扫描 nfo 失败: %v", err)
		}
		return
	}

	// 只做离线补值：不联网、不需要任何 API Key
	if backfill {
		if err := runBackfill(cfg, logger); err != nil {
			logger.Fatalf("离线补值失败: %v", err)
		}
		return
	}

	if cfg.TPDBAPIKey == "" && cfg.JavStashAPIKey == "" && cfg.StashDBAPIKey == "" {
		logger.Fatal("至少要配一个数据源 Key（TPDB / JavStash / StashDB），否则所有演员都会被判成 no_source")
	}

	// 交互服务不读 actors.txt，名字由消息给
	if serveBot {
		if err := runTelegramServe(ctx, cfg, logger); err != nil {
			logger.Fatalf("交互服务退出: %v", err)
		}
		return
	}

	// Worker webhook 转来的一条 update：处理完就退出，给 Actions 用
	if tgOnce != "" {
		if err := runTelegramOnce(ctx, cfg, logger, tgOnce); err != nil {
			logger.Fatalf("处理 Telegram update 失败: %v", err)
		}
		return
	}

	// ---------- 0. 准备抓取器（清单要读字典，字典在这里加载） ----------
	hc := &http.Client{Timeout: 30 * time.Second}
	fetcher := buildFetcher(cfg, hc, logger)

	// ---------- 1. 读本地库 ----------
	db, err := actorsync.LoadDb(cfg.DataFile)
	if err != nil {
		logger.Fatalf("读取本地库失败: %v", err)
	}
	logger.Printf("本地库：%d 条（%s）", len(db.Actors), cfg.DataFile)

	// ---------- 2. 读待抓清单 ----------
	// 清单主体来自工作流 1 产出的字典（它才是演员总表），
	// actors.txt 只作为手工追加通道，可以是空文件或不存在。
	names, err := collectNames(fetcher.Dict, cfg.NamesFile, logger)
	if err != nil {
		logger.Fatalf("读取演员清单失败: %v", err)
	}
	logger.Printf("演员清单：%d 个（%s）", len(names), cfg.NamesFile)

	// ---------- 2.5 Telegram 人工选图：先把上一轮攒下的点击收回来 ----------
	reviewOn := cfg.TelegramToken != "" && cfg.TelegramChatID != "" && cfg.ReviewMax > 0
	var (
		tg          *actorsync.TelegramClient
		reviewState *actorsync.ReviewState
		choices     *actorsync.Choices
	)
	if reviewOn {
		tg = newTelegram(cfg)
		reviewState, err = actorsync.LoadReviewState(cfg.ReviewStateFile)
		if err != nil {
			logger.Printf("⚠️  读取 %s 失败，按空状态继续: %v", cfg.ReviewStateFile, err)
			reviewState = actorsync.NewReviewState(cfg.ReviewStateFile)
		}
		choices, err = actorsync.LoadChoices(cfg.ChoicesFile)
		if err != nil {
			logger.Printf("⚠️  读取 %s 失败，按空结果继续: %v", cfg.ChoicesFile, err)
			choices = actorsync.NewChoices(cfg.ChoicesFile)
		}
		if dropped := reviewState.PruneExpired(7*24*time.Hour, time.Now().UTC()); dropped > 0 {
			logger.Printf("清理了 %d 条过期的待选记录", dropped)
		}

		applied, picked, perr := actorsync.CollectPicks(ctx, tg, reviewState, choices)
		switch {
		case perr != nil:
			logger.Printf("⚠️  收取 Telegram 点击失败: %v", perr)
		case applied > 0:
			logger.Printf("收到 %d 个选图结果：%s", applied, strings.Join(picked, "、"))
		}
		if applied > 0 {
			logger.Printf("人工选图库：%d 条（%s）", len(choices.Choices), cfg.ChoicesFile)
		}
		// offset 即使没收到点击也可能前进，必须落盘，否则同一批 update 会被反复拉取
		if !cfg.DryRun {
			if err := reviewState.Save(""); err != nil {
				logger.Printf("⚠️  保存 %s 失败: %v", cfg.ReviewStateFile, err)
			}
			if applied > 0 {
				if err := choices.Save(""); err != nil {
					logger.Printf("⚠️  保存 %s 失败: %v", cfg.ChoicesFile, err)
				}
			}
		}
	}

	// ---------- 3. 算本轮待抓名单 ----------
	var refreshNames []string
	for _, n := range strings.Split(refresh, ",") {
		if s := strings.TrimSpace(n); s != "" {
			refreshNames = append(refreshNames, s)
		}
	}
	plan := actorsync.BuildPlanWithFill(db, names, refreshNames, refreshAll, fillFieldList)

	stats := actorsync.RunStats{
		StartedAt:   start.UTC().Format(time.RFC3339),
		Picked:      len(plan),
		Source:      "txt",
		SourceCount: len(names),
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

	newCount, retryCount, refreshCount, fillCount := 0, 0, 0, 0
	for _, it := range plan {
		switch it.Reason {
		case "new":
			newCount++
		case "retry":
			retryCount++
		case "fill":
			fillCount++
		default:
			refreshCount++
		}
	}
	logger.Printf("本轮待处理 %d 个（新增 %d / 重试 %d / 刷新 %d / 补缺字段 %d）",
		len(plan), newCount, retryCount, refreshCount, fillCount)

	// ---------- 4. 组装 Engine ----------
	// 抓取 → 生成记录 → 用网络头像 这条链路统一走 Engine，
	// 和 --tg-serve 交互服务共用，避免两边行为漂移。
	engine := &actorsync.Engine{
		DB:       db,
		Fetcher:  fetcher,
		Choices:  choices,
		HTTP:     hc,
		DataFile: cfg.DataFile,
		Log:      logger.Printf,
	}

	// ---------- 5. 逐个抓 ----------
	srcStats := map[string]map[string]int{}
	added, updated, failed, kept := 0, 0, 0, 0

	// 需要人工挑图的人：新演员、或抓完仍然没有头像的
	type reviewCand struct {
		Key    string
		Name   string
		Images []string
	}
	var reviewQueue []reviewCand

	// 并发抓取、串行落库：
	// 网络请求是瓶颈（每个演员要串 5~6 个源），所以抓取开 N 个 worker；
	// 写库和计数留在主 goroutine，避免给 DB 加锁、也保证 review 队列有序。
	concurrency := cfg.Concurrency
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > len(plan) {
		concurrency = len(plan)
	}
	logger.Printf("并发抓取：%d", concurrency)

	idxQueue := make(chan int, len(plan))
	for i := range plan {
		idxQueue <- i
	}
	close(idxQueue)

	type fetched struct {
		idx      int
		item     actorsync.PlanItem
		res      *actorsync.FetchResult
		panicked any // 单个源的解析 panic，只跳过这个人，不能拖垮整轮
	}
	results := make(chan fetched, concurrency*2)

	var wg sync.WaitGroup
	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range idxQueue {
				res, panicked := safeFetch(engine, ctx, plan[idx].Name)
				results <- fetched{idx: idx, item: plan[idx], res: res, panicked: panicked}
			}
		}()
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	done := 0
	for f := range results {
		prefix := fmt.Sprintf("[%d/%d] %s（%s）", f.idx+1, len(plan), f.item.Name, f.item.Reason)
		if f.panicked != nil {
			failed++
			logger.Printf("%s → 抓取 panic，已跳过（不动库里的旧数据）: %v", prefix, f.panicked)
			continue
		}
		if len(f.item.Missing) > 0 {
			prefix += fmt.Sprintf("（缺 %s）", strings.Join(f.item.Missing, ","))
		}

		opts := actorsync.ProcessOptions{NoDBWrite: cfg.DryRun}
		if f.item.Reason == "fill" {
			opts.FillFields = fillFieldList
			opts.KeepOnFailure = true
		}
		out, perr := engine.ApplyPhase(ctx, f.item.Name, f.res, opts)
		if perr != nil {
			logger.Printf("%s → 处理失败: %v", prefix, perr)
			failed++
			continue
		}
		actor := out.Actor
		extra := ""
		if len(out.Filled) > 0 {
			extra += " | 补: " + strings.Join(out.Filled, ",")
		}
		if out.Kept {
			extra += "（保留旧值）"
		}

		// 只对「新演员」和「还没有头像的」发候选图；至少 2 张才值得问
		if reviewOn && !cfg.DryRun && !out.Manual &&
			len(reviewQueue) < cfg.ReviewMax &&
			len(out.Candidates) >= 2 &&
			(f.item.Reason == "new" || actor.Avatar == nil) &&
			!reviewState.HasKey(out.Key) {
			reviewQueue = append(reviewQueue, reviewCand{Key: out.Key, Name: actor.Name, Images: out.Candidates})
		}

		countSources(srcStats, f.res.Sources)

		if cfg.DryRun {
			logger.Printf("%s → %s | %s | %s%s", prefix, actor.Status, f.res.Summary(), out.AvatarNote, extra)
			continue
		}

		if out.Kept {
			kept++
		} else if out.Added {
			added++
		} else {
			updated++
		}
		if actor.Status == actorsync.StatusFailed {
			failed++
		}

		logger.Printf("%s → %s | %s | %s%s", prefix, actor.Status, f.res.Summary(), out.AvatarNote, extra)

		// 每 20 个落盘一次，中途挂掉也不丢进度
		done++
		if done%20 == 0 {
			if err := db.Save(cfg.DataFile); err != nil {
				logger.Printf("⚠️  中途保存失败: %v", err)
			}
		}
	}

	stats.Added = added
	stats.Updated = updated
	stats.Failed = failed

	// ---------- 5.5 把需要挑图的人发给 Telegram ----------
	if reviewOn && !cfg.DryRun && len(reviewQueue) > 0 {
		sent := 0
		for _, cand := range reviewQueue {
			id := reviewState.NextID()
			msgs, err := tg.SendCandidates(ctx, id, cand.Name, cand.Images)
			if err != nil {
				logger.Printf("⚠️  候选图发送失败（%s）: %v", cand.Name, err)
				continue
			}
			reviewState.Add(actorsync.ReviewPending{
				ID:       id,
				Key:      cand.Key,
				Name:     cand.Name,
				Messages: msgs,
				Images:   cand.Images,
			})
			sent++
			time.Sleep(3 * time.Second) // 同一会话约 20 条/分钟，留点余量
		}
		if err := reviewState.Save(""); err != nil {
			logger.Printf("⚠️  保存 %s 失败: %v", cfg.ReviewStateFile, err)
		}
		if sent > 0 {
			logger.Printf("已发送 %d 个人的候选图，等你选择（下次运行时生效）", sent)
		}
	}

	// ---------- 6. 收尾 ----------
	if cfg.DryRun {
		logger.Printf("dry-run 结束：处理 %d 个，未写本地文件", len(plan))
		return
	}
	if err := db.Save(cfg.DataFile); err != nil {
		logger.Fatalf("保存 %s 失败: %v", cfg.DataFile, err)
	}
	// jsonl 是真源，再派生一份 actors.json 给不认 jsonl 的下游
	if actorsync.IsJSONLPath(cfg.DataFile) {
		jsonPath := strings.TrimSuffix(cfg.DataFile, ".jsonl") + ".json"
		if err := db.ExportJSON(jsonPath); err != nil {
			logger.Printf("⚠️  派生 %s 失败: %v", jsonPath, err)
		} else {
			logger.Printf("已派生 %s", jsonPath)
		}
	}
	writeManifest(logger, cfg, db, stats, srcStats, start)
	if !noXlsx {
		if err := actorsync.ExportXLSX(cfg.XlsxFile, db); err != nil {
			logger.Printf("⚠️  导出 Excel 失败: %v", err)
		} else {
			logger.Printf("Excel 已导出：%s", cfg.XlsxFile)
		}
	}
	logger.Printf("完成：库内 %d 条，新增 %d，更新 %d，保留 %d，失败 %d，用时 %s",
		len(db.Actors), added, updated, kept, failed, time.Since(start).Round(time.Second))
}

// safeFetch 把单个演员的抓取 panic 拦在 worker 内。
// 任何一个数据源的 HTML 解析出错都不该让两万条的全量任务整体崩掉；
// 主循环收到 panicked != nil 时只跳过这个人，不动库里的旧数据。
func safeFetch(e *actorsync.Engine, ctx context.Context, name string) (res *actorsync.FetchResult, panicked any) {
	defer func() { panicked = recover() }()
	return e.FetchPhase(ctx, name), nil
}

// runBackfillBirthday 用 MDCx 离线档案给库里空着的生日补值。
//
// 为什么要单独一个命令：抓取侧的兜底只在 Fetch 里生效，而 BuildPlan 只挑
// 「库里没有 / failed / no_source / 手动 refresh」的人，已经 ok 的记录不会重抓。
// 存量两万条想套上兜底，只能靠这个离线命令——秒级完成，不用再烧一轮 API。
//
// 三条规则：
//   - 已有生日一律不动（JavStash 是权威）
//   - 旧记录有生日但没标来源的，补标 javstash（那时候只有这一个来源）
//   - 只给 ok/partial 的空生日补 MDCx 值，并标 mdcx
//
// runBackfill 用 MDCx 离线档案给库里已有的记录补空字段。
//
// 为什么需要它：`BuildPlan` 只挑 new / failed / no_source / refresh，库里已经 ok 的
// 两万条永远不会重抓，所以抓取侧新增的字段（生日兜底、三围）对存量数据不生效。
// 而这些字段 MDCx 本地就有，重抓 21,293 人要 12 小时，离线补只要几秒。
//
// 只填空位，绝不覆盖：JavStash 的生日、TPDB 的三围都保留原值。
func runBackfill(cfg *Config, logger *log.Logger) error {
	if cfg.MDCxCSV == "" {
		return fmt.Errorf("缺少 MDCX_CSV，先跑 scripts/export_profiles.py 导出档案")
	}
	mdcx, err := actorsync.LoadMDCxCSV(cfg.MDCxCSV)
	if err != nil {
		return fmt.Errorf("读 MDCx 档案失败: %w", err)
	}
	if mdcx.Size() == 0 {
		return fmt.Errorf("%s 里没有演员档案", cfg.MDCxCSV)
	}
	db, err := actorsync.LoadDb(cfg.DataFile)
	if err != nil {
		return fmt.Errorf("读本地库失败: %w", err)
	}
	logger.Printf("本地库 %d 条 / MDCx 档案 %d 人（%s）", len(db.Actors), mdcx.Size(), cfg.MDCxCSV)

	// 字典用于给存量补中文别名：同一个人在字典里可能有多个中文名
	// （如「桥本有菜」/「新有菜」都指向 橋本ありな），抓取时只取一个当主名，
	// 其余只存在字典里。这里把它们补进 aliases，Emby 用旧名也能命中。
	dict, err := actorsync.LoadDict(cfg.DictFile)
	if err != nil {
		return fmt.Errorf("读字典失败: %w", err)
	}
	if local, err2 := actorsync.LoadDict(cfg.DictLocalFile); err2 == nil && local.Size() > 0 {
		dict.Merge(local)
	}
	logger.Printf("字典 %d 条别名（%s）", dict.Size(), cfg.DictFile)

	var fakeDate, unitTag, upgraded int
	var nBirth, nMeas, nTags, nHeight, nCup, nCareer, nPlace, nAlias, noMatch int
	for _, a := range db.Actors {
		if a == nil {
			continue
		}
		// 老库的第一段可能只是胸围（44-25-39）。如果 MDCx 有罩杯，可以升级成
		// 「底围+杯」（36J-25-39），也就是欧美内衣的标法。已经带字母的
		// （34E-21-31，TPDB 原文）本身就是标准写法，保持不动。
		hasBandCup := false
		if s := actorsync.StrVal(a.Measurements); s != "" {
			first := strings.SplitN(s, "-", 2)[0]
			hasBandCup = strings.IndexFunc(first, unicode.IsLetter) >= 0
		}
		// 老库没有 measurements_unit：那时候有三围只可能来自 TPDB（英寸）
		if actorsync.StrVal(a.Measurements) != "" && actorsync.StrVal(a.MeasurementsUnit) == "" {
			a.MeasurementsUnit = actorsync.StrPtr("in")
			unitTag++
		}
		// 老库没有 birth_date_source，那时候的生日只可能来自 JavStash
		if actorsync.StrVal(a.BirthDate) != "" && actorsync.StrVal(a.BirthDateSource) == "" {
			a.BirthDateSource = actorsync.StrPtr("javstash")
			fakeDate++
		}
		// skipped / no_source / failed 不补：它们不进下游，补了也没人看
		if !a.Exportable() {
			continue
		}

		// 中文名 → 日文原名 → 别名，逐个试（MDCx 索引里三种都可能有）
		names := []string{a.Name}
		if v := actorsync.StrVal(a.NameOriginal); v != "" {
			names = append(names, v)
		}
		names = append(names, a.Aliases...)

		// 用字典给存量补中文别名。字典里同一个日文名可能对应多个中文名，
		// 抓取时只有一个能当主名（如「新有菜」），其余（「桥本有菜」）此前从未
		// 进库。这里按 name/原名/现有别名反查回日文名，把全部别名并进来，
		// 按规范化去重，Emby 用旧名就能命中。
		if dict.Size() > 0 {
			before := len(a.Aliases)
			// 主名/原名不放进 aliases：它们本来就能匹配到，重复只会让表格变脏。
			selfNames := map[string]bool{actorsync.NormalizeName(a.Name): true}
			if v := actorsync.StrVal(a.NameOriginal); v != "" {
				selfNames[actorsync.NormalizeName(v)] = true
			}
			seen := map[string]bool{}
			for _, v := range a.Aliases {
				seen[actorsync.NormalizeName(v)] = true
			}
			for _, n := range names {
				if strings.TrimSpace(n) == "" {
					continue
				}
				for _, v := range dict.AllAliases(n) {
					k := actorsync.NormalizeName(v)
					if k == "" || seen[k] || selfNames[k] {
						continue
					}
					seen[k] = true
					a.Aliases = append(a.Aliases, v)
				}
			}
			if len(a.Aliases) > before {
				nAlias++
			}
		}

		var p *actorsync.MDCxProfile
		for _, n := range names {
			if strings.TrimSpace(n) == "" {
				continue
			}
			if got := mdcx.Lookup(n); got != nil {
				p = got
				break
			}
		}
		if p == nil {
			noMatch++
			continue
		}

		if actorsync.StrVal(a.BirthDate) == "" && p.Birthday != "" {
			a.BirthDate = actorsync.StrPtr(p.Birthday)
			a.BirthDateSource = actorsync.StrPtr("mdcx")
			nBirth++
		}
		if !hasBandCup {
			if s := actorsync.MeasurementsInches(p.Bust, p.Waist, p.Hip, p.Cup); s != "" {
				if actorsync.StrVal(a.Measurements) != s {
					if actorsync.StrVal(a.Measurements) != "" {
						upgraded++
					} else {
						nMeas++
					}
					a.Measurements = actorsync.StrPtr(s)
					a.MeasurementsUnit = actorsync.StrPtr("in")
				}
			}
		}
		if len(a.Tags) == 0 && len(p.Tags) > 0 {
			a.Tags = p.Tags
			nTags++
		}
		if a.HeightCm == nil && p.Height > 0 {
			a.HeightCm = actorsync.IntPtr(p.Height)
			nHeight++
		}
		if actorsync.StrVal(a.CupSize) == "" && p.Cup != "" {
			a.CupSize = actorsync.StrPtr(p.Cup)
			nCup++
		}
		if a.CareerStart == nil && p.CareerStart > 0 {
			a.CareerStart = actorsync.IntPtr(p.CareerStart)
			nCareer++
		}
		if actorsync.StrVal(a.BirthPlace) == "" && p.BirthPlace != "" {
			// 和在线抓取保持同一口径：都道府县级补上国家，写成「日本·东京都」
			a.BirthPlace = actorsync.StrPtr(actorsync.QualifyBirthPlace(p.BirthPlace, p.Country))
			if actorsync.StrVal(a.Country) == "" && p.Country != "" {
				a.Country = actorsync.StrPtr(p.Country)
			}
			nPlace++
		}
	}

	// 出生地补国家：老库里只存了「东京都」，下游（Emby ProductionLocations）
	// 看不出是哪国。这一步纯离线、幂等，跑一次 backfill 就能把存量改成
	// 「日本·东京都」，不必为了一个字段重抓两万条。
	nPlaceFix := 0
	for _, a := range db.Actors {
		if actorsync.QualifyActorBirthPlace(a) {
			nPlaceFix++
		}
	}

	logger.Printf("补标来源：三围单位 %d 条、生日来源 %d 条；三围升级成「底围+罩杯」%d 条",
		unitTag, fakeDate, upgraded)
	logger.Printf("补空字段：生日 %d、三围 %d、标签 %d、身高 %d、罩杯 %d、出道年 %d、出生地 %d（MDCx 查不到 %d 条）",
		nBirth, nMeas, nTags, nHeight, nCup, nCareer, nPlace, noMatch)
	logger.Printf("补中文别名：%d 条（字典里的旧名/异名，让 Emby 按旧名也能命中）", nAlias)
	logger.Printf("出生地补国家：%d 条（都道府县级 → 「日本·东京都」）", nPlaceFix)

	total := nBirth + nMeas + nTags + nHeight + nCup + nCareer + nPlace + nAlias + unitTag + fakeDate + upgraded + nPlaceFix
	if total == 0 {
		logger.Printf("字段没有需要补的改动，仍重新派生 actors.json")
	}
	if cfg.DryRun {
		logger.Printf("dry-run：不写文件")
		return nil
	}
	// 即使字段一个没补，也要重新派生 json：Exportable() 的过滤规则可能变了
	// （比如把老字典的下划线名字挡在下游），只在 jsonl 有变化时才写它。
	if total > 0 {
		if err := db.Save(cfg.DataFile); err != nil {
			return fmt.Errorf("保存 %s 失败: %w", cfg.DataFile, err)
		}
		logger.Printf("已写回 %s", cfg.DataFile)
	}
	if actorsync.IsJSONLPath(cfg.DataFile) {
		jsonPath := strings.TrimSuffix(cfg.DataFile, ".jsonl") + ".json"
		if err := db.ExportJSON(jsonPath); err != nil {
			return fmt.Errorf("派生 %s 失败: %w", jsonPath, err)
		}
		logger.Printf("已派生 %s", jsonPath)
	}
	return nil
}

// runScanNFO 递归读 alist 目录下的 nfo，把演员名写成待抓清单。
func runScanNFO(ctx context.Context, cfg *Config, logger *log.Logger) error {
	if cfg.AlistURL == "" || cfg.AlistUser == "" {
		return fmt.Errorf("扫描 nfo 需要配置 ALIST_URL / ALIST_USER / ALIST_PASS")
	}
	if cfg.NFODir == "" {
		return fmt.Errorf("缺少 nfo 目录：设置 ALIST_NFO_DIR 或传 --nfo-dir")
	}

	alist := actorsync.NewAlistClient(cfg.AlistURL, cfg.AlistUser, cfg.AlistPass)
	if err := alist.Login(ctx); err != nil {
		return fmt.Errorf("alist 登录失败: %w", err)
	}
	logger.Printf("开始扫描：%s（最多 %d 个 nfo，0 = 不限）", cfg.NFODir, cfg.NFOMaxFiles)

	names, st, err := actorsync.ScanNFODirectory(ctx, alist, cfg.NFODir, cfg.NFOMaxFiles, func(s actorsync.ScanStats) {
		logger.Printf("  已读 %d 个目录 / %d 个 nfo", s.Dirs, s.NFOCount)
	})
	if err != nil {
		return err
	}
	logger.Printf("扫描完成：目录 %d，nfo %d，读取失败 %d，演员 %d", st.Dirs, st.NFOCount, st.ParseFail, len(names))

	if cfg.DryRun {
		logger.Printf("dry-run：不写 %s，前 5 个：%v", cfg.NamesFile, firstN(names, 5))
		return nil
	}
	if len(names) == 0 {
		return fmt.Errorf("一个演员都没解析到，检查目录是否正确、nfo 里是否有 <actor><name>")
	}
	if err := actorsync.SaveNamesFile(cfg.NamesFile, names); err != nil {
		return fmt.Errorf("写入 %s 失败: %w", cfg.NamesFile, err)
	}
	logger.Printf("已写入 %s（%d 个演员）", cfg.NamesFile, len(names))
	return nil
}

// newTelegram 组装客户端；配了 TELEGRAM_API_BASE 就走反代
// （国内直连 api.telegram.org 基本不通，常驻服务尤其需要这一点）。
func newTelegram(cfg *Config) *actorsync.TelegramClient {
	tg := actorsync.NewTelegramClient(cfg.TelegramToken, cfg.TelegramChatID)
	if base := strings.TrimSpace(cfg.TelegramAPIBase); base != "" {
		tg.APIBase = base
	}
	return tg
}

// collectNames 组装本轮待抓清单。
//
// 主体是工作流 1 产出的字典（actor_dict.csv 里的全部日文名，也就是演员总表），
// actors.txt 只作手工追加通道——留空、甚至文件不存在都不影响。
func collectNames(dict *actorsync.Dict, txtPath string, logger *log.Logger) ([]string, error) {
	var names []string
	seen := map[string]bool{}
	add := func(n string) {
		n = strings.TrimSpace(n)
		if n == "" {
			return
		}
		k := actorsync.NormalizeName(n)
		if seen[k] {
			return
		}
		seen[k] = true
		names = append(names, n)
	}

	fromDict := 0
	if dict != nil {
		for _, jp := range dict.Actors() {
			before := len(names)
			add(jp)
			if len(names) > before {
				fromDict++
			}
		}
	}
	fromTxt := 0
	if txtPath != "" {
		if manual, err := actorsync.LoadNamesFile(txtPath); err == nil {
			for _, n := range manual {
				before := len(names)
				add(n)
				if len(names) > before {
					fromTxt++
				}
			}
		} else if !os.IsNotExist(err) {
			logger.Printf("⚠️  读取 %s 失败（忽略）: %v", txtPath, err)
		}
	}
	logger.Printf("清单来源：字典 %d 个 + 手工 %d 个", fromDict, fromTxt)

	if len(names) == 0 {
		return nil, fmt.Errorf("清单为空：字典和 %s 都没有内容", txtPath)
	}
	return names, nil
}

// buildFetcher 组装所有数据源，没配 Key 的源直接不挂上，Fetch 会自动跳过。
func buildFetcher(cfg *Config, hc *http.Client, logger *log.Logger) *actorsync.Fetcher {
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
	// 离线档案：结构化字段的地基（MDCx）
	if cfg.MDCxCSV != "" {
		if db, err := actorsync.LoadMDCxCSV(cfg.MDCxCSV); err != nil {
			logger.Printf("⚠️  读取 MDCx 档案失败（不影响主流程）: %v", err)
		} else if db.Size() > 0 {
			fetcher.MDCx = db
			logger.Printf("MDCx 档案：%d 人（%s）", db.Size(), cfg.MDCxCSV)
		}
	}
	// 头像库：6MB 索引，只在显式开启时拉
	if cfg.Gfriends {
		var (
			g   *actorsync.Gfriends
			err error
		)
		if cfg.GfriendsIndex != "" {
			g, err = actorsync.LoadGfriendsFile(cfg.GfriendsIndex)
		} else {
			g, err = actorsync.LoadGfriends(context.Background(), hc)
		}
		if err != nil {
			logger.Printf("⚠️  gfriends 索引加载失败，头像退到数据源原图: %v", err)
		} else {
			fetcher.Gfriends = g
			logger.Printf("gfriends 索引：%d 张图", g.Size())
		}
	}
	if cfg.DramaFace {
		fetcher.DramaFace = actorsync.NewDramaFaceClient(hc)
	}
	if cfg.Yinrenlu {
		fetcher.Yinrenlu = actorsync.NewYinrenluClient(hc)
	}
	if cfg.Cinewho {
		fetcher.Cinewho = actorsync.NewCinewhoClient(hc)
	}
	if cfg.IdolDB {
		fetcher.IdolDB = actorsync.NewIdolDBClient(hc)
	}
	if cfg.TheActorBase {
		fetcher.TheActorBase = actorsync.NewTheActorBaseClient(hc)
	}

	dict, err := actorsync.LoadDict(cfg.DictFile)
	if err != nil {
		logger.Printf("⚠️  读取字典失败（不影响主流程）: %v", err)
	} else if dict.Size() > 0 {
		logger.Printf("中文名字典：%d 条（%s）", dict.Size(), cfg.DictFile)
	} else {
		logger.Printf("提示：未找到中文名字典 %s，中文名可能搜不到日系数据源", cfg.DictFile)
	}
	// 补充字典：手工维护，覆盖主字典里的同名条目
	if local, err := actorsync.LoadDict(cfg.DictLocalFile); err != nil {
		logger.Printf("⚠️  读取补充字典失败: %v", err)
	} else if local.Size() > 0 {
		dict.Merge(local)
		logger.Printf("补充字典：%d 条（%s），合计 %d 条", local.Size(), cfg.DictLocalFile, dict.Size())
	}
	fetcher.Dict = dict
	return fetcher
}

// runTelegramServe 常驻运行交互服务：长轮询收消息，发名字即走完整刮削并回候选图。
//
// 注意：同一个 bot token 只能有一个 getUpdates 客户端。跑这个服务时，
// 批量同步那边要把 ACTORS_REVIEW_MAX 设成 0，否则两边会抢 update（409 Conflict）。
func runTelegramServe(ctx context.Context, cfg *Config, logger *log.Logger) error {
	if cfg.TelegramToken == "" || cfg.TelegramChatID == "" {
		return fmt.Errorf("需要 TELEGRAM_BOT_TOKEN 和 TELEGRAM_CHAT_ID")
	}

	db, err := actorsync.LoadDb(cfg.DataFile)
	if err != nil {
		return fmt.Errorf("读取本地库失败: %w", err)
	}
	logger.Printf("本地库：%d 条（%s）", len(db.Actors), cfg.DataFile)

	hc := &http.Client{Timeout: 30 * time.Second}
	choices, err := actorsync.LoadChoices(cfg.ChoicesFile)
	if err != nil {
		logger.Printf("⚠️  读取 %s 失败，按空结果继续: %v", cfg.ChoicesFile, err)
		choices = actorsync.NewChoices(cfg.ChoicesFile)
	}
	state, err := actorsync.LoadReviewState(cfg.ReviewStateFile)
	if err != nil {
		logger.Printf("⚠️  读取 %s 失败，按空状态继续: %v", cfg.ReviewStateFile, err)
		state = actorsync.NewReviewState(cfg.ReviewStateFile)
	}
	if dropped := state.PruneExpired(7*24*time.Hour, time.Now().UTC()); dropped > 0 {
		logger.Printf("清理了 %d 条过期的待选记录", dropped)
	}

	engine := &actorsync.Engine{
		DB:       db,
		Fetcher:  buildFetcher(cfg, hc, logger),
		Choices:  choices,
		HTTP:     hc,
		DataFile: cfg.DataFile,
		Log:      logger.Printf,
	}

	logger.Printf("提示：同一个 bot token 只能有一个 getUpdates 客户端，批量同步那边请把 ACTORS_REVIEW_MAX 设为 0")

	bot := &actorsync.Bot{
		TG:       newTelegram(cfg),
		Engine:   engine,
		State:    state,
		Choices:  choices,
		ChatID:   cfg.TelegramChatID,
		MaxNames: 10, // 一条消息最多处理 10 个：再多 Telegram 侧就该限流了
		Log:      logger.Printf,
	}
	return bot.Run(ctx)
}

// runTelegramOnce 处理 Cloudflare Worker 转发来的一条 update 后退出。
// 没有长轮询、不碰 getUpdates，因此可以和 webhook 共存，适合 GitHub Actions。
func runTelegramOnce(ctx context.Context, cfg *Config, logger *log.Logger, raw string) error {
	// webhook 模式：回复目标从 update 里取，不必预置 TELEGRAM_CHAT_ID；
	// 若显式配置了 chat id，则 Bot 会额外把它当作来源白名单。
	if cfg.TelegramToken == "" {
		return fmt.Errorf("需要 TELEGRAM_BOT_TOKEN")
	}

	db, err := actorsync.LoadDb(cfg.DataFile)
	if err != nil {
		return fmt.Errorf("读取本地库失败: %w", err)
	}
	hc := &http.Client{Timeout: 30 * time.Second}
	choices, err := actorsync.LoadChoices(cfg.ChoicesFile)
	if err != nil {
		logger.Printf("⚠️  读取 %s 失败，按空结果继续: %v", cfg.ChoicesFile, err)
		choices = actorsync.NewChoices(cfg.ChoicesFile)
	}
	state, err := actorsync.LoadReviewState(cfg.ReviewStateFile)
	if err != nil {
		logger.Printf("⚠️  读取 %s 失败，按空状态继续: %v", cfg.ReviewStateFile, err)
		state = actorsync.NewReviewState(cfg.ReviewStateFile)
	}
	if dropped := state.PruneExpired(7*24*time.Hour, time.Now().UTC()); dropped > 0 {
		logger.Printf("清理了 %d 条过期的待选记录", dropped)
	}

	engine := &actorsync.Engine{
		DB:       db,
		Fetcher:  buildFetcher(cfg, hc, logger),
		Choices:  choices,
		HTTP:     hc,
		DataFile: cfg.DataFile,
		Log:      logger.Printf,
	}
	bot := &actorsync.Bot{
		TG:         newTelegram(cfg),
		Engine:     engine,
		State:      state,
		Choices:    choices,
		ChatID:     cfg.TelegramChatID,
		DeriveChat: true,
		MaxNames:   10,
		Log:        logger.Printf,
	}

	if err := bot.HandleUpdate(ctx, []byte(raw)); err != nil {
		return err
	}
	// Bot 内部已经存过，这里再兜一次底：回调分支和异常路径都要落盘
	if err := engine.Save(); err != nil {
		return fmt.Errorf("保存数据失败: %w", err)
	}
	if err := state.Save(""); err != nil {
		logger.Printf("⚠️  保存 %s 失败: %v", cfg.ReviewStateFile, err)
	}
	if err := choices.Save(""); err != nil {
		logger.Printf("⚠️  保存 %s 失败: %v", cfg.ChoicesFile, err)
	}
	if !cfg.DryRun {
		if err := actorsync.ExportXLSX(cfg.XlsxFile, db); err != nil {
			logger.Printf("⚠️  导出 Excel 失败: %v", err)
		}
	}
	logger.Printf("本次 update 处理完毕：库内 %d 条", len(db.Actors))
	return nil
}

func firstN(s []string, n int) []string {
	if len(s) < n {
		return s
	}
	return s[:n]
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
