package actorsync

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// 人工选图分两个文件，职责分开：
//
//   avatar_review.json  —— 运行时状态（发过哪些候选、消息 id、getUpdates 游标），
//                          丢了会导致重复发图或漏收点击，所以也要提交到仓库。
//   avatar_choices.json —— 人工裁决结果（key → 选中的图片 URL），
//                          这是真正的数据，同步时优先于任何自动抓到的图。

const ReviewStateVersion = 1

// ReviewPending 是一次「已发出、还没被选择」的候选图消息。
type ReviewPending struct {
	ID       string   `json:"id"`       // 短 id，如 "r7"，用在 callback_data 里
	Key      string   `json:"key"`      // actors.json 的主键
	Name     string   `json:"name"`     //
	Messages []int    `json:"messages"` // 发出的消息 id（相册是 1 条，逐张兜底时多条）
	Images   []string `json:"images"`   // 候选图，顺序与按钮序号一致
	SentAt   string   `json:"sent_at"`
}

// ReviewState 是持久化的交互状态。
type ReviewState struct {
	Version      int             `json:"version"`
	UpdateOffset int64           `json:"update_offset"` // 下次 getUpdates 的 offset
	Seq          int             `json:"seq"`           // 短 id 自增种子
	Pending      []ReviewPending `json:"pending"`
	path         string
}

func NewReviewState(path string) *ReviewState {
	return &ReviewState{Version: ReviewStateVersion, Pending: []ReviewPending{}, path: path}
}

func LoadReviewState(path string) (*ReviewState, error) {
	st := NewReviewState(path)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return st, nil
		}
		return nil, fmt.Errorf("读取 %s 失败: %w", path, err)
	}
	if len(data) == 0 {
		return st, nil
	}
	if err := json.Unmarshal(data, st); err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", path, err)
	}
	st.path = path
	if st.Pending == nil {
		st.Pending = []ReviewPending{}
	}
	return st, nil
}

func (s *ReviewState) Save(path string) error {
	if path == "" {
		path = s.path
	}
	if path == "" {
		return fmt.Errorf("没有指定 review 状态文件路径")
	}
	s.Version = ReviewStateVersion
	if s.Pending == nil {
		s.Pending = []ReviewPending{}
	}
	sort.SliceStable(s.Pending, func(i, j int) bool { return s.Pending[i].SentAt < s.Pending[j].SentAt })
	return writeJSONAtomic(path, s)
}

// NextID 分配一个短 id（r1、r2 ...）。
func (s *ReviewState) NextID() string {
	s.Seq++
	return fmt.Sprintf("r%d", s.Seq)
}

// HasKey 判断这个人是否已经有候选图在等选择。
func (s *ReviewState) HasKey(key string) bool {
	for i := range s.Pending {
		if s.Pending[i].Key == key {
			return true
		}
	}
	return false
}

// FindByID 按短 id 找待选记录。
func (s *ReviewState) FindByID(id string) *ReviewPending {
	for i := range s.Pending {
		if s.Pending[i].ID == id {
			return &s.Pending[i]
		}
	}
	return nil
}

func (s *ReviewState) Add(p ReviewPending) {
	p.SentAt = time.Now().UTC().Format(time.RFC3339)
	s.Pending = append(s.Pending, p)
}

// RemoveByKey 删掉某人的待选记录（选完了）。
func (s *ReviewState) RemoveByKey(key string) {
	out := s.Pending[:0]
	for _, p := range s.Pending {
		if p.Key != key {
			out = append(out, p)
		}
	}
	s.Pending = out
}

// PruneExpired 丢弃超过 maxAge 的待选记录。
// Telegram 的 updates 队列只保留 24 小时，太老的点不过来，留着只会占地方。
func (s *ReviewState) PruneExpired(maxAge time.Duration, now time.Time) int {
	out := s.Pending[:0]
	dropped := 0
	for _, p := range s.Pending {
		t, err := time.Parse(time.RFC3339, p.SentAt)
		if err != nil || now.Sub(t) > maxAge {
			dropped++
			continue
		}
		out = append(out, p)
	}
	s.Pending = out
	return dropped
}

