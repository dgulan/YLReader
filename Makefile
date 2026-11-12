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

.PHONY: build vet test lint migrate setup-test-db db-guard-migrate db-guard-setup-test-db test-integration frontend-install frontend-type-check frontend-build compose-config

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

# 静态数据库目标门禁：解析 DATABASE_URL / TEST_DATABASE_URL 的 host/port/dbname，
# 默认拒绝非本地测试目标。失败时 make 在进入 recipe 前即中止。
db-guard-migrate:
	@$(DB_GUARD) --mode migrate

db-guard-setup-test-db:
	@$(DB_GUARD) --mode setup-test-db

# 数据库迁移（DATABASE_URL 必填，防止静默连错默认库；目标须通过静态门禁）。
migrate: db-guard-migrate
	$(PSQL) "$$DATABASE_URL" -v ON_ERROR_STOP=1 -f docs/schema.sql

# 集成测试库（幂等）：先 DROP 再 CREATE，随后灌入权威 DDL。
# 需要 DATABASE_URL（维护库，仅允许 postgres）与 TEST_DATABASE_URL（仅 yreader_test）。
# 两连接串必须指向同一回环地址与端口；全部静态检查先于任何 DROP/CREATE 执行。
setup-test-db: db-guard-setup-test-db
	$(PSQL) "$$DATABASE_URL" -c "DROP DATABASE IF EXISTS yreader_test WITH (FORCE);"
	$(PSQL) "$$DATABASE_URL" -c "CREATE DATABASE yreader_test;"
	$(PSQL) "$$TEST_DATABASE_URL" -v ON_ERROR_STOP=1 -f docs/schema.sql

# PostgreSQL 集成测试（需先 setup-test-db，并 export TEST_DATABASE_URL）
test-integration:
	cd backend && $(GO) test -tags integration -v ./internal/repository/...

# 前端
frontend-install:
	cd frontend && npm install

frontend-type-check:
	cd frontend && npm run type-check

frontend-build:
	cd frontend && npm run build

# 容器编排校验（仅语法，不启动）
compose-config:
	docker compose config
