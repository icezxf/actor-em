package actorsync

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Bot 是常驻的 Telegram 交互服务：用户发名字 → 走完整刮削 → 发候选图 →
// 点选后立刻换头像。
//
// 它和批量同步共用 Engine，所以「发名字」和「跑一轮同步」拿到的是同一套元数据、
// 同一套头像路径、同一套 choices 裁决。
type Bot struct {
	TG      *TelegramClient
	Engine  *Engine
	State   *ReviewState
	Choices *Choices

	ChatID      string // 只服务这个会话，其他来源一律忽略
	MaxNames    int    // 一条消息最多处理几个名字
	PollTimeout int    // 长轮询秒数

	Log func(format string, args ...any)
}

func (b *Bot) logf(format string, args ...any) {
	if b.Log != nil {
		b.Log(format, args...)
	}
}

func (b *Bot) reply(ctx context.Context, text string) {
	if err := b.TG.Notify(ctx, clampText(text, 3900)); err != nil {
		b.logf("发送消息失败: %v", err)
	}
}

// Run 长轮询直到 ctx 取消。
func (b *Bot) Run(ctx context.Context) error {
	if b.TG == nil || b.TG.Token == "" {
		return fmt.Errorf("缺少 TELEGRAM_BOT_TOKEN")
	}
	if b.State == nil {
		return fmt.Errorf("缺少 review 状态")
	}
	if b.Engine == nil {
		return fmt.Errorf("缺少 Engine")
	}
	if b.PollTimeout <= 0 {
		b.PollTimeout = 25
	}
	if b.MaxNames <= 0 {
		b.MaxNames = 10
	}

	b.logf("交互服务已启动（chat=%s，长轮询 %ds），等待消息…", b.ChatID, b.PollTimeout)
	if strings.Contains(b.TG.APIBase, "api.telegram.org") {
		b.logf("提示：境内直连 api.telegram.org 通常不通，一直失败就把 TELEGRAM_API_BASE 指向反代")
	}

	backoff := time.Second
	for {
		if ctx.Err() != nil {
			b.logf("收到退出信号，交互服务结束")
			return nil
		}

		updates, offset, err := b.TG.PollUpdates(ctx, b.State.UpdateOffset, b.PollTimeout, "message", "callback_query")
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			b.logf("轮询失败: %v（%s 后重试）", err, backoff)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return nil
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second

		for i := range updates {
			b.safeHandle(ctx, &updates[i])
		}

		// offset 必须落盘，否则重启后会把已处理的消息再跑一遍
		if offset != b.State.UpdateOffset {
			b.State.UpdateOffset = offset
			if err := b.State.Save(""); err != nil {
				b.logf("保存游标失败: %v", err)
			}
		}
	}
}

// HandleUpdate 处理一条 Telegram webhook 推送的 update（JSON 原文）。
// 常驻模式（Run）和一次性模式（--tg-once）共用同一套处理逻辑。
func (b *Bot) HandleUpdate(ctx context.Context, raw []byte) error {
	var u tgUpdate
	if err := json.Unmarshal(raw, &u); err != nil {
		return fmt.Errorf("解析 Telegram update 失败: %w", err)
	}
	b.safeHandle(ctx, &u)
	return nil
}

// safeHandle 保证一条坏消息不会掀掉整个服务。
func (b *Bot) safeHandle(ctx context.Context, u *tgUpdate) {
	defer func() {
		if r := recover(); r != nil {
			b.logf("处理 update %d 时 panic: %v", u.UpdateID, r)
		}
	}()
	switch {
	case u.CallbackQuery != nil:
		b.handleCallback(ctx, u.CallbackQuery)
	case u.Message != nil:
		b.handleMessage(ctx, u.Message)
	}
}

func (b *Bot) handleMessage(ctx context.Context, m *tgMessage) {
	text := strings.TrimSpace(m.Text)
	if text == "" {
		return
	}

	chatID := strconv.FormatInt(m.Chat.ID, 10)
	if b.ChatID != "" && chatID != b.ChatID {
		// /id 是为了让人能自报家门，方便第一次配置
		if strings.HasPrefix(text, "/id") {
			_ = b.TG.Notify(ctx, "当前会话 id："+chatID)
		}
		b.logf("忽略未知会话 %s 的消息", chatID)
		return
	}

	cmd, args := splitCommand(text)
	switch cmd {
	case "/start", "/help":
		b.reply(ctx, botHelpText)
	case "/id":
		b.reply(ctx, "当前会话 id："+chatID)
	case "/list", "/find":
		b.handleList(ctx, args)
	case "/status":
		b.handleStatus(ctx, args)
	case "/repick":
		b.handleNames(ctx, args)
	case "":
		b.handleNames(ctx, text)
	default:
		b.reply(ctx, "不认识的命令 "+cmd+"，发 /help 看用法")
	}
}

