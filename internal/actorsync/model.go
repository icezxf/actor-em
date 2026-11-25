package actorsync

import "strings"

// Actor 是 actors.jsonl 里的一行（一个演员）。
// 字段全部扁平，空值一律 null（用指针表达），不写空字符串。
// 数组字段空时写 []，不写 null。
type Actor struct {
	Key          string   `json:"key"`
	Name         string   `json:"name"` // 中文名，Emby 的 Name
	NameOriginal *string  `json:"name_original"`
	Aliases      []string `json:"aliases"`

	Gender    *string `json:"gender"`
	BirthDate *string `json:"birth_date"`
	// BirthYear：只有年份的生日（StashDB 常见 "2004"），无法填进 birth_date 时存这里。
	// 完整日期仍走 birth_date（权威口径不变）；下游若只要精确生日可忽略本字段。
	BirthYear *int `json:"birth_year,omitempty"`
	// BirthDateSource：javstash（权威）/ stashdb / tpdb / mdcx / yinrenlu / cinewho / idoldb / castwiki。
	// 空表示还没有生日。下游若不信任二手生日，按这个字段过滤即可。
	BirthDateSource *string `json:"birth_date_source"`
	// BirthPlace 一律带国家：都道府县级写成「日本·东京都」，只有国家时写「日本」。
	BirthPlace *string `json:"birth_place"`
	Country    *string `json:"country"`

	HeightCm         *int    `json:"height_cm"`
	WeightKg         *int    `json:"weight_kg"`         // 只有 TPDB 有，覆盖很低
	Measurements     *string `json:"measurements"`      // "44-25-39"，英寸，胸围-腰围-臀围
	MeasurementsUnit *string `json:"measurements_unit"` // 恒为 in；空表示没有三围
	CupSize          *string `json:"cup_size"`

	CareerStart *int     `json:"career_start"` // 出道年，Emby 的 ProductionYear
	Tags        []string `json:"tags"`

	Bio     *string `json:"bio"`
	BioLang *string `json:"bio_lang"`

	Avatar           *string `json:"avatar"`             // 原图直链
	AvatarCN         *string `json:"avatar_cn"`          // 国内可访问镜像（jsdelivr）
	AvatarSourceSite *string `json:"avatar_source_site"` // gfriends / javstash / tpdb / theactorbase

	HomePageURL *string           `json:"homepage_url"` // Emby 的 HomePageUrl
	ProviderIDs map[string]string `json:"provider_ids"`

	Status     string          `json:"status"`
	Sources    map[string]bool `json:"sources"`
	FailCount  int             `json:"fail_count"`
	RetryAfter *string         `json:"retry_after"`

	CreatedAt string `json:"created_at"`
	ScrapedAt string `json:"scraped_at"`
}

// Db 是内存中的全量演员表。落盘是一行一条的 JSONL，这个结构只用于进程内。
type Db struct {
	SchemaVersion int      `json:"schema_version"`
	GeneratedAt   string   `json:"generated_at"`
	Count         int      `json:"count"`
	Actors        []*Actor `json:"actors"`
}

// SchemaVersion 3：对齐 docs/actor-fields.md
//   - 删 death_date / waist_cm / hip_cm（忌日不要；三围改成原样字符串)
//   - 删 alist 时代的 avatar_path/dir/sha256/bytes/w/h 和冗余的 avatar_source
//   - 增 measurements / tags / avatar_cn / homepage_url
//   - 增 birth_date_source（javstash / mdcx）与 measurements_unit（cm / in）
const SchemaVersion = 3

// 记录状态
const (
	StatusOK       = "ok"        // 至少关键字段有值
	StatusPartial  = "partial"   // 命中了源，但生日和简介都为空
	StatusNoSource = "no_source" // 所有源都没有这个人
	StatusFailed   = "failed"    // 网络/限流错误，需要重试
	StatusSkipped  = "skipped"   // 不是目标演员（男性等），留档但不再抓、不导出
)

// Exportable 表示这条记录该不该交给下游（actors.json / Emby 刮削）。
//
// 只有拿到过数据的记录才算数：
//   - skipped：男性或非目标演员，留档只为不再重抓
//   - no_source：所有源都没有这个人（多是片商名、导演、系列名混进了字典）
//   - failed：网络/限流没抓成，下轮会重试，此刻给下游只会是一行空壳
//
// 三类都还留在 actors.jsonl 真源里，只是不进派生物。
func (a *Actor) Exportable() bool {
	if a == nil {
		return false
	}
	switch a.Status {
	case StatusOK, StatusPartial:
	default:
		return false
	}
	// 老字典把拉丁名里的空格写成了下划线（Kristina_Rose / Lucy_Heart）。
	// build_base_db.py 现在会规范成空格，新版名字会作为新人入库——旧名如果还
	// 出现在下游，Emby 里就是同一个人的两条重复记录。留着 jsonl 备查，不导出。
	if strings.Contains(a.Name, "_") {
		return false
	}
	return true
}

// Manifest 是 actors.manifest.json 的结构，给人和监控看。
type Manifest struct {
	SchemaVersion int                       `json:"schema_version"`
	GeneratedAt   string                    `json:"generated_at"`
	Count         int                       `json:"count"`
	ByStatus      map[string]int            `json:"by_status"`
	LastRun       RunStats                  `json:"last_run"`
	Sources       map[string]map[string]int `json:"sources"`
}

type RunStats struct {
	StartedAt   string `json:"started_at"`
	DurationSec int    `json:"duration_sec"`
	Picked      int    `json:"picked"`
	Added       int    `json:"added"`
	Updated     int    `json:"updated"`
	Failed      int    `json:"failed"`
	Source      string `json:"source"`       // 固定为 dict（清单来自 actor_dict.csv）
	SourceCount int    `json:"source_count"` // 待抓清单里的演员总数
}
