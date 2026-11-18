package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"yreader/backend/internal/engine/fetcher"
	"yreader/backend/internal/engine/sandbox"
	"yreader/backend/internal/repository"
)

type stubHealthStore struct {
	successCalls int
	lastElapsed  int32
	failureCalls int
	lastKind     string
	lastCode     string
	lastMessage  string
}

func (s *stubHealthStore) RecordSuccess(_ context.Context, _, _ uuid.UUID, elapsedMs int32) error {
	s.successCalls++
	s.lastElapsed = elapsedMs
	return nil
}

func (s *stubHealthStore) RecordFailure(_ context.Context, _, _ uuid.UUID, kind, errorCode, message string, _ time.Duration) error {
	s.failureCalls++
	s.lastKind = kind
	s.lastCode = errorCode
	s.lastMessage = message
	return nil
}

func TestClassifySourceFailure_SanitizedAndAccurate(t *testing.T) {
	secretURL := "https://secret-token@internal.example.com/private?key=abc"
	cases := []struct {
		name     string
		err      error
		wantKind string
		wantCode string
	}{
		{"timeout", fmt.Errorf("%w: %s", context.DeadlineExceeded, secretURL), repository.FailureKindTimeout, "source_timeout"},
		{"sandbox_timeout", fmt.Errorf("%w: %s", sandbox.ErrSandboxTimeout, secretURL), repository.FailureKindTimeout, "sandbox_timeout"},
		{"ssrf", fmt.Errorf("%w: %s", fetcher.ErrSSRFBlocked, secretURL), repository.FailureKindBlocked, "blocked_private_address"},
		{"challenge", fmt.Errorf("%w: %s", fetcher.ErrChallengeRequired, secretURL), repository.FailureKindBlocked, "source_request_failed"},
		{"rule", fmt.Errorf("%w: %s", sandbox.ErrRuleExecution, secretURL), repository.FailureKindRule, "rule_execution_failed"},
		{"unsupported", fmt.Errorf("%w: %s", sandbox.ErrUnsupportedRule, secretURL), repository.FailureKindUnsupported, "unsupported_rule"},
		{"auth_401", fmt.Errorf("%w: %s", &fetcher.HTTPStatusError{StatusCode: 401}, secretURL), repository.FailureKindAuth, "source_request_failed"},
		{"upstream", fmt.Errorf("%w: %s", fetcher.ErrUpstreamError, secretURL), repository.FailureKindNetwork, "source_request_failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, code, msg := ClassifySourceFailure(tc.err)
			if kind != tc.wantKind || code != tc.wantCode {
				t.Fatalf("got (%s, %s), want (%s, %s)", kind, code, tc.wantKind, tc.wantCode)
			}
			if strings.Contains(msg, "secret-token") || strings.Contains(msg, "internal.example.com") {
				t.Fatalf("message leaked sensitive url: %q", msg)
			}
		})
	}
}

func TestSourceHealthService_IgnoresCanceledAndRecordsOutcomes(t *testing.T) {
	store := &stubHealthStore{}
	svc := NewSourceHealthServiceWithStore(store, nil)
	uid, sid := uuid.New(), uuid.New()

	svc.RecordFailure(uid, sid, context.Canceled)
	if store.failureCalls != 0 {
		t.Fatalf("canceled should not increment failureCalls, got %d", store.failureCalls)
	}

	svc.RecordFailure(uid, sid, errors.New("some upstream error"))
	if store.failureCalls != 1 || store.lastKind != repository.FailureKindNetwork {
		t.Fatalf("failureCalls=%d kind=%s", store.failureCalls, store.lastKind)
	}

	svc.RecordSuccess(uid, sid, 42*time.Millisecond)
	if store.successCalls != 1 || store.lastElapsed != 42 {
		t.Fatalf("successCalls=%d elapsed=%d", store.successCalls, store.lastElapsed)
	}
}

func TestJobService_RecordMetricCallback(t *testing.T) {
	var seen []string
	js := NewJobService(nil, nil, nil, nil).WithJobMetric(func(jobType, status string) {
		seen = append(seen, jobType+":"+status)
	})
	js.recordMetric("toc_refresh", "running")
	js.recordMetric("source_health", "succeeded")
	if len(seen) != 2 || seen[0] != "toc_refresh:running" || seen[1] != "source_health:succeeded" {
		t.Fatalf("unexpected metrics: %v", seen)
	}
}
