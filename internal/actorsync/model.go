package actorsync

// Actor 是 actors.jsonl 里的一行（一个演员）。
// 字段全部扁平，空值一律 null（用指针表达），不写空字符串。
// 数组字段空时写 []，不写 null。
type Actor struct {
	Key          string   `json:"key"`
	Name         string   `json:"name"` // 中文名，Emby 的 Name
	NameOriginal *string  `json:"name_original"`
	Aliases      []string `json:"aliases"`

	Gender     *string `json:"gender"`
	BirthDate  *string `json:"birth_date"` // 只认 JavStash
	BirthPlace *string `json:"birth_place"`
	Country    *string `json:"country"`

	HeightCm     *int    `json:"height_cm"`
	WeightKg     *int    `json:"weight_kg"`    // 只有 TPDB 有，覆盖很低
	Measurements *string `json:"measurements"` // 原样字符串，如 "34E-21-31"，来自 TPDB
	CupSize      *string `json:"cup_size"`

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
		return true
	}
	return false
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
