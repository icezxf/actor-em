.PHONY: build run scan dict fmt vet

build:
	go build -o bin/actorsync ./cmd/actorsync

# 本地跑：需要先生成 cmd/actorsync/config.local.go
run:
	go run -tags local ./cmd/actorsync $(ARGS)

# 从 alist 的 nfo 生成 actors.txt（目录取自 config.local.go 的 NFODir）
scan:
	go run -tags local ./cmd/actorsync --scan-nfo $(ARGS)

# 刷新中文名字典
dict:
	python3 scripts/gen_dict.py

fmt:
	gofmt -w ./cmd ./internal

vet:
	go vet ./...
