# 常驻交互服务用的镜像。批量同步直接走 GitHub Actions，不需要它。
#
#   docker build -t actor-em .
#   docker run -d --name actor-em --restart unless-stopped \
#     --env-file secrets.env \
#     -v "$PWD/actors.json:/app/actors.json" \
#     -v "$PWD/data:/app/data" \
#     actor-em
#
# 挂载 actors.json 和 data/ 是必须的：bot 会往这两个地方写。
# 反过来，宿主机改了 actors.json 需要重启容器才会被读到（启动时只加载一次）。

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
