package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"yreader/backend/internal/pkg/storage"
)

type memoryFiles struct {
	mu   sync.Mutex
	data map[string][]byte
}

func (m *memoryFiles) Get(_ context.Context, k string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.data[k]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(append([]byte(nil), b...))), nil
}
func (m *memoryFiles) CreateOnly(_ context.Context, k string, r io.Reader, _ int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.data[k]; ok {
		return storage.ErrConflict
	}
	b, e := io.ReadAll(r)
	m.data[k] = b
	return e
}
func (m *memoryFiles) CompareAndSwap(_ context.Context, k, token string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	sum := sha256.Sum256(m.data[k])
	if hex.EncodeToString(sum[:]) != token {
		return storage.ErrConflict
	}
	m.data[k] = append([]byte(nil), data...)
	return nil
}

func example(t *testing.T) Operation {
	t.Helper()
	data, e := os.ReadFile("../../../docs/examples/restore-operation.json")
	if e != nil {
		t.Fatal(e)
	}
	var o Operation
	if e = json.Unmarshal(data, &o); e != nil {
		t.Fatal(e)
	}
	return o
}
func advance(o Operation, phase string) Operation {
	o.Generation++
	o.Phase = phase
	o.UpdatedAt = o.UpdatedAt.Add(time.Second)
	o.HeartbeatAt = o.UpdatedAt
	if terminal(phase) {
		end := o.UpdatedAt
		o.FinishedAt = &end
	}
	return o
}

func TestControlLifecycle(t *testing.T) {
	ctx := context.Background()
	c := Control{&memoryFiles{data: map[string][]byte{}}}
	o := example(t)
	if e := c.Create(ctx, o); e != nil {
		t.Fatal(e)
	}
	if e := c.Create(ctx, o); !errors.Is(e, storage.ErrConflict) {
		t.Fatal("duplicate created")
	}
	for _, phase := range []string{"staging", "validating", "ready_for_cutover", "completed"} {
		next := advance(o, phase)
		if e := c.Update(ctx, o.ID, o.Generation, next, false); e != nil {
			t.Fatal(e)
		}
		o = next
	}
	if _, _, e := c.Load(ctx, o.ID); e != nil {
		t.Fatal(e)
	}
	if e := c.Update(ctx, o.ID, o.Generation, advance(o, "completed"), false); !errors.Is(e, ErrState) {
		t.Fatal("terminal changed")
	}
	if o.Stale(o.HeartbeatAt.Add(time.Hour)) {
		t.Fatal("terminal reported stale")
	}
}

func TestControlConcurrentUpdates(t *testing.T) {
	ctx := context.Background()
	c := Control{&memoryFiles{data: map[string][]byte{}}}
	o := example(t)
	if e := c.Create(ctx, o); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	results := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- c.Update(ctx, o.ID, 1, advance(o, "staging"), false) }()
	}
	wg.Wait()
	close(results)
	successes := 0
	for e := range results {
		if e == nil {
			successes++
		} else if !errors.Is(e, storage.ErrConflict) {
			t.Fatal(e)
		}
	}
	if successes != 1 {
		t.Fatalf("writers succeeded: %d", successes)
	}
}

func TestStateFailureAndCancel(t *testing.T) {
	o := example(t)
	if o.Stale(o.HeartbeatAt.Add(60*time.Second)) || !o.Stale(o.HeartbeatAt.Add(61*time.Second)) {
		t.Fatal("stale boundary")
	}
	bad := advance(o, "validating")
	if ValidateTransition(o, bad, 1, false) == nil {
		t.Fatal("phase skip allowed")
	}
	request := advance(o, "planned")
	request.CancelRequested = true
	if e := ValidateTransition(o, request, 1, false); e != nil {
		t.Fatal(e)
	}
	if ValidateTransition(request, advance(request, "staging"), 2, false) == nil {
		t.Fatal("cancel progressed")
	}
	canceled := advance(request, "canceled")
	if e := ValidateTransition(request, canceled, 2, false); e != nil {
		t.Fatal(e)
	}
	failed := advance(o, "failed")
	code := "operation_interrupted"
	failed.FailureCode = &code
	if ValidateTransition(o, failed, 1, false) == nil {
		t.Fatal("timeout accepted as death")
	}
	if e := ValidateTransition(o, failed, 1, true); e != nil {
		t.Fatal(e)
	}
	changed := advance(o, "staging")
	changed.PlanDigest = strings.Repeat("f", 64)
	if ValidateTransition(o, changed, 1, false) == nil {
		t.Fatal("plan binding changed")
	}
}

func TestControlRejectsMalformedPersistedState(t *testing.T) {
	o := example(t)
	base, _ := json.Marshal(o)
	key, _ := operationKey(o.ID)
	variants := []string{string(base) + "{}", strings.Replace(string(base), `"cancel_requested":false`, `"cancel_requested":null`, 1), strings.Replace(string(base), `"generation":1`, `"generation":1,"generation":1`, 1), strings.Replace(string(base), `"generation":1`, `"generation":1,"unknown":1`, 1), strings.Replace(string(base), `"cancel_requested":false,`, "", 1), strings.Replace(string(base), "T08:00:00Z", "T08:00:00+00:00", 1), strings.Replace(string(base), `"owner_id"`, `"OWNER_ID"`, 1)}
	for _, data := range variants {
		c := Control{&memoryFiles{data: map[string][]byte{key: []byte(data)}}}
		if _, _, e := c.Load(context.Background(), o.ID); !errors.Is(e, ErrState) {
			t.Fatalf("malformed state accepted: %v", e)
		}
	}
}
