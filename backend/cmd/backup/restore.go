package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"yreader/backend/internal/backup"
)

type restoreExec func(ctx context.Context, root string, params backup.ControlledRestoreParams, r backup.RestoreRecordReader, clock func() time.Time) (backup.ControlledRestoreResult, error)

func runRestore(ctx context.Context, args []string, out, errs io.Writer, open openControl, clock func() time.Time, exec restoreExec) int {
	usage := func() int {
		fmt.Fprintln(errs, "usage: backup restore --input-root ABSOLUTE_PATH --request-key KEY --config-key KEY --plan-key KEY --plan-digest SHA256 --confirm-target UUID --owner UUID --header-keys ABSOLUTE_PATH --pg-restore ABSOLUTE_PATH --timeout DURATION [--handover-key KEY] [--format json|text]")
		return 2
	}
	if len(args) < 21 || len(args)%2 != 1 || args[0] != "restore" {
		return usage()
	}
	allowed := map[string]bool{
		"--input-root":     true,
		"--request-key":    true,
		"--config-key":     true,
		"--plan-key":       true,
		"--plan-digest":    true,
		"--confirm-target": true,
		"--owner":          true,
		"--header-keys":    true,
		"--pg-restore":     true,
		"--timeout":        true,
		"--handover-key":   true,
		"--format":         true,
	}
	values := map[string]string{}
	for i := 1; i < len(args); i += 2 {
		k, v := args[i], args[i+1]
		if !allowed[k] || v == "" {
			return usage()
		}
		if _, seen := values[k]; seen {
			return usage()
		}
		for _, c := range v {
			if c < 32 || c == 127 {
				return usage()
			}
		}
		values[k] = v
	}
	for _, req := range []string{"--input-root", "--request-key", "--config-key", "--plan-key", "--plan-digest", "--confirm-target", "--owner", "--header-keys", "--pg-restore", "--timeout"} {
		if values[req] == "" {
			return usage()
		}
	}
	format := values["--format"]
	if format == "" {
		format = "json"
	}
	if format != "json" && format != "text" {
		return usage()
	}
	root := values["--input-root"]
	headerKeys := values["--header-keys"]
	pgRestore := values["--pg-restore"]
	for _, abs := range []string{root, headerKeys, pgRestore} {
		if !safeAbsolutePath(abs) {
			return usage()
		}
	}
	timeout, e := time.ParseDuration(values["--timeout"])
	if e != nil || timeout < time.Second || timeout > 30*time.Minute {
		return usage()
	}
	targetUUID, te := uuid.Parse(values["--confirm-target"])
	ownerUUID, oe := uuid.Parse(values["--owner"])
	if te != nil || targetUUID.String() != values["--confirm-target"] || oe != nil || ownerUUID.String() != values["--owner"] || ownerUUID == uuid.Nil {
		return usage()
	}
	params := backup.ControlledRestoreParams{
		PlanDigest:     values["--plan-digest"],
		ConfirmTarget:  values["--confirm-target"],
		OwnerID:        values["--owner"],
		RequestKey:     values["--request-key"],
		ConfigKey:      values["--config-key"],
		PlanKey:        values["--plan-key"],
		HeaderKeysPath: headerKeys,
		PGRestorePath:  pgRestore,
		HandoverKey:    values["--handover-key"],
		Timeout:        timeout,
	}
	if !validHexDigest(params.PlanDigest) || !validPreviewKey(params.RequestKey) || !validPreviewKey(params.ConfigKey) || !validPreviewKey(params.PlanKey) {
		return usage()
	}
	if params.HandoverKey != "" && !validPreviewKey(params.HandoverKey) {
		return usage()
	}
	if params.Validate() != nil {
		return usage()
	}
	if ctx == nil || open == nil || clock == nil || exec == nil {
		fmt.Fprintln(errs, "restore unavailable")
		return 3
	}
	timedCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	reader, e := open(root)
	if e != nil || reader == nil {
		fmt.Fprintln(errs, "safe input directory unavailable or unsupported")
		return 3
	}
	res, execErr := exec(timedCtx, root, params, reader, clock)
	if reader.Close() != nil {
		fmt.Fprintln(errs, "input directory close unconfirmed")
		return 5
	}
	res = backup.SanitizeControlledRestoreResult(res, execErr)
	var writeErr error
	if format == "json" {
		writeErr = json.NewEncoder(out).Encode(res)
	} else {
		_, writeErr = fmt.Fprintf(out, "受控恢复状态码: %s\n阶段: %s\n结果: %s\n数据库状态: %s\n字段密钥核验: %s\n对象发布状态: %s\n交接审阅状态: %s\n版本登记状态: %s\n保留产物: %s\n允许重放: 否\n允许执行/切换/部署: 否\n",
			res.StatusCode, res.Phase, res.Outcome, res.DatabaseOutcome, yesNoCN(res.HeadersChecked), res.PublicationState, res.HandoverReviewStatus, res.RegistrationState, yesNoCN(res.ArtifactsRetained))
	}
	if writeErr != nil {
		fmt.Fprintln(errs, "restore output failed")
		return 5
	}
	if execErr == nil && (res.StatusCode == "RESTORE_VALIDATING_PUBLISHED" || res.StatusCode == "RESTORE_VALIDATING_REGISTERED") {
		return 0
	}
	fmt.Fprintln(errs, res.StatusCode)
	return 4
}

func safeAbsolutePath(p string) bool {
	if p == "" || len(p) > 4096 || !filepath.IsAbs(p) || filepath.Clean(p) != p || strings.HasPrefix(p, "//") || strings.HasPrefix(p, `\\`) {
		return false
	}
	for _, seg := range strings.Split(strings.ReplaceAll(p, `\`, "/"), "/") {
		if seg == ".." || seg == "." {
			return false
		}
	}
	return true
}

func validHexDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func yesNoCN(v bool) string {
	if v {
		return "是"
	}
	return "否"
}