// ---------- 人工裁决结果 ----------

// ChoiceEntry 是一条人工选择。
type ChoiceEntry struct {
	URL    string `json:"url"`
	At     string `json:"at"`
	Source string `json:"source"` // telegram / manual
}

// Choices 是 key → 选中图片 的映射。
type Choices struct {
	Version int                    `json:"version"`
	Choices map[string]ChoiceEntry `json:"choices"`
	path    string
}

func NewChoices(path string) *Choices {
	return &Choices{Version: ReviewStateVersion, Choices: map[string]ChoiceEntry{}, path: path}
}

func LoadChoices(path string) (*Choices, error) {
	c := NewChoices(path)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil
		}
		return nil, fmt.Errorf("读取 %s 失败: %w", path, err)
	}
	if len(data) == 0 {
		return c, nil
	}
	if err := json.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", path, err)
	}
	c.path = path
	if c.Choices == nil {
		c.Choices = map[string]ChoiceEntry{}
	}
	return c, nil
}

func (c *Choices) Save(path string) error {
	if path == "" {
		path = c.path
	}
	if path == "" {
		return fmt.Errorf("没有指定 choices 文件路径")
	}
	c.Version = ReviewStateVersion
	if c.Choices == nil {
		c.Choices = map[string]ChoiceEntry{}
	}
	return writeJSONAtomic(path, c)
}

func (c *Choices) Get(key string) (string, bool) {
	if c == nil || c.Choices == nil {
		return "", false
	}
	e, ok := c.Choices[key]
	if !ok || strings.TrimSpace(e.URL) == "" {
		return "", false
	}
	return e.URL, true
}

func (c *Choices) Set(key, url, source string) {
	if c.Choices == nil {
		c.Choices = map[string]ChoiceEntry{}
	}
	c.Choices[key] = ChoiceEntry{
		URL:    url,
		At:     time.Now().UTC().Format(time.RFC3339),
		Source: source,
	}
}

// ---------- 收点击 ----------

// CollectPicks 把 Telegram 里攒下的按钮点击转成 choices。
// 返回本次新确认的人数。
func CollectPicks(ctx context.Context, tg *TelegramClient, st *ReviewState, ch *Choices) (int, []string, error) {
	if !tg.Enabled() {
		return 0, nil, nil
	}
	picks, newOffset, err := tg.PollPicks(ctx, st.UpdateOffset)
	st.UpdateOffset = newOffset
	if err != nil {
		return 0, nil, err
	}

	applied := 0
	var names []string
	for _, p := range picks {
		pend := st.FindByID(p.ReviewID)
		if pend == nil {
			// 记录已被清理（或超过 24h），忽略
			continue
		}
		if p.Index < 0 || p.Index >= len(pend.Images) {
			continue
		}
		ch.Set(pend.Key, pend.Images[p.Index], "telegram")
		st.RemoveByKey(pend.Key)
		applied++
		names = append(names, fmt.Sprintf("%s → 第 %d 张", pend.Name, p.Index+1))
	}
	return applied, names, nil
}

// ---------- 小工具 ----------

// OrderCandidates 去重并把「当前正在用的图」排到第一位。
// 这样相册里第 1 张就是你现在看到的头像，方便对照着挑。
func OrderCandidates(images []string, current string) []string {
	out := mergeStrings(nil, images)
	current = strings.TrimSpace(current)
	if current == "" {
		return out
	}
	ordered := make([]string, 0, len(out)+1)
	ordered = append(ordered, current)
	for _, u := range out {
		if u != current {
			ordered = append(ordered, u)
		}
	}
	return ordered
}

// writeJSONAtomic 先写 .tmp 再 rename，避免 Action 被中止时把状态文件写坏。
func writeJSONAtomic(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
