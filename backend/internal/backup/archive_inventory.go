package backup

import (
	"bufio"
	"context"
	_ "embed"
	"errors"
	"io"
	"regexp"
	"strconv"
	"strings"

	"yreader/backend/internal/migration"
)

var ErrArchiveInventory = errors.New("backup: PostgreSQL archive inventory rejected")

// This catalog was generated from our immutable migration v1 in a disposable
// PostgreSQL 16 database. It is a name/type inventory, NOT an SQL safety proof.
// Archived DDL must never be executed by the isolated restore path.
//
//go:embed archive_inventory_v1.txt
var archiveInventoryV1 string

var archiveTOCEntry = regexp.MustCompile(`^([1-9][0-9]*); ([0-9]+) ([0-9]+) (.+)$`)
var archiveOwner = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)

var restoreTableOrder = []string{
	"users", "book_sources", "books", "book_source_mappings", "chapters",
	"reading_progress", "chapter_source_overrides", "bookmarks", "replace_rules",
	"app_settings", "auth_sessions", "source_failures", "jobs", "schema_migrations",
}

// ArchiveInventory deliberately exposes no SQL or source owner. DataUseList is
// only an ordered pg_restore TOC selector; it is not authorization to restore.
type ArchiveInventory struct{ dataUseList string }

func (i ArchiveInventory) DataUseList() string { return i.dataUseList }

func archiveBaselineSupported() bool {
	return migration.Expected().Version == 1 && migration.Expected().Checksum == "7e275f0f5dc37fa7b0834bd3d80da7c192f7bc7b854041f2f1e99b0cae3c3e51"
}

func parseArchiveInventory(ctx context.Context, reader io.Reader) (ArchiveInventory, error) {
	if e := ctx.Err(); e != nil {
		return ArchiveInventory{}, e
	}
	// A future migration requires an explicit new catalog, not silent reuse.
	if !archiveBaselineSupported() {
		return ArchiveInventory{}, ErrArchiveInventory
	}
	const budget = 256 << 10
	bounded := &io.LimitedReader{R: reader, N: budget + 1}
	scanner := bufio.NewScanner(bounded)
	scanner.Buffer(make([]byte, 4096), 4096)
	expected := map[string]bool{}
	for _, s := range strings.FieldsFunc(archiveInventoryV1, func(r rune) bool { return r == '\n' }) {
		expected[s] = true
	}
	seen := map[string]bool{}
	ids := map[uint64]bool{}
	data := map[string]string{}
	custom, major := false, false
	for scanner.Scan() {
		if e := ctx.Err(); e != nil {
			return ArchiveInventory{}, e
		}
		line := scanner.Text()
		if strings.HasPrefix(line, ";") {
			if line == ";     Format: CUSTOM" {
				custom = true
			}
			if strings.HasPrefix(line, ";     Dumped by pg_dump version: 16.") {
				major = true
			}
			continue
		}
		if line == "" {
			continue
		}
		m := archiveTOCEntry.FindStringSubmatch(line)
		if m == nil {
			return ArchiveInventory{}, ErrArchiveInventory
		}
		id, e := strconv.ParseUint(m[1], 10, 32)
		if e != nil || ids[id] {
			return ArchiveInventory{}, ErrArchiveInventory
		}
		ids[id] = true
		for _, oid := range m[2:4] {
			if _, e = strconv.ParseUint(oid, 10, 32); e != nil {
				return ArchiveInventory{}, ErrArchiveInventory
			}
		}
		signature := strings.TrimSpace(m[4])
		if strings.Contains(signature, " public ") {
			index := strings.LastIndexByte(signature, ' ')
			if index < 0 || !archiveOwner.MatchString(signature[index+1:]) {
				return ArchiveInventory{}, ErrArchiveInventory
			}
			signature = signature[:index]
		}
		if !expected[signature] || seen[signature] {
			return ArchiveInventory{}, ErrArchiveInventory
		}
		seen[signature] = true
		if strings.HasPrefix(signature, "TABLE DATA public ") || strings.HasPrefix(signature, "SEQUENCE SET public ") {
			// Catalog IDs are supplied by the archive. Everything after them is
			// rebuilt from the trusted signature rather than relaying source text.
			data[signature] = m[1] + "; " + m[2] + " " + m[3] + " " + signature + "\n"
		}
	}
	if scanner.Err() != nil || bounded.N <= 0 || !custom || !major || len(seen) != len(expected) {
		return ArchiveInventory{}, ErrArchiveInventory
	}
	var selection strings.Builder
	for _, table := range restoreTableOrder {
		selection.WriteString(data["TABLE DATA public "+table])
	}
	for _, sequence := range []string{"chapters_id_seq", "source_failures_id_seq"} {
		selection.WriteString(data["SEQUENCE SET public "+sequence])
	}
	return ArchiveInventory{selection.String()}, nil
}
