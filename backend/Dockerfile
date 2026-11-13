# syntax=docker/dockerfile:1
# ---- 构建阶段（官方镜像，严禁打入本地 .toolchain）----
FROM --platform=$BUILDPLATFORM golang:1.26.11-alpine3.24@sha256:ae52b5bc6ed045a9ed26ab03dc19327a802a9cc9aa8b27ed57f5c6b60579b0bb AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# ---- 运行阶段（alpine 自带 busybox wget，供 compose healthcheck 使用）----
FROM alpine:3.24.4@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf
RUN apk add --no-cache 'zlib>=1.4-r0' ca-certificates tzdata && adduser -D -u 10001 yreader
RUN mkdir -p /var/lib/yreader/storage && chown -R yreader:yreader /var/lib/yreader
COPY --from=build /out/server /server
EXPOSE 8080
USER yreader
ENTRYPOINT ["/server"]
