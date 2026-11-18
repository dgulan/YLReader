package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"yreader/backend/internal/backup"
)

func validRestoreCLIArgs(root, hdr, pgr string) []string {
	return []string{
		"restore",
		"--input-root", root,
		"--request-key", "request.json",
		"--config-key", "config.json",
		"--plan-key", "plan.json",
		"--plan-digest", strings.Repeat("a", 64),
		"--confirm-target", "20000000-0000-4000-8000-000000000001",
		"--owner", "50000000-0000-4000-8000-000000000001",
		"--header-keys", hdr,
		"--pg-restore", pgr,
		"--timeout", "2m",
	}
}

func TestRestoreAndPreviewSeparation(t *testing.T) {
	_, _, clock := commandFixture()
	root := t.TempDir()

	// Read-only commands MUST reject restore execution parameters and never invoke restore logic.
	for _, ro := range [][]string{
		{"inspect", "--input-root", root, "--request-key", "request.json", "--plan-digest", strings.Repeat("a", 64)},
		{"plan", "--input-root", root, "--request-key", "request.json", "--config-key", "config.json", "--confirm-target", "20000000-0000-4000-8000-000000000001"},
		{"status", "--control-root", root, "--operation", "00000000-0000-4000-8000-000000000001", "--owner", "50000000-0000-4000-8000-000000000001"},
		{"behavior-status", "--control-root", root, "--operation", "00000000-0000-4000-8000-000000000001", "--pg-restore", root},
	} {
		called := false
		var out, errs bytes.Buffer
		code := run(context.Background(), ro, &out, &errs, func(string) (controlReader, error) { called = true; return nil, nil }, clock)
		if code != 2 || called || out.Len() != 0 {
			t.Fatalf("read-only command accepted restore flag %v: code=%d called=%v", ro, code, called)
		}
	}

	hdr := root + string([]rune{'/', 'h'})
	if !strings.HasPrefix(root, "/") {
		hdr = root + `\h`
	}
	base := validRestoreCLIArgs(root, hdr, hdr)
	// Missing any required flag or malformed value rejects with exit 2 before opening directory.
	for idx := 1; idx < 21; idx += 2 {
		// Drop flag pair idx..idx+1
		bad := append(append([]string{}, base[:idx]...), base[idx+2:]...)
		called := false
		var out, errs bytes.Buffer
		code := runRestore(context.Background(), bad, &out, &errs, func(string) (controlReader, error) { called = true; return nil, nil }, clock, func(context.Context, string, backup.ControlledRestoreParams, backup.RestoreRecordReader, func() time.Time) (backup.ControlledRestoreResult, error) {
			called = true
			return backup.ControlledRestoreResult{}, nil
		})
		if code != 2 || called || out.Len() != 0 {
			t.Fatalf("missing flag at %d accepted: code=%d", idx, code)
		}
	}
	for _, extra := range [][]string{
		append(append([]string{}, base...), "--dsn", "postgres://SECRET"),
		append(append([]string{}, base...), "--timeout", "100ms"),
		append(append([]string{}, base...), "--timeout", "45m"),
		append(append([]string{}, base...), "--owner", uuid.Nil.String()),
		append(append([]string{}, base...), "--plan-digest", "not-sha"),
		append(append([]string{}, base...), "--input-root", "relative/path"),
		append(append([]string{}, base...), "--request-key", "../SECRET"),
		append(append([]string{}, base...), "--format", "yaml"),
	} {
		called := false
		var out, errs bytes.Buffer
		code := runRestore(context.Background(), extra, &out, &errs, func(string) (controlReader, error) { called = true; return nil, nil }, clock, func(context.Context, string, backup.ControlledRestoreParams, backup.RestoreRecordReader, func() time.Time) (backup.ControlledRestoreResult, error) {
			called = true
			return backup.ControlledRestoreResult{}, nil
		})
		if code != 2 || called || out.Len() != 0 || strings.Contains(errs.String(), "SECRET") {
			t.Fatalf("unsafe restore argument accepted or leaked: %v code=%d", extra, code)
		}
	}
}