// handleNames 是主入口：把一条消息里的名字逐个走完整刮削流程。
func (b *Bot) handleNames(ctx context.Context, text string) {
	names := SplitNames(text)
	if len(names) == 0 {
		b.reply(ctx, "没看出演员名。一行一个，或者用逗号、顿号分隔都行。")
		return
	}
	if len(names) > b.MaxNames {
		b.reply(ctx, fmt.Sprintf("一次最多处理 %d 个，先做前 %d 个。", b.MaxNames, b.MaxNames))
		names = names[:b.MaxNames]
	}

	b.reply(ctx, fmt.Sprintf("收到 %d 个名字，开始刮削（每个约 5–15 秒）…", len(names)))
	for _, name := range names {
		if ctx.Err() != nil {
			return
		}
		b.processOne(ctx, name)
	}
}

// processOne 抓一个人、落库、必要时发候选图等选择。
func (b *Bot) processOne(ctx context.Context, name string) {
	existing := b.Engine.DB.FindByName(name)

	res, err := b.Engine.Process(ctx, name, ProcessOptions{KeepOnFailure: existing != nil})
	if err != nil {
		b.reply(ctx, fmt.Sprintf("❌ %s：%v", name, err))
		return
	}
	if err := b.Engine.Save(); err != nil {
		b.logf("保存 actors.json 失败: %v", err)
	}

	head := b.headLine(res)

	// 拼错名字、或者所有源都没这个人：只回一句，别往库里塞垃圾记录
	if res.Kept && res.Actor == nil {
		b.reply(ctx, head)
		return
	}

	// 只有一张图等于没得选，直接回摘要，别让人白等
	if len(res.Candidates) < 2 {
		summary := actorSummary(res.Actor)
		if summary == "" {
			b.reply(ctx, head)
		} else {
			b.reply(ctx, head+"\n"+summary)
		}
		return
	}

	id := b.State.NextID()
	msgs, err := b.TG.SendCandidates(ctx, id, res.Name, res.Candidates)
	if err != nil {
		b.reply(ctx, head+"\n⚠️ 候选图发送失败："+err.Error())
		return
	}
	b.State.Add(ReviewPending{
		ID:       id,
		Key:      res.Key,
		Name:     res.Name,
		Messages: msgs,
		Images:   res.Candidates,
	})
	if err := b.State.Save(""); err != nil {
		b.logf("保存待选状态失败: %v", err)
	}

	b.reply(ctx, head+"\n第 1 张是现在用的那张，点数字按钮换图。")
}

func (b *Bot) headLine(res *ProcessResult) string {
	if res.Kept {
		return "「" + res.Name + "」" + res.AvatarNote
	}
	parts := []string{fmt.Sprintf("「%s」%s", res.Name, statusLabel(res.Actor.Status))}
	if res.Res != nil {
		parts = append(parts, res.Res.Summary())
	}
	if res.AvatarNote != "" {
		parts = append(parts, res.AvatarNote)
	}
	if res.Added {
		parts = append(parts, "新增入库")
	}
	return strings.Join(parts, "｜")
}

func (b *Bot) handleCallback(ctx context.Context, cq *tgCallback) {
	// 同一会话之外的人点按钮不处理（群里其他人也能看到按钮）
	if b.ChatID != "" && cq.Message != nil {
		if got := strconv.FormatInt(cq.Message.Chat.ID, 10); got != b.ChatID {
			b.logf("忽略来自未知会话 %s 的按钮点击", got)
			_ = b.TG.AnswerCallback(ctx, cq.ID, "")
			return
		}
	}

	id, idx, ok := parsePickData(cq.Data)
	if !ok {
		_ = b.TG.AnswerCallback(ctx, cq.ID, "这条按钮认不出来")
		return
	}
	pend := b.State.FindByID(id)
	if pend == nil {
		_ = b.TG.AnswerCallback(ctx, cq.ID, "这条已经处理过了")
		return
	}
	if idx < 0 || idx >= len(pend.Images) {
		_ = b.TG.AnswerCallback(ctx, cq.ID, "序号越界")
		return
	}

	url := pend.Images[idx]
	_, note, err := b.Engine.ApplyChoice(ctx, pend.Key, url)
	if err != nil {
		b.logf("应用选图失败（%s）: %v", pend.Name, err)
		_ = b.TG.AnswerCallback(ctx, cq.ID, "失败，看聊天里的提示")
		b.reply(ctx, fmt.Sprintf("❌ %s 换图失败：%v", pend.Name, err))
		return
	}
	if err := b.Engine.Save(); err != nil {
		b.logf("保存 actors.json 失败: %v", err)
	}

	// 记进 choices：以后同步到这个人都直接用这张，不会再被自动抓的图覆盖
	if b.Choices != nil {
		b.Choices.Set(pend.Key, url, "telegram")
		if err := b.Choices.Save(""); err != nil {
			b.logf("保存 choices 失败: %v", err)
		}
	}
	b.State.RemoveByKey(pend.Key)
	if err := b.State.Save(""); err != nil {
		b.logf("保存待选状态失败: %v", err)
	}

	label := fmt.Sprintf("已换成第 %d 张", idx+1)
	_ = b.TG.AnswerCallback(ctx, cq.ID, label)
	b.reply(ctx, fmt.Sprintf("✅ %s → %s（%s）", pend.Name, label, note))
}

