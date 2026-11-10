//go:build integration

package api_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"yreader/backend/internal/engine/fetcher"
	"yreader/backend/internal/engine/sandbox"
	"yreader/backend/internal/repository/testutil"
	"yreader/backend/internal/service"
)

// TestSearchQueueTimeout 覆盖 M1b-2 P1 修补：并发信号量/限流排队超时的源
// 不得静默丢失，必须全部进入 failures 且携带 context.DeadlineExceeded。
func TestSearchQueueTimeout(t *testing.T) {
	pool := testutil.TestPool(t)
	userID, _, _ := seedUser(t, pool)

	rules := `{"search":{"searchUrl":"https://mock.example/search?q={{key}}","bookList":".item","name":".name","bookUrl":"a@href"}}`
	ids := make([]uuid.UUID, 0, 12)
	for i := 0; i < 12; i++ {
		ids = append(ids, seedSource(t, pool, userID, fmt.Sprintf("https://mock%d.example", i), rules))
	}

	sb := sandbox.New(sandbox.Config{ScriptTimeout: 300 * time.Millisecond}, nil)
	svc := service.NewSearchService(pool, blockingFetcher{}, sb)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	out, err := svc.Search(ctx, userID, "kw", ids, 1, 20)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(out.Failures) != len(ids) {
		t.Fatalf("failures=%d want %d (%v)", len(out.Failures), len(ids), out.Failures)
	}
	deadline := 0
	for _, f := range out.Failures {
		if errors.Is(f.Err, context.DeadlineExceeded) {
			deadline++
		}
	}
	// 10 个源占满信号量后阻塞至 ctx 到期，其余 2 个在排队阶段直接超时。
	if deadline < 2 {
		t.Fatalf("expected >=2 DeadlineExceeded failures, got %d (%v)", deadline, out.Failures)
	}
}

// blockingFetcher 模拟「上游迟迟不返回」的书源，直到 ctx 到期才返回。
type blockingFetcher struct{}

func (blockingFetcher) Get(ctx context.Context, _ string, _ http.Header) (*fetcher.Response, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
