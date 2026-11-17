package main

import (
	"os"
	"strconv"
	"strings"
)

// Config 集中所有可配置项。
// 优先级：命令行 flag > 环境变量 > config.local.go（可选） > 下面的默认值。
type Config struct {
	// Emby
	EmbyURL    string
	EmbyUser   string
	EmbyPass   string
	EmbyAPIKey string // 开了 2FA 时用这个兜底

	// 数据源
	TPDBAPIKey       string
	JavStashAPIKey   string
	StashDBAPIKey    string
	TPDBEndpoint     string
	JavStashEndpoint string
	StashDBEndpoint  string
	DeepLAPIKey      string
	WikiEmail        string

	// alist
	AlistURL        string
	AlistUser       string
	AlistPass       string
	AlistDir        string // /actors
	AlistPublicBase string // 直链前缀，留空 = AlistURL

	// 演员清单来源
	Source    string // txt（默认）/ emby
	NamesFile string // 一行一个演员名的 txt

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
	Limit       int
	DryRun      bool
	Verbose     bool
	ForceAvatar bool
}

func DefaultConfig() *Config {
	return &Config{
		AlistDir:         "/actors",
		TPDBEndpoint:     "https://api.theporndb.net/performers",
		JavStashEndpoint: "https://javastash.org/graphql",
		StashDBEndpoint:  "https://stashdb.org/graphql",
		DataFile:         "actors.json",
		ManifestFile:     "actors.manifest.json",
		DictFile:         "data/actor_dict.csv",
		DictLocalFile:    "data/actor_dict.local.csv",
		Source:           "txt",
		NamesFile:        "actors.txt",
		XlsxFile:         "actors.xlsx",
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
	set(&c.EmbyURL, "EMBY_URL")
	set(&c.EmbyUser, "EMBY_USER")
	set(&c.EmbyPass, "EMBY_PASS")
	set(&c.EmbyAPIKey, "EMBY_API_KEY")
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
	set(&c.AlistDir, "ALIST_DIR")
	set(&c.AlistPublicBase, "ALIST_PUBLIC_BASE")
	set(&c.DataFile, "ACTORS_FILE")
	set(&c.ManifestFile, "ACTORS_MANIFEST")
	set(&c.DictFile, "ACTOR_DICT")
	set(&c.DictLocalFile, "ACTOR_DICT_LOCAL")
	set(&c.XlsxFile, "ACTORS_XLSX")
	set(&c.Source, "ACTORS_SOURCE")
	set(&c.NamesFile, "ACTORS_TXT")
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
	c.ForceAvatar = os.Getenv("ACTORS_FORCE_AVATAR") == "1"
}
