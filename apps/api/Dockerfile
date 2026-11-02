# Dockerfile 以多阶段构建 API 与 Worker 两个二进制，运行镜像内置 ffmpeg 并以非 root 用户运行。
FROM golang:1.22-alpine AS builder
WORKDIR /src

# 先复制依赖清单，利用层缓存减少重复下载。
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/pulsefeed-api ./cmd/api \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/pulsefeed-worker ./cmd/worker

FROM alpine:3.20
# ffmpeg 供上传接口探测时长/编码；ca-certificates 供容器内 HTTPS。
RUN apk add --no-cache ffmpeg ca-certificates

WORKDIR /app
COPY --from=builder /out/pulsefeed-api /out/pulsefeed-worker /app/
COPY configs /app/configs

RUN mkdir -p /app/uploads \
 && adduser -D -u 10001 pulsefeed \
 && chown -R pulsefeed:pulsefeed /app
USER pulsefeed

EXPOSE 8090 9092
CMD ["/app/pulsefeed-api", "-config", "/app/configs/config.docker.yaml"]
