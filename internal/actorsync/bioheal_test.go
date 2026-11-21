package actorsync

import "testing"

func TestPurgeSharedBios(t *testing.T) {
	bio := "市井桃（ＭＯ☆ＭＯ，1986年8月12日－）是日本的前AV女优，兴趣是美甲艺术，特长是游泳。"

	owner := &Actor{Key: "a", Name: "市井桃", Bio: StrPtr(bio)}
	same := &Actor{Key: "b", Name: "坂田もも", Bio: StrPtr(bio)}
	other := &Actor{Key: "c", Name: "唐木ひとみ", Bio: StrPtr(bio)}

	// 另一段独有简介，不该被动
	solo := &Actor{Key: "d", Name: "三上悠亚", Bio: StrPtr("三上悠亚是日本演员，1993 年出生于爱知县。")}

	if n := PurgeSharedBios([]*Actor{owner, same, other, solo}); n != 2 {
		t.Fatalf("应清掉 2 条，实际 %d", n)
	}
	if StrVal(owner.Bio) == "" {
		t.Error("名字出现在简介里的记录应当保留")
	}
	if StrVal(same.Bio) != "" || StrVal(other.Bio) != "" {
		t.Error("不属于他们的简介应被清空")
	}
	if StrVal(solo.Bio) == "" {
		t.Error("独有简介不该被动")
	}
	if owner.BioLang != nil {
		t.Error("没动过的记录 BioLang 应保持原样")
	}

	// 幂等：再跑一次没有任何改动
	if n := PurgeSharedBios([]*Actor{owner, same, other, solo}); n != 0 {
		t.Fatalf("第二次应为 0，实际 %d", n)
	}
}

func TestPurgeSharedBiosNoNameMatchClearsAll(t *testing.T) {
	bio := "某位日本女优，2010 年出道，兴趣是旅行。"
	a := &Actor{Key: "a", Name: "甲", Bio: StrPtr(bio)}
	b := &Actor{Key: "b", Name: "乙", Bio: StrPtr(bio)}
	if n := PurgeSharedBios([]*Actor{a, b}); n != 2 {
		t.Fatalf("一个都对不上应整组清空，实际 %d", n)
	}
}
