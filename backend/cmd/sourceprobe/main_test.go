package main

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"yreader/backend/internal/engine/fetcher"
)

func TestClassifyWrappedErrorsWithoutErrorText(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&net.DNSError{Err: "secret-address", Name: "secret-host"}, "dns_error"},
		{&net.OpError{Op: "dial", Err: fmt.Errorf("private-address")}, "connect_error"},
		{x509.UnknownAuthorityError{}, "tls_error"},
		{&fetcher.HTTPStatusError{StatusCode: 503}, "http_error"},
		{context.DeadlineExceeded, "deadline"},
		{fetcher.ErrChallengeRequired, "browser_verification"},
	} {
		wrapped := &url.Error{Op: "Get", URL: "https://user:secret@private-host/path?token=secret", Err: tc.err}
		if got := classify(wrapped); got != tc.want {
			t.Fatal(got, tc.want)
		}
	}
}
func TestProbeKeepsSSRFAndDoesNotExposeTargets(t *testing.T) {
	input := `{"targets":[{"label":"private","url":"http://127.0.0.1/private?token=SECRET"},{"label":"userinfo","url":"https://user:SECRET@example.com/"}]}`
	var output bytes.Buffer
	if !run(strings.NewReader(input), &output) {
		t.Fatal(output.String())
	}
	text := output.String()
	for _, secret := range []string{"127.0.0.1", "SECRET", "example.com", "token=", "private?", "user:"} {
		if strings.Contains(text, secret) {
			t.Fatal("target leaked", text)
		}
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	var first, second report
	if decoder.Decode(&first) != nil || decoder.Decode(&second) != nil || first.Outcome != "ssrf_blocked" || second.Outcome != "unsafe_url" {
		t.Fatal(text)
	}
}
func TestInvalidInputDoesNotPrintItsContents(t *testing.T) {
	for _, input := range []string{`null`, `{"targets":[]}`, `{"targets":[{"label":"secret URL","url":"SECRET"}]}`, `{"targets":[{"label":"a","url":"SECRET","unknown":true}]}`, strings.Repeat("SECRET", 12000)} {
		var output bytes.Buffer
		if run(strings.NewReader(input), &output) || strings.Contains(output.String(), "SECRET") {
			t.Fatal("invalid input accepted or exposed")
		}
	}
}
func TestTrackerBoundsConcurrentEvents(t *testing.T) {
	trace := &tracker{start: time.Now()}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				trace.add("connect_done", false)
			}
		}()
	}
	wg.Wait()
	got := trace.snapshot()
	if len(got) != 32 {
		t.Fatal(len(got))
	}
	got[0].Phase = "changed"
	if trace.snapshot()[0].Phase == "changed" {
		t.Fatal("snapshot aliases live events")
	}
}
