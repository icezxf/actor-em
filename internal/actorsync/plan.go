package actorsync

import (
	"sort"
	"strings"
	"time"
)

// PlanItem 是本轮要抓的一个人。
type PlanItem struct {
	Name     string
	Reason   string // new / retry / refresh
	Existing *Actor
}

// BuildPlan 对比 txt 清单和本地库，算出本轮该抓谁。
//
// 规则：
//   - 库里没有 → new
//   - 有记录，且 --refresh / --refresh-all 指定 → refresh
//   - 有记录，状态是 failed / no_source，且过了 retry_after → retry
//   - 其余（ok / partial）跳过，除非手动 refresh
func BuildPlan(db *Db, names []string, refresh []string, refreshAll bool) []PlanItem {
	refreshSet := map[string]bool{}
	for _, n := range refresh {
		refreshSet[NormalizeName(n)] = true
	}

	now := time.Now().UTC()
	var items []PlanItem

	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}

		existing := db.FindByName(name)
		if existing == nil {
			items = append(items, PlanItem{Name: name, Reason: "new"})
			continue
		}

		switch {
		case refreshAll, refreshSet[NormalizeName(name)]:
			items = append(items, PlanItem{Name: name, Reason: "refresh", Existing: existing})
		case (existing.Status == StatusFailed || existing.Status == StatusNoSource) && retryDue(existing, now):
			items = append(items, PlanItem{Name: name, Reason: "retry", Existing: existing})
		}
	}

	// 新演员优先，其次是重试，最后是手动刷新
	rank := map[string]int{"new": 0, "retry": 1, "refresh": 2}
	sort.SliceStable(items, func(i, j int) bool {
		return rank[items[i].Reason] < rank[items[j].Reason]
	})
	return items
}

func retryDue(a *Actor, now time.Time) bool {
	if a.RetryAfter == nil || *a.RetryAfter == "" {
		return true
	}
	t, err := time.Parse(time.RFC3339, *a.RetryAfter)
	if err != nil {
		return true
	}
	return now.After(t)
}

// BuildActor 把抓取结果落成一条记录。
func BuildActor(name string, res *FetchResult, existing *Actor) *Actor {
	now := time.Now().UTC().Format(time.RFC3339)

	key := BuildKey(res.ProviderIDs, name)
	created := now
	if existing != nil {
		// 已有记录不换主键，避免因为新匹配到 ID 而在库里出现重复条目
		key = existing.Key
		if existing.CreatedAt != "" {
			created = existing.CreatedAt
		}
	}

	// 清单里直接写 UUID 的情况：用数据源返回的真实名字
	displayName := name
	if IsUUID(name) && res.CanonicalName != "" {
		displayName = res.CanonicalName
	}
	// 有中文名就用中文名，Emby 的 Name 是中文
	if res.ChineseName != "" {
		displayName = res.ChineseName
	}

	// name_original 存数据源返回的原始名（日文原名），和清单里的名字不同才有价值
	origName := res.NameOriginal
	if res.CanonicalName != "" && NormalizeName(res.CanonicalName) != NormalizeName(displayName) {
		origName = res.CanonicalName
	}

	a := &Actor{
		Key:              key,
		Name:             displayName,
		NameOriginal:     StrPtr(origName),
		Aliases:          res.Aliases,
		Gender:           StrPtr(res.Gender),
		BirthDate:        StrPtr(res.BirthDate),
		BirthDateSource:  StrPtr(res.BirthDateSource),
		BirthPlace:       StrPtr(res.BirthPlace),
		Country:          StrPtr(res.Country),
		HeightCm:         IntPtr(res.HeightCm),
		WeightKg:         IntPtr(res.WeightKg),
		Measurements:     StrPtr(res.Measurements),
		MeasurementsUnit: StrPtr(res.MeasurementsUnit),
		CupSize:          StrPtr(res.CupSize),
		CareerStart:      IntPtr(res.CareerStart),
		Tags:             res.Tags,
		Bio:              StrPtr(res.Bio),
		BioLang:          StrPtr(res.BioLang),
		HomePageURL:      StrPtr(res.HomePageURL),
		ProviderIDs:      res.ProviderIDs,
		Status:           res.Outcome(),
		Sources:          res.Sources,
		CreatedAt:        created,
		ScrapedAt:        now,
	}
	if a.Aliases == nil {
		a.Aliases = []string{}
	}
	if a.Tags == nil {
		a.Tags = []string{}
	}
	if a.ProviderIDs == nil {
		a.ProviderIDs = map[string]string{}
	}
	if a.Sources == nil {
		a.Sources = map[string]bool{}
	}
	// 名字哈希兜底时，原始名和 name 相同，不必重复存
	if a.NameOriginal != nil && NormalizeName(*a.NameOriginal) == NormalizeName(displayName) {
		a.NameOriginal = nil
	}

	// 失败状态写入退避时间
	switch a.Status {
	case StatusFailed:
		a.FailCount = existingFailCount(existing) + 1
		if a.FailCount >= 3 {
			// 连续 3 次都出错，转成 no_source，避免永远重试
			a.Status = StatusNoSource
			a.RetryAfter = StrPtr(nowPlusDays(180))
		} else {
			a.RetryAfter = StrPtr(nowPlusDays(1 << a.FailCount)) // 2、4 天
		}
	case StatusNoSource:
		a.FailCount = 0
		a.RetryAfter = StrPtr(nowPlusDays(180))
	default:
		a.FailCount = 0
		a.RetryAfter = nil
	}

	// 先保留旧头像：新图没抓到、或者处理失败时，不要把已有直链擦掉
	if existing != nil {
		a.Avatar = existing.Avatar
		a.AvatarCN = existing.AvatarCN
		a.AvatarSourceSite = existing.AvatarSourceSite
	}
	// 有新图时记录新图来源；人工选图后会由调用方覆盖
	if res.ImageURL != "" {
		a.Avatar = StrPtr(res.ImageURL)
		a.AvatarCN = StrPtr(MirrorCN(res.ImageURL))
		a.AvatarSourceSite = StrPtr(res.ImageSite)
	}

	return a
}

func existingFailCount(a *Actor) int {
	if a == nil {
		return 0
	}
	return a.FailCount
}

func nowPlusDays(d int) string {
	return time.Now().UTC().AddDate(0, 0, d).Format(time.RFC3339)
}
