// Command backup provides read-only diagnostics and authenticated previews.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"yreader/backend/internal/backup"
	"yreader/backend/internal/pkg/storage"
)

type controlReader interface {
	backup.RestoreRecordReader
	Close() error
}
type openControl func(string) (controlReader, error)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, func(root string) (controlReader, error) { return storage.OpenBackupFiles(root) }, func() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }))
}

func run(ctx context.Context, args []string, out, errs io.Writer, open openControl, clock func() time.Time) int {
	if len(args) > 0 && (args[0] == "inspect" || args[0] == "plan") {
		return runPreview(ctx, args, out, errs, open, clock)
	}
	usage := func() int {
		fmt.Fprintln(errs, "usage: backup status|behavior-status --control-root ABSOLUTE_PATH --operation UUID [--format json|text]")
		return 2
	}
	if (len(args) != 5 && len(args) != 7) || (args[0] != "status" && args[0] != "behavior-status") {
		return usage()
	}
	values := map[string]string{}
	for i := 1; i < len(args); i += 2 {
		name, value := args[i], args[i+1]
		if (name != "--control-root" && name != "--operation" && name != "--format") || value == "" {
			return usage()
		}
		if _, seen := values[name]; seen {
			return usage()
		}
		values[name] = value
	}
	root, id := values["--control-root"], values["--operation"]
	format := values["--format"]
	if format == "" {
		format = "json"
	}
	u, e := uuid.Parse(id)
	if root == "" || len(root) > 4096 || !filepath.IsAbs(root) || filepath.Clean(root) != root || e != nil || u.String() != id || (format != "json" && format != "text") {
		return usage()
	}
	for _, c := range root {
		if c < 32 || c == 127 {
			return usage()
		}
	}
	if ctx == nil || open == nil || clock == nil {
		fmt.Fprintln(errs, "status unavailable")
		return 3
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	reader, e := open(root)
	if e != nil || reader == nil {
		fmt.Fprintln(errs, "safe control directory unavailable or unsupported")
		return 3
	}
	if args[0] == "behavior-status" {
		return renderBehaviorStatus(ctx, reader, id, clock(), format, out, errs)
	}
	status := backup.DiagnoseRestoreStatus(ctx, reader, id, clock())
	if reader.Close() != nil {
		fmt.Fprintln(errs, "control directory close unconfirmed")
		return 5
	}
	if format == "json" {
		e = json.NewEncoder(out).Encode(status)
	} else {
		_, e = fmt.Fprintf(out, "诊断状态: %s\n原因: %s\n操作版本: %d\n操作阶段: %s\n报告声明: %s\n报告时效: %s\n登记记录: %s\n版本链: %s\n持久确认: 无法从磁盘推断\n允许切换/部署: 否\n", status.Status, status.Reason, status.Generation, status.Phase, status.ReportClaims, status.ReportFreshness, status.Registration, status.Chain)
	}
	if e != nil {
		fmt.Fprintln(errs, "status output failed")
		return 5
	}
	if status.Status == "observed_registered" {
		return 0
	}
	return 4
}
