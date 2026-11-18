package backup

import (
	"context"
	"errors"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"yreader/backend/internal/migration"
)

func dataFixture() string {
	var out strings.Builder
	out.WriteString("-- synthetic fixture\n\\restrict testToken123\n")
	out.WriteString(strings.Join(archivePrologue, "\n") + "\n")
	for _, header := range strings.Split(strings.TrimSpace(archiveColumnsV1), "\n") {
		out.WriteString(header + "\n")
		if strings.HasPrefix(header, "COPY public.schema_migrations ") {
			baseline := migration.Expected()
			out.WriteString(strconv.FormatInt(baseline.Version, 10) + "\t" + baseline.Name + "\t" + baseline.Checksum + "\t2026-11-13 08:00:00+00\n")
		}
		out.WriteString("\\.\n")
	}
	out.WriteString("SELECT pg_catalog.setval('public.chapters_id_seq', 1, false);\nSELECT pg_catalog.setval('public.source_failures_id_seq', 1, false);\n\\unrestrict testToken123\n")
	return out.String()
}

func TestArchiveDataGrammar(t *testing.T) {
	// This parser is exclusively a regression check for OUR frozen, trusted
	// baseline formatting. It is never used on archived/untrusted SQL.
	baseline, e := os.ReadFile("../migration/001_baseline.sql")
	if e != nil {
		t.Fatal(e)
	}
	tables := regexp.MustCompile(`(?ms)^CREATE TABLE IF NOT EXISTS ([a-z_]+) \((.*?)^\);`).FindAllStringSubmatch(string(baseline), -1)
	column := regexp.MustCompile(`^\s*([a-z_][a-z_0-9]*)\s+`)
	if len(tables) != 14 {
		t.Fatal("trusted baseline table count", len(tables))
	}
	headers := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(archiveColumnsV1), "\n") {
		headers[line] = true
	}
	if len(headers) != 14 {
		t.Fatal("COPY table count", len(headers))
	}
	for _, table := range tables {
		var cols []string
		for _, line := range strings.Split(table[2], "\n") {
			if m := column.FindStringSubmatch(line); m != nil {
				cols = append(cols, m[1])
			}
		}
		if !headers["COPY public."+table[1]+" ("+strings.Join(cols, ", ")+") FROM stdin;"] {
			t.Fatal("COPY columns differ from trusted baseline", table[1])
		}
	}
	valid := dataFixture()
	counts := ManifestCounts{Migrations: 1}
	parsed, e := parseArchiveData(context.Background(), strings.NewReader(valid), counts, int64(len(valid)))
	if e != nil || parsed.Counts != counts || parsed.ChaptersSequence != (SequenceState{1, false}) {
		t.Fatal(parsed, e)
	}
	// Historical PostgreSQL 16 output without psql restriction markers is also
	// accepted, while a marker, when present, must have its matching terminator.
	legacy := strings.ReplaceAll(strings.ReplaceAll(valid, "\\restrict testToken123\n", ""), "\\unrestrict testToken123\n", "")
	if _, e = parseArchiveData(context.Background(), strings.NewReader(legacy), counts, 1<<20); e != nil {
		t.Fatal("legacy profile", e)
	}
	for name, bad := range map[string]string{
		"missing-table":           strings.Replace(valid, "COPY public.users (", "COPY public.unknown (", 1),
		"column-drift":            strings.Replace(valid, "user_id, key, value", "user_id, wrong, value", 1),
		"column-reorder":          strings.Replace(valid, "user_id, key, value", "key, user_id, value", 1),
		"missing-copy-end":        strings.Replace(valid, "\\.\n", "", 1),
		"duplicate-table":         strings.Replace(valid, "COPY public.app_settings (user_id, key, value, updated_at) FROM stdin;", "COPY public.app_settings (user_id, key, value, updated_at) FROM stdin;\n\\.\nCOPY public.app_settings (user_id, key, value, updated_at) FROM stdin;", 1),
		"ledger-name":             strings.Replace(valid, "\tbaseline_v1_5\t", "\tunknown\t", 1),
		"ledger-checksum":         strings.Replace(valid, migration.Expected().Checksum, strings.Repeat("0", 64), 1),
		"ledger-null-date":        strings.Replace(valid, "2026-11-13 08:00:00+00", `\N`, 1),
		"ledger-width":            strings.Replace(valid, "\tbaseline_v1_5\t", "\tbaseline_v1_5\textra\t", 1),
		"copy-program":            strings.Replace(valid, "FROM stdin;", "FROM PROGRAM 'anything';", 1),
		"insert-sql":              strings.Replace(valid, "SET row_security = off;", "SET row_security = off;\nINSERT INTO public.users DEFAULT VALUES;", 1),
		"psql-command":            strings.Replace(valid, "SET row_security = off;", "SET row_security = off;\n\\! echo arbitrary", 1),
		"changed-prologue":        strings.Replace(valid, "SET standard_conforming_strings = on;", "SET standard_conforming_strings = off;", 1),
		"overflow-sequence":       strings.Replace(valid, "chapters_id_seq', 1, false", "chapters_id_seq', 9223372036854775808, false", 1),
		"exhausted-sequence":      strings.Replace(valid, "chapters_id_seq', 1, false", "chapters_id_seq', 9223372036854775807, true", 1),
		"unknown-sequence":        strings.Replace(valid, "source_failures_id_seq'", "unknown_seq'", 1),
		"duplicate-sequence":      strings.Replace(valid, "\\unrestrict", "SELECT pg_catalog.setval('public.chapters_id_seq', 1, false);\n\\unrestrict", 1),
		"missing-sequence":        strings.Replace(valid, "SELECT pg_catalog.setval('public.chapters_id_seq', 1, false);\n", "", 1),
		"restriction-mismatch":    strings.Replace(valid, "\\unrestrict testToken123", "\\unrestrict other", 1),
		"restriction-missing-end": strings.Replace(valid, "\\unrestrict testToken123", "", 1),
		"restriction-injection":   strings.Replace(valid, "\\restrict testToken123", "\\restrict test;SELECT", 1),
		"after-end":               valid + "SELECT 1;\n",
		"bad-utf8":                valid + "--\xff\n",
		"nul":                     valid + "--\x00\n",
		"row-limit":               strings.Replace(valid, "-- synthetic fixture", "--"+strings.Repeat("x", 8<<20), 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, e := parseArchiveData(context.Background(), strings.NewReader(bad), counts, 32<<20); !errors.Is(e, ErrArchiveData) {
				t.Fatal("invalid output accepted", e)
			}
		})
	}
	for name, c := range map[string]ManifestCounts{"row-count": {Migrations: 1, Users: 1}, "negative": {Migrations: 1, Users: -1}, "ledger-count": {Migrations: 2}} {
		t.Run(name, func(t *testing.T) {
			if _, e := parseArchiveData(context.Background(), strings.NewReader(valid), c, 1<<20); !errors.Is(e, ErrArchiveData) {
				t.Fatal(e)
			}
		})
	}
	if _, e = parseArchiveData(context.Background(), strings.NewReader(valid), counts, int64(len(valid)-1)); !errors.Is(e, ErrArchiveData) {
		t.Fatal("expanded budget", e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = parseArchiveData(ctx, strings.NewReader(valid), counts, 1<<20); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if _, e = parseArchiveData(context.Background(), io.MultiReader(strings.NewReader(valid), errorReader{}), counts, 1<<20); !errors.Is(e, ErrArchiveData) {
		t.Fatal("source error", e)
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("synthetic secret diagnostic") }

func TestArchiveDataRowsAndSequences(t *testing.T) {
	valid := dataFixture()
	// SQL-looking user text stays a data field; no line is ever executed.
	row := "1\tSELECT pg_catalog.evil();\t\\N\t0\t0\ttitle\\tinline\tkey\turl\thash\tf\tf\t1\t\\N\t2026-11-13 00:00:00+00\t2026-11-13 00:00:00+00\n"
	header := "COPY public.chapters (id, book_id, source_id, chapter_index, sort_order, title, normalized_title, url, url_hash, is_volume, is_vip, word_count, published_at, created_at, updated_at) FROM stdin;\n"
	valid = strings.Replace(valid, header, header+row, 1)
	counts := ManifestCounts{Migrations: 1, Chapters: 1}
	for name, seq := range map[string]string{"collision": "1, false", "safe": "1, true", "ahead": "2, false"} {
		t.Run(name, func(t *testing.T) {
			sample := strings.Replace(valid, "chapters_id_seq', 1, false", "chapters_id_seq', "+seq, 1)
			_, e := parseArchiveData(context.Background(), strings.NewReader(sample), counts, 1<<20)
			if name == "collision" {
				if !errors.Is(e, ErrArchiveData) {
					t.Fatal(e)
				}
			} else if e != nil {
				t.Fatal(e)
			}
		})
	}
	if _, e := parseArchiveData(context.Background(), strings.NewReader(valid), ManifestCounts{Migrations: 1}, 1<<20); !errors.Is(e, ErrArchiveData) {
		t.Fatal("extra rows", e)
	}
}
