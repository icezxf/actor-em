package actorsync

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// PlanItem 是本轮要抓的一个人。
type PlanItem struct {
	Name     string
	Reason   string // new / retry / refresh / fill
	Existing *Actor
	Missing  []string // fill 模式：本条记录缺哪些字段
}

// FillOrder 决定 --fill-missing 按什么顺序挑人。顺序直接决定一轮能补到多少：
// 同一批 2000 人，「缺得最多优先」只补到 3.4%，「最完整优先」能到 8% 以上
// （有生日+出道年的人拿到简介的概率 8.3%，两样都没有的是 0%）。
type FillOrder string

const (
	// FillOrderComplete 先挑字段最全的：资料越全的人越有名，越可能有简介等
	// 长尾数据可补。同分时先挑最久没试过的，避免每轮都重抓同一批人。
	FillOrderComplete FillOrder = "complete"
	// FillOrderMissing 先挑缺得最多的：同样预算先把最残的记录救起来。
	FillOrderMissing FillOrder = "missing"
	// FillOrderName 按名字排，结果可复现，适合排查/小批量。
	FillOrderName FillOrder = "name"
)

// ParseFillOrder 解析 --fill-order，留空默认 complete。
func ParseFillOrder(s string) (FillOrder, error) {
	switch FillOrder(strings.ToLower(strings.TrimSpace(s))) {
	case "", FillOrderComplete:
		return FillOrderComplete, nil
	case FillOrderMissing:
		return FillOrderMissing, nil
	case FillOrderName:
		return FillOrderName, nil
	}
	return "", fmt.Errorf("未知的补字段排序 %q（可选 complete / missing / name）", s)
}

// BuildPlanWithFill 在 BuildPlan 之上支持「缺字段重抓」。
//
// fillFields 非 nil 表示 --fill-missing 模式：只挑库里已经导出、但仍有空字段的
// 记录（reason=fill），不掺 new/retry；补值在 Engine 里按只填空位合并。
// 传 nil 时行为与 BuildPlan 完全一致。
//
// 等价于 BuildPlanWithFillOrder(..., FillOrderMissing, 0)，即老行为。
func BuildPlanWithFill(db *Db, names []string, refresh []string, refreshAll bool, fillFields []string) []PlanItem {
	return BuildPlanWithFillOrder(db, names, refresh, refreshAll, fillFields, FillOrderMissing, 0)
}

// BuildPlanWithFillOrder 同上，但可以指定挑人顺序，以及跳过最近刚试过的人。
//
// minAgeDays > 0 时，上轮补字段刚试过（retry_after 还没到期）的记录本轮跳过——
// 配合「最完整优先」就是一列不重复的队列：每轮往后走一段，不会把同一批硬骨头反复抓。
// 用 retry_after 而不是 scraped_at，是因为后者会被 refresh_all 顺手刷新。
func BuildPlanWithFillOrder(db *Db, names []string, refresh []string, refreshAll bool, fillFields []string, order FillOrder, minAgeDays int) []PlanItem {
	if fillFields == nil {
		return BuildPlan(db, names, refresh, refreshAll)
	}

	now := time.Now().UTC()
	var items []PlanItem
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		existing := db.FindByName(name)
		if existing == nil || !existing.Exportable() {
			// skipped / no_source / failed 本来就不进下游，补了也没人看。
			continue
		}
		if minAgeDays > 0 && fillBackoffActive(existing, now) {
			continue
		}
		missing := MissingFields(existing, fillFields)
		if len(missing) == 0 {
			continue
		}
		items = append(items, PlanItem{
			Name:     name,
			Reason:   "fill",
			Existing: existing,
			Missing:  missing,
		})
	}

	switch order {
	case FillOrderName:
		sort.SliceStable(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	case FillOrderMissing:
		// 缺得最多的排前面：同样的预算先救最残的记录。
		sort.SliceStable(items, func(i, j int) bool {
			return len(items[i].Missing) > len(items[j].Missing)
		})
	default:
		// 最完整优先；完整度相同的先挑最久没试过的（scraped_at 最早），再按名字，
		// 保证每轮挑到的都是「更可能有货」且「很久没打扰」的人。
		sort.SliceStable(items, func(i, j int) bool {
			a, b := items[i], items[j]
			if pa, pb := fillPriority(a.Existing), fillPriority(b.Existing); pa != pb {
				return pa > pb
			}
			if a.Existing.ScrapedAt != b.Existing.ScrapedAt {
				return a.Existing.ScrapedAt < b.Existing.ScrapedAt
			}
			if len(a.Missing) != len(b.Missing) {
				return len(a.Missing) < len(b.Missing)
			}
			return a.Name < b.Name
		})
	}
	return items
}

// fillPriority 是「最完整优先」的打分。权重照实测产出标定：
// 生日、出道年是简介可得性的最强预测（有则 8.3%，都没有 0%），给最高分；
// 身高/三围/头像/标签/provider_ids 是「这人被多个源收录过」的旁证，给低分。
func fillPriority(a *Actor) int {
	if a == nil {
		return 0
	}
	score := 0
	if StrVal(a.BirthDate) != "" {
		score += 5
	}
	if a.CareerStart != nil && *a.CareerStart > 0 {
		score += 5
	}
	if a.HeightCm != nil && *a.HeightCm > 0 {
		score += 2
	}
	if StrVal(a.Measurements) != "" {
		score += 2
	}
	if StrVal(a.Avatar) != "" {
		score += 2
	}
	if n := len(a.Tags); n > 0 {
		if n > 5 {
			n = 5
		}
		score += n
	}
	if len(a.ProviderIDs) > 0 {
		score += 2
	}
	return score
}

// CountFillBackoff 数一数库里有几条记录还在补字段退避期里（排查用）。
func CountFillBackoff(actors []*Actor, now time.Time) int {
	n := 0
	for _, a := range actors {
		if a != nil && fillBackoffActive(a, now) {
			n++
		}
	}
	return n
}

// fillBackoffActive 判断「这个人上轮补字段刚试过、还没到再试的时候」。
//
// 判据是 retry_after，不是 scraped_at：scraped_at 会被全量刷新顺手更新
// （refresh_all 之后所有人都算刚抓过，用它当过滤条件会把整张计划清空），
// 而 retry_after 只有补字段这一条路径会为 ok/partial 记录设置。
func fillBackoffActive(a *Actor, now time.Time) bool {
	if a.RetryAfter == nil || *a.RetryAfter == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, *a.RetryAfter)
	if err != nil {
		return false
	}
	return now.Before(t)
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

	// name_original 存数据源返回的原始名（日文原名），和清单里的名字不同才有价值。
	// UUID 清单模式下清单名是 StashDB 的 ID，绝不能当 original——
	// 用数据源返回的真实英文名；后面「与 displayName 相同就清空」会处理掉重复。
	origName := res.NameOriginal
	if IsUUID(name) {
		origName = res.CanonicalName
	}
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
		BirthYear:        IntPtr(res.BirthYear),
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