func TestRestoreExecutionOutputsAndSanitization(t *testing.T) {
	_, _, clock := commandFixture()
	root := t.TempDir()
	hdr := root + `\keys.json`
	pgr := root + `\pg_restore.exe`
	if strings.HasPrefix(root, "/") {
		hdr = root + "/keys.json"
		pgr = root + "/pg_restore"
	}
	args := validRestoreCLIArgs(root, hdr, pgr)

	// Directory open error -> exit 3, zero secret leak
	var out, errs bytes.Buffer
	if code := runRestore(context.Background(), args, &out, &errs, func(string) (controlReader, error) { return nil, errors.New("SECRET") }, clock, func(context.Context, string, backup.ControlledRestoreParams, backup.RestoreRecordReader, func() time.Time) (backup.ControlledRestoreResult, error) {
		return backup.ControlledRestoreResult{}, nil
	}); code != 3 || out.Len() != 0 || strings.Contains(errs.String(), "SECRET") {
		t.Fatalf("open failure leaked or wrong exit: %d %s", code, errs.String())
	}

	// Success (published & validating) -> exit 0, bound timeout applied, text + json verified
	for _, tc := range []struct {
		format     string
		statusCode string
	}{
		{"json", "RESTORE_VALIDATING_PUBLISHED"},
		{"text", "RESTORE_VALIDATING_REGISTERED"},
	} {
		r := &commandReader{data: map[string][]byte{}}
		out.Reset()
		errs.Reset()
		var gotDeadline bool
		code := runRestore(context.Background(), append(append([]string{}, args...), "--format", tc.format), &out, &errs, func(string) (controlReader, error) { return r, nil }, clock, func(ctx context.Context, _ string, p backup.ControlledRestoreParams, _ backup.RestoreRecordReader, _ func() time.Time) (backup.ControlledRestoreResult, error) {
			_, gotDeadline = ctx.Deadline()
			return backup.ControlledRestoreResult{
				OperationID:          "40000000-0000-4000-8000-000000000001",
				BackupID:             "30000000-0000-4000-8000-000000000001",
				PlanDigest:           p.PlanDigest,
				TargetID:             p.ConfirmTarget,
				OwnerID:              p.OwnerID,
				Phase:                "validating",
				Outcome:              "validated_published",
				StatusCode:           tc.statusCode,
				DatabaseOutcome:      "committed",
				HeadersChecked:       true,
				PublicationState:     "verified",
				HandoverReviewStatus: "complete_registered",
				RegistrationState:    "registered",
				ArtifactsRetained:    true,
			}, nil
		})
		if code != 0 || !r.closed || !gotDeadline || errs.Len() != 0 {
			t.Fatalf("restore success failed (%s): code=%d closed=%v deadline=%v err=%s", tc.format, code, r.closed, gotDeadline, errs.String())
		}
		if tc.format == "json" {
			var decoded backup.ControlledRestoreResult
			if json.Unmarshal(out.Bytes(), &decoded) != nil || decoded.StatusCode != tc.statusCode || decoded.RetrySafe || decoded.ExecutionAuthorized || decoded.ReadyForCutover || decoded.DeploymentAuthorized {
				t.Fatalf("unsafe restore json output: %s", out.String())
			}
		} else if !strings.Contains(out.String(), "受控恢复状态码: "+tc.statusCode) || !strings.Contains(out.String(), "允许重放: 否") || !strings.Contains(out.String(), "允许执行/切换/部署: 否") {
			t.Fatalf("unsafe restore text output: %s", out.String())
		}
	}

	// Unknown / failure outcomes -> exit 4, sanitized status code only, secrets stripped, retry_safe=false
	for _, errCase := range []struct {
		err  error
		want string
	}{
		{backup.ErrRestoreCommitUnknown, "RESTORE_COMMIT_UNKNOWN"},
		{backup.ErrPublication, "RESTORE_PUBLICATION_FAILED"},
		{context.Canceled, "RESTORE_CANCELED"},
		{errors.New("postgres://user:SECRET@127.0.0.1/db"), "RESTORE_REJECTED"},
	} {
		r := &commandReader{data: map[string][]byte{}}
		out.Reset()
		errs.Reset()
		code := runRestore(context.Background(), args, &out, &errs, func(string) (controlReader, error) { return r, nil }, clock, func(context.Context, string, backup.ControlledRestoreParams, backup.RestoreRecordReader, func() time.Time) (backup.ControlledRestoreResult, error) {
			return backup.ControlledRestoreResult{ArtifactsRetained: true}, errCase.err
		})
		if code != 4 || !r.closed || strings.Contains(out.String()+errs.String(), "SECRET") || !strings.Contains(out.String(), errCase.want) || strings.TrimSpace(errs.String()) != errCase.want {
			t.Fatalf("uncertainty output check failed for %v: code=%d out=%s err=%s", errCase.err, code, out.String(), errs.String())
		}
	}

	// Close error -> exit 5, stdout empty
	rClose := &commandReader{data: map[string][]byte{}, closeErr: errors.New("SECRET")}
	out.Reset()
	errs.Reset()
	if code := runRestore(context.Background(), args, &out, &errs, func(string) (controlReader, error) { return rClose, nil }, clock, func(context.Context, string, backup.ControlledRestoreParams, backup.RestoreRecordReader, func() time.Time) (backup.ControlledRestoreResult, error) {
		return backup.ControlledRestoreResult{StatusCode: "RESTORE_VALIDATING_PUBLISHED"}, nil
	}); code != 5 || out.Len() != 0 || strings.Contains(errs.String(), "SECRET") {
		t.Fatalf("close error leaked or wrong exit: code=%d", code)
	}
}
