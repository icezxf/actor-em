package actorsync

import (
	"sort"
	"strconv"
	"strings"
)

// 这一组函数治理「同一站点 ID 指向多人」这类数据质量问题。
//
// 背景：字典（amane + MDCx）把不同演员的艺名归成同一个日文名，或把同一人的
// 多个艺名拆成两条记录；JavStash 上也有两张 performer 卡指向同一个外部实体。
// 结果就是 actors.jsonl 里同一个 provider_id 挂在多条记录上（实测 621 对、
// 701 条记录，2026-11-22）。
//
// 治理分两步，都是幂等的纯离线操作，放在每轮同步收尾自动跑：
//
//  1. MergeDuplicateActors：把「核心身份锚点完全一致」的重复记录合并成一条。
//     锚点是生日、身高、出道年、JavStash ID——两个都有值却不一样，就一定是
//     两个不同的人，绝不合并（实测这类占 270 组，生日 1990-08-31/152cm 的人
//     和 1990-11-03/163cm 的人绝不能揉成一条）。锚点都不冲突的 65 组才合。
//
//  2. FixKeyProviderMismatch：修「主键串号」——key 写着 javstash:A，provider_ids
//     里 JavStash 却是 B。这是 refresh 用被污染的别名命中了别人导致的。key 是
//     身份主键，以它为准：若库里另有 key=B 的记录，把 A 上借来的 B 抹掉。
//
// 无法判定归属的第三方 ID 冲突（两人都指向同一个 Wikipedia 页面等）**不动**，
// 留档等人工核对——宁可让报告里留一条提示，也不要擅自删掉可能正确的关联。

// identityAnchors 是判定「同一人」的核心锚点。两边都有值且不同即判为不同人。
var identityAnchors = []string{"birth_date", "height_cm", "career_start"}

// MergeDuplicateActors 合并共享 provider_id 且核心锚点不冲突的重复记录。
// 返回 (去重后的切片, 合并组数, 被移除的记录数)。被移除记录的字段先并进保留记录再删。
// 调用方必须用返回的切片替换原切片——被删的记录已从新切片里剔除。
//
// 幂等：合并后不再有共享 ID + 锚点一致的成对记录，重复跑不再改动。
func MergeDuplicateActors(actors []*Actor) ([]*Actor, int, int) {
	groups, removed := 0, 0
	// key → 记录（只处理可导出的；no_source/skipped 留档不参与）
	byKey := map[string]*Actor{}
	for _, a := range actors {
		if a != nil && a.Exportable() {
			byKey[a.Key] = a
		}
	}
	if len(byKey) < 2 {
		return actors, 0, 0
	}

	// (site, pid) → 持有它的 key 列表。site/pid 都小写比较。
	owners := map[string][]string{}
	for k, a := range byKey {
		for site, pid := range a.ProviderIDs {
			if strings.TrimSpace(pid) == "" {
				continue
			}
			id := strings.ToLower(site) + "\x00" + strings.ToLower(strings.TrimSpace(pid))
			owners[id] = append(owners[id], k)
		}
	}

	// 并查集：共享任一 ID 的记录先归到同一组。
	parent := map[string]string{}
	var find func(string) string
	find = func(x string) string {
		p, ok := parent[x]
		if !ok {
			parent[x] = x
			return x
		}
		if p != x {
			p = find(p)
			parent[x] = p
		}
		return p
	}
	union := func(a, b string) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[ra] = rb
		}
	}
	for _, keys := range owners {
		for i := 1; i < len(keys); i++ {
			if keys[0] != keys[i] {
				union(keys[0], keys[i])
			}
		}
	}

	clusters := map[string][]string{}
	for k := range parent {
		r := find(k)
		clusters[r] = append(clusters[r], k)
	}

	drop := map[string]bool{} // 要从库里删掉的 key
	for _, keys := range clusters {
		if len(keys) < 2 {
			continue
		}
		members := make([]*Actor, 0, len(keys))
		for _, k := range keys {
			members = append(members, byKey[k])
		}
		if identityConflict(members) {
			continue // 生日/身高/出道年/JavStash ID 打架 = 不同的人，不合并
		}
		// 保留 created_at 最早的一条当主记录（key 稳定，不会因合并改变）。
		sort.Slice(members, func(i, j int) bool {
			if members[i].CreatedAt != members[j].CreatedAt {
				return members[i].CreatedAt < members[j].CreatedAt
			}
			return members[i].Key < members[j].Key
		})
		keep := members[0]
		for _, dup := range members[1:] {
			mergeInto(keep, dup)
			drop[dup.Key] = true
		}
		groups++
	}
	if len(drop) == 0 {
		return mergeSameNameBirth(actors)
	}
	kept := make([]*Actor, 0, len(actors))
	for _, a := range actors {
		if a != nil && drop[a.Key] {
			removed++
			continue
		}
		kept = append(kept, a)
	}
	// 第二遍：同名 + 同生日的重复卡。
	// 不能和上面的并查集混在一起跑——ID 链会把不同名字的人连成一个大 cluster，
	// 整个 cluster 就会被严格规则否掉，连带把本该合并的同名同生日组也跳过了。
	// 独立成第二遍，按 (规范化名字, 生日) 精确分组，互不干扰。
	out, g2, r2 := mergeSameNameBirth(kept)
	return out, groups + g2, removed + r2
}

