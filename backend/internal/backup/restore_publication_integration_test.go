//go:build integration && linux

package backup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	headercrypto "yreader/backend/internal/pkg/crypto"
	"yreader/backend/internal/pkg/storage"
	"yreader/backend/internal/repository"
)

type publicationFaultFiles struct {
	storage.BackupFiles
	mode   string
	writes int
}

func (f *publicationFaultFiles) CreateOnly(ctx context.Context, key string, r io.Reader, size int64) error {
	if strings.HasSuffix(key, "/publication.json") && f.mode == "intent" {
		f.writes++
		return storage.ErrCommitUnknown
	}
	return f.BackupFiles.CreateOnly(ctx, key, r, size)
}
func (f *publicationFaultFiles) CompareAndSwap(ctx context.Context, key, token string, data []byte) error {
	if strings.HasSuffix(key, "/publication.json") {
		var p publicationReceipt
		if exactJSON(data, &p) == nil && (p.State == "verified" && (f.mode == "fail" || f.mode == "ack") || p.State == "copying" && f.mode == "copying" || p.State == "publishing" && f.mode == "publishing") {
			f.writes++
			if f.mode == "ack" {
				if e := f.BackupFiles.CompareAndSwap(ctx, key, token, data); e != nil {
					return e
				}
			}
			return storage.ErrCommitUnknown
		}
	}
	return f.BackupFiles.CompareAndSwap(ctx, key, token, data)
}

func assertPublicationIntegration(t *testing.T, g *restoreGuard, stage *stagedObjects, c Config, p Plan, owner, raw string, ring *headercrypto.Keyring, mode string) {
	t.Helper()
	ctx := context.Background()
	var faults *publicationFaultFiles
	if mode != "" {
		faults = &publicationFaultFiles{BackupFiles: g.locations.files[3], mode: mode}
		g.locations.files[3] = faults
	}
	result, e := g.PublishObjects(ctx, stage, owner, c.Target.InstanceID, time.Now().UTC())
	if mode == "" {
		if e != nil || !result.Executed || result.Receipt.State != "verified" {
			t.Fatal("publication failed", e)
		}
	} else if !errors.Is(e, ErrPublication) {
		t.Fatal("uncertain publication reported success", e)
	}
	store := publicationStore{Files: g.locations.files[3]}
	receipt, _, readErr := store.Load(ctx, p.OperationID)
	if mode == "intent" {
		if !errors.Is(readErr, storage.ErrNotFound) {
			t.Fatal("failed intent created publication", readErr)
		}
		loc, _ := stage.temp.(locatedFiles).Location(ctx)
		if e := g.locations.files[1].(storage.RestoreDirectory).OnlyDirectory(ctx, stage.Reference(), loc.Identity); e != nil {
			t.Fatal("intent failure wrote tree", e)
		}
	} else {
		if readErr != nil {
			t.Fatal(readErr)
		}
		want := "verified"
		if mode == "fail" {
			want = "publishing"
		}
		if mode == "copying" {
			want = "planned"
		}
		if mode == "publishing" {
			want = "copying"
		}
		if receipt.State != want {
			t.Fatal("wrong publication receipt", receipt.State)
		}
		if mode == "copying" || mode == "publishing" {
			pending := filepath.Join(c.Target.Storage.Root, c.Target.Storage.Namespace, receipt.Pending)
			entries, e := os.ReadDir(pending)
			if e != nil || (mode == "copying" && len(entries) != 0) || (mode == "publishing" && len(entries) == 0) {
				t.Fatal("write/rename preceded durable state", e)
			}
			if _, e = os.Stat(filepath.Join(c.Target.Storage.Root, c.Target.Storage.Namespace, receipt.Output)); !os.IsNotExist(e) {
				t.Fatal("unconfirmed tree exposed final name", e)
			}
		} else {
			root, e := g.locations.files[1].OpenDirectory(ctx, receipt.Output, false)
			if e != nil {
				t.Fatal("renamed data not retained", e)
			}
			r, e := root.Get(ctx, "local/20000000-0000-4000-8000-000000000002/0.txt")
			if e != nil {
				t.Fatal(e)
			}
			body, e := io.ReadAll(r)
			r.Close()
			root.Close()
			if e != nil || !bytes.Contains(body, []byte("恢复正文 2 中文 😀")) {
				t.Fatal("published bytes wrong", e)
			}
		}
	}
	if stage.Reference() == "" {
		t.Fatal("original staging deleted")
	}
	repeat, repeatErr := g.PublishObjects(ctx, stage, owner, c.Target.InstanceID, time.Now().UTC().Add(time.Hour))
	if repeat.Executed {
		t.Fatal("publication replayed")
	}
	if mode == "fail" || mode == "intent" || mode == "copying" || mode == "publishing" {
		if !errors.Is(repeatErr, ErrPublication) {
			t.Fatal("pending publication resumed", repeatErr)
		}
	} else if repeatErr != nil || repeat.Receipt.State != "verified" {
		t.Fatal("verified status repeat", repeatErr)
	}
	if faults != nil && faults.writes != 1 {
		t.Fatal("uncertain write retried", faults.writes)
	}
	op, _, e := (Control{Files: g.locations.files[3]}).Load(ctx, p.OperationID)
	if e != nil || op.Phase != "validating" {
		t.Fatal("publication advanced service readiness", e)
	}
	if mode != "" {
		if mode == "ack" {
			s, e := stableHandoverSnapshot(ctx, g.locations.files[3], p.OperationID)
			if e != nil {
				t.Fatal(e)
			}
			if proof, e := g.probeHandoverData(ctx, stage, owner, c.Target.InstanceID, time.Now().UTC(), ring, g.verified.manifest.HeaderKIDs, s); e != errHandoverReview || proof.plan != "" {
				t.Fatal("lost publication acknowledgement granted data proof")
			}
		}
		return
	}
	// Read the same root with the existing application Provider and Repository.
	provider, e := storage.NewLocalFS(filepath.Join(c.Target.Storage.Root, c.Target.Storage.Namespace, receipt.Output))
	if e != nil {
		t.Fatal(e)
	}
	r, e := provider.Get(ctx, "local/20000000-0000-4000-8000-000000000001/0.txt")
	if e != nil {
		t.Fatal(e)
	}
	body, e := io.ReadAll(r)
	r.Close()
	if e != nil || !bytes.Contains(body, []byte("恢复正文 1 中文 😀")) {
		t.Fatal("application storage read", e)
	}
	pool := newIsolatedTargetPool(t, ctx, raw)
	defer pool.Close()
	old := headercrypto.DefaultKeyring()
	headercrypto.SetDefaultKeyring(ring)
	defer headercrypto.SetDefaultKeyring(old)
	source, e := repository.NewSourceRepository().Get(ctx, pool, uuid.MustParse("00000000-0000-4000-8000-000000000001"), uuid.MustParse("10000000-0000-4000-8000-000000000001"))
	if e != nil || !bytes.Contains(source.Header, []byte("synthetic-private-token")) {
		t.Fatal("application header read failed")
	}
}
