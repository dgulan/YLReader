# syntax=docker/dockerfile:1
# ---- 构建阶段（官方镜像，严禁打入本地 .toolchain）----
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# ---- 运行阶段（alpine 自带 busybox wget，供 compose healthcheck 使用）----
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 yreader
COPY --from=build /out/server /server
EXPOSE 8080
USER yreader
ENTRYPOINT ["/server"]
