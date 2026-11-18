package backup

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

func oldAccessFixture(t *testing.T, c behaviorHTTPConfig, key []byte, user uuid.UUID, expires time.Time) string {
	t.Helper()
	claims := behaviorClaims{TokenVersion: 1, RegisteredClaims: jwt.RegisteredClaims{Subject: user.String(), IssuedAt: jwt.NewNumericDate(c.Clock().Add(-time.Minute)), ExpiresAt: jwt.NewNumericDate(expires)}}
	raw, e := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(key)
	if e != nil {
		t.Fatal(e)
	}
	return raw
}

func TestBehaviorOldAccessBoundedObservation(t *testing.T) {
	for _, mode := range []string{"good", "accepted", "forbidden", "redirect", "network", "compressed", "large", "duplicate", "wrong_code", "wrong_shape", "bad_close", "cancel", "wrong_source", "target_signed", "expired", "wrong_user", "wrong_version", "closed"} {
		t.Run(mode, func(t *testing.T) {
			c := behaviorHTTPFixture(t)
			tr, e := newBehaviorHTTPTransport(c)
			if e != nil {
				t.Fatal(e)
			}
			defer tr.Close()
			key := bytes.Repeat([]byte("s"), 32)
			id := c.Users[0]
			version := 2
			access := oldAccessFixture(t, c, key, id, c.Clock().Add(time.Minute))
			ctx := context.Background()
			switch mode {
			case "wrong_source":
				key = bytes.Repeat([]byte("z"), 32)
			case "target_signed":
				access = oldAccessFixture(t, c, c.JWT, id, c.Clock().Add(time.Minute))
			case "expired":
				access = oldAccessFixture(t, c, key, id, c.Clock().Add(-time.Second))
			case "wrong_user":
				id = uuid.New()
			case "wrong_version":
				version = 3
			case "closed":
				tr.Close()
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			calls := 0
			tr.client.Transport = behaviorRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != "GET" || r.URL.String() != c.Endpoint+"/api/v1/auth/me" || r.Body != nil || r.Header.Get("Authorization") != "Bearer "+access || r.Header.Get("Cookie") != "" || r.GetBody != nil {
					t.Fatal("request escaped read-only boundary")
				}
				if mode == "network" {
					return nil, fmt.Errorf("SECRET network")
				}
				status := 401
				body := `{"error":{"code":"unauthorized","message":"Unauthorized"}}`
				header := http.Header{"Content-Type": []string{"application/json"}}
				switch mode {
				case "accepted":
					status = 200
				case "forbidden":
					status = 403
				case "redirect":
					status = 307
					header.Set("Location", "https://127.0.0.1:1")
				case "compressed":
					header.Set("Content-Encoding", "gzip")
				case "large":
					body = strings.Repeat("x", 8193)
				case "duplicate":
					body = `{"error":{"code":"unauthorized","code":"unauthorized","message":"Unauthorized"}}`
				case "wrong_code":
					body = `{"error":{"code":"validation_error","message":"No"}}`
				case "wrong_shape":
					body = `{"error":{"code":"unauthorized","message":"No","request_id":3}}`
				}
				var reader io.ReadCloser = io.NopCloser(strings.NewReader(body))
				if mode == "bad_close" {
					reader = behaviorHTTPBadClose{strings.NewReader(body)}
				}
				return &http.Response{StatusCode: status, Header: header, Body: reader, Request: r}, nil
			})
			err := tr.rejectOldAccess(ctx, id, version, access, key)
			if (err == nil) != (mode == "good") {
				t.Fatal("unexpected rejection result", err)
			}
			if err != nil && err != errBehaviorHTTP {
				t.Fatal("error exposed details")
			}
			first := calls
			if tr.rejectOldAccess(ctx, id, version, access, key) != errBehaviorHTTP || calls != first {
				t.Fatal("observation replayed")
			}
			if mode == "good" && calls != 1 {
				t.Fatal("missing request")
			}
			for _, local := range []string{"wrong_source", "target_signed", "expired", "wrong_user", "wrong_version", "closed", "cancel"} {
				if mode == local && calls != 0 {
					t.Fatal("invalid credential sent")
				}
			}
		})
	}
}
