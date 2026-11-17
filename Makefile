.PHONY: build run fmt vet

build:
	go build -o bin/actorsync ./cmd/actorsync

# 本地跑：需要先生成 cmd/actorsync/config.local.go
run:
	go run -tags local ./cmd/actorsync $(ARGS)

fmt:
	gofmt -w ./cmd ./internal

vet:
	go vet ./...
