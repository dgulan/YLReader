//go:build integration

package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"yreader/backend/internal/pkg/crypto"
)

// installAPITestKeyring 为 API 集成测试装配临时密钥环（测试结束清空）。
func installAPITestKeyring(t *testing.T) *crypto.Keyring {
	t.Helper()
	ring, err := crypto.NewKeyring("k1", map[string][]byte{"k1": bytes.Repeat([]byte{'Z'}, crypto.KeySize)})
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	crypto.SetDefaultKeyring(ring)
	t.Cleanup(func() { crypto.SetDefaultKeyring(nil) })
	return ring
}

func TestSourceHeaderTransparentViaAPI(t *testing.T) {
	installAPITestKeyring(t)
	ts, pool := newTestServer(t, nil)
	userID, user, pass := seedUser(t, pool)
	base := ts.URL + "/api/v1"
	token := login(t, base, user, pass)
	ctx := context.Background()

	body, _ := json.Marshal(map[string]any{
		"book_source_url":  "https://header.example/api-transparent",
		"book_source_name": "API 透明加解密",
		"header":           map[string]any{"Cookie": "s=1", "User-Agent": "yreader"},
	})
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM book_sources WHERE user_id = $1`, userID)
	})

	status, resp := doJSON(t, http.MethodPost, base+"/sources", token, body)
	if status != http.StatusCreated {
		t.Fatalf("POST /sources status = %d, body = %s", status, resp)
	}
	var created struct {
		ID     uuid.UUID         `json:"id"`
		Header map[string]string `json:"header"`
	}
	if err := json.Unmarshal(resp, &created); err != nil {
		t.Fatalf("decode created: %v (%s)", err, resp)
	}
	if created.ID == uuid.Nil {
		t.Fatal("响应缺少 id")
	}
	// 客户端视角：响应必须是明文。
	if created.Header["Cookie"] != "s=1" {
		t.Fatalf("POST 响应 header 非明文: %v", created.Header)
	}

	var hasEnvelope, hasCookie bool
	if err := pool.QueryRow(ctx,
		`SELECT header ? '__enc', header ? 'Cookie' FROM book_sources WHERE id = $1`, created.ID).
		Scan(&hasEnvelope, &hasCookie); err != nil {
		t.Fatalf("raw header: %v", err)
	}
	if !hasEnvelope {
		t.Fatal("DB 中 header 缺少 __enc 信封")
	}
	if hasCookie {
		t.Fatal("DB 中 header 仍含明文键 Cookie")
	}

	status, resp = doJSON(t, http.MethodGet, base+"/sources/"+created.ID.String(), token, nil)
	if status != http.StatusOK {
		t.Fatalf("GET /sources/{id} status = %d, body = %s", status, resp)
	}
	var got struct {
		Header map[string]string `json:"header"`
	}
	if err := json.Unmarshal(resp, &got); err != nil {
		t.Fatalf("decode get: %v", err)
	}
	if got.Header["Cookie"] != "s=1" || got.Header["User-Agent"] != "yreader" {
		t.Fatalf("GET 未透明解密: %v", got.Header)
	}

	// 列表接口同样返回明文。
	status, resp = doJSON(t, http.MethodGet, base+"/sources?limit=100", token, nil)
	if status != http.StatusOK {
		t.Fatalf("GET /sources status = %d, body = %s", status, resp)
	}
	var page struct {
		Items []struct {
			ID     uuid.UUID         `json:"id"`
			Header map[string]string `json:"header"`
		} `json:"items"`
	}
	if err := json.Unmarshal(resp, &page); err != nil {
		t.Fatalf("decode page: %v", err)
	}
	found := false
	for _, it := range page.Items {
		if it.ID == created.ID {
			found = true
			if it.Header["Cookie"] != "s=1" {
				t.Fatalf("列表 header 未解密: %v", it.Header)
			}
		}
	}
	if !found {
		t.Fatal("列表未返回新建书源")
	}
}

func TestSourceImportDryRunDoesNotSeal(t *testing.T) {
	installAPITestKeyring(t)
	ts, pool := newTestServer(t, nil)
	userID, user, pass := seedUser(t, pool)
	base := ts.URL + "/api/v1"
	token := login(t, base, user, pass)
	ctx := context.Background()

	const url = "https://header.example/import-dryrun"
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM book_sources WHERE user_id = $1`, userID)
	})

	payload, _ := json.Marshal([]map[string]any{{
		"bookSourceUrl":  url,
		"bookSourceName": "导入 dry_run",
		"header":         `{"Cookie":"dry=1"}`,
	}})
	body, _ := json.Marshal(map[string]any{
		"data":    string(payload),
		"format":  "legado_json",
		"dry_run": true,
	})

	status, resp := doJSON(t, http.MethodPost, base+"/sources/import", token, body)
	if status != http.StatusOK {
		t.Fatalf("import dry_run status = %d, body = %s", status, resp)
	}
	var result struct {
		DryRun   bool `json:"dry_run"`
		Imported int  `json:"imported"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("decode import result: %v", err)
	}
	if !result.DryRun || result.Imported != 1 {
		t.Fatalf("dry_run 结果不符: %+v", result)
	}

	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM book_sources WHERE user_id = $1 AND book_source_url = $2`, userID, url).
		Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("dry_run=true 不得落库，实际 %d 行", count)
	}
}

