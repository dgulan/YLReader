// sandboxworker is a dedicated, offline, OS-isolated process; never run inside API.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	_ "time/tzdata" // scratch images must honor the same explicit TZ as the API.

	"yreader/backend/internal/engine/sandbox"
)

func main() {
	path := flag.String("socket", "", "absolute private Unix socket path")
	flag.Parse()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if flag.NArg() != 0 || sandbox.ServeWorker(ctx, *path, func() { os.Exit(124) }) != nil {
		fmt.Fprintln(os.Stderr, "isolated sandbox worker unavailable")
		os.Exit(1)
	}
}
