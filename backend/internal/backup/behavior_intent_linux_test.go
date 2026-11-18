//go:build integration && linux

package backup

import (
	"bytes"
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"yreader/backend/internal/pkg/storage"
)

func TestBehaviorIntentSecureFiles(t *testing.T) {
	if os.Getenv("BACKUP_INTENT_FIXTURE") != "isolated-private-files" {
		t.Skip("explicit intent fixture required")
	}
	if os.Geteuid() != 10001 {
		t.Fatal("non-root fixture required")
	}
	for _, mode := range []string{"persist_reopen", "concurrent", "ack_lost", "readback_failure"} {
		t.Run(mode, func(t *testing.T) {
			prepared, base, p, clock := intentFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			root := t.TempDir()
			if os.Chmod(root, 0700) != nil {
				t.Fatal("permissions")
			}
			files, e := storage.OpenBackupFiles(root)
			if e != nil {
				t.Fatal(e)
			}
			defer func() {
				if files != nil {
					files.Close()
				}
			}()
			for key, data := range base.data {
				if e = files.CreateOnly(ctx, key, bytes.NewReader(data), int64(len(data))); e != nil {
					t.Fatal(e)
				}
			}
			before := statusFixtureBytes(t, root)
			if mode == "concurrent" {
				var wg sync.WaitGroup
				results := make(chan error, 8)
				for i := 0; i < 8; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						_, err := recordBehaviorIntent(ctx, files, prepared, p, prepared.consent.RuntimeSHA, clock)
						results <- err
					}()
				}
				wg.Wait()
				close(results)
				wins := 0
				for err := range results {
					if err == nil {
						wins++
					} else if !errors.Is(err, storage.ErrConflict) {
						t.Fatal(err)
					}
				}
				if wins != 1 {
					t.Fatal("one durable winner required")
				}
			} else {
				var writer storage.ConditionalFiles = files
				if mode == "ack_lost" {
					writer = &intentFaultFiles{ConditionalFiles: files, mode: "ack_lost"}
				}
				if mode == "readback_failure" {
					writer = &intentFaultFiles{ConditionalFiles: files, mode: "readback"}
				}
				_, e = recordBehaviorIntent(ctx, writer, prepared, p, prepared.consent.RuntimeSHA, clock)
				if mode == "persist_reopen" && e != nil {
					t.Fatal(e)
				}
				if mode != "persist_reopen" && e != errBehaviorIntentUnknown {
					t.Fatal("fault must retain unknown intent")
				}
			}
			if e = files.Close(); e != nil {
				t.Fatal(e)
			}
			files = nil
			files, e = storage.OpenBackupFiles(root)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = recordBehaviorIntent(ctx, files, prepared, p, prepared.consent.RuntimeSHA, clock); e != storage.ErrConflict {
				t.Fatal("reopened intent allowed replay")
			}
			d := DiagnoseBehaviorIntent(ctx, files, p.OperationID, clock())
			if d.Status != "outcome_unknown" || d.Persistence != "not_inferred" || d.Dispatch || d.Retry {
				t.Fatal("reopened record inferred outcome")
			}
			after := statusFixtureBytes(t, root)
			for path, hash := range before {
				if after[path] != hash {
					t.Fatal("existing control record content/mode changed")
				}
			}
		})
	}
	t.Log(`BACKUP_INTENT_EVIDENCE {"scope":"isolated-behavior-intent-fixture","checks":4,"existing_control_content_and_modes_unchanged":true,"network_used":false,"execution_authorized":false}`)
}
