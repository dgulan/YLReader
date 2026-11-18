package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"yreader/backend/internal/backup"
)

func renderBehaviorStatus(ctx context.Context, reader controlReader, id string, now time.Time, format string, out, errs io.Writer) int {
	status := backup.DiagnoseBehaviorLedger(ctx, reader, id, now)
	if reader.Close() != nil {
		fmt.Fprintln(errs, "control directory close unconfirmed")
		return 5
	}
	var e error
	if format == "json" {
		e = json.NewEncoder(out).Encode(status)
	} else {
		_, e = fmt.Fprintf(out, "诊断状态: %s\n原因: %s\n派发记录: %d\n已知会话记录: %d\n登出响应声明: %d\n%s数据库撤销: 未核验\n历史持久确认: 无法推断\n允许派发/重试/部署: 否\n", status.Status, status.Reason, status.Dispatches, status.Known, status.LogoutClaims, formatBoundReviewText(status.BoundReview))
	}
	if e != nil {
		fmt.Fprintln(errs, "status output failed")
		return 5
	}
	boundOK := status.BoundReview.Status == "absent" || status.BoundReview.Status == "complete_unregistered" || status.BoundReview.Status == "complete_registered"
	if status.Status == "responses_observed" && boundOK {
		return 0
	}
	return 4
}
