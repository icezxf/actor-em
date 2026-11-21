package actorsync

import "strings"

// PurgeSharedBios 清掉「同一段简介挂在多个人身上」的错配。
//
// 典型来源：dramaface 的短 slug 会撞到同名别人。实测「坂田もも」按末段
// slug "Momo" 拿回了「市井桃（ＭＯ☆ＭＯ）」的简介，同一段文字最终挂到
// 19 个名字带「もも/桃」的人身上；全库这种成组的重复简介有 100 组、559 人。
//
// 给 Emby 配上错误的人物简介，比没有简介糟糕得多，所以这里宁可清空：
//
//  1. 按简介前 80 字（去空白）分组，只处理出现 ≥2 次的组；
//  2. 组内谁的「中文名」或「日文原名」出现在这段简介里，谁保留；
//  3. 一个都对不上的，整组清空——这段简介本来就不属于其中任何人。
//
// 幂等：清空后这些记录不再成组，重复跑不会二次改动。
// 清空的记录会在下一轮 --refresh / --fill-missing（bio）时按修好的匹配重抓。
// 返回被清掉的人数。
func PurgeSharedBios(actors []*Actor) int {
	groups := map[string][]*Actor{}
	for _, a := range actors {
		key := bioGroupKey(a)
		if key != "" {
			groups[key] = append(groups[key], a)
		}
	}

	purged := 0
	for _, list := range groups {
		if len(list) < 2 {
			continue
		}
		bio := StrVal(list[0].Bio)
		keep := map[*Actor]bool{}
		for _, a := range list {
			for _, name := range []string{a.Name, StrVal(a.NameOriginal)} {
				name = strings.TrimSpace(name)
				if len([]rune(name)) >= 2 && strings.Contains(bio, name) {
					keep[a] = true
					break
				}
			}
		}
		for _, a := range list {
			if keep[a] {
				continue
			}
			a.Bio = nil
			a.BioLang = nil
			purged++
		}
	}
	return purged
}

// bioGroupKey 取简介前 80 字做分组键，空白不影响比对。
func bioGroupKey(a *Actor) string {
	bio := StrVal(a.Bio)
	if bio == "" {
		return ""
	}
	r := []rune(strings.Join(strings.Fields(bio), ""))
	if len(r) > 80 {
		r = r[:80]
	}
	return string(r)
}
