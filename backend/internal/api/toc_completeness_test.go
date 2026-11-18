//go:build integration

package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"yreader/backend/internal/api"
	"yreader/backend/internal/engine/fetcher"
)

type tocIncompleteAPIFetcher struct {
	mock    *tocMock
	failure error
	failURL string
}

func (f *tocIncompleteAPIFetcher) Get(ctx context.Context, target string, h http.Header) (*fetcher.Response, error) {
	if target == f.failURL {
		return nil, f.failure
	}
	return f.mock.Get(ctx, target, h)
}
func TestTOCIncompleteHTTPAndInitialPublication(t *testing.T) {
	for _, kind := range []string{"network", "timeout", "empty", "cycle"} {
		t.Run(kind, func(t *testing.T) {
			const p1 = "https://toc-complete.example/book"
			const p2 = "https://toc-complete.example/page2"
			mock := newTOCMock(map[string]string{p1: tocPage(false, []string{"/a"}, p2), p2: tocPage(false, []string{"/b", "/c"}, "")})
			ft := &tocIncompleteAPIFetcher{mock: mock}
			statusWant, codeWant := http.StatusUnprocessableEntity, "rule_execution_failed"
			switch kind {
			case "network":
				ft.failURL = p2
				ft.failure = fetcher.ErrUpstreamError
				statusWant, codeWant = http.StatusBadGateway, "source_request_failed"
			case "timeout":
				ft.failURL = p2
				ft.failure = context.DeadlineExceeded
				statusWant, codeWant = http.StatusGatewayTimeout, "source_timeout"
			case "empty":
				mock.Set(p2, "<body>无目录</body>")
			case "cycle":
				mock.Set(p2, tocPage(false, []string{"/b", "/c"}, p1))
			}
			ts, pool := newTestServerOpt(t, ft, func(deps *api.Deps) {
				deps.TOC = deps.TOC.WithSleep(func(context.Context, time.Duration) error { return nil })
			})
			base := ts.URL + "/api/v1"
			user, username, password := seedUser(t, pool)
			token := login(t, base, username, password)
			src := seedSource(t, pool, user, "https://toc-complete.example", tocRulesJSON("ul.list li", "a@text", "a@href", "", "a.next@href"))
			_, book := createBook(t, base, token, "分页发布验收", src, p1)
			status, raw := doJSON(t, http.MethodGet, base+"/books/"+book.ID.String()+"/chapters", token, nil)
			var failure struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(raw, &failure); err != nil {
				t.Fatal(err)
			}
			if status != statusWant || failure.Error.Code != codeWant || strings.Contains(string(raw), "toc-complete.example") {
				t.Fatalf("status=%d code=%s", status, failure.Error.Code)
			}
			if n := dbInt(t, pool, `SELECT count(*) FROM chapters WHERE book_id=$1`, book.ID); n != 0 {
				t.Fatal("initial fetch stored partial chapters", n)
			}
			var synced *time.Time
			if err := pool.QueryRow(context.Background(), `SELECT last_synced_at FROM book_source_mappings WHERE book_id=$1 AND source_id=$2`, book.ID, src).Scan(&synced); err != nil || synced != nil {
				t.Fatal("failed fetch marked synced", err)
			}
		})
	}
}
