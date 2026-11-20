# syntax=docker/dockerfile:1
# ---- 构建阶段（官方镜像，严禁打入本地 .toolchain）----
FROM --platform=$BUILDPLATFORM golang:1.26.11-alpine3.24 AS build
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server \
    && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/migrator ./cmd/migrator

# ---- 运行阶段（alpine 自带 busybox wget，供 compose healthcheck 使用）----
FROM alpine:3.24.4
RUN apk upgrade --no-cache \
    && apk add --no-cache 'zlib>=1.4-r0' ca-certificates tzdata \
    && adduser -D -u 10001 yreader
RUN mkdir -p /var/lib/yreader/storage && chown -R yreader:yreader /var/lib/yreader
COPY --from=build /out/server /server
COPY --from=build /out/migrator /migrator
EXPOSE 8080
USER yreader
ENTRYPOINT ["/server"]