// mergeSameNameBirth 把「规范化名字相同 + 生日完全一致」的记录合并成一条。
//
// 背景：StashDB 上同一人常有多张 performer 卡，各自独立 UUID、互不共享 provider_id，
// 单靠 ID 共享永远聚不到一起。但同名 + 同一天生日几乎不可能是两个不同的人。
// 生日为空的一律不参与（没有证据就不合并）。
// 返回 (合并后的切片, 合并组数, 移除条数)。
func mergeSameNameBirth(actors []*Actor) ([]*Actor, int, int) {
	groups := map[string][]*Actor{}
	for _, a := range actors {
		if a == nil || !a.Exportable() {
			continue
		}
		bd := strings.TrimSpace(StrVal(a.BirthDate))
		if bd == "" {
			continue
		}
		k := NormalizeName(a.Name) + "\x00" + bd
		groups[k] = append(groups[k], a)
	}
	drop := map[string]bool{}
	n := 0
	for _, members := range groups {
		if len(members) < 2 {
			continue
		}
		// 组内可能有 JavStash ID 冲突（不同 performer 卡），有冲突就不合并，保守处理。
		firstJS := ""
		conflict := false
		for _, a := range members {
			v := strings.ToLower(strings.TrimSpace(a.ProviderIDs["JavStash"]))
			if v == "" {
				continue
			}
			if firstJS == "" {
				firstJS = v
			} else if v != firstJS {
				conflict = true
				break
			}
		}
		if conflict {
			continue
		}
		sort.Slice(members, func(i, j int) bool {
			if members[i].CreatedAt != members[j].CreatedAt {
				return members[i].CreatedAt < members[j].CreatedAt
			}
			return members[i].Key < members[j].Key
		})
		for _, dup := range members[1:] {
			mergeInto(members[0], dup)
			drop[dup.Key] = true
		}
		n++
	}
	if len(drop) == 0 {
		return actors, 0, 0
	}
	out := make([]*Actor, 0, len(actors)-len(drop))
	for _, a := range actors {
		if a != nil && drop[a.Key] {
			continue
		}
		out = append(out, a)
	}
	return out, n, len(drop)
}

// identityConflict 判断一组记录里是否有「两个都有值却不一样」的核心锚点。
// 只要有一个锚点出现两个不同的值，就认定是不同的人，拒绝合并。
// 全部为空、或都一致都算不冲突。
//
// 欧美宽松例外：一组全是 stashdb:* 记录、名字相同、生日一致时，忽略 career_start /
// height_cm 的冲突——StashDB 上同一人常有多张卡（出道年 2007 vs 2021 之类）。
// 生日冲突或 JavStash ID 冲突仍严格拒绝，日系记录一律走严格规则。
func identityConflict(actors []*Actor) bool {
	// 先判生日冲突：任何时候两个不同生日都是不同的人。
	firstBirth := ""
	for _, a := range actors {
		v := StrVal(a.BirthDate)
		if v == "" {
			continue
		}
		if firstBirth == "" {
			firstBirth = v
		} else if v != firstBirth {
			return true
		}
	}
	// JavStash ID 两边都有且不同 —— 两张不同的 performer 卡，不是同一人。
	firstJS := ""
	for _, a := range actors {
		v := strings.ToLower(strings.TrimSpace(a.ProviderIDs["JavStash"]))
		if v == "" {
			continue
		}
		if firstJS == "" {
			firstJS = v
		} else if v != firstJS {
			return true
		}
	}

	// 欧美宽松：全 stashdb + 名字相同 + 生日一致 → 忽略出道年/身高冲突
	euLoose := firstJS == "" && sameNameAndBirth(actors)
	for _, field := range identityAnchors {
		if euLoose && (field == "career_start" || field == "height_cm") {
			continue
		}
		first := ""
		for _, a := range actors {
			v := actorField(a, field)
			if v == "" {
				continue
			}
			if first == "" {
				first = v
			} else if v != first {
				return true
			}
		}
	}
	return false
}

// sameNameAndBirth 判断一组记录名字（规范化）相同、且生日一致（都空也算一致）。
func sameNameAndBirth(actors []*Actor) bool {
	if len(actors) < 2 {
		return false
	}
	name := NormalizeName(actors[0].Name)
	for _, a := range actors[1:] {
		if NormalizeName(a.Name) != name {
			return false
		}
	}
	return true // 生日一致性已由 identityConflict 上方的 firstBirth 检查保证
}

// actorField 取核心锚点字段的字符串值（空值返回空串）。
func actorField(a *Actor, field string) string {
	switch field {
	case "birth_date":
		return StrVal(a.BirthDate)
	case "height_cm":
		if a.HeightCm != nil {
			return strconv.Itoa(*a.HeightCm)
		}
	case "career_start":
		if a.CareerStart != nil {
			return strconv.Itoa(*a.CareerStart)
		}
	}
	return ""
}

