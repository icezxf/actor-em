# 常驻交互服务用的镜像。批量同步直接走 GitHub Actions，不需要它。
#
#   docker build -t actor-em .
#   docker run -d --name actor-em --restart unless-stopped \
#     --env-file secrets.env \
#     -v "$PWD/actors.jsonl:/app/actors.jsonl" \
#     -v "$PWD/actor_dict.csv:/app/actor_dict.csv:ro" \
#     -v "$PWD/data:/app/data" \
#     actor-em
#
# 挂载说明：
#   actors.jsonl   真源，bot 会往里写（不挂就丢数据）
#   data/          选图状态、手工补充字典，bot 也会写
#   actor_dict.csv 只读，字典变了重启容器才会生效（启动时只加载一次）
#
# 注意：不要挂 actors.json —— 那是从 jsonl 派生的产物，挂了也不会被读。

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/actorsync ./cmd/actorsync

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 1000 actor
WORKDIR /app
COPY --from=build /out/actorsync /app/actorsync
COPY data /app/data
USER actor
ENTRYPOINT ["/app/actorsync"]
CMD ["--tg-serve"]
