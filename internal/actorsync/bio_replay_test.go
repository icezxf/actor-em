package actorsync

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

// 拿真实 936 条数据回放 ValidBio，看修复后能留下多少、丢掉的都是什么。
//
//	BIO_FIXTURE=/tmp/baseline.jsonl go test ./internal/actorsync -run ReplayBio -v
func TestReplayBioFixture(t *testing.T) {
	p := os.Getenv("BIO_FIXTURE")
	if p == "" {
		t.Skip("未设置 BIO_FIXTURE")
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	keep, drop := 0, 0
	dropSamples := []string{}
	var keepNames []string
	for dec.More() {
		var r struct {
			Name     string `json:"name"`
			Bio      string `json:"bio"`
			HeightCm int    `json:"height_cm"`
		}
		if err := dec.Decode(&r); err != nil {
			break
		}
		if r.Bio == "" {
			continue
		}
		ok := ValidBio(r.Bio)
		conflict := ok && BioConflictsHeight(r.Bio, r.HeightCm)
		if ok && !conflict {
			keep++
			if len(keepNames) < 12 {
				keepNames = append(keepNames, r.Name)
			}
		} else {
			drop++
			if len(dropSamples) < 15 {
				why := "不像本人"
				if conflict {
					why = fmt.Sprintf("身高冲突(%d vs %d)", BioHeightCm(r.Bio), r.HeightCm)
				}
				dropSamples = append(dropSamples, fmt.Sprintf("%-16s [%s] %s", r.Name, why, TruncateUTF8(r.Bio, 60)))
			}
		}
	}
	total := keep + drop
	t.Logf("有简介 %d 条 → 保留 %d (%.1f%%) / 丢弃 %d (%.1f%%)",
		total, keep, float64(keep)*100/float64(total), drop, float64(drop)*100/float64(total))
	t.Log("保留样例: " + fmt.Sprint(keepNames))
	t.Log("丢弃样例:")
	for _, s := range dropSamples {
		t.Log("   " + s)
	}
}