func (b *Bot) handleList(ctx context.Context, args string) {
	kw := NormalizeName(args)
	var lines []string
	for _, a := range b.Engine.DB.Actors {
		if kw != "" && !strings.Contains(NormalizeName(a.Name), kw) &&
			(a.NameOriginal == nil || !strings.Contains(NormalizeName(*a.NameOriginal), kw)) {
			continue
		}
		mark := "·"
		if a.Avatar != nil && StrVal(a.Avatar) != "" {
			mark = "🖼"
		}
		lines = append(lines, fmt.Sprintf("%s %s %s", mark, a.Name, statusLabel(a.Status)))
		if len(lines) >= 40 {
			lines = append(lines, "…（只显示前 40 条，加关键词收窄）")
			break
		}
	}
	if len(lines) == 0 {
		b.reply(ctx, fmt.Sprintf("库里共 %d 条，没有匹配「%s」的", len(b.Engine.DB.Actors), args))
		return
	}
	b.reply(ctx, fmt.Sprintf("库内共 %d 条，匹配 %d 条：\n%s", len(b.Engine.DB.Actors), len(lines), strings.Join(lines, "\n")))
}

func (b *Bot) handleStatus(ctx context.Context, args string) {
	names := SplitNames(args)
	if len(names) == 0 {
		b.reply(ctx, "用法：/status 演员名")
		return
	}
	var out []string
	for _, n := range names {
		a := b.Engine.DB.FindByName(n)
		if a == nil {
			out = append(out, fmt.Sprintf("「%s」不在库里", n))
			continue
		}
		out = append(out, fmt.Sprintf("「%s」%s\n%s", a.Name, statusLabel(a.Status), actorSummary(a)))
	}
	b.reply(ctx, strings.Join(out, "\n\n"))
}

// ---------- 文本小工具 ----------

const botHelpText = `发演员名字给我，我会走完整刮削流程：
抓元数据 → 上传头像 → 把候选图发给你挑。

用法
· 直接发名字，一行一个或用逗号隔开
  例：小凑四叶
  例：小凑四叶，三上悠亚

命令
/repick 名字   重抓并重新发候选图（换掉已选的）
/status 名字   只看库里现有信息，不抓
/list [关键词] 列出库里的人
/id            告诉你当前会话 id
/help          这条说明

说明
· 第 1 张候选是你现在正在用的那张
· 不在库里的会自动刮削补全元数据
· 选完立刻生效，且以后同步不会被自动图覆盖`

func statusLabel(s string) string {
	switch s {
	case StatusOK:
		return "✅ 完整"
	case StatusPartial:
		return "🟡 部分"
	case StatusNoSource:
		return "⚪ 未找到"
	case StatusFailed:
		return "❌ 失败"
	}
	return s
}

func actorSummary(a *Actor) string {
	if a == nil {
		return ""
	}
	var parts []string
	if v := StrVal(a.BirthDate); v != "" {
		parts = append(parts, "生日 "+v)
	}
	if a.HeightCm != nil && *a.HeightCm > 0 {
		parts = append(parts, fmt.Sprintf("%d cm", *a.HeightCm))
	}
	if v := StrVal(a.BirthPlace); v != "" {
		parts = append(parts, v)
	}
	if a.ProviderIDs != nil {
		var ids []string
		for _, k := range []string{"JavStash", "StashDB", "TPDB"} {
			if v := a.ProviderIDs[k]; v != "" {
				ids = append(ids, k+"="+shortID(v))
			}
		}
		if len(ids) > 0 {
			parts = append(parts, strings.Join(ids, " "))
		}
	}
	if v := StrVal(a.Avatar); v != "" {
		parts = append(parts, "头像已上传")
	}
	if len(parts) == 0 {
		return "（暂无元数据）"
	}
	return strings.Join(parts, "｜")
}

func shortID(s string) string {
	if len(s) > 8 {
		return s[:8] + "…"
	}
	return s
}

// SplitNames 把一条消息拆成演员名列表。
// 不用空格分隔——罗马字名（如 "Yua Mikami"）本身就带空格。
func SplitNames(text string) []string {
	fields := strings.FieldsFunc(text, func(r rune) bool {
		switch r {
		case '\n', '\r', ',', '，', '、', ';', '；', '|':
			return true
		}
		return false
	})
	seen := map[string]bool{}
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		s := strings.TrimSpace(f)
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		key := NormalizeName(s)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, s)
	}
	return out
}

// splitCommand 解析 "/status 某人" → ("/status", "某人")。
// 同时剥掉群聊里带的 @botname 后缀。
func splitCommand(text string) (cmd, args string) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return "", text
	}
	if i := strings.IndexAny(text, " \n\t"); i >= 0 {
		cmd, args = text[:i], strings.TrimSpace(text[i+1:])
	} else {
		cmd = text
	}
	if i := strings.IndexByte(cmd, '@'); i > 0 {
		cmd = cmd[:i]
	}
	return strings.ToLower(cmd), args
}

// clampText 防止超过 Telegram 单条 4096 字上限。
func clampText(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}
