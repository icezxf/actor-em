package actorsync

import (
	"sort"
	"strings"
	"time"
)

// PlanItem 是本轮要抓的一个人。
type PlanItem struct {
	EmbyID   string
	Name     string
	Reason   string // new / retry / refresh
	Existing *Actor
}

// BuildPlan 对比 Emby 人物清单和本地库，算出本轮该抓谁。
// 主路径只有一条：Emby 里有、库里没有 → 抓新演员。
func BuildPlan(db *Db, persons []EmbyPerson, refresh []string, refreshAll bool) []PlanItem {
	refreshSet := map[string]bool{}
	for _, n := range refresh {
		refreshSet[NormalizeName(n)] = true
	}

	now := time.Now().UTC()
	var items []PlanItem

	for _, p := range persons {
		name := strings.TrimSpace(p.Name)
		if name == "" {
			continue
		}

		// ① 先按 Emby Id 找
		if a := db.FindByEmbyID(p.ID); a != nil {
			switch {
			case refreshAll:
				items = append(items, PlanItem{EmbyID: p.ID, Name: name, Reason: "refresh", Existing: a})
			case refreshSet[NormalizeName(name)]:
				items = append(items, PlanItem{EmbyID: p.ID, Name: name, Reason: "refresh", Existing: a})
			case a.Status == StatusFailed && retryDue(a, now):
				items = append(items, PlanItem{EmbyID: p.ID, Name: name, Reason: "retry", Existing: a})
			}
			continue
		}

		// ② 再按名字找：Emby 里改名了，但人还是同一个
		if a := db.FindByName(name); a != nil && len(a.EmbyIDs) == 0 {
			a.EmbyIDs = mergeStrings(a.EmbyIDs, []string{p.ID})
			if refreshAll || refreshSet[NormalizeName(name)] {
				items = append(items, PlanItem{EmbyID: p.ID, Name: name, Reason: "refresh", Existing: a})
			}
			continue
		}

		// ③ 用 Emby 已有的外部 ID 再找一次（Emby 里常常已经刮到了 Tmdb/Imdb）
		if len(p.ProviderIDs) > 0 {
			if a := db.FindByProviderIDs(p.ProviderIDs); a != nil {
				a.EmbyIDs = mergeStrings(a.EmbyIDs, []string{p.ID})
				if refreshAll || refreshSet[NormalizeName(name)] {
					items = append(items, PlanItem{EmbyID: p.ID, Name: name, Reason: "refresh", Existing: a})
				}
				continue
			}
		}

		// ④ 库里没有 → 新演员
		items = append(items, PlanItem{EmbyID: p.ID, Name: name, Reason: "new"})
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
func BuildActor(embyID, name string, res *FetchResult, existing *Actor) *Actor {
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

	// Emby 里把 UUID 当名字存的情况：用数据源返回的真实名字
	displayName := name
	if IsUUID(name) && res.CanonicalName != "" {
		displayName = res.CanonicalName
	}

	// name_original 存数据源返回的原始名（日文原名），和 Emby 里的名字不同才有价值
	origName := res.NameOriginal
	if res.CanonicalName != "" && NormalizeName(res.CanonicalName) != NormalizeName(displayName) {
		origName = res.CanonicalName
	}

	a := &Actor{
		Key:              key,
		Name:             displayName,
		NameOriginal:     StrPtr(origName),
		Aliases:          res.Aliases,
		EmbyIDs:          mergeStrings(nil, []string{embyID}),
		Gender:           StrPtr(res.Gender),
		BirthDate:        StrPtr(res.BirthDate),
		BirthPlace:       StrPtr(res.BirthPlace),
		Country:          StrPtr(res.Country),
		HeightCm:         IntPtr(res.HeightCm),
		WeightKg:         IntPtr(res.WeightKg),
		CupSize:          StrPtr(res.CupSize),
		WaistCm:          IntPtr(res.WaistCm),
		HipCm:            IntPtr(res.HipCm),
		CareerStart:      IntPtr(res.CareerStart),
		Bio:              StrPtr(res.Bio),
		BioLang:          StrPtr(res.BioLang),
		AvatarSource:     StrPtr(res.ImageURL),
		AvatarSourceSite: StrPtr(res.ImageSite),
		ProviderIDs:      res.ProviderIDs,
		Status:           res.Outcome(),
		Sources:          res.Sources,
		CreatedAt:        created,
		ScrapedAt:        now,
	}
	if a.Aliases == nil {
		a.Aliases = []string{}
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

	// 清单来自 txt 时没有 Emby Id，保留记录里原有的
	if embyID == "" && existing != nil {
		a.EmbyIDs = existing.EmbyIDs
	}

	// 没抓到图时，保留旧头像字段，不要把已有图擦掉
	if a.AvatarSource == nil && existing != nil {
		a.Avatar = existing.Avatar
		a.AvatarPath = existing.AvatarPath
		a.AvatarDir = existing.AvatarDir
		a.AvatarSource = existing.AvatarSource
		a.AvatarSourceSite = existing.AvatarSourceSite
		a.AvatarSHA256 = existing.AvatarSHA256
		a.AvatarBytes = existing.AvatarBytes
		a.AvatarW = existing.AvatarW
		a.AvatarH = existing.AvatarH
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
