package backup

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// PostgresArchiveTool invokes an explicitly installed, trusted executable.
// It has no database target option and never consults PATH or PG* variables.
// The binary and its installation directory must be controlled by the operator.
type PostgresArchiveTool struct{ Executable string }

type archiveOutput struct {
	bytes.Buffer
	remaining int
}

func (b *archiveOutput) Write(p []byte) (int, error) {
	if len(p) > b.remaining {
		return 0, ErrArchiveInventory
	}
	b.remaining -= len(p)
	return b.Buffer.Write(p)
}

func (tool PostgresArchiveTool) command(ctx context.Context, input io.Reader, args ...string) (*exec.Cmd, error) {
	if !filepath.IsAbs(tool.Executable) {
		return nil, ErrArchiveInventory
	}
	command := exec.CommandContext(ctx, tool.Executable, args...)
	command.WaitDelay = time.Second
	command.Env = []string{"LC_ALL=C", "LANG=C"}
	// Windows process creation needs the OS installation location, never DB
	// defaults or credentials. No shell, connection URI, or archive path is used.
	if root := os.Getenv("SystemRoot"); root != "" {
		command.Env = append(command.Env, "SystemRoot="+root)
	}
	command.Stdin = input
	command.Stderr = io.Discard // Tool diagnostics may contain source identifiers.
	return command, nil
}

func (tool PostgresArchiveTool) run(ctx context.Context, input io.Reader, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	command, e := tool.command(ctx, input, args...)
	if e != nil {
		return nil, e
	}
	output := &archiveOutput{remaining: 256 << 10}
	command.Stdout = output
	if e := command.Run(); e != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrArchiveInventory
	}
	return output.Bytes(), nil
}

// inspectData streams stdout into a closed grammar without retaining SQL or
// user rows. A failed parse cancels the producer and reaps the child process.
func (tool PostgresArchiveTool) inspectData(ctx context.Context, archive io.Reader, counts ManifestCounts, maxBytes int64) (ArchiveDataSummary, error) {
	var result ArchiveDataSummary
	e := tool.stream(ctx, archive, func(ctx context.Context, reader io.Reader) error {
		var e error
		result, e = parseArchiveData(ctx, reader, counts, maxBytes)
		return e
	}, "--data-only", "--no-owner", "--no-acl", "--file=-")
	return result, e
}

func (tool PostgresArchiveTool) stream(ctx context.Context, archive io.Reader, consume func(context.Context, io.Reader) error, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	command, e := tool.command(ctx, archive, args...)
	if e != nil {
		return e
	}
	stdout, e := command.StdoutPipe()
	if e != nil {
		return ErrArchiveData
	}
	if e = command.Start(); e != nil {
		stdout.Close()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrArchiveData
	}
	parseErr := consume(ctx, stdout)
	contextErr := ctx.Err()
	if parseErr != nil {
		cancel()
		stdout.Close()
	}
	waitErr := command.Wait()
	if contextErr != nil {
		return contextErr
	}
	if parseErr != nil {
		return parseErr
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if waitErr != nil {
		return ErrArchiveData
	}
	return nil
}

// VerifyDatabaseData performs two fresh reads from the private authenticated
// package: TOC first, then bounded data expansion. maxExpandedBytes is a separate
// operational budget, not a claim that SQL size equals custom archive size.
// No emitted SQL is saved or executed; success grants no restore permission.
func (v *VerifiedPackage) VerifyDatabaseData(ctx context.Context, tool PostgresArchiveTool, maxExpandedBytes int64) (ArchiveDataSummary, error) {
	if maxExpandedBytes < 1 || maxExpandedBytes > 1<<40 {
		return ArchiveDataSummary{}, ErrArchiveData
	}
	if _, e := v.InspectDatabaseInventory(ctx, tool); e != nil {
		return ArchiveDataSummary{}, e
	}
	r, e := v.OpenDatabase(ctx)
	if e != nil {
		return ArchiveDataSummary{}, e
	}
	defer r.Close()
	return tool.inspectData(ctx, r, v.manifest.Counts, maxExpandedBytes)
}

// Inspect reads the entire TOC using official pg_restore in output-only mode.
// This does not inspect COPY column lists, decompress table data, verify row
// counts, or prove the archive's SQL safe. Only trusted-source packages qualify
// for later isolated restoration, which must use the trusted schema baseline.
func (tool PostgresArchiveTool) Inspect(ctx context.Context, archive io.Reader) (ArchiveInventory, error) {
	version, e := tool.run(ctx, nil, "--version")
	if e != nil {
		return ArchiveInventory{}, e
	}
	if !strings.HasPrefix(string(version), "pg_restore (PostgreSQL) 16.") {
		return ArchiveInventory{}, ErrArchiveInventory
	}
	listing, e := tool.run(ctx, archive, "--list")
	if e != nil {
		return ArchiveInventory{}, e
	}
	return parseArchiveInventory(ctx, bytes.NewReader(listing))
}

// InspectDatabaseInventory retains a package reader for the complete external
// tool operation, so its private scratch cannot be cleaned up concurrently.
func (v *VerifiedPackage) InspectDatabaseInventory(ctx context.Context, tool PostgresArchiveTool) (ArchiveInventory, error) {
	r, e := v.OpenDatabase(ctx)
	if e != nil {
		return ArchiveInventory{}, e
	}
	defer r.Close()
	return tool.Inspect(ctx, r)
}