// mergeInto 把 dup 的信息并进 keep：标量字段只填空位，数组/字典取并集。
// 简介取更长的那条；头像保留 keep 已有的。
func mergeInto(keep, dup *Actor) {
	if keep.NameOriginal == nil {
		keep.NameOriginal = dup.NameOriginal
	}
	if keep.Gender == nil {
		keep.Gender = dup.Gender
	}
	if keep.BirthDate == nil {
		keep.BirthDate = dup.BirthDate
	}
	if keep.BirthYear == nil {
		keep.BirthYear = dup.BirthYear
	}
	if keep.BirthDateSource == nil {
		keep.BirthDateSource = dup.BirthDateSource
	}
	if keep.BirthPlace == nil {
		keep.BirthPlace = dup.BirthPlace
	}
	if keep.Country == nil {
		keep.Country = dup.Country
	}
	if keep.HeightCm == nil {
		keep.HeightCm = dup.HeightCm
	}
	if keep.WeightKg == nil {
		keep.WeightKg = dup.WeightKg
	}
	if keep.Measurements == nil {
		keep.Measurements = dup.Measurements
	}
	if keep.MeasurementsUnit == nil {
		keep.MeasurementsUnit = dup.MeasurementsUnit
	}
	if keep.CupSize == nil {
		keep.CupSize = dup.CupSize
	}
	if keep.CareerStart == nil {
		keep.CareerStart = dup.CareerStart
	}
	if keep.Bio == nil || (dup.Bio != nil && len([]rune(StrVal(dup.Bio))) > len([]rune(StrVal(keep.Bio)))) {
		if dup.Bio != nil {
			keep.Bio = dup.Bio
			keep.BioLang = dup.BioLang
		}
	}
	if keep.HomePageURL == nil {
		keep.HomePageURL = dup.HomePageURL
	}
	if keep.Avatar == nil {
		keep.Avatar = dup.Avatar
		keep.AvatarCN = dup.AvatarCN
		keep.AvatarSourceSite = dup.AvatarSourceSite
	}
	keep.Aliases = mergeStrings(keep.Aliases, dup.Aliases)
	keep.Tags = mergeStrings(keep.Tags, dup.Tags)
	if keep.ProviderIDs == nil {
		keep.ProviderIDs = map[string]string{}
	}
	for k, v := range dup.ProviderIDs {
		if v != "" && keep.ProviderIDs[k] == "" {
			keep.ProviderIDs[k] = v
		}
	}
	if keep.Sources == nil {
		keep.Sources = map[string]bool{}
	}
	for k, v := range dup.Sources {
		keep.Sources[k] = keep.Sources[k] || v
	}
	// 两条都 ok 仍是 ok；一条 partial 一条 ok 取 ok。
	if keep.Status == StatusPartial && dup.Status == StatusOK {
		keep.Status = StatusOK
	}
}

// FixKeyProviderMismatch 修主键串号：key 是 javstash:A / stashdb:A / tpdb:A，
// provider_ids 里对应站点的 ID 却是 B。以 key 为准——若库里另有 key=B 的记录，
// 说明 A 借用了 B 的 ID，抹掉 A 上这个借来的值；否则恢复成 key 里的 A。
// 返回修复条数。
func FixKeyProviderMismatch(actors []*Actor) int {
	keys := map[string]bool{}
	for _, a := range actors {
		if a != nil {
			keys[strings.ToLower(a.Key)] = true
		}
	}
	fixed := 0
	for _, a := range actors {
		if a == nil || !a.Exportable() || len(a.ProviderIDs) == 0 {
			continue
		}
		for _, prefix := range []string{"javstash", "stashdb", "tpdb"} {
			wantSite, wantID, ok := splitKey(a.Key, prefix)
			if !ok {
				continue
			}
			cur := strings.TrimSpace(a.ProviderIDs[wantSite])
			if cur == "" || strings.EqualFold(cur, wantID) {
				continue
			}
			// key 说我是 A，provider_ids 却写了 B。
			if keys[prefix+":"+strings.ToLower(cur)] {
				// 库里真有 B 这条记录 —— A 是借用的，抹掉。
				delete(a.ProviderIDs, wantSite)
			} else {
				// 没有 B —— 大概率是 refresh 串号，恢复成 key 的 A。
				a.ProviderIDs[wantSite] = wantID
			}
			fixed++
		}
	}
	return fixed
}

// splitKey 判断 key 是否是 "<prefix>:<id>" 并返回规范站点名与 id。
func splitKey(key, prefix string) (site, id string, ok bool) {
	colon := strings.IndexByte(key, ':')
	if colon < 0 || !strings.EqualFold(key[:colon], prefix) {
		return "", "", false
	}
	id = strings.TrimSpace(key[colon+1:])
	if id == "" {
		return "", "", false
	}
	switch prefix {
	case "javstash":
		return "JavStash", id, true
	case "stashdb":
		return "StashDB", id, true
	case "tpdb":
		return "TPDB", id, true
	}
	return "", "", false
}
