package backup

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

func behaviorHTTPFixture(t *testing.T) behaviorHTTPConfig {
	t.Helper()
	cleanPG(t)
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, e := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	source, target := databaseIdentityPin{"1", 1, 1}, databaseIdentityPin{"1", 2, 2}
	pin, _ := target.fingerprint()
	return behaviorHTTPConfig{Endpoint: "https://127.0.0.1:8443", Certificate: der, Binding: behaviorTransportBinding{uuid.NewString(), pin, strings.Repeat("a", 64)}, DatabaseURL: "postgres://reader:@127.0.0.1:5432/yreader_a1_iso_http?sslmode=disable", Source: source, Target: target, JWT: bytes.Repeat([]byte("k"), 32), Users: []uuid.UUID{uuid.New()}, Clock: func() time.Time { return now }}
}

func TestBehaviorHTTPRejectsUnboundTargets(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1:8443", "https://localhost:8443", "https://10.0.0.1:8443", "https://127.0.0.1", "https://127.0.0.1:08443", "https://u:p@127.0.0.1:8443", "https://127.0.0.1:8443/api", "https://127.0.0.1:8443?", "https://127.0.0.1:8443#", "https://127.0.0.1:8443/%2f"} {
		if _, e := parseBehaviorEndpoint(raw); e != errBehaviorHTTP {
			t.Fatal("unbound URL accepted")
		}
	}
	for _, mode := range []string{"key", "cert", "database", "users", "binding", "clock"} {
		t.Run(mode, func(t *testing.T) {
			c := behaviorHTTPFixture(t)
			switch mode {
			case "key":
				c.JWT = []byte("SECRET")
			case "cert":
				c.Certificate = []byte("SECRET")
			case "database":
				c.DatabaseURL = "SECRET"
			case "users":
				c.Users = append(c.Users, c.Users[0])
			case "binding":
				c.Binding.Database = strings.Repeat("f", 64)
			case "clock":
				c.Clock = nil
			}
			if tr, e := newBehaviorHTTPTransport(c); e != errBehaviorHTTP || tr != nil {
				t.Fatal("invalid binding accepted")
			}
		})
	}
}

func TestBehaviorHTTPPinAndSecretLifetime(t *testing.T) {
	c := behaviorHTTPFixture(t)
	tr, e := newBehaviorHTTPTransport(c)
	if e != nil {
		t.Fatal(e)
	}
	transport := tr.client.Transport.(*http.Transport)
	cert, _ := x509.ParseCertificate(c.Certificate)
	if transport.Proxy != nil || !transport.DisableKeepAlives || !transport.DisableCompression || transport.ForceAttemptHTTP2 || tr.client.Jar != nil {
		t.Fatal("unsafe client defaults")
	}
	if e = transport.TLSClientConfig.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}); e != nil {
		t.Fatal(e)
	}
	wrong := *cert
	wrong.Raw = append([]byte(nil), cert.Raw...)
	wrong.Raw[len(wrong.Raw)-1] ^= 1
	if e = transport.TLSClientConfig.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{&wrong}}); e != errBehaviorHTTP {
		t.Fatal("wrong certificate accepted")
	}
	if _, e = transport.DialContext(context.Background(), "tcp", "127.0.0.1:9999"); e != errBehaviorHTTP {
		t.Fatal("dial escaped binding")
	}
	if tr.client.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Fatal("redirects allowed")
	}
	for _, v := range []any{c, tr, *tr} {
		if raw, e := json.Marshal(v); e == nil || bytes.Contains(raw, c.JWT) {
			t.Fatal("configuration serialized")
		}
		if strings.Contains(fmt.Sprintf("%#v", v), string(c.JWT)) {
			t.Fatal("key formatting leaked")
		}
	}
	clear(c.JWT)
	if tr.secrets.key[0] != 'k' {
		t.Fatal("caller key alias retained")
	}
	tr.Close()
	tr.Close()
	if !tr.secrets.closed || !bytes.Equal(tr.secrets.key, make([]byte, 32)) || len(tr.secrets.known) != 0 {
		t.Fatal("Close retained local secrets")
	}
}

