package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExplicitTarget(t *testing.T) {
	for _, raw := range []string{
		"host=127.0.0.1 port=5432 dbname=yreader_test password='a b!@'",
		"postgres://u:p@127.0.0.1:5432/yreader_test",
		"postgres://u:p@db.internal:5432/yreader_prod?sslmode=require",
	} {
		if !explicitTarget(raw) {
			t.Fatalf("valid target rejected: %q", raw)
		}
	}
	for _, raw := range []string{
		"dbname=yreader_test password='host=x port=1'",
		"host=127.0.0.1 port=5432 dbname=yreader_test host=other",
		"postgres://localhost/yreader_test",
		"postgres://127.0.0.1:5432/",
		"postgres://127.0.0.1:5432,127.0.0.1:5433/yreader_test",
		"postgres://127.0.0.1:5432/yreader_test?service=prod",
		"host=localhost port=5432 dbname=yreader_test service=x",
	} {
		if explicitTarget(raw) {
			t.Fatalf("ambiguous target accepted: %q", raw)
		}
	}
}

func TestAuthorizeSchemaUpTarget(t *testing.T) {
	noEnv := func(string) (string, bool) { return "", false }

	// Default test-only mode preserves test allowlist and blocks non-test DBs.
	for _, raw := range []string{
		"postgres://u:p@127.0.0.1:5432/yreader_test",
		"host=localhost port=5432 dbname=yreader_a1_iso_mig1",
	} {
		if _, err := authorizeSchemaUpTarget(raw, "", noEnv); err != nil {
			t.Fatalf("expected local test target %q to succeed: %v", raw, err)
		}
	}
	for _, raw := range []string{
		"postgres://u:p@127.0.0.1:5432/yreader",
		"postgres://u:p@postgres:5432/yreader",
		"postgres://u:p@10.0.0.5:5432/yreader_test",
	} {
		if _, err := authorizeSchemaUpTarget(raw, "", noEnv); err == nil {
			t.Fatalf("expected non-test target %q to fail without confirmation", raw)
		}
	}

	// Production confirmation mode allows remote/production targets ONLY on exact DB match.
	cfg, err := authorizeSchemaUpTarget("postgres://u:p@postgres:5432/yreader_prod?sslmode=require", "yreader_prod", noEnv)
	if err != nil || cfg.ConnConfig.Database != "yreader_prod" {
		t.Fatalf("expected confirmed production target to succeed: cfg=%v err=%v", cfg, err)
	}
	if _, err := authorizeSchemaUpTarget("postgres://u:p@postgres:5432/yreader_prod", "wrong_db", noEnv); err == nil {
		t.Fatal("mismatched confirmation db accepted")
	}

	// PG* env overrides remain rejected even with confirmation.
	withPGHost := func(k string) (string, bool) {
		if k == "PGHOST" {
			return "evil", true
		}
		return "", false
	}
	if _, err := authorizeSchemaUpTarget("postgres://u:p@postgres:5432/yreader_prod", "yreader_prod", withPGHost); err == nil {
		t.Fatal("PGHOST override accepted")
	}
}

func TestParseSchemaUpArgs(t *testing.T) {
	if confirm, err := parseSchemaUpArgs(nil); err != nil || confirm != "" {
		t.Fatalf("empty args: %q %v", confirm, err)
	}
	if confirm, err := parseSchemaUpArgs([]string{"--production-confirm-db=yreader_prod"}); err != nil || confirm != "yreader_prod" {
		t.Fatalf("flag parse failed: %q %v", confirm, err)
	}
	for _, bad := range [][]string{
		{"--production-confirm-db="},
		{"--unknown-flag"},
		{"extra-positional"},
	} {
		if _, err := parseSchemaUpArgs(bad); err == nil {
			t.Fatalf("invalid schema up args accepted: %v", bad)
		}
	}
}

func TestParseImportSourcesArgsAndPayload(t *testing.T) {
	opts, err := parseImportSourcesArgs([]string{"--user=alice", "--file=sources.json", "--overwrite=update", "--dry-run"})
	if err != nil || opts.User != "alice" || opts.File != "sources.json" || opts.Overwrite != "update" || !opts.DryRun {
		t.Fatalf("valid import args rejected: %+v %v", opts, err)
	}
	for _, bad := range [][]string{
		{"--file=sources.json"},
		{"--user=alice"},
		{"--user=alice", "--file=sources.json", "--overwrite=delete"},
		{"--user=alice", "--file=sources.json", "extra"},
	} {
		if _, err := parseImportSourcesArgs(bad); err == nil {
			t.Fatalf("invalid import args accepted: %v", bad)
		}
	}

	arr, err := parseLegadoImportData([]byte("\xef\xbb\xbf[{\"bookSourceUrl\":\"https://example.com\",\"bookSourceName\":\"Demo\"}]"))
	if err != nil || len(arr) != 1 {
		t.Fatalf("BOM JSON array failed: %d %v", len(arr), err)
	}
	single, err := parseLegadoImportData([]byte(`{"bookSourceUrl":"https://example.com"}`))
	if err != nil || len(single) != 1 {
		t.Fatalf("single JSON object failed: %d %v", len(single), err)
	}
	for _, bad := range []string{"", "[]", `"string"`, "{bad"} {
		if _, err := parseLegadoImportData([]byte(bad)); err == nil {
			t.Fatalf("invalid Legado payload accepted: %q", bad)
		}
	}
}

func TestRunImportLegadoSourcesRequiresKeyring(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "sources.json")
	if err := os.WriteFile(path, []byte(`[{"bookSourceUrl":"https://example.com","bookSourceName":"Demo"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATABASE_URL", "postgres://u:p@127.0.0.1:5432/yreader_test")
	// Ensure no keyring is loaded when YREADER_HEADER_KEY_* is absent.
	if err := runImportLegadoSources([]string{"--user=alice", "--file=" + path, "--dry-run"}); err == nil {
		t.Fatal("expected import without header keyring to fail closed")
	}
}
