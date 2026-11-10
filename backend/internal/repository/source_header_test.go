//go:build integration

package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"yreader/backend/internal/pkg/crypto"
	"yreader/backend/internal/repository/testutil"
)

// installTestKeyring 装配进程级测试密钥环，并在测试结束后清空。
func installTestKeyring(t *testing.T, kids ...string) *crypto.Keyring {
	t.Helper()
	keys := make(map[string][]byte, len(kids))
	for i, kid := range kids {
		keys[kid] = bytes.Repeat([]byte{byte('K' + i)}, crypto.KeySize)
	}
	current := kids[len(kids)-1]
	ring, err := crypto.NewKeyring(current, keys)
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	crypto.SetDefaultKeyring(ring)
	t.Cleanup(func() { crypto.SetDefaultKeyring(nil) })
	return ring
}

// rawHeader 直接读库，绕过 Repository 的透明解密。
func rawHeader(t *testing.T, q Querier, id uuid.UUID) (raw string, hasEnvelope bool, hasCookieKey bool) {
	t.Helper()
	err := q.QueryRow(context.Background(),
		`SELECT header::text, header ? '__enc', header ? 'Cookie' FROM book_sources WHERE id = $1`, id).
		Scan(&raw, &hasEnvelope, &hasCookieKey)
	if err != nil {
		t.Fatalf("raw header: %v", err)
	}
	return raw, hasEnvelope, hasCookieKey
}

func TestSourceHeaderEncryptedAtRest(t *testing.T) {
	pool := testutil.TestPool(t)
	tx := testutil.BeginTx(t, pool)
	userID := mustSeedUser(t, tx)
	installTestKeyring(t, "k1")
	repo := NewSourceRepository()
	ctx := context.Background()

	src := &BookSource{
		UserID:         userID,
		BookSourceURL:  "https://header.example/at-rest",
		BookSourceName: "加密落库",
		Enabled:        true,
		Header:         json.RawMessage(`{"Cookie":"session=abc","User-Agent":"yreader"}`),
		Rules:          json.RawMessage(`{}`),
	}
	if err := repo.Create(ctx, tx, src); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if src.ID == uuid.Nil {
		t.Fatal("Create 未预生成/回填 id（AAD 依赖 id）")
	}

	raw, hasEnvelope, hasCookieKey := rawHeader(t, tx, src.ID)
	if !hasEnvelope {
		t.Fatalf("DB header 缺少 __enc 信封: %s", raw)
	}
	if hasCookieKey {
		t.Fatalf("DB header 仍以明文键 Cookie 存储: %s", raw)
	}
	var stored struct {
		Enc string `json:"__enc"`
	}
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		t.Fatalf("DB header 不是 JSON 对象: %v", err)
	}
	if !bytes.HasPrefix([]byte(stored.Enc), []byte("v1:k1:")) {
		t.Fatalf("信封格式不符: %s", stored.Enc)
	}

	got, err := repo.Get(ctx, tx, userID, src.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	var header map[string]string
	if err := json.Unmarshal(got.Header, &header); err != nil {
		t.Fatalf("解密后的 header 不是合法 JSON: %v (%s)", err, got.Header)
	}
	if header["Cookie"] != "session=abc" || header["User-Agent"] != "yreader" {
		t.Fatalf("透明解密结果不符: %v", header)
	}
}

func TestSourceHeaderLegacyPlaintextReadable(t *testing.T) {
	pool := testutil.TestPool(t)
	tx := testutil.BeginTx(t, pool)
	userID := mustSeedUser(t, tx)
	installTestKeyring(t, "k1")
	repo := NewSourceRepository()
	ctx := context.Background()

	id := uuid.New()
	if _, err := tx.Exec(ctx, `
		INSERT INTO book_sources (id, user_id, book_source_url, book_source_name, enabled, header, rules)
		VALUES ($1,$2,$3,$4,true,$5,'{}'::jsonb)`,
		id, userID, "https://header.example/legacy", "历史明文", `{"Cookie":"legacy=1"}`); err != nil {
		t.Fatalf("seed legacy plaintext: %v", err)
	}

	got, err := repo.Get(ctx, tx, userID, id)
	if err != nil {
		t.Fatalf("Get legacy: %v", err)
	}
	var header map[string]string
	if err := json.Unmarshal(got.Header, &header); err != nil {
		t.Fatalf("legacy header 解析失败: %v", err)
	}
	if header["Cookie"] != "legacy=1" {
		t.Fatalf("迁移前历史明文应原样返回，got %v", header)
	}
}

// TestSourceHeaderNonObjectRejected 覆盖历史脏数据：header 是合法 JSON 却不是对象
// （schema 未强制 object）。透明语义要求显式报错，而不是把 [1,2,3] 原样放行。
func TestSourceHeaderNonObjectRejected(t *testing.T) {
	pool := testutil.TestPool(t)
	tx := testutil.BeginTx(t, pool)
	userID := mustSeedUser(t, tx)
	repo := NewSourceRepository()
	ctx := context.Background()

	id := uuid.New()
	if _, err := tx.Exec(ctx, `
		INSERT INTO book_sources (id, user_id, book_source_url, book_source_name, enabled, header, rules)
		VALUES ($1,$2,$3,$4,true,'[1,2,3]'::jsonb,'{}'::jsonb)`,
		id, userID, "https://header.example/non-object", "非对象 header"); err != nil {
		t.Fatalf("seed non-object header: %v", err)
	}

	_, err := repo.Get(ctx, tx, userID, id)
	if err == nil {
		t.Fatal("非对象 header 必须显式报错，不得静默放行")
	}
	if !errors.Is(err, crypto.ErrMalformed) {
		t.Fatalf("want ErrMalformed, got %v", err)
	}
}

