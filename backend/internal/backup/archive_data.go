package backup

import (
	"bufio"
	"context"
	_ "embed"
	"errors"
	"io"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"yreader/backend/internal/migration"
)

var ErrArchiveData = errors.New("backup: PostgreSQL archive data rejected")

// Exact COPY headers from the immutable trusted baseline, not a parser for
// arbitrary archived DDL. Generation was checked against all 14 baseline tables.
//
//go:embed archive_columns_v1.txt
var archiveColumnsV1 string

var archivePrologue = []string{
	"SET statement_timeout = 0;", "SET lock_timeout = 0;",
	"SET idle_in_transaction_session_timeout = 0;", "SET client_encoding = 'UTF8';",
	"SET standard_conforming_strings = on;", "SELECT pg_catalog.set_config('search_path', '', false);",
	"SET check_function_bodies = false;", "SET xmloption = content;",
	"SET client_min_messages = warning;", "SET row_security = off;",
}
var restrictToken = regexp.MustCompile(`^[A-Za-z0-9]{1,128}$`)
var sequenceSQL = regexp.MustCompile(`^SELECT pg_catalog\.setval\('public\.(chapters_id_seq|source_failures_id_seq)', ([1-9][0-9]*), (true|false)\);$`)

type SequenceState struct {
	Value  int64
	Called bool
}
type ArchiveDataSummary struct {
	Counts           ManifestCounts
	ChaptersSequence SequenceState
	FailuresSequence SequenceState
}

func manifestCountMap(counts ManifestCounts) map[string]int64 {
	result := map[string]int64{}
	value := reflect.ValueOf(counts)
	for i := 0; i < value.NumField(); i++ {
		result[value.Type().Field(i).Tag.Get("json")] = value.Field(i).Int()
	}
	return result
}

// The emitted SQL is ONLY inspected, never executed. COPY bodies have their own
// state so content resembling SQL/comments/meta-commands is treated as row data.
// maxBytes bounds expanded output independently of the compressed archive size.
func parseArchiveData(ctx context.Context, reader io.Reader, expected ManifestCounts, maxBytes int64) (ArchiveDataSummary, error) {
	return parseArchiveDataSelection(ctx, reader, expected, maxBytes, "", nil)
}

