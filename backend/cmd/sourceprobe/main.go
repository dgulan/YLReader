// sourceprobe performs bounded, credential-free GET diagnostics using the safe
// Fetcher. It never evaluates source rules or acts as a production HTTP endpoint.
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http/httptrace"
	"os"
	"regexp"
	"sync"
	"time"

	"yreader/backend/internal/engine/fetcher"
)

type target struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}
type event struct {
	Phase        string `json:"phase"`
	OK           bool   `json:"ok"`
	Milliseconds int64  `json:"milliseconds"`
}
type report struct {
	Label        string  `json:"label"`
	Outcome      string  `json:"outcome"`
	HTTPStatus   int     `json:"http_status,omitempty"`
	BodyBytes    int     `json:"body_bytes,omitempty"`
	Milliseconds int64   `json:"milliseconds"`
	Events       []event `json:"events"`
}
type tracker struct {
	mu     sync.Mutex
	start  time.Time
	events []event
}

func (t *tracker) add(phase string, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.events) < 32 {
		t.events = append(t.events, event{phase, ok, time.Since(t.start).Milliseconds()})
	}
}
func (t *tracker) snapshot() []event {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]event{}, t.events...)
}
func classify(err error) string {
	if err == nil {
		return "ok"
	}
	switch {
	case errors.Is(err, fetcher.ErrChallengeRequired):
		return "browser_verification"
	case errors.Is(err, fetcher.ErrSSRFBlocked):
		return "ssrf_blocked"
	case errors.Is(err, fetcher.ErrUnsafeURL):
		return "unsafe_url"
	case errors.Is(err, fetcher.ErrRedirectTooMany):
		return "redirect_limit"
	case errors.Is(err, fetcher.ErrBodyTooLarge):
		return "response_limit"
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return "dns_error"
	}
	var unknown x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	var verification *tls.CertificateVerificationError
	var record tls.RecordHeaderError
	if errors.As(err, &unknown) || errors.As(err, &hostname) || errors.As(err, &invalid) || errors.As(err, &verification) || errors.As(err, &record) {
		return "tls_error"
	}
	var status *fetcher.HTTPStatusError
	if errors.As(err, &status) {
		return "http_error"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return "connect_error"
	}
	var timed net.Error
	if errors.As(err, &timed) && timed.Timeout() {
		return "network_timeout"
	}
	return "request_failed"
}
func probe(parent context.Context, input target) report {
	ctx, cancel := context.WithTimeout(parent, 12*time.Second)
	defer cancel()
	trace := &tracker{start: time.Now()}
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		DNSStart:             func(httptrace.DNSStartInfo) { trace.add("dns_start", true) },
		DNSDone:              func(info httptrace.DNSDoneInfo) { trace.add("dns_done", info.Err == nil) },
		ConnectStart:         func(string, string) { trace.add("connect_start", true) },
		ConnectDone:          func(_ string, _ string, err error) { trace.add("connect_done", err == nil) },
		TLSHandshakeStart:    func() { trace.add("tls_start", true) },
		TLSHandshakeDone:     func(_ tls.ConnectionState, err error) { trace.add("tls_done", err == nil) },
		GotFirstResponseByte: func() { trace.add("response_first_byte", true) },
	})
	response, err := fetcher.New(fetcher.Config{MaxBodyBytes: 1 << 20}).Get(ctx, input.URL, nil)
	out := report{Label: input.Label, Outcome: classify(err), Milliseconds: time.Since(trace.start).Milliseconds(), Events: trace.snapshot()}
	var status *fetcher.HTTPStatusError
	if errors.As(err, &status) {
		out.HTTPStatus = status.StatusCode
	}
	if response != nil {
		out.HTTPStatus = response.StatusCode
		out.BodyBytes = len(response.Body)
		if out.HTTPStatus >= 400 {
			out.Outcome = "http_error"
		}
	}
	return out
}

var labelPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,48}$`)

func run(input io.Reader, output io.Writer) bool {
	data, err := io.ReadAll(io.LimitReader(input, (64<<10)+1))
	var request struct {
		Targets []target `json:"targets"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	valid := err == nil && len(data) <= 64<<10 && decoder.Decode(&request) == nil && decoder.Decode(new(any)) == io.EOF && len(request.Targets) > 0 && len(request.Targets) <= 3
	seen := map[string]bool{}
	for _, t := range request.Targets {
		if !labelPattern.MatchString(t.Label) || seen[t.Label] || len(t.URL) == 0 || len(t.URL) > 4096 {
			valid = false
		}
		seen[t.Label] = true
	}
	if !valid {
		json.NewEncoder(output).Encode(map[string]string{"error": "invalid_input"})
		return false
	}
	for _, t := range request.Targets {
		if json.NewEncoder(output).Encode(probe(context.Background(), t)) != nil {
			return false
		}
	}
	return true
}
func main() {
	if !run(os.Stdin, os.Stdout) {
		os.Exit(1)
	}
}
