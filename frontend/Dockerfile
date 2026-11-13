# syntax=docker/dockerfile:1
# ---- 构建阶段 ----
FROM --platform=$BUILDPLATFORM node:22-alpine3.24@sha256:1314fb30ec37592d5845e5a160d522c0e680972aa534549940b8d73bde88481a AS build
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
COPY . .
RUN npm run build

# ---- 运行阶段（静态托管）----
FROM nginx:1.30.6-alpine3.24@sha256:583b7f2fce1cdb8da5ad5c39ebd3c33b98b87d493190e548bdee1fa8c619c311
RUN apk add --no-cache 'zlib>=1.4-r0' 'libxslt>=1.1.43-r4'
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /app/dist /usr/share/nginx/html
EXPOSE 80
