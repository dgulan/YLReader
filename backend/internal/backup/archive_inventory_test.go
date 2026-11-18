package backup

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func inventoryFixture() string {
	var out strings.Builder
	out.WriteString(";     Format: CUSTOM\n;     Dumped by pg_dump version: 16.15\n")
	for i, s := range strings.Split(strings.TrimSpace(archiveInventoryV1), "\n") {
		owner := ""
		if strings.Contains(s, " public ") {
			owner = " restorer"
		}
		fmt.Fprintf(&out, "%d; 0 %d %s%s\n", i+1, i+100, s, owner)
	}
	return out.String()
}

func TestArchiveInventory(t *testing.T) {
	valid := inventoryFixture()
	parsed, e := parseArchiveInventory(context.Background(), strings.NewReader(valid))
	if e != nil {
		t.Fatal(e)
	}
	if windows, e := parseArchiveInventory(context.Background(), strings.NewReader(strings.ReplaceAll(valid, "\n", "\r\n"))); e != nil || windows.DataUseList() != parsed.DataUseList() {
		t.Fatal("CRLF inventory differs", e)
	}
	lines := strings.Split(strings.TrimSpace(parsed.DataUseList()), "\n")
	if len(lines) != 16 {
		t.Fatal("data selection size", len(lines))
	}
	for i, name := range restoreTableOrder {
		if !strings.HasSuffix(lines[i], "TABLE DATA public "+name) {
			t.Fatal("dependency order", i)
		}
	}
	for name, bad := range map[string]string{
		"missing":         strings.Replace(valid, "TABLE DATA public users", "TABLE DATA public unknown", 1),
		"extra":           valid + "9999; 0 0 TABLE DATA public evil restorer\n",
		"duplicate-id":    valid + strings.Split(valid, "\n")[2] + "\n",
		"duplicate-name":  valid + "9999; 0 0 TABLE DATA public users restorer\n",
		"blob":            valid + "9999; 0 0 BLOB - 1 restorer\n",
		"foreign-schema":  strings.Replace(valid, "TABLE DATA public users", "TABLE DATA private users", 1),
		"sequence":        strings.Replace(valid, "SEQUENCE SET public chapters_id_seq", "SEQUENCE SET public evil", 1),
		"function":        strings.Replace(valid, "FUNCTION public set_updated_at()", "FUNCTION public evil()", 1),
		"owner-injection": strings.Replace(valid, "TABLE DATA public users restorer", "TABLE DATA public users \"bad owner\"", 1),
		"version":         strings.Replace(valid, "version: 16.", "version: 17.", 1),
		"format":          strings.Replace(valid, "Format: CUSTOM", "Format: TAR", 1),
		"overflow-id":     valid + "999999999999999999999; 0 0 TABLE DATA public users restorer\n",
		"line-budget":     valid + ";" + strings.Repeat("x", 4096),
		"total-budget":    valid + strings.Repeat(";padding\n", 40000),
		"nul":             valid + "\x00\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, e := parseArchiveInventory(context.Background(), strings.NewReader(bad)); !errors.Is(e, ErrArchiveInventory) {
				t.Fatal("accepted invalid inventory", e)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := parseArchiveInventory(ctx, strings.NewReader(valid)); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}

func TestArchiveToolRejectsImplicitExecutable(t *testing.T) {
	if _, e := (PostgresArchiveTool{Executable: "pg_restore"}).Inspect(context.Background(), strings.NewReader("PGDMP")); !errors.Is(e, ErrArchiveInventory) {
		t.Fatal(e)
	}
}

func TestArchiveOutputBound(t *testing.T) {
	b := &archiveOutput{remaining: 3}
	if n, e := b.Write([]byte("abc")); n != 3 || e != nil {
		t.Fatal(n, e)
	}
	if _, e := b.Write([]byte("SECRET")); !errors.Is(e, ErrArchiveInventory) || b.String() != "abc" {
		t.Fatal("unbounded output")
	}
}
