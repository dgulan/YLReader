package backup

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSelectedCopyBodyIsolation(t *testing.T) {
	header := "COPY public.app_settings (user_id, key, value, updated_at) FROM stdin;\n"
	row := "id\tSELECT anything;\tvalue\\twith\\nescapes\t2026-11-13 00:00:00+00\n"
	fixture := "\\restrict test123\n" + strings.Join(archivePrologue, "\n") + "\n" + header + row + "\\.\n\\unrestrict test123\n"
	counts := ManifestCounts{Migrations: 1, Settings: 1}
	var output bytes.Buffer
	if _, e := parseArchiveDataSelection(context.Background(), strings.NewReader(fixture), counts, 1<<20, "app_settings", &output); e != nil {
		t.Fatal(e)
	}
	if output.String() != row {
		t.Fatal("SQL or metadata leaked into COPY protocol")
	}
	for name, invalid := range map[string]string{
		"cross-table":      strings.Replace(fixture, "COPY public.app_settings (user_id, key, value, updated_at)", "COPY public.schema_migrations (version, name, checksum, applied_at)", 1),
		"sequence":         strings.Replace(fixture, "\\unrestrict", "SELECT pg_catalog.setval('public.chapters_id_seq', 1, false);\n\\unrestrict", 1),
		"sql-after-copy":   strings.Replace(fixture, "\\unrestrict", "DELETE FROM public.users;\n\\unrestrict", 1),
		"truncated-footer": strings.Replace(fixture, "\\unrestrict test123", "", 1),
	} {
		t.Run(name, func(t *testing.T) {
			var body bytes.Buffer
			if _, e := parseArchiveDataSelection(context.Background(), strings.NewReader(invalid), counts, 1<<20, "app_settings", &body); !errors.Is(e, ErrArchiveData) {
				t.Fatal(e)
			}
		})
	}
	if _, e := parseArchiveDataSelection(context.Background(), strings.NewReader(fixture), counts, 1<<20, "unknown", &output); !errors.Is(e, ErrArchiveData) {
		t.Fatal(e)
	}
	if _, e := parseArchiveDataSelection(context.Background(), strings.NewReader(fixture), counts, 1<<20, "app_settings", nil); !errors.Is(e, ErrArchiveData) {
		t.Fatal(e)
	}
}

func TestRestorePrimitiveRejectsBusinessTarget(t *testing.T) {
	cleanPG(t)
	if _, e := restoreIsolatedDatabase(context.Background(), "postgres://reader:@127.0.0.1:5432/yreader", nil, PostgresArchiveTool{}, 1<<20); !errors.Is(e, ErrTargetDenied) {
		t.Fatal(e)
	}
	if _, e := restoreIsolatedDatabase(context.Background(), "postgres://reader:@127.0.0.1:5432/yreader_a1_iso_unit", nil, PostgresArchiveTool{}, 1<<20); !errors.Is(e, ErrRestoreDatabase) {
		t.Fatal(e)
	}
}
