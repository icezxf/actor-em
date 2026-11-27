package actorsync

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 人工选图结果：data/avatar_choices.json（key → 选中的图片 URL）。
// 这是真正的数据，优先级高于任何自动抓到的头像。
// 页面选图 → Worker → avatar-pick workflow → 写这个文件 → 应用到数据库。
//
// 早期这里还有一份 avatar_review.json（Telegram 交互状态）和 ReviewState/
// CollectPicks，已随 Telegram 交互一起移除；网页选图不需要它们。

const ChoiceVersion = 1

// ChoiceEntry 是一条人工选择。
type ChoiceEntry struct {
	URL    string `json:"url"`
	At     string `json:"at"`
	Source string `json:"source"` // pages / manual
}

// Choices 是 key → 选中图片 的映射。
type Choices struct {
	Version int                    `json:"version"`
	Choices map[string]ChoiceEntry `json:"choices"`
	path    string
}

func NewChoices(path string) *Choices {
	return &Choices{Version: ChoiceVersion, Choices: map[string]ChoiceEntry{}, path: path}
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
	c.Version = ChoiceVersion
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

// OrderCandidates 去重并把「当前正在用的图」排到第一位。
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

// writeJSONAtomic 先写 .tmp 再 rename，避免 Action 被中止时把文件写坏。
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