// TestSourceImportNonDryRunSeals 证明批量导入（dry_run=false）也走加密：
// 库内只能是 {"__enc": ...}，API 读取必须透明还原明文。
func TestSourceImportNonDryRunSeals(t *testing.T) {
	installAPITestKeyring(t)
	ts, pool := newTestServer(t, nil)
	userID, user, pass := seedUser(t, pool)
	base := ts.URL + "/api/v1"
	token := login(t, base, user, pass)
	ctx := context.Background()

	const url = "https://header.example/import-real"
	const secret = "session=secret_token_123"
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM book_sources WHERE user_id = $1`, userID)
	})

	payload, _ := json.Marshal([]map[string]any{{
		"bookSourceUrl":  url,
		"bookSourceName": "导入 non-dry-run",
		"header":         `{"Cookie":"` + secret + `","User-Agent":"yreader"}`,
	}})
	body, _ := json.Marshal(map[string]any{
		"data":    string(payload),
		"format":  "legado_json",
		"dry_run": false,
	})

	status, resp := doJSON(t, http.MethodPost, base+"/sources/import", token, body)
	if status != http.StatusOK {
		t.Fatalf("import status = %d, body = %s", status, resp)
	}
	var result struct {
		DryRun   bool `json:"dry_run"`
		Imported int  `json:"imported"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("decode import result: %v (body %s)", err, resp)
	}
	if result.DryRun || result.Imported < 1 {
		t.Fatalf("导入结果不符: %+v", result)
	}

	var id uuid.UUID
	if err := pool.QueryRow(ctx,
		`SELECT id FROM book_sources WHERE user_id = $1 AND book_source_url = $2`, userID, url).
		Scan(&id); err != nil {
		t.Fatalf("查询导入书源: %v", err)
	}

	// 原生 SQL 点查：绕过 Repository 透明解密。
	var hasEnvelope, hasCookie bool
	if err := pool.QueryRow(ctx,
		`SELECT header ? '__enc', header ? 'Cookie' FROM book_sources WHERE id = $1`, id).
		Scan(&hasEnvelope, &hasCookie); err != nil {
		t.Fatalf("raw header: %v", err)
	}
	if !hasEnvelope {
		t.Fatal("导入后 DB header 缺少 __enc 信封（明文落库）")
	}
	if hasCookie {
		t.Fatal("导入后 DB header 仍含明文键 Cookie")
	}

	status, resp = doJSON(t, http.MethodGet, base+"/sources/"+id.String(), token, nil)
	if status != http.StatusOK {
		t.Fatalf("GET /sources/{id} status = %d, body = %s", status, resp)
	}
	var got struct {
		Header map[string]string `json:"header"`
	}
	if err := json.Unmarshal(resp, &got); err != nil {
		t.Fatalf("decode get: %v", err)
	}
	if got.Header["Cookie"] != secret || got.Header["User-Agent"] != "yreader" {
		t.Fatalf("GET 未透明解密: %v", got.Header)
	}
}

// TestSourceExportTransparent 证明导出读路径同样透明：body 原始文本不含 __enc，
// 解析后 header.Cookie 为明文。
func TestSourceExportTransparent(t *testing.T) {
	installAPITestKeyring(t)
	ts, pool := newTestServer(t, nil)
	userID, user, pass := seedUser(t, pool)
	base := ts.URL + "/api/v1"
	token := login(t, base, user, pass)
	ctx := context.Background()

	const url = "https://header.example/export-transparent"
	const secret = "export_secret=xyz"
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM book_sources WHERE user_id = $1`, userID)
	})

	body, _ := json.Marshal(map[string]any{
		"book_source_url":  url,
		"book_source_name": "导出透明",
		"header":           map[string]any{"Cookie": secret},
	})
	status, resp := doJSON(t, http.MethodPost, base+"/sources", token, body)
	if status != http.StatusCreated {
		t.Fatalf("POST /sources status = %d, body = %s", status, resp)
	}

	status, resp = doJSON(t, http.MethodGet, base+"/sources/export", token, nil)
	if status != http.StatusOK {
		t.Fatalf("GET /sources/export status = %d, body = %s", status, resp)
	}
	if strings.Contains(string(resp), crypto.EnvelopeKey) {
		t.Fatalf("导出响应 body 泄漏信封键 %q: %s", crypto.EnvelopeKey, resp)
	}

	var items []map[string]any
	if err := json.Unmarshal(resp, &items); err != nil {
		t.Fatalf("导出结果不是 JSON 数组: %v (%s)", err, resp)
	}
	found := false
	for _, item := range items {
		if item["bookSourceUrl"] != url {
			continue
		}
		header, ok := item["header"].(map[string]any)
		if !ok {
			t.Fatalf("导出项 header 不是对象: %#v", item["header"])
		}
		if header["Cookie"] != secret {
			t.Fatalf("导出 header 未透明解密: %#v", header)
		}
		found = true
	}
	if !found {
		t.Fatalf("导出结果缺少被测书源 %s", url)
	}
}
