package actorsync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// TelegramClient 做四件事：发候选图、收按钮回调、回执、收消息。
//
// 三种运行方式共用这套客户端：
//   - 批量同步（GitHub Actions）：进程活不过一轮，用 timeout=0 的
//     "拉一次就走"，点击会留在 Telegram updates 队列（24h），下轮取回。
//   - 交互服务（--tg-serve）：跑在常驻进程里，用 25 秒长轮询实时收消息。
//     长轮询是客户端主动出站 HTTPS，不需要公网 IP，也不需要证书。
//   - Worker webhook（--tg-once）：CF Worker 收 Telegram 推送并触发 Actions，
//     进程只处理一条 update 就退出，完全不碰 getUpdates。
//
// webhook 与 getUpdates 互斥：启用 Worker 时，批量同步必须把
// ACTORS_REVIEW_MAX 设成 0，否则那边的 PollPicks 会拿到 409 Conflict。
type TelegramClient struct {
	Token   string
	ChatID  string
	APIBase string
	HTTP    *http.Client
}

func NewTelegramClient(token, chatID string) *TelegramClient {
	return &TelegramClient{
		Token:   strings.TrimSpace(token),
		ChatID:  strings.TrimSpace(chatID),
		APIBase: "https://api.telegram.org",
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *TelegramClient) Enabled() bool {
	return c != nil && c.Token != "" && c.ChatID != ""
}

func (c *TelegramClient) endpoint(method string) string {
	base := c.APIBase
	if base == "" {
		base = "https://api.telegram.org"
	}
	return fmt.Sprintf("%s/bot%s/%s", strings.TrimRight(base, "/"), c.Token, method)
}

type tgResponse struct {
	OK          bool            `json:"ok"`
	Description string          `json:"description"`
	Result      json.RawMessage `json:"result"`
}

// call 发一个 JSON 请求，返回 result 原文。
func (c *TelegramClient) call(ctx context.Context, method string, payload any) (json.RawMessage, error) {
	return c.callHTTP(ctx, c.HTTP, method, payload)
}

// callHTTP 允许指定 client：长轮询要等 25 秒以上，默认 30 秒超时会误判失败。
func (c *TelegramClient) callHTTP(ctx context.Context, hc *http.Client, method string, payload any) (json.RawMessage, error) {
	if hc == nil {
		hc = c.HTTP
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(method), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out tgResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("telegram %s 响应解析失败: %w", method, err)
	}
	if !out.OK {
		return nil, fmt.Errorf("telegram %s 失败: %s", method, out.Description)
	}
	return out.Result, nil
}

// PickKeyboard 生成「1 / 2 / 3 ...」按钮，callback_data 为 "<reviewID>:<idx>"。
// 用短 reviewID 而不是 actor key：Telegram 限制 callback_data ≤ 64 字节，
// 而 "javstash:<uuid>" 这类 key 本身就有 45 字节。
func PickKeyboard(reviewID string, n int) map[string]any {
	row := make([]map[string]string, 0, n)
	for i := 0; i < n; i++ {
		row = append(row, map[string]string{
			"text":          strconv.Itoa(i + 1),
			"callback_data": fmt.Sprintf("%s:%d", reviewID, i),
		})
	}
	return map[string]any{"inline_keyboard": [][]map[string]string{row}}
}

// SendCandidates 把候选图发给用户，返回本次发出的所有 message_id。
// 至少需要 2 张——只有一张时没什么可选，调用方应跳过。
func (c *TelegramClient) SendCandidates(ctx context.Context, reviewID, name string, images []string) ([]int, error) {
	images = mergeStrings(nil, images)
	if len(images) < 2 {
		return nil, fmt.Errorf("候选图不足 2 张，无需选择")
	}
	if len(images) > 10 {
		images = images[:10] // Telegram 相册上限
	}

	// 首选：相册 + 一行按钮，形如 [1][2][3][4][5]
	if id, err := c.sendAlbum(ctx, reviewID, name, images); err == nil {
		return []int{id}, nil
	}

	// 兜底：URL 拉不动（防盗链、非图片、超限）时，下载后以 multipart 上传。
	// 每张单独一条消息，用户点对应消息里的「这张」按钮。
	var ids []int
	var lastErr error
	for i, u := range images {
		id, err := c.sendPhotoUpload(ctx, reviewID, name, i, len(images), u)
		if err != nil {
			lastErr = err
			continue
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		if lastErr == nil {
			lastErr = fmt.Errorf("全部候选图发送失败")
		}
		return nil, lastErr
	}
	return ids, nil
}

func (c *TelegramClient) sendAlbum(ctx context.Context, reviewID, name string, images []string) (int, error) {
	type mediaItem struct {
		Type    string `json:"type"`
		Media   string `json:"media"`
		Caption string `json:"caption,omitempty"`
	}
	items := make([]mediaItem, 0, len(images))
	for i, u := range images {
		caption := strconv.Itoa(i + 1)
		if i == 0 {
			caption = fmt.Sprintf("%s · 点下面的按钮选一张", name)
		}
		items = append(items, mediaItem{Type: "photo", Media: u, Caption: caption})
	}
	mediaJSON, err := json.Marshal(items)
	if err != nil {
		return 0, err
	}

	payload := map[string]any{
		"chat_id":      c.ChatID,
		"media":        string(mediaJSON),
		"reply_markup": PickKeyboard(reviewID, len(images)),
	}
	raw, err := c.call(ctx, "sendMediaGroup", payload)
	if err != nil {
		return 0, err
	}
	var msgs []struct {
		MessageID int `json:"message_id"`
	}
	if err := json.Unmarshal(raw, &msgs); err != nil {
		return 0, err
	}
	if len(msgs) == 0 {
		return 0, fmt.Errorf("sendMediaGroup 没有返回消息")
	}
	return msgs[0].MessageID, nil
}

// sendPhotoUpload 下载图片后用 multipart 上传（绕过 Telegram 拉不到 URL 的情况）。
func (c *TelegramClient) sendPhotoUpload(ctx context.Context, reviewID, name string, idx, total int, imageURL string) (int, error) {
	data, err := DownloadImage(ctx, c.HTTP, imageURL)
	if err != nil {
		return 0, err
	}

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("chat_id", c.ChatID)
	_ = w.WriteField("caption", fmt.Sprintf("%s · 第 %d/%d 张", name, idx+1, total))
	markup, _ := json.Marshal(map[string]any{
		"inline_keyboard": [][]map[string]string{{{
			"text":          fmt.Sprintf("选这张（%d）", idx+1),
			"callback_data": fmt.Sprintf("%s:%d", reviewID, idx),
		}}},
	})
	_ = w.WriteField("reply_markup", string(markup))

	part, err := w.CreateFormFile("photo", fmt.Sprintf("candidate-%d.jpg", idx+1))
	if err != nil {
		return 0, err
	}
	if _, err := part.Write(data); err != nil {
		return 0, err
	}
	if err := w.Close(); err != nil {
		return 0, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint("sendPhoto"), &buf)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var out struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
		Result      struct {
			MessageID int `json:"message_id"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return 0, err
	}
	if !out.OK {
		return 0, fmt.Errorf("sendPhoto 失败: %s", out.Description)
	}
	return out.Result.MessageID, nil
}

// Pick 是一次按钮点击。
type Pick struct {
	ReviewID  string
	Index     int
	MessageID int
}

// parsePickData 解析 "r7:2" → ("r7", 2, true)。
func parsePickData(data string) (string, int, bool) {
	data = strings.TrimSpace(data)
	i := strings.LastIndex(data, ":")
	if i <= 0 || i == len(data)-1 {
		return "", 0, false
	}
	id := data[:i]
	idx, err := strconv.Atoi(data[i+1:])
	if err != nil || idx < 0 {
		return "", 0, false
	}
	return id, idx, true
}

// ---------- 长轮询 ----------

type tgChat struct {
	ID int64 `json:"id"`
}

type tgFrom struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

type tgMessage struct {
	MessageID int     `json:"message_id"`
	Date      int64   `json:"date"`
	Chat      tgChat  `json:"chat"`
	From      *tgFrom `json:"from"`
	Text      string  `json:"text"`
}

type tgCallback struct {
	ID      string     `json:"id"`
	Data    string     `json:"data"`
	Message *tgMessage `json:"message"`
}

type tgUpdate struct {
	UpdateID      int64       `json:"update_id"`
	Message       *tgMessage  `json:"message"`
	CallbackQuery *tgCallback `json:"callback_query"`
}

// PollUpdates 长轮询一次。timeoutSec=0 表示「没有就立刻返回」。
// allowed 为空表示全部类型。
func (c *TelegramClient) PollUpdates(ctx context.Context, offset int64, timeoutSec int, allowed ...string) ([]tgUpdate, int64, error) {
	payload := map[string]any{
		"offset":  offset,
		"timeout": timeoutSec,
	}
	if len(allowed) > 0 {
		payload["allowed_updates"] = allowed
	}

	// HTTP 超时必须比 Telegram 侧的 timeout 更长，否则每轮都超时
	hc := &http.Client{
		Timeout:   c.HTTP.Timeout + time.Duration(timeoutSec)*time.Second + 15*time.Second,
		Transport: c.HTTP.Transport,
	}
	raw, err := c.callHTTP(ctx, hc, "getUpdates", payload)
	if err != nil {
		return nil, offset, err
	}
	var updates []tgUpdate
	if err := json.Unmarshal(raw, &updates); err != nil {
		return nil, offset, err
	}

	newOffset := offset
	for i := range updates {
		if updates[i].UpdateID >= newOffset {
			newOffset = updates[i].UpdateID + 1
		}
	}
	return updates, newOffset, nil
}

// PollPicks 拉取待处理的按钮点击，返回解析结果与新的 offset。
// timeout=0 表示「没有就立刻返回」，适合 Action 这种短生命周期场景。
func (c *TelegramClient) PollPicks(ctx context.Context, offset int64) ([]Pick, int64, error) {
	updates, newOffset, err := c.PollUpdates(ctx, offset, 0, "callback_query")
	if err != nil {
		return nil, offset, err
	}

	var picks []Pick
	for i := range updates {
		cq := updates[i].CallbackQuery
		if cq == nil {
			continue
		}
		messageID := 0
		if cq.Message != nil {
			messageID = cq.Message.MessageID
		}
		if id, idx, ok := parsePickData(cq.Data); ok {
			picks = append(picks, Pick{ReviewID: id, Index: idx, MessageID: messageID})
		}
		// 无论能否解析都要回执，否则客户端按钮会一直转圈
		_ = c.AnswerCallback(ctx, cq.ID, "")
	}
	return picks, newOffset, nil
}

// AnswerCallback 回执一次点击，text 非空时会在客户端顶部弹一下。
func (c *TelegramClient) AnswerCallback(ctx context.Context, callbackID, text string) error {
	payload := map[string]any{"callback_query_id": callbackID}
	if text != "" {
		payload["text"] = text
	}
	_, err := c.call(ctx, "answerCallbackQuery", payload)
	return err
}

// Notify 发一条纯文本（用作回执汇总）。
func (c *TelegramClient) Notify(ctx context.Context, text string) error {
	payload := map[string]any{
		"chat_id":                  c.ChatID,
		"text":                     text,
		"disable_web_page_preview": true,
	}
	_, err := c.call(ctx, "sendMessage", payload)
	return err
}
