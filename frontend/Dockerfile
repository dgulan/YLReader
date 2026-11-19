# ---- 构建阶段 ----
FROM --platform=$BUILDPLATFORM node:22-alpine3.24@sha256:1314fb30ec37592d5845e5a160d522c0e680972aa534549940b8d73bde88481a AS build
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
COPY . .
RUN npm run build

# ---- 运行阶段（最小化 Alpine 3.24.4 + 核心 Nginx，去除多余模块与 curl/libxslt 等 50+ 额外包）----
FROM alpine:3.24.4@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf
RUN apk upgrade --no-cache \
    && apk add --no-cache nginx 'zlib>=1.4-r0' \
    && rm -rf /etc/nginx/http.d/default.conf \
    && mkdir -p /etc/nginx/conf.d /var/cache/nginx /var/run /usr/share/nginx/html \
    && chown -R nginx:nginx /var/cache/nginx
COPY nginx-main.conf /etc/nginx/nginx.conf
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /app/dist /usr/share/nginx/html
EXPOSE 80
CMD ["nginx", "-g", "daemon off;"]
