package backup

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func cleanPG(t *testing.T) {
	t.Helper()
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(strings.ToUpper(key), "PG") {
			key, value := key, value
			os.Unsetenv(key)
			t.Cleanup(func() { os.Setenv(key, value) })
		}
	}
}

func TestTargetGate(t *testing.T) {
	cleanPG(t)
	good := "postgres://reader:synthetic@127.0.0.1:55439/yreader_a1_iso_backup?sslmode=disable"
	for _, uri := range []string{good, strings.Replace(good, "127.0.0.1", "localhost", 1), strings.Replace(good, "127.0.0.1", "[::1]", 1)} {
		cfg, e := ParseTarget(uri)
		if e != nil {
			t.Fatal(e)
		}
		if cfg.Host == "localhost" {
			t.Fatal("DNS hostname was not pinned")
		}
	}
	for _, uri := range []string{
		strings.Replace(good, "127.0.0.1", "example.com", 1), strings.Replace(good, ":55439", "", 1),
		strings.Replace(good, "yreader_a1_iso_backup", "yreader", 1), strings.Replace(good, "yreader_a1_iso_backup", "yreader_test", 1),
		strings.Replace(good, "reader:synthetic@", "reader@", 1), good + "&host=10.0.0.1", good + "&options=-csearch_path=evil", good + "&sslmode=disable", good + "#fragment",
		"host=127.0.0.1 port=55439 dbname=yreader_a1_iso_backup", strings.Replace(good, ":55439", ":0", 1), strings.Replace(good, ":55439", ":65536", 1),
		strings.Replace(good, "/yreader_a1_iso_backup", "/yreader_a1_iso_backup/other", 1), good + "&service=elsewhere", good + "&passfile=elsewhere",
	} {
		if _, e := ParseTarget(uri); !errors.Is(e, ErrTargetDenied) || strings.Contains(e.Error(), "synthetic") {
			t.Fatalf("unsafe target accepted or leaked: %v", e)
		}
	}
}

func TestAmbientPGOverridesDenied(t *testing.T) {
	cleanPG(t)
	for _, key := range []string{"PGHOST", "PGHOSTADDR", "PGSERVICE", "PGOPTIONS", "PGPASSWORD", "PGPASSFILE", "PGUSER", "PGSSLROOTCERT"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, "synthetic")
			if _, e := ParseTarget("postgres://reader:synthetic@127.0.0.1:55439/yreader_a1_iso_backup"); !errors.Is(e, ErrTargetDenied) {
				t.Fatal("environment override allowed")
			}
		})
	}
}

func TestSyncTerminateConnWaitsForPeerEOF(t *testing.T) {
	cleanPG(t)
	cfg, e := ParseTarget("postgres://reader:synthetic@127.0.0.1:55439/yreader_a1_iso_backup?sslmode=disable")
	if e != nil || cfg.AfterNetConnect == nil {
		t.Fatal("missing AfterNetConnect wrapper", e)
	}
	client, server := net.Pipe()
	wrapped, e := cfg.AfterNetConnect(context.Background(), &cfg.Config, client)
	if e != nil {
		t.Fatal(e)
	}
	var peerExited atomic.Bool
	go func() {
		var buf [5]byte
		_, _ = server.Read(buf[:])
		time.Sleep(15 * time.Millisecond)
		peerExited.Store(true)
		_ = server.Close()
	}()
	if _, e := wrapped.Write([]byte{'X', 0, 0, 0, 4}); e != nil {
		t.Fatal(e)
	}
	if e := wrapped.Close(); e != nil || !peerExited.Load() {
		t.Fatal("Close returned before peer exited after Terminate", e)
	}
	// Repeat Close must be idempotent.
	if e := wrapped.Close(); e != nil {
		t.Fatal(e)
	}
}

func TestPlanStageErrorRedactionAndUnwrap(t *testing.T) {
	staged := planStageError("target_not_exclusive")
	if !errors.Is(staged, ErrPlan) || !strings.Contains(staged.Error(), "target_not_exclusive") {
		t.Fatal("stage error did not unwrap to ErrPlan", staged)
	}
	preserved := preservePlanError(staged, "target_check")
	if !errors.Is(preserved, ErrPlan) || preserved.Error() != staged.Error() {
		t.Fatal("known plan stage lost", preserved)
	}
	redacted := preservePlanError(errors.New("postgres://secret-dsn"), "target_check")
	if !errors.Is(redacted, ErrPlan) || strings.Contains(redacted.Error(), "secret-dsn") || !strings.Contains(redacted.Error(), "target_check") {
		t.Fatal("raw inspector error leaked", redacted)
	}
	targetStaged := targetDeniedStageError("publication_db_not_exclusive")
	if !errors.Is(targetStaged, ErrTargetDenied) || !strings.Contains(targetStaged.Error(), "publication_db_not_exclusive") {
		t.Fatal("target denied stage error did not unwrap to ErrTargetDenied", targetStaged)
	}
}
