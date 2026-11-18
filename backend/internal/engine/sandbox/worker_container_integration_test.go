//go:build integration && linux

package sandbox

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"yreader/backend/internal/engine/fetcher"
)

type workerQuotaProbe struct {
	calls    atomic.Int32
	expected string
	t        *testing.T
}

func (p *workerQuotaProbe) Take(context.Context, string, time.Duration) (time.Duration, error) {
	return 0, nil
}
func (p *workerQuotaProbe) TakeSource(_ context.Context, _ string, key, _ string, _ int, _ time.Duration, _ time.Duration) (time.Duration, error) {
	p.calls.Add(1)
	if key != p.expected {
		p.t.Error("trusted source identity changed")
	}
	return 0, errors.New("synthetic quota denial")
}
func (*workerQuotaProbe) ReleaseSource(context.Context, string, string) error { return nil }

func TestWorkerContainerIntegration(t *testing.T) {
	path := os.Getenv("TEST_SANDBOX_WORKER_SOCKET")
	if path == "" {
		t.Skip("requires separately isolated worker container")
	}
	type ownerKey struct{}
	var requests, writes, blocked atomic.Int32
	id := uuid.NewString()
	quota := &workerQuotaProbe{expected: fmt.Sprintf("yreader:fetch:source:v1:%x", sha256.Sum256([]byte(id))), t: t}
	ft := fetcher.New(fetcher.Config{DomainLimiter: fetcher.NewDomainLimiter(quota)})
	client := workerHTTPFunc(func(ctx context.Context, method, url string, body []byte) ([]byte, error) {
		requests.Add(1)
		if ctx.Value(ownerKey{}) != "parent-owner" {
			t.Error("parent identity/context lost")
		}
		if strings.Contains(url, "/wait") {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		if strings.Contains(url, "/once") {
			writes.Add(1)
			if method != "POST" || string(body) != "write" {
				t.Error("POST changed")
			}
			return []byte("written"), nil
		}
		if strings.Contains(url, "quota.example") || strings.Contains(url, "127.0.0.1") {
			resp, err := ft.Fetch(ctx, method, url, nil, nil)
			if errors.Is(err, fetcher.ErrSSRFBlocked) {
				blocked.Add(1)
			}
			if err != nil {
				return nil, err
			}
			return resp.Body, nil
		}
		return []byte("parent-proxy"), nil
	})
	s, err := NewRemote(Config{}, client, []string{path})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), ownerKey{}, "parent-owner")
	if err := s.Probe(ctx); err != nil {
		t.Fatal("isolated profile/handshake", err)
	}
	t.Run("normal-and-data", func(t *testing.T) {
		out, err := s.Eval(ctx, `java.get("https://synthetic.invalid/data")`, nil)
		if err != nil || out != "parent-proxy" {
			t.Fatal(out, err)
		}
		v, err := s.EvalValue(ctx, `({n:1000000000,items:[1,true,null]})`, nil)
		if err != nil || v.(map[string]any)["n"] != int64(1000000000) {
			t.Fatal(v, err)
		}
	})
	t.Run("explicit-timezone", func(t *testing.T) {
		zoneName := os.Getenv("TEST_SANDBOX_WORKER_TZ")
		if zoneName == "" {
			zoneName = "UTC"
		}
		zone, err := time.LoadLocation(zoneName)
		if err != nil {
			t.Fatal(err)
		}
		_, offset := time.Unix(0, 0).In(zone).Zone()
		prefix := fmt.Sprintf("%d|", -offset/60)
		before := time.Now().In(zone).Format("2006-01-02-15")
		out, err := s.Eval(ctx, `new Date(0).getTimezoneOffset()+"|"+java.timeFormat("yyyy-MM-dd-HH")`, nil)
		after := time.Now().In(zone).Format("2006-01-02-15")
		if err != nil || (out != prefix+before && out != prefix+after) {
			t.Fatal("worker TZ mismatch", out, err)
		}
	})
	t.Run("ssrf-and-source-context", func(t *testing.T) {
		_, err := s.Eval(ctx, `java.ajax("http://127.0.0.1/private")`, nil)
		if !errors.Is(err, ErrRuleExecution) || blocked.Load() != 1 {
			t.Fatal("parent SSRF did not apply", err, blocked.Load())
		}
		_, err = s.Eval(fetcher.WithSource(ctx, id, "1"), `java.ajax("https://quota.example/")`, nil)
		if !errors.Is(err, ErrRuleExecution) || quota.calls.Load() != 1 {
			t.Fatal("parent source quota not applied", err, quota.calls.Load())
		}
	})
	t.Run("cancellation", func(t *testing.T) {
		short, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		_, err := s.Eval(short, `java.post("https://synthetic.invalid/wait","data")`, nil)
		if !errors.Is(err, ErrSandboxTimeout) {
			t.Fatal(err)
		}
		if out, err := s.Eval(ctx, `"after-cancel"`, nil); err != nil || out != "after-cancel" {
			t.Fatal(out, err)
		}
	})
	t.Run("proxy-count", func(t *testing.T) {
		before := requests.Load()
		_, err := s.Eval(ctx, `for(var i=0;i<65;i++){java.ajax("https://synthetic.invalid/data")}`, nil)
		if !errors.Is(err, ErrRuleExecution) || requests.Load()-before != 64 {
			t.Fatal(err, requests.Load()-before)
		}
	})
	t.Run("oom-no-post-replay-and-recovery", func(t *testing.T) {
		before := writes.Load()
		_, err := s.Eval(ctx, `java.post("https://synthetic.invalid/once","write"); "x".repeat(536870912)`, nil)
		if err == nil || writes.Load()-before != 1 {
			t.Fatal("OOM execution replayed or succeeded", err, writes.Load()-before)
		}
		until := time.Now().Add(10 * time.Second)
		for {
			out, err := s.Eval(ctx, `"after-oom"`, nil)
			if err == nil && out == "after-oom" {
				break
			}
			if time.Now().After(until) {
				t.Fatal("worker restart not recovered", out, err)
			}
			time.Sleep(100 * time.Millisecond)
		}
		if writes.Load()-before != 1 {
			t.Fatal("POST was repeated after crash")
		}
	})
}
