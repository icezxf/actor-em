package main

import (
	"context"
	"testing"

	"github.com/icezxf/actor-em/internal/actorsync"
)

type panicFetcher struct{}

func (panicFetcher) Fetch(context.Context, string) *actorsync.FetchResult {
	panic("模拟某个数据源解析崩溃")
}

func TestSafeFetchRecoversPanic(t *testing.T) {
	e := &actorsync.Engine{Fetcher: panicFetcher{}}
	res, panicked := safeFetch(e, context.Background(), "某人")
	if panicked == nil {
		t.Fatal("panic 应该被拦下并返回")
	}
	if res != nil {
		t.Fatalf("panic 时不该返回结果: %+v", res)
	}
}

// 手输一个字典里没有的名字时，--refresh 也要能把它带进本轮计划，
// 否则 BuildPlan 只遍历清单、这个名字会被静默忽略。
func TestMergeNamesAddsUnknown(t *testing.T) {
	got := mergeNames([]string{"三上悠亚", "波多野结衣"}, []string{"新垣结衣", "三上悠亚", " ", "新垣结衣"})
	if len(got) != 3 {
		t.Fatalf("got %v", got)
	}
	if got[2] != "新垣结衣" {
		t.Errorf("新名字应追加到尾部，得到 %v", got)
	}
	// 全都不在清单里 → 全部追加
	got = mergeNames(nil, []string{"a", "b"})
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("got %v", got)
	}
}
