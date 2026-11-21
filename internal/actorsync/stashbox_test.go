package actorsync

import (
	"encoding/json"
	"strings"
	"testing"
)

// JavStash 和 StashDB 的 Performer 现在都只有扁平三围字段，
// 旧的嵌套 measurements 和 weight 已经删了。查询里带上不存在的字段会让整条
// GraphQL 请求校验失败（不是只丢那个字段），所以这里死守字段清单。
func TestStashGraphQLFieldsOnlyFlat(t *testing.T) {
	for _, gone := range []string{"measurements", "weight"} {
		if strings.Contains(stashGraphQLFields, gone) {
			t.Errorf("查询里不该再出现 %q，两个站都已删除该字段，带上会导致整条查询失败", gone)
		}
	}
	for _, need := range []string{"cup_size", "band_size", "waist_size", "hip_size"} {
		if !strings.Contains(stashGraphQLFields, need) {
			t.Errorf("查询里缺 %q，三围兜底和罩杯标签都要靠它", need)
		}
	}
}

// JavStash 页面上显示的「34E-24-35」就是这四项扁平字段拼出来的。
func TestToPerformerFlatMeasurements(t *testing.T) {
	raw := `{
		"id": "2076161d-d539-433b-87d0-56e5f7e9a51e",
		"name": "田中ねね",
		"birth_date": "1996-01-01",
		"country": "JP",
		"height": 155,
		"cup_size": "E",
		"band_size": 34,
		"waist_size": 24,
		"hip_size": 35
	}`
	var p stashPerformerJSON
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	out := toPerformer("javstash", &p)
	if out.CupSize != "E" || out.Band != 34 || out.Waist != 24 || out.Hip != 35 {
		t.Fatalf("扁平三围没映射上: cup=%q band=%d waist=%d hip=%d", out.CupSize, out.Band, out.Waist, out.Hip)
	}
	// 进库后应该拼成 34E-24-35，并落一个「E罩杯」标签
	if got := bandCupMeasurements(out.Band, out.CupSize, out.Waist, out.Hip); got != "34E-24-35" {
		t.Errorf("拼接结果 = %q, want 34E-24-35", got)
	}
}

// Search 组出来的 GraphQL 语句必须和字段清单一致（防止有人只改了一边）。
func TestStashBoxSearchQueryShape(t *testing.T) {
	for _, term := range []string{"2076161d-d539-433b-87d0-56e5f7e9a51e", "田中ねね"} {
		c := &StashBoxClient{Source: "javstash", Endpoint: "http://127.0.0.1:1/graphql", APIKey: "x"}
		// 不走网络：直接看拼出来的字段清单是否会进查询（Search 内部用同一常量）
		q, _ := c.queryFor(term)
		if strings.Contains(q, "measurements") || strings.Contains(q, "weight") {
			t.Errorf("查询里带了已删除的字段: %s", q)
		}
		if !strings.Contains(q, "band_size") {
			t.Errorf("查询里缺 band_size: %s", q)
		}
	}
}
