//go:build integration && linux

package backup

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"yreader/backend/internal/service"
)

func assertGeminiConcurrentRefresh(t *testing.T, pool *pgxpool.Pool, key []byte, username, password string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	auth := service.NewAuthService(pool, key, time.Minute, time.Hour)
	pair, err := auth.Login(ctx, username, password)
	if err != nil {
		t.Fatal("synthetic concurrent login")
	}
	var results [2]*service.TokenPair
	var failures [2]error
	var group sync.WaitGroup
	start := make(chan struct{})
	for i := range results {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			<-start
			results[i], failures[i] = auth.Refresh(ctx, pair.RefreshToken)
		}(i)
	}
	close(start)
	group.Wait()
	success := 0
	denied := 0
	issued := []string{pair.RefreshToken}
	for i, result := range results {
		if result != nil && failures[i] == nil {
			issued = append(issued, result.RefreshToken)
			success++
			if _, e := auth.Refresh(ctx, result.RefreshToken); !errors.Is(e, service.ErrUnauthorized) {
				t.Fatal("concurrent replay left replacement usable")
			}
		}
		if errors.Is(failures[i], service.ErrUnauthorized) {
			denied++
		}
	}
	if success != 1 || denied != 1 {
		t.Fatal("unexpected concurrent refresh outcomes", success, denied)
	}
	return issued
}
