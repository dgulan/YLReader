# YReader 统一构建/测试/Lint/迁移入口
# 约定：所有命令在仓库根目录执行。
# 工具链解耦：默认使用 PATH 中的 go/psql/gofmt；
#   本机可用 `make GO=./.toolchain/go/bin/go ...` 覆盖 go，`make PSQL="..."` 覆盖 psql。

GO ?= go
PSQL ?= psql
GOFMT ?= gofmt
# 静态数据库目标门禁（SAFE-01 / ADR-013 S3）：纯标准库 Python，不连库、不执行 SQL。
PYTHON ?= python3
DB_GUARD := $(PYTHON) scripts/check-db-target.py
COMPOSE_ENV_FILE ?= .env

.PHONY: build vet test lint migrate setup-test-db test-integration frontend-install frontend-type-check frontend-build frontend-test frontend-e2e frontend-api-generate compose-config security-go security-frontend

# 后端构建 / 静态检查 / 测试
build:
	cd backend && $(GO) build ./...

vet:
	cd backend && $(GO) vet ./...

test:
	cd backend && $(GO) test ./...

lint:
	@cd backend && out=$$($(GOFMT) -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	cd backend && $(GO) vet ./...

# 数据库迁移（DATABASE_URL 必填，防止静默连错默认库）。
# 门禁内联在同一条执行链首部（而非 prerequisite），失败即短路：
# `make -o` 只能跳过 prerequisite，无法跳过 recipe 内部的 `&&` 链。
migrate:
	@$(DB_GUARD) --mode migrate && \
	  cd backend && $(GO) run ./cmd/migrator schema up

# 集成测试库（幂等）：门禁 → DROP → CREATE → schema，四步同链，任一步失败即停止。
# 需要 DATABASE_URL（维护库，仅允许 postgres）与 TEST_DATABASE_URL（仅 yreader_test）；
# 两连接串必须指向同一回环地址与端口。
setup-test-db:
	@$(DB_GUARD) --mode setup-test-db && \
	  $(PSQL) "$$DATABASE_URL" -c "DROP DATABASE IF EXISTS yreader_test WITH (FORCE);" && \
	  $(PSQL) "$$DATABASE_URL" -c "CREATE DATABASE yreader_test;" && \
	  $(PSQL) "$$TEST_DATABASE_URL" -v ON_ERROR_STOP=1 -f docs/schema.sql

# 全量集成：串行测试包/测试，保留测试内部 goroutine 并发，减少计时断言的负载干扰。
# 使用已准备好的隔离 TEST_DATABASE_URL；setup-test-db 会重建库，不能用于现有数据。
test-integration:
	cd backend && $(GO) test -tags integration -p 1 -parallel 1 -count=1 -v ./...

# 前端
frontend-install:
	cd frontend && npm ci

frontend-type-check:
	cd frontend && npm run type-check

frontend-build:
	cd frontend && npm run build

# 容器编排校验（仅语法，不启动）
compose-config:
	docker compose --env-file "$(COMPOSE_ENV_FILE)" config --quiet

frontend-test:
	cd frontend && npm test

frontend-e2e:
	cd frontend && npm run test:e2e

frontend-api-generate:
	cd frontend && npm run generate:api

# 官方固定扫描器，使用非 JSON 退出码；模块匹配与实际调用路径均检查。
security-go:
	cd backend && $(GO) run golang.org/x/vuln/cmd/govulncheck@v1.9.0 -C cmd/server -scan module
	cd backend && $(GO) run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...

security-frontend:
	cd frontend && npm audit --package-lock-only