// A selected render may feed only its known COPY body into PostgreSQL's COPY
// protocol. Headers, SQL, sequence statements and psql commands never reach it.
func parseArchiveDataSelection(ctx context.Context, reader io.Reader, expected ManifestCounts, maxBytes int64, selected string, output io.Writer) (ArchiveDataSummary, error) {
	if e := ctx.Err(); e != nil {
		return ArchiveDataSummary{}, e
	}
	if !archiveBaselineSupported() || maxBytes < 1 || maxBytes > 1<<40 {
		return ArchiveDataSummary{}, ErrArchiveData
	}
	want := manifestCountMap(expected)
	for _, n := range want {
		if n < 0 {
			return ArchiveDataSummary{}, ErrArchiveData
		}
	}
	if expected.Migrations != 1 {
		return ArchiveDataSummary{}, ErrArchiveData
	}
	headers := map[string]string{}
	widths := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(archiveColumnsV1), "\n") {
		start := strings.IndexByte(line, '(')
		table := strings.TrimSuffix(strings.TrimPrefix(line[:start], "COPY public."), " ")
		headers[line] = table
		widths[table] = strings.Count(line[start:], ",") + 1
	}
	tableCount, sequenceCount := 14, 2
	if selected != "" {
		if _, ok := widths[selected]; !ok || output == nil {
			return ArchiveDataSummary{}, ErrArchiveData
		}
		tableCount, sequenceCount = 1, 0
	}
	bounded := &io.LimitedReader{R: reader, N: maxBytes + 1}
	scanner := bufio.NewScanner(bounded)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	seen := map[string]bool{}
	rows := map[string]int64{}
	sequences := map[string]SequenceState{}
	maxID := map[string]int64{}
	prologue := 0
	active, restriction := "", ""
	ended := false
	for scanner.Scan() {
		if e := ctx.Err(); e != nil {
			return ArchiveDataSummary{}, e
		}
		line := scanner.Text()
		if !utf8.ValidString(line) || strings.ContainsRune(line, 0) {
			return ArchiveDataSummary{}, ErrArchiveData
		}
		if active != "" {
			if line == `\.` {
				if rows[active] != want[active] {
					return ArchiveDataSummary{}, ErrArchiveData
				}
				active = ""
				continue
			}
			if strings.Count(line, "\t")+1 != widths[active] || rows[active] >= want[active] {
				return ArchiveDataSummary{}, ErrArchiveData
			}
			rows[active]++
			if active == "schema_migrations" {
				fields := strings.Split(line, "\t")
				baseline := migration.Expected()
				if fields[0] != strconv.FormatInt(baseline.Version, 10) || fields[1] != baseline.Name || fields[2] != baseline.Checksum || fields[3] == `\N` || fields[3] == "" {
					return ArchiveDataSummary{}, ErrArchiveData
				}
			}
			if active == "chapters" || active == "source_failures" {
				first, _, _ := strings.Cut(line, "\t")
				id, e := strconv.ParseInt(first, 10, 64)
				if e != nil || id < 1 {
					return ArchiveDataSummary{}, ErrArchiveData
				}
				if id > maxID[active] {
					maxID[active] = id
				}
			}
			if selected != "" {
				if _, e := io.WriteString(output, line+"\n"); e != nil {
					return ArchiveDataSummary{}, ErrArchiveData
				}
			}
			continue
		}
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}
		if ended {
			return ArchiveDataSummary{}, ErrArchiveData
		}
		if strings.HasPrefix(line, `\restrict `) {
			token := strings.TrimPrefix(line, `\restrict `)
			if restriction != "" || prologue != 0 || !restrictToken.MatchString(token) {
				return ArchiveDataSummary{}, ErrArchiveData
			}
			restriction = token
			continue
		}
		if prologue < len(archivePrologue) {
			if line != archivePrologue[prologue] {
				return ArchiveDataSummary{}, ErrArchiveData
			}
			prologue++
			continue
		}
		if table, ok := headers[line]; ok {
			if seen[table] || len(sequences) != 0 || (selected != "" && selected != table) {
				return ArchiveDataSummary{}, ErrArchiveData
			}
			seen[table] = true
			active = table
			continue
		}
		if match := sequenceSQL.FindStringSubmatch(line); match != nil {
			if selected != "" || len(seen) != 14 {
				return ArchiveDataSummary{}, ErrArchiveData
			}
			if _, ok := sequences[match[1]]; ok {
				return ArchiveDataSummary{}, ErrArchiveData
			}
			value, e := strconv.ParseInt(match[2], 10, 64)
			if e != nil {
				return ArchiveDataSummary{}, ErrArchiveData
			}
			sequences[match[1]] = SequenceState{Value: value, Called: match[3] == "true"}
			continue
		}
		if restriction != "" && line == `\unrestrict `+restriction && len(sequences) == sequenceCount && len(seen) == tableCount {
			ended = true
			continue
		}
		return ArchiveDataSummary{}, ErrArchiveData
	}
	if e := ctx.Err(); e != nil {
		return ArchiveDataSummary{}, e
	}
	if scanner.Err() != nil || bounded.N <= 0 || active != "" || prologue != len(archivePrologue) || len(seen) != tableCount || len(sequences) != sequenceCount || (restriction != "" && !ended) {
		return ArchiveDataSummary{}, ErrArchiveData
	}
	if selected != "" {
		return ArchiveDataSummary{}, nil
	}
	for seq, table := range map[string]string{"chapters_id_seq": "chapters", "source_failures_id_seq": "source_failures"} {
		state := sequences[seq]
		if state.Value < maxID[table] || (!state.Called && state.Value <= maxID[table]) || (state.Called && state.Value == 1<<63-1) {
			return ArchiveDataSummary{}, ErrArchiveData
		}
	}
	return ArchiveDataSummary{expected, sequences["chapters_id_seq"], sequences["source_failures_id_seq"]}, nil
}
