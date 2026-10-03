# syntax=docker/dockerfile:1
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# 一次编译两个二进制（网关 + 登录工具），容器内可直接跑登录。全部 -trimpath -s -w。
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/deveco2api-panel ./cmd/server \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/deveco2api-login ./cmd/login

FROM alpine:3.20
# ca-certificates：华为 API 走 HTTPS；tzdata：日志与 jwt 到期时间按本地时区看
RUN apk add --no-cache ca-certificates tzdata wget \
 && adduser -D -u 10001 app
WORKDIR /app
COPY --from=build /out/deveco2api-panel /app/deveco2api-panel
COPY --from=build /out/deveco2api-login /app/deveco2api-login
# 镜像不带真实配置：落 example 作为默认（生产由挂载卷 /app/config.toml 覆盖）
COPY config.example.toml /app/config.toml
RUN chown -R app:app /app
USER app
EXPOSE 10102
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s \
  CMD wget -qO- http://127.0.0.1:10102/health || exit 1
ENTRYPOINT ["/app/deveco2api-panel", "-config", "/app/config.toml"]
