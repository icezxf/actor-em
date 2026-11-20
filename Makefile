.PHONY: build run scan dict profiles fmt vet test

build:
	go build -o bin/actorsync ./cmd/actorsync

# 本地跑：需要先生成 cmd/actorsync/config.local.go
run:
	go run -tags local ./cmd/actorsync $(ARGS)

# 从 alist 的 nfo 补充演员名到 actors.txt（目录取自 config.local.go 的 NFODir）
# 主清单已经是字典里的全部演员，这里只是手工补充
scan:
	go run -tags local ./cmd/actorsync --scan-nfo $(ARGS)

# 重建中文字典 actor_dict.csv（amane + MDCx 合并，4 列）
# 两个源都没变会直接跳过；--force 强制重下，--dry-run 只统计
dict:
	python3 scripts/build_base_db.py $(ARGS)

# 把 MDCx 的 sqlite 折成扁平 CSV，喂给 MDCX_CSV
profiles:
	python3 scripts/export_profiles.py -o /tmp/mdcx_profiles.csv $(ARGS)

fmt:
	gofmt -w ./cmd ./internal

vet:
	go vet ./...

test:
	go test ./...
