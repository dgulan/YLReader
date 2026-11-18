//go:build integration && linux

package backup

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"yreader/backend/internal/pkg/storage"
)

func TestHandoverRegistrationFiles(t *testing.T) {
	for _, mode := range []string{"success", "concurrent", "state_ack", "receipt_ack", "stale"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			if os.Chmod(root, 0700) != nil {
				t.Fatal("fixture permissions")
			}
			files, e := storage.OpenBackupFiles(root)
			if e != nil {
				t.Fatal(e)
			}
			defer func() {
				if files != nil {
					_ = files.Close()
				}
			}()
			p, r, clock := registrationFixture(t, files)
			switch mode {
			case "success":
				reg, e := registerIsolatedReview(context.Background(), files, p, r, clock)
				if e != nil {
					t.Fatal(e)
				}
				if files.Close() != nil {
					t.Fatal("fixture close")
				}
				files, e = storage.OpenBackupFiles(root)
				if e != nil {
					t.Fatal(e)
				}
				loaded, _, e := (handoverRegistrationStore{files}).Load(context.Background(), p.OperationID)
				if e != nil || loaded != reg {
					t.Fatal("registration reopen failed", e)
				}
				op, sha, e := (Control{files}).Load(context.Background(), p.OperationID)
				if e != nil || op.Phase != "validating" || op.Generation != reg.NextGeneration || sha != reg.NextSHA {
					t.Fatal("wrong reopened state")
				}
			case "concurrent":
				var wg sync.WaitGroup
				results := make(chan error, 2)
				for i := 0; i < 2; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						_, e := registerIsolatedReview(context.Background(), files, p, r, clock)
						results <- e
					}()
				}
				wg.Wait()
				close(results)
				wins := 0
				for e := range results {
					if e == nil {
						wins++
					}
				}
				if wins != 1 {
					t.Fatal("durable registration must have one winner")
				}
			case "state_ack", "receipt_ack":
				fault := &registrationFaultFiles{ConditionalFiles: files, mode: mode}
				if _, e := registerIsolatedReview(context.Background(), fault, p, r, clock); e != errRegistrationUnknown {
					t.Fatal("lost acknowledgement reported success", e)
				}
				op, _, e := (Control{files}).Load(context.Background(), p.OperationID)
				if e != nil || op.Generation != r.Generation+1 || op.Phase != "validating" {
					t.Fatal("actual CAS not retained")
				}
				reg, _, e := (handoverRegistrationStore{files}).Load(context.Background(), p.OperationID)
				want := "prepared"
				if mode == "receipt_ack" {
					want = "registered"
				}
				if e != nil || reg.State != want {
					t.Fatal("wrong retained receipt")
				}
				writes := fault.stateCAS
				if _, e := registerIsolatedReview(context.Background(), fault, p, r, clock); e == nil || fault.stateCAS != writes {
					t.Fatal("unknown replay wrote state")
				}
			case "stale":
				if _, e := registerIsolatedReview(context.Background(), files, p, r, func() time.Time { return r.Expires }); e != errRegistration {
					t.Fatal("expired report accepted")
				}
				key, _ := registrationKey(p.OperationID)
				reader, e := files.Get(context.Background(), key)
				if reader != nil {
					reader.Close()
				}
				if !errors.Is(e, storage.ErrNotFound) {
					t.Fatal("expired report wrote receipt")
				}
			}
		})
	}
}