func TestSourceHeaderUnknownKIDFailsLoudly(t *testing.T) {
	pool := testutil.TestPool(t)
	tx := testutil.BeginTx(t, pool)
	userID := mustSeedUser(t, tx)
	installTestKeyring(t, "k2") // 只有 k2，密文却是 k1 写的
	repo := NewSourceRepository()
	ctx := context.Background()

	oldRing, err := crypto.NewKeyring("k1", map[string][]byte{"k1": bytes.Repeat([]byte{'X'}, crypto.KeySize)})
	if err != nil {
		t.Fatalf("old ring: %v", err)
	}
	id := uuid.New()
	sealed, err := oldRing.Seal([]byte(`{"Cookie":"k1=1"}`), crypto.AAD(userID, id))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO book_sources (id, user_id, book_source_url, book_source_name, enabled, header, rules)
		VALUES ($1,$2,$3,$4,true,$5,'{}'::jsonb)`,
		id, userID, "https://header.example/unknown-kid", "未知kid",
		`{"__enc":"`+sealed+`"}`); err != nil {
		t.Fatalf("seed sealed: %v", err)
	}

	_, err = repo.Get(ctx, tx, userID, id)
	if err == nil {
		t.Fatal("未知 kid 必须显式报错，绝不静默回退明文")
	}
	if !errors.Is(err, crypto.ErrUnknownKID) {
		t.Fatalf("want ErrUnknownKID, got %v", err)
	}
}

// seedCommittedUser 创建已提交的用户：迁移工具走独立连接（pool），
// 必须能看到测试写入的书源行，因此不能复用未提交的 BeginTx。
func seedCommittedUser(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	tx := testutil.BeginTx(t, pool)
	userID := mustSeedUser(t, tx)
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatalf("commit seed user: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM book_sources WHERE user_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
	})
	return userID
}

func seedPlainHeaderSource(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID, url, header string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO book_sources (id, user_id, book_source_url, book_source_name, enabled, header, rules)
		VALUES ($1,$2,$3,$4,true,$5,'{}'::jsonb)`,
		id, userID, url, "待迁移", header); err != nil {
		t.Fatalf("seed plain header: %v", err)
	}
	return id
}

func TestMigrateSourceHeadersIdempotent(t *testing.T) {
	pool := testutil.TestPool(t)
	userID := seedCommittedUser(t, pool)
	ring := installTestKeyring(t, "k1")
	ctx := context.Background()

	ids := make([]uuid.UUID, 0, 3)
	for i := 0; i < 3; i++ {
		ids = append(ids, seedPlainHeaderSource(t, pool, userID,
			fmt.Sprintf("https://header.example/migrate/%d", i),
			fmt.Sprintf(`{"Cookie":"c=%d"}`, i)))
	}

	first, err := MigrateSourceHeaders(ctx, pool, ring, 2, false, nil)
	if err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	if first < 3 {
		t.Fatalf("first migrate = %d, want >= 3", first)
	}

	for _, id := range ids {
		raw, hasEnvelope, hasCookieKey := rawHeader(t, pool, id)
		if !hasEnvelope || hasCookieKey {
			t.Fatalf("row %s 未正确加密: env=%v cookie=%v raw=%s", id, hasEnvelope, hasCookieKey, raw)
		}
	}

	second, err := MigrateSourceHeaders(ctx, pool, ring, 2, false, nil)
	if err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if second != 0 {
		t.Fatalf("second migrate = %d, want 0 (幂等)", second)
	}
}

func TestMigrateSourceHeadersDryRunAndResume(t *testing.T) {
	pool := testutil.TestPool(t)
	userID := seedCommittedUser(t, pool)
	ring := installTestKeyring(t, "k1")
	ctx := context.Background()

	ids := make([]uuid.UUID, 0, 4)
	for i := 0; i < 4; i++ {
		ids = append(ids, seedPlainHeaderSource(t, pool, userID,
			fmt.Sprintf("https://header.example/resume/%d", i),
			`{"Authorization":"Bearer t"}`))
	}

	dry, err := MigrateSourceHeaders(ctx, pool, ring, 1000, true, nil)
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if dry != 4 {
		t.Fatalf("dry-run = %d, want 4", dry)
	}
	for _, id := range ids {
		if _, hasEnvelope, _ := rawHeader(t, pool, id); hasEnvelope {
			t.Fatalf("dry-run 不应写库: row %s", id)
		}
	}

	// 手工加密第 1 行，模拟「迁移中途失败后已提交的批次」。
	sealed, err := ring.Seal([]byte(`{"Authorization":"Bearer t"}`), crypto.AAD(userID, ids[0]))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE book_sources SET header = $2 WHERE id = $1`,
		ids[0], `{"__enc":"`+sealed+`"}`); err != nil {
		t.Fatalf("pre-migrate: %v", err)
	}

	resumed, err := MigrateSourceHeaders(ctx, pool, ring, 1, false, nil)
	if err != nil {
		t.Fatalf("resume migrate: %v", err)
	}
	if resumed < 3 {
		t.Fatalf("resume = %d, want >= 3 (只处理剩余行)", resumed)
	}
	for _, id := range ids {
		if _, hasEnvelope, _ := rawHeader(t, pool, id); !hasEnvelope {
			t.Fatalf("row %s 未被加密", id)
		}
	}
}
