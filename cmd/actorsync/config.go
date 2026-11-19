package main

import (
	"os"
	"strconv"
	"strings"
)

// Config 集中所有可配置项。
// 优先级：命令行 flag > 环境变量 > config.local.go（可选） > 下面的默认值。
type Config struct {
	// 数据源
	TPDBAPIKey       string
	JavStashAPIKey   string
	StashDBAPIKey    string
	TPDBEndpoint     string
	JavStashEndpoint string
	StashDBEndpoint  string
	DeepLAPIKey      string
	WikiEmail        string

	// alist（仅 --scan-nfo 用；头像直接用网络地址，不转存）
	AlistURL        string
	AlistUser       string
	AlistPass       string
	AlistPublicBase string // nfo 直链前缀，留空 = AlistURL

	// 演员清单（唯一来源，一行一个名字）
	NamesFile string

	// Telegram 人工选图（两个都配了才启用）
	TelegramToken   string // TELEGRAM_BOT_TOKEN
	TelegramChatID  string // TELEGRAM_CHAT_ID
	TelegramAPIBase string // TELEGRAM_API_BASE，国内直连不通时填反代地址
	ReviewStateFile string // 发图状态 + getUpdates 游标
	ChoicesFile     string // 人工选中的结果
	ReviewMax       int    // 每轮最多发几个人等选（限流保护）

	// 本地文件
	DataFile      string
	ManifestFile  string
	DictFile      string
	DictLocalFile string // 手工补充字典，覆盖主字典
	XlsxFile      string // 导出的 Excel（派生产物）

	// NFO 扫描（--scan-nfo）
	NFODir      string
	NFOMaxFiles int

	// 行为
	Limit   int
	DryRun  bool
	Verbose bool
}

func DefaultConfig() *Config {
	return &Config{
		TPDBEndpoint:     "https://api.theporndb.net/performers",
		JavStashEndpoint: "https://javstash.org/graphql",
		StashDBEndpoint:  "https://stashdb.org/graphql",
		DataFile:         "actors.json",
		ManifestFile:     "actors.manifest.json",
		DictFile:         "data/actor_dict.csv",
		DictLocalFile:    "data/actor_dict.local.csv",
		NamesFile:        "actors.txt",
		XlsxFile:         "actors.xlsx",
		ReviewStateFile:  "data/avatar_review.json",
		ChoicesFile:      "data/avatar_choices.json",
		ReviewMax:        6,
		Limit:            0,
	}
}

// LoadConfig 组装最终配置。
func LoadConfig() *Config {
	c := DefaultConfig()
	applyLocalFile(c) // config.local.go（-tags local）里覆盖
	applyEnv(c)
	return c
}

func applyEnv(c *Config) {
	set := func(dst *string, keys ...string) {
		for _, k := range keys {
			if v := strings.TrimSpace(os.Getenv(k)); v != "" {
				*dst = v
				return
			}
		}
	}
	set(&c.TPDBAPIKey, "TPDB_API_KEY")
	set(&c.JavStashAPIKey, "JAVSTASH_API_KEY")
	set(&c.StashDBAPIKey, "STASHDB_API_KEY")
	set(&c.TPDBEndpoint, "TPDB_ENDPOINT")
	set(&c.JavStashEndpoint, "JAVSTASH_ENDPOINT")
	set(&c.StashDBEndpoint, "STASHDB_ENDPOINT")
	set(&c.DeepLAPIKey, "DEEPL_API_KEY")
	set(&c.WikiEmail, "WIKI_EMAIL")
	set(&c.AlistURL, "ALIST_URL")
	set(&c.AlistUser, "ALIST_USER")
	set(&c.AlistPass, "ALIST_PASS")
	set(&c.AlistPublicBase, "ALIST_PUBLIC_BASE")
	set(&c.DataFile, "ACTORS_FILE")
	set(&c.ManifestFile, "ACTORS_MANIFEST")
	set(&c.DictFile, "ACTOR_DICT")
	set(&c.DictLocalFile, "ACTOR_DICT_LOCAL")
	set(&c.XlsxFile, "ACTORS_XLSX")
	set(&c.NamesFile, "ACTORS_TXT")
	set(&c.TelegramToken, "TELEGRAM_BOT_TOKEN")
	set(&c.TelegramChatID, "TELEGRAM_CHAT_ID")
	set(&c.TelegramAPIBase, "TELEGRAM_API_BASE")
	set(&c.ReviewStateFile, "ACTORS_REVIEW_STATE")
	set(&c.ChoicesFile, "ACTORS_CHOICES")
	if v := os.Getenv("ACTORS_REVIEW_MAX"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			c.ReviewMax = n
		}
	}
	set(&c.NFODir, "ALIST_NFO_DIR")
	if v := os.Getenv("ALIST_NFO_MAX"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.NFOMaxFiles = n
		}
	}

	if v := os.Getenv("ACTORS_LIMIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Limit = n
		}
	}
	if v := os.Getenv("ACTORS_DRY_RUN"); v == "1" || strings.EqualFold(v, "true") {
		c.DryRun = true
	}
	c.Verbose = c.Verbose || os.Getenv("ACTORS_VERBOSE") == "1"
}
