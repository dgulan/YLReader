#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

COMPOSE_FILE="docker-compose.prod.yml"
ENV_FILE=".env"
ENV_EXAMPLE=".env.example"
BUILD_MODE=0

usage() {
  cat <<'EOF'
用法: ./scripts/deploy.sh [--build]

  （无参数）  拉取 GitHub Actions 预构建镜像并启动，无需 Go/Node 编译器。
  --build     使用本地源码构建镜像后启动（可配合 .env 中的 ALPINE_MIRROR /
              GOPROXY / NPM_REGISTRY 使用国内镜像加速）。
EOF
}

for arg in "$@"; do
  case "$arg" in
    --build) BUILD_MODE=1 ;;
    -h|--help) usage; exit 0 ;;
    *)
      echo "错误: 未知参数 $arg" >&2
      usage >&2
      exit 1
      ;;
  esac
done

set_env_var() {
  local key="$1"
  local val="$2"
  local file="$3"
  local tmp_file
  tmp_file="$(mktemp)"
  awk -v k="$key" -v v="$val" '
    BEGIN { found = 0 }
    index($0, k "=") == 1 {
      print k "=" v
      found = 1
      next
    }
    { print }
    END {
      if (!found) {
        print k "=" v
      }
    }
  ' "$file" > "$tmp_file"
  mv "$tmp_file" "$file"
}

if [ ! -f "$ENV_FILE" ]; then
  if ! command -v openssl >/dev/null 2>&1; then
    echo "错误: 未找到 openssl，无法生成随机密钥。" >&2
    exit 1
  fi
  if [ ! -f "$ENV_EXAMPLE" ]; then
    echo "错误: 未找到 $ENV_EXAMPLE。" >&2
    exit 1
  fi

  cp "$ENV_EXAMPLE" "$ENV_FILE"

  jwt_secret="$(openssl rand -hex 32)"
  header_key="$(openssl rand -base64 32 | tr -d '\r\n')"

  set_env_var "JWT_SECRET" "$jwt_secret" "$ENV_FILE"
  set_env_var "YREADER_HEADER_KEY_primary" "$header_key" "$ENV_FILE"
  chmod 600 "$ENV_FILE"

  cat <<'EOF'
已从 .env.example 初始化 .env，并已通过 openssl 自动生成随机 JWT_SECRET 与 YREADER_HEADER_KEY_primary。
请先编辑 .env（例如执行 nano .env）修改至少以下密码与账户配置后，再次执行 ./scripts/deploy.sh：
  - POSTGRES_PASSWORD
  - REDIS_PASSWORD
  - SEED_USERNAME / SEED_PASSWORD（首次初始化管理员账号）
可选配置：
  - YLREADER_BIND_IP（默认 127.0.0.1；局域网直连请改为 0.0.0.0）
  - YLREADER_IMAGE_TAG（预构建镜像版本，默认 v1.1.3）
  - ALPINE_MIRROR / GOPROXY / NPM_REGISTRY（仅 --build 源码构建时生效，国内网络建议取消注释）
EOF
  exit 0
fi

if ! docker compose version >/dev/null 2>&1; then
  echo "错误: 未检测到 docker compose 插件，请先安装 Docker Compose v2。" >&2
  exit 1
fi

set -a
# shellcheck disable=SC1090
. "$ROOT_DIR/$ENV_FILE"
set +a

for required_var in JWT_SECRET YREADER_HEADER_KEY_primary POSTGRES_PASSWORD REDIS_PASSWORD; do
  if [ -z "${!required_var:-}" ]; then
    echo "错误: $ENV_FILE 中 $required_var 不能为空，请补充后重试。" >&2
    exit 1
  fi
done

mkdir -p data/backups

docker compose -f "$COMPOSE_FILE" config --quiet

image_tag="${YLREADER_IMAGE_TAG:-v1.1.3}"

if [ "$BUILD_MODE" -eq 1 ]; then
  echo "==> 源码构建模式：开始构建本地镜像（首次构建较慢，可在 .env 中配置 ALPINE_MIRROR / GOPROXY / NPM_REGISTRY 加速）..."
  docker compose -f "$COMPOSE_FILE" build
else
  echo "==> 预构建镜像模式：拉取 ghcr.io/dgulan/ylreader-{backend,frontend,worker}:${image_tag} ..."
  if ! docker compose -f "$COMPOSE_FILE" pull; then
    echo "错误: 预构建镜像拉取失败，请确认网络可达 ghcr.io 且镜像标签 ${image_tag} 已发布。" >&2
    echo "      若提示 denied，请先 docker login ghcr.io，或将镜像包可见性调整为 public。" >&2
    echo "      也可改用源码构建：./scripts/deploy.sh --build" >&2
    exit 1
  fi
fi

docker compose -f "$COMPOSE_FILE" up -d --wait postgres redis

db_name="${POSTGRES_DB:-yreader}"
docker compose -f "$COMPOSE_FILE" run --rm --no-deps --entrypoint /migrator app \
  schema up --production-confirm-db="$db_name"

if [ "$BUILD_MODE" -eq 1 ]; then
  docker compose -f "$COMPOSE_FILE" up -d --build
  mode_desc="源码构建 (--build)"
else
  docker compose -f "$COMPOSE_FILE" up -d
  mode_desc="预构建镜像 (${image_tag})"
fi

bind_ip="${YLREADER_BIND_IP:-127.0.0.1}"
http_port="${YLREADER_HTTP_PORT:-8080}"
echo "YLReader 已在后台启动（${mode_desc}），当前监听地址: http://${bind_ip}:${http_port}"
docker compose -f "$COMPOSE_FILE" ps