func httpTokenFixture(t *testing.T, c behaviorHTTPConfig) []byte {
	t.Helper()
	now := c.Clock()
	claims := behaviorClaims{RegisteredClaims: jwt.RegisteredClaims{Subject: c.Users[0].String(), IssuedAt: jwt.NewNumericDate(now.Add(-time.Second)), ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute))}, TokenVersion: 1}
	access, e := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(c.JWT)
	if e != nil {
		t.Fatal(e)
	}
	data, _ := json.Marshal(map[string]any{"access_token": access, "refresh_token": strings.Repeat("a", 64), "token_type": "Bearer", "expires_in": 60, "user": map[string]any{"id": c.Users[0], "username": "reader", "role": "user", "token_version": 1, "created_at": now, "display_name": nil, "last_login_at": nil}})
	return data
}
func TestBehaviorHTTPStrictResponses(t *testing.T) {
	c := behaviorHTTPFixture(t)
	data := httpTokenFixture(t, c)
	decoded, e := decodeBehaviorHTTPTokens(data)
	if e != nil || verifyHTTPAccess(decoded.Access, c.JWT, c.Users[0], 1, c.Clock()) != nil {
		t.Fatal("nullable API response rejected")
	}
	for _, mode := range []string{"duplicate", "unknown", "null_user", "oversized", "type", "malformed_nullable", "duplicate_nullable", "invalid_utf8", "bad_unicode"} {
		t.Run(mode, func(t *testing.T) {
			bad := append([]byte(nil), data...)
			switch mode {
			case "duplicate":
				bad = append([]byte(`{"token_type":"Bearer",`), bad[1:]...)
			case "unknown":
				bad = append([]byte(`{"SECRET":1,`), bad[1:]...)
			case "null_user":
				var m map[string]any
				json.Unmarshal(bad, &m)
				m["user"] = nil
				bad, _ = json.Marshal(m)
			case "oversized":
				bad = bytes.Repeat([]byte(" "), 65537)
			case "type":
				bad = bytes.Replace(bad, []byte(`"Bearer"`), []byte(`"bearer"`), 1)
			case "malformed_nullable":
				bad = bytes.Replace(bad, []byte(`"display_name":null`), []byte(`"display_name":42`), 1)
			case "duplicate_nullable":
				bad = bytes.Replace(bad, []byte(`"display_name":null`), []byte(`"display_name":null,"display_name":null`), 1)
			case "invalid_utf8":
				bad = append(bad, 0xff)
			case "bad_unicode":
				bad = bytes.Replace(bad, []byte(`"reader"`), []byte(`"\ud800"`), 1)
			}
			if _, e := decodeBehaviorHTTPTokens(bad); e != errBehaviorHTTP {
				t.Fatal("malformed response accepted")
			}
		})
	}
	if verifyHTTPAccess(decoded.Access, bytes.Repeat([]byte("x"), 32), c.Users[0], 1, c.Clock()) != errBehaviorHTTP || verifyHTTPAccess(decoded.Access, c.JWT, c.Users[0], 2, c.Clock()) != errBehaviorHTTP || verifyHTTPAccess(decoded.Access, c.JWT, c.Users[0], 1, c.Clock().Add(2*time.Minute)) != errBehaviorHTTP {
		t.Fatal("JWT identity/time not bound")
	}
}

type behaviorRoundTripFunc func(*http.Request) (*http.Response, error)
type behaviorHTTPBadClose struct{ io.Reader }

func (behaviorHTTPBadClose) Close() error { return fmt.Errorf("SECRET close failure") }

func (f behaviorRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestBehaviorHTTPNoReplayAndBoundedBody(t *testing.T) {
	for _, mode := range []string{"redirect", "large_body", "network_error", "wrong_token", "compressed", "body_close"} {
		t.Run(mode, func(t *testing.T) {
			c := behaviorHTTPFixture(t)
			tr, e := newBehaviorHTTPTransport(c)
			if e != nil {
				t.Fatal(e)
			}
			defer tr.Close()
			calls := 0
			tr.client.Transport = behaviorRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != "POST" || r.URL.Path != "/api/v1/auth/login" || r.GetBody != nil || r.Header.Get("Idempotency-Key") != "" || r.Header.Get("Authorization") != "" {
					t.Fatal("unsafe request")
				}
				if mode == "network_error" {
					return nil, fmt.Errorf("SECRET network")
				}
				status := 200
				body := httpTokenFixture(t, c)
				header := http.Header{"Content-Type": []string{"application/json"}}
				if mode == "redirect" {
					status = 307
					header.Set("Location", "https://127.0.0.1:1")
				}
				if mode == "large_body" {
					body = bytes.Repeat([]byte("x"), 65537)
				}
				if mode == "compressed" {
					header.Set("Content-Encoding", "gzip")
				}
				if mode == "wrong_token" {
					body = bytes.Replace(body, []byte(`"Bearer"`), []byte(`"wrong"`), 1)
				}
				var responseBody io.ReadCloser = io.NopCloser(bytes.NewReader(body))
				if mode == "body_close" {
					responseBody = behaviorHTTPBadClose{bytes.NewReader(body)}
				}
				return &http.Response{StatusCode: status, Header: header, Body: responseBody, Request: r}, nil
			})
			credential := behaviorCredential{UserID: c.Users[0].String(), Username: "reader", Password: "synthetic-password"}
			for i := 0; i < 2; i++ {
				if s, e := tr.Login(context.Background(), credential); e != errBehaviorHTTP || s.SessionID != uuid.Nil || strings.Contains(e.Error(), "SECRET") {
					t.Fatal("uncertain HTTP outcome accepted")
				}
			}
			if calls != 1 {
				t.Fatal("POST replayed")
			}
		})
	}
}

func TestBehaviorHTTPRequestBodyOwnsSensitiveBytes(t *testing.T) {
	caller := []byte("SECRET synthetic request")
	body := newBehaviorRequestBody(caller)
	clear(caller)
	read, e := io.ReadAll(body)
	if e != nil || string(read) != "SECRET synthetic request" {
		t.Fatal("request retained caller buffer alias")
	}
	clear(read)
	if strings.Contains(fmt.Sprintf("%#v", body), "SECRET") {
		t.Fatal("request body formatting leaked")
	}
	if _, e = json.Marshal(body); e == nil {
		t.Fatal("request body serialized")
	}
	body.Close()
	body.Close()
	if _, e = body.Read(make([]byte, 1)); e != io.EOF || !bytes.Equal(body.buffer.data, make([]byte, len(body.buffer.data))) {
		t.Fatal("request body retained readable bytes after close")
	}
}
