#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

COMPOSE_FILE="docker-compose.prod.yml"
ENV_FILE=".env"
ENV_EXAMPLE=".env.example"

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
请先编辑 .env 修改至少以下密码与账户配置后，再次执行 ./scripts/deploy.sh：
  - POSTGRES_PASSWORD
  - REDIS_PASSWORD
  - SEED_USERNAME / SEED_PASSWORD（首次初始化管理员账号）
可选配置：
  - YLREADER_BIND_IP（默认 127.0.0.1；局域网直连请改为 0.0.0.0）
  - GOPROXY / NPM_REGISTRY（国内网络建议取消对应镜像源注释）
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
docker compose -f "$COMPOSE_FILE" build
docker compose -f "$COMPOSE_FILE" up -d --wait postgres redis

db_name="${POSTGRES_DB:-yreader}"
docker compose -f "$COMPOSE_FILE" run --rm --no-deps --entrypoint /migrator app \
  schema up --production-confirm-db="$db_name"

docker compose -f "$COMPOSE_FILE" up -d

bind_ip="${YLREADER_BIND_IP:-127.0.0.1}"
http_port="${YLREADER_HTTP_PORT:-8080}"
echo "YLReader 已在后台启动，当前监听地址: http://${bind_ip}:${http_port}"
docker compose -f "$COMPOSE_FILE" ps
