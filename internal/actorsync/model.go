package actorsync

// Actor 是 actors.json 里的一条记录。
// 字段全部扁平，空值一律 null（用指针表达），不写空字符串。
type Actor struct {
	Key          string   `json:"key"`
	Name         string   `json:"name"`
	NameOriginal *string  `json:"name_original"`
	Aliases      []string `json:"aliases"`
	EmbyIDs      []string `json:"emby_ids"`

	Gender     *string `json:"gender"`
	BirthDate  *string `json:"birth_date"`
	DeathDate  *string `json:"death_date"`
	BirthPlace *string `json:"birth_place"`
	Country    *string `json:"country"`

	HeightCm *int    `json:"height_cm"`
	WeightKg *int    `json:"weight_kg"`
	CupSize  *string `json:"cup_size"`
	WaistCm  *int    `json:"waist_cm"`
	HipCm    *int    `json:"hip_cm"`

	CareerStart *int `json:"career_start"`

	Bio     *string `json:"bio"`
	BioLang *string `json:"bio_lang"`

	Avatar           *string `json:"avatar"`
	AvatarPath       *string `json:"avatar_path"`
	AvatarDir        *string `json:"avatar_dir"`
	AvatarSource     *string `json:"avatar_source"`
	AvatarSourceSite *string `json:"avatar_source_site"`
	AvatarSHA256     *string `json:"avatar_sha256"`
	AvatarBytes      *int64  `json:"avatar_bytes"`
	AvatarW          *int    `json:"avatar_w"`
	AvatarH          *int    `json:"avatar_h"`

	ProviderIDs map[string]string `json:"provider_ids"`

	Status     string          `json:"status"`
	Sources    map[string]bool `json:"sources"`
	FailCount  int             `json:"fail_count"`
	RetryAfter *string         `json:"retry_after"`

	CreatedAt string `json:"created_at"`
	ScrapedAt string `json:"scraped_at"`
}

// Db 是 actors.json 的顶层结构。
type Db struct {
	SchemaVersion int      `json:"schema_version"`
	GeneratedAt   string   `json:"generated_at"`
	Count         int      `json:"count"`
	Actors        []*Actor `json:"actors"`
}

const SchemaVersion = 1

// 记录状态
const (
	StatusOK       = "ok"        // 至少关键字段有值
	StatusPartial  = "partial"   // 命中了源，但生日和简介都为空
	StatusNoSource = "no_source" // 所有源都没有这个人
	StatusFailed   = "failed"    // 网络/限流错误，需要重试
)

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
	EmbyPersons int    `json:"emby_persons"`
}
