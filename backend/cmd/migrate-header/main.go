// Package main 是 v1.4（ADR-010）书源 header 信封加密的分批迁移工具。
//
// 行为（实现在 repository.MigrateSourceHeaders，便于集成测试）：
//   - 以 id 游标分批（默认 1000 行/批），每批一个独立事务；
//   - 只处理「非空且尚未加密」的 header，因此重跑天然幂等，最终扫到 0 行；
//   - 中途失败可直接重跑：已完成的行不会再被选中（断点续跑）；
//   - -dry-run 只统计待迁移行数，不写库。
//
// 用法：
//
//	DATABASE_URL=... YREADER_HEADER_KEY_k1=<base64 32B> go run ./cmd/migrate-header [-batch 1000] [-dry-run]
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"yreader/backend/internal/pkg/crypto"
	"yreader/backend/internal/repository"
)

func main() {
	batch := flag.Int("batch", 1000, "rows per batch (each batch runs in its own transaction)")
	dryRun := flag.Bool("dry-run", false, "only count rows that need migration, do not write")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	dsn := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dsn == "" {
		log.Error("DATABASE_URL is required")
		os.Exit(1)
	}
	ring, err := crypto.LoadKeyringFromEnv()
	if err != nil {
		log.Error("load header keyring (set YREADER_HEADER_KEY_<KID>)", "error", err)
		os.Exit(1)
	}
	crypto.SetDefaultKeyring(ring)

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Error("connect postgres", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	total, err := repository.MigrateSourceHeaders(ctx, pool, ring, *batch, *dryRun, func(migrated int, lastID uuid.UUID) {
		log.Info("batch migrated", "total", migrated, "cursor", lastID)
	})
	if err != nil {
		log.Error("migrate header", "migrated", total, "error", err)
		os.Exit(1)
	}
	log.Info("migrate header done", "migrated", total, "dry_run", *dryRun)
}
