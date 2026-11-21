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
