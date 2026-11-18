package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"yreader/backend/internal/backup"
)

func runPreview(ctx context.Context, args []string, out, errs io.Writer, open openControl, clock func() time.Time) int {
	usage := func() int {
		fmt.Fprintln(errs, "usage: backup inspect|plan --input-root ABSOLUTE_PATH --request-key KEY [--config-key KEY (plan only)] [--format json|text]")
		return 2
	}
	if len(args) < 5 || len(args)%2 != 1 {
		return usage()
	}
	values := map[string]string{}
	for i := 1; i < len(args); i += 2 {
		k, v := args[i], args[i+1]
		if v == "" || (k != "--input-root" && k != "--request-key" && k != "--format" && (k != "--config-key" || args[0] != "plan")) {
			return usage()
		}
		if _, ok := values[k]; ok {
			return usage()
		}
		values[k] = v
	}
	root := values["--input-root"]
	format := values["--format"]
	if format == "" {
		format = "json"
	}
	if root == "" || len(root) > 4096 || !filepath.IsAbs(root) || filepath.Clean(root) != root || values["--request-key"] == "" || (args[0] == "plan" && values["--config-key"] == "") || (format != "json" && format != "text") {
		return usage()
	}
	for _, v := range values {
		for _, c := range v {
			if c < 32 || c == 127 {
				return usage()
			}
		}
	}
	if ctx == nil || open == nil || clock == nil {
		fmt.Fprintln(errs, "preview unavailable")
		return 3
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	r, e := open(root)
	if e != nil || r == nil {
		fmt.Fprintln(errs, "safe input directory unavailable or unsupported")
		return 3
	}
	result, e := preview(ctx, args[0], root, values, r, clock())
	if r.Close() != nil {
		fmt.Fprintln(errs, "input directory close unconfirmed")
		return 5
	}
	if e != nil {
		fmt.Fprintln(errs, "package authentication or read-only preview rejected")
		return 4
	}
	if format == "json" {
		e = json.NewEncoder(out).Encode(result)
	} else {
		// The JSON body is already restricted to public preview fields.
		var data []byte
		data, e = json.MarshalIndent(result, "", "  ")
		if e == nil {
			_, e = fmt.Fprintf(out, "只读预览；执行/部署未获授权\n%s\n", data)
		}
	}
	if e != nil {
		fmt.Fprintln(errs, "preview output failed")
		return 5
	}
	return 0
}

func preview(ctx context.Context, command, root string, values map[string]string, r controlReader, now time.Time) (any, error) {
	load := func(key string, limit int64) ([]byte, error) {
		// Validate before passing user-supplied keys even to injected readers.
		if !validPreviewKey(key) {
			return nil, backup.ErrPackageSchema
		}
		f, e := r.Get(ctx, key)
		if e != nil {
			return nil, e
		}
		data, e := io.ReadAll(io.LimitReader(f, limit+1))
		ce := f.Close()
		if e != nil || ce != nil || int64(len(data)) > limit {
			return nil, backup.ErrPackageSchema
		}
		return data, nil
	}
	data, e := load(values["--request-key"], 16<<10)
	if e != nil {
		return nil, e
	}
	request, e := backup.LoadInspectionRequest(bytes.NewReader(data))
	if e != nil {
		return nil, e
	}
	inspection, e := backup.InspectStoredPackage(ctx, r, request)
	if e != nil {
		return nil, e
	}
	if command == "inspect" {
		return inspection.Summary(), nil
	}
	data, e = load(values["--config-key"], 1<<20)
	if e != nil {
		return nil, e
	}
	c, e := backup.LoadConfig(bytes.NewReader(data))
	if e != nil {
		return nil, e
	}
	if !backup.CheckInspectionIdentity(root, request, c) {
		return nil, backup.ErrPackageSchema
	}
	plan, e := inspection.PreparePlan(ctx, c, now)
	if e != nil {
		return nil, e
	}
	return struct {
		Format        string      `json:"preview_format"`
		Version       int         `json:"preview_version"`
		Scope         string      `json:"scope"`
		Plan          backup.Plan `json:"plan"`
		Authenticated bool        `json:"package_authenticated"`
		Checked       bool        `json:"target_readonly_checked"`
		Execution     bool        `json:"execution_authorized"`
		Deployment    bool        `json:"deployment_authorized"`
	}{"ylreader-plan-preview", 1, "isolated-plan-preview", plan, true, true, false, false}, nil
}

func validPreviewKey(key string) bool {
	if key == "" || len(key) > 4096 || filepath.IsAbs(key) {
		return false
	}
	for _, c := range key {
		if c < 32 || c == 127 || c == '\\' || c == ':' {
			return false
		}
	}
	for _, p := range bytes.Split([]byte(key), []byte("/")) {
		if len(p) == 0 || string(p) == "." || string(p) == ".." {
			return false
		}
	}
	return true
}
