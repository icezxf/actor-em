package actorsync

import (
	"reflect"
	"testing"
)

func okActor(key, name string) *Actor {
	return &Actor{
		Key:         key,
		Name:        name,
		Status:      StatusOK,
		ProviderIDs: map[string]string{},
		Sources:     map[string]bool{},
		Aliases:     []string{},
		Tags:        []string{},
	}
}

func TestMergeDuplicateActors_IdenticalTwins(t *testing.T) {
	// 两条共享 JavStash+TPDB，生日/身高/出道年/JavStash ID 全一致 → 合并
	birth := "1995-03-10"
	h, career := 160, 2015
	a := okActor("name:aaa", "世良しずく")
	a.BirthDate, a.HeightCm, a.CareerStart = &birth, &h, &career
	a.ProviderIDs = map[string]string{"JavStash": "id-A", "TPDB": "shared-1"}
	a.Aliases = []string{"しずく"}
	b := okActor("name:bbb", "しずく")
	b.BirthDate, b.HeightCm, b.CareerStart = &birth, &h, &career
	b.ProviderIDs = map[string]string{"JavStash": "id-A", "TPDB": "shared-1"}
	b.Bio = StrPtr("一段更长的中文简介，用来验证简介取更长的那条。")
	b.CreatedAt = "2020-01-01T00:00:00Z"

	actors := []*Actor{a, b}
	actors, g, r := MergeDuplicateActors(actors)
	if g != 1 || r != 1 {
		t.Fatalf("want 1 group / 1 removed, got %d/%d", g, r)
	}
	if len(actors) != 1 {
		t.Fatalf("want 1 actor left, got %d", len(actors))
	}
	if actors[0].Bio == nil || *actors[0].Bio != *b.Bio {
		t.Errorf("longer bio not kept")
	}
	if !reflect.DeepEqual(actors[0].Aliases, []string{"しずく"}) {
		t.Errorf("aliases not merged: %v", actors[0].Aliases)
	}
}

func TestMergeDuplicateActors_DifferentBirthday_NoMerge(t *testing.T) {
	// 共享 ID 但生日不同 → 两个不同的人，不合并
	b1, b2 := "1990-08-31", "1990-11-03"
	a := okActor("javstash:id-a", "皆濑ふう花")
	a.BirthDate = &b1
	a.ProviderIDs = map[string]string{"JavStash": "id-a", "TPDB": "shared"}
	b := okActor("javstash:id-b", "濑波结奈")
	b.BirthDate = &b2
	b.ProviderIDs = map[string]string{"JavStash": "id-b", "TPDB": "shared"}

	actors := []*Actor{a, b}
	actors, g, r := MergeDuplicateActors(actors)
	if g != 0 || r != 0 {
		t.Fatalf("different birthday must NOT merge, got %d/%d", g, r)
	}
	if len(actors) != 2 {
		t.Fatalf("want 2 actors left, got %d", len(actors))
	}
}

func TestMergeDuplicateActors_DifferentJavStashID_NoMerge(t *testing.T) {
	// 生日身高都缺，但 JavStash ID 不同 → 两张 performer 卡，不合并
	a := okActor("javstash:id-a", "A")
	a.ProviderIDs = map[string]string{"JavStash": "id-a", "Wikidata": "Q1"}
	b := okActor("javstash:id-b", "B")
	b.ProviderIDs = map[string]string{"JavStash": "id-b", "Wikidata": "Q1"}

	actors := []*Actor{a, b}
	actors, g, r := MergeDuplicateActors(actors)
	if g != 0 || r != 0 {
		t.Fatalf("different JavStash ID must NOT merge, got %d/%d", g, r)
	}
}

func TestMergeDuplicateActors_Idempotent(t *testing.T) {
	birth := "1992-01-01"
	a := okActor("name:aaa", "A")
	a.BirthDate = &birth
	a.ProviderIDs = map[string]string{"JavStash": "id-A", "TPDB": "shared"}
	b := okActor("name:bbb", "B")
	b.BirthDate = &birth
	b.ProviderIDs = map[string]string{"JavStash": "id-A", "TPDB": "shared"}

	actors := []*Actor{a, b}
	actors, _, _ = MergeDuplicateActors(actors)
	g2, r2 := 0, 0
	_, g2, r2 = MergeDuplicateActors(actors)
	if g2 != 0 || r2 != 0 {
		t.Fatalf("second run must be no-op, got %d/%d", g2, r2)
	}
}

func TestFixKeyProviderMismatch(t *testing.T) {
	// a 的 key 是 javstash:A，但 provider_ids 写了 B；库里另有 key=B 的记录 → 抹掉借来的 B
	a := okActor("javstash:id-A", "A")
	a.ProviderIDs = map[string]string{"JavStash": "id-B", "TPDB": "t1"}
	b := okActor("javstash:id-B", "B")
	b.ProviderIDs = map[string]string{"JavStash": "id-B"}

	actors := []*Actor{a, b}
	n := FixKeyProviderMismatch(actors)
	if n != 1 {
		t.Fatalf("want 1 fix, got %d", n)
	}
	if _, ok := a.ProviderIDs["JavStash"]; ok {
		t.Errorf("borrowed JavStash ID should be removed, got %v", a.ProviderIDs)
	}
}

func TestFixKeyProviderMismatch_Restore(t *testing.T) {
	// key=javstash:A，provider_ids 写了 B，但库里没有 B → 恢复成 A
	a := okActor("javstash:id-A", "A")
	a.ProviderIDs = map[string]string{"JavStash": "id-B"}

	actors := []*Actor{a}
	n := FixKeyProviderMismatch(actors)
	if n != 1 {
		t.Fatalf("want 1 fix, got %d", n)
	}
	if a.ProviderIDs["JavStash"] != "id-A" {
		t.Errorf("want restored id-A, got %v", a.ProviderIDs)
	}
}

func TestFixKeyProviderMismatch_NameHashUntouched(t *testing.T) {
	// name:hash 主键没有站点 ID，不动
	a := okActor("name:abc123", "A")
	a.ProviderIDs = map[string]string{"JavStash": "id-X"}
	actors := []*Actor{a}
	if n := FixKeyProviderMismatch(actors); n != 0 {
		t.Fatalf("name-hash key must be untouched, got %d", n)
	}
}
