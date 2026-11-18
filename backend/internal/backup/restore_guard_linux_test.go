//go:build linux

package backup

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"yreader/backend/internal/pkg/storage"
)

func guardFixture(t *testing.T) (Config, *VerifiedPackage, Plan, TargetInspector, time.Time) {
	t.Helper()
	cleanPG(t)
	ctx := context.Background()
	c := localConfig(t)
	scratch, e := storage.OpenBackupFiles(c.Temporary.Root)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { scratch.Close() })
	m, entries := packageFixture(t)
	c.Source.InstanceID = m.SourceID
	id := identityFixture(t)
	cipher, options := encryptFixture(t, tarFixture(t, m, entries, nil), id)
	c.Limits = options.Limits
	pkg, e := VerifyPackage(ctx, bytes.NewReader(cipher), id, options, scratch)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := pkg.Close(); e != nil {
			t.Error(e)
		}
	})
	inspect := inspectorFunc(func(context.Context, *pgx.ConnConfig) error { return nil })
	now := time.Now().UTC().Truncate(time.Microsecond)
	p, e := PreparePlan(ctx, c, pkg.Binding(), "", now, inspect)
	if e != nil {
		t.Fatal(e)
	}
	return c, pkg, p, inspect, now
}

func TestRestoreGuardRecordedStage(t *testing.T) {
	c, pkg, p, inspect, now := guardFixture(t)
	ctx := context.Background()
	g, e := acquireRestoreGuard(ctx, p, c, pkg, c.Target.InstanceID, now, inspect)
	if e != nil {
		t.Fatal(e)
	}
	defer g.Close()
	if _, e = acquireRestoreGuard(ctx, p, c, pkg, c.Target.InstanceID, now, inspect); !errors.Is(e, storage.ErrBusy) {
		t.Fatal("second attempt entered", e)
	}
	stage, op, e := g.StageRecorded(ctx, pkg, "50000000-0000-4000-8000-000000000001", c.Target.InstanceID, now, inspect)
	if e != nil || stage == nil || op.Phase != "validating" || op.Generation != 3 {
		t.Fatal("recorded staging", op, e)
	}
	defer stage.Close()
	receipt, _, e := (attemptStore{Files: g.locations.files[3]}).Load(ctx, p.OperationID)
	if e != nil || receipt.Generation == nil || *receipt.Generation != stage.Reference() || receipt.DatabaseOutcome != "not_started" {
		t.Fatal("generation not registered", receipt, e)
	}
	// Expired repeat is a status lookup, not new authority or takeover.
	duplicate, current, e := g.StageRecorded(ctx, pkg, "50000000-0000-4000-8000-000000000002", c.Target.InstanceID, now.Add(time.Hour), inspect)
	if e != nil || duplicate != nil || current.Generation != op.Generation || current.OwnerID != op.OwnerID {
		t.Fatal("replayed operation", current, e)
	}
	if e = g.Close(); e != nil {
		t.Fatal(e)
	}
	r, e := stage.Open(ctx, pkg.manifest.Objects[0].Key)
	if e != nil {
		t.Fatal("guard close cleaned objects", e)
	}
	r.Close()
}

func TestRestoreGuardRejectsDriftAndPolicy(t *testing.T) {
	c, pkg, p, inspect, now := guardFixture(t)
	ctx := context.Background()
	changedC, changedP := c, p
	changedC.Limits.Object--
	changedP.Limits = changedC.Limits
	changedP.Digest, _ = changedP.digest()
	if _, e := acquireRestoreGuard(ctx, changedP, changedC, pkg, c.Target.InstanceID, now, inspect); !errors.Is(e, ErrPlan) {
		t.Fatal("verification policy drift", e)
	}
	if _, e := acquireRestoreGuard(ctx, p, c, pkg, c.Target.InstanceID, now.Add(15*time.Minute), inspect); !errors.Is(e, ErrPlan) {
		t.Fatal("expired guard", e)
	}
	g, e := acquireRestoreGuard(ctx, p, c, pkg, c.Target.InstanceID, now, inspect)
	if e != nil {
		t.Fatal(e)
	}
	defer g.Close()
	path := filepath.Join(c.Target.Storage.Root, c.Target.Storage.Namespace)
	if e = os.Rename(path, path+"-moved"); e != nil {
		t.Fatal(e)
	}
	if e = os.Mkdir(path, 0700); e != nil {
		t.Fatal(e)
	}
	if e = g.Recheck(ctx, c.Target.InstanceID, now, inspect); !errors.Is(e, ErrLocations) {
		t.Fatal("replaced path accepted", e)
	}
}
