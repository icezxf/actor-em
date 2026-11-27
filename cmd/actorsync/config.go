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
	WikiEmail        string

	// 离线数据源（工作流 1/2 之间传递）
	MDCxCSV       string // MDCX_CSV：从 actor-profile.sqlite3 导出的演员档案
	Gfriends      bool   // GFRIENDS=1：启用 gfriends 头像库（要拉 6MB 索引）
	GfriendsIndex string // GFRIENDS_INDEX：本地 Filetree.json，填了就不联网拉
	TheActorBase  bool   // THEACTORBASE=1：启用 theactorbase 中文简介兜底
	DramaFace     bool   // DRAMAFACE=0 关闭；默认开启，中文简介优先补充源
	Yinrenlu      bool   // YINRENLU=0 关闭；映人录中文简介+生日/出道年
	Cinewho       bool   // CINEWHO=0 关闭；影中人中文简介+生日/身高/三围
	IdolDB        bool   // IDOLDB=0 关闭；MagicBude/jav-idol-db 档案补充
	CastWiki      bool   // CASTWIKI=0 关闭；卡司Wiki 中文简介（整段，最长）
	WikiEN        bool   // WIKI_EN=1：英文维基简介兜底（欧美演员，严格身份对账）

	// 演员清单（唯一来源，一行一个名字）
	NamesFile string

	// 人工选图结果（网页选图写入，优先级高于自动抓的头像）
	ChoicesFile     string // 人工选中的结果

	// 本地文件
	DataFile      string
	ManifestFile  string
	DictFile      string
	DictLocalFile string // 手工补充字典，覆盖主字典
	XlsxFile      string // 导出的 Excel（派生产物）

	// 行为
	Limit       int
	Concurrency int // ACTORS_CONCURRENCY：并发抓取数，首铺时调到 10
	DryRun      bool
	Verbose     bool
}

func DefaultConfig() *Config {
	return &Config{
		TPDBEndpoint:     "https://api.theporndb.net/performers",
		JavStashEndpoint: "https://javstash.org/graphql",
		StashDBEndpoint:  "https://stashdb.org/graphql",
		DataFile:         "actors.jsonl",
		ManifestFile:     "actors.manifest.json",
		DictFile:         "actor_dict.csv",
		DictLocalFile:    "data/actor_dict.local.csv",
		NamesFile:        "actors.txt",
		XlsxFile:         "actors.xlsx",
		ChoicesFile:      "data/avatar_choices.json",
		Limit:            0,
		Concurrency:      1,
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
	set(&c.WikiEmail, "WIKI_EMAIL")
	set(&c.MDCxCSV, "MDCX_CSV")
	set(&c.GfriendsIndex, "GFRIENDS_INDEX")
	c.Gfriends = os.Getenv("GFRIENDS") == "1" || strings.EqualFold(os.Getenv("GFRIENDS"), "true")
	c.TheActorBase = os.Getenv("THEACTORBASE") == "1" || strings.EqualFold(os.Getenv("THEACTORBASE"), "true")
	// 以下中文人物站默认开启（对应 =0 可关）：无 Key 需求，身份靠原名对账。
	c.DramaFace = !(os.Getenv("DRAMAFACE") == "0" || strings.EqualFold(os.Getenv("DRAMAFACE"), "false"))
	c.Yinrenlu = !(os.Getenv("YINRENLU") == "0" || strings.EqualFold(os.Getenv("YINRENLU"), "false"))
	c.Cinewho = !(os.Getenv("CINEWHO") == "0" || strings.EqualFold(os.Getenv("CINEWHO"), "false"))
	c.IdolDB = !(os.Getenv("IDOLDB") == "0" || strings.EqualFold(os.Getenv("IDOLDB"), "false"))
	c.CastWiki = !(os.Getenv("CASTWIKI") == "0" || strings.EqualFold(os.Getenv("CASTWIKI"), "false"))
	// 英文维基默认关（日系用不到，且有同名误配风险）；欧美工作流显式 WIKI_EN=1 开
	c.WikiEN = os.Getenv("WIKI_EN") == "1" || strings.EqualFold(os.Getenv("WIKI_EN"), "true")
	set(&c.DataFile, "ACTORS_FILE")
	set(&c.ManifestFile, "ACTORS_MANIFEST")
	set(&c.DictFile, "ACTOR_DICT")
	set(&c.DictLocalFile, "ACTOR_DICT_LOCAL")
	set(&c.XlsxFile, "ACTORS_XLSX")
	set(&c.NamesFile, "ACTORS_TXT")
	set(&c.ChoicesFile, "ACTORS_CHOICES")
	if v := os.Getenv("ACTORS_LIMIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Limit = n
		}
	}
	if v := os.Getenv("ACTORS_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.Concurrency = n
		}
	}
	if v := os.Getenv("ACTORS_DRY_RUN"); v == "1" || strings.EqualFold(v, "true") {
		c.DryRun = true
	}
	c.Verbose = c.Verbose || os.Getenv("ACTORS_VERBOSE") == "1"
}
