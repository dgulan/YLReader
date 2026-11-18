package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"yreader/backend/internal/pkg/storage"
)

var errBehaviorHTTP = errors.New("backup: bound HTTPS behavior response unconfirmed; do not retry")

type behaviorHTTPConfig struct {
	Endpoint       string
	Certificate    []byte
	Binding        behaviorTransportBinding
	DatabaseURL    string
	Source, Target databaseIdentityPin
	JWT            []byte
	Users          []uuid.UUID
	Clock          func() time.Time
}

func (behaviorHTTPConfig) Format(s fmt.State, _ rune) {
	io.WriteString(s, "[private behavior HTTPS configuration]")
}
func (behaviorHTTPConfig) MarshalJSON() ([]byte, error) { return nil, errBehaviorHTTP }

type behaviorHTTPKnown struct {
	user            uuid.UUID
	access, refresh string
	version         int
	started         time.Time
	attempted       bool
}
type behaviorHTTPSecrets struct {
	key             []byte
	known           map[uuid.UUID]behaviorHTTPKnown
	loginTried      map[uuid.UUID]bool
	completed       map[uuid.UUID]behaviorSessionResponse
	oldAccessTried  map[uuid.UUID]bool
	oldRefreshTried map[uuid.UUID]bool
	closed          bool
}
type behaviorHTTPTransport struct {
	mu                 *sync.Mutex
	client             *http.Client
	endpoint           string
	binding            behaviorTransportBinding
	certificateSHA     string
	databaseURL        string
	source, target     databaseIdentityPin
	users              map[uuid.UUID]bool
	clock              func() time.Time
	secrets            *behaviorHTTPSecrets
	oldRefreshObserver func(context.Context, uuid.UUID, string) (oldRefreshSessionSnapshot, error)
}

func (behaviorHTTPTransport) Format(s fmt.State, _ rune) {
	io.WriteString(s, "[bound HTTPS behavior transport; secrets redacted]")
}
func (behaviorHTTPTransport) MarshalJSON() ([]byte, error)         { return nil, errBehaviorHTTP }
func (t *behaviorHTTPTransport) Binding() behaviorTransportBinding { return t.binding }

func parseBehaviorEndpoint(raw string) (*url.URL, error) {
	u, e := url.Parse(raw)
	if len(raw) > 4096 || e != nil || u.Scheme != "https" || u.Opaque != "" || u.User != nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "::1") || (u.Path != "" && u.Path != "/") || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") {
		return nil, errBehaviorHTTP
	}
	port, e := strconv.Atoi(u.Port())
	if e != nil || port < 1 || port > 65535 || strconv.Itoa(port) != u.Port() || u.Host != net.JoinHostPort(u.Hostname(), u.Port()) {
		return nil, errBehaviorHTTP
	}
	return u, nil
}

func newBehaviorHTTPTransport(c behaviorHTTPConfig) (*behaviorHTTPTransport, error) {
	u, e := parseBehaviorEndpoint(c.Endpoint)
	if e != nil || c.Clock == nil || !validUUID(c.Binding.TargetID) || c.Binding.TargetID == uuid.Nil.String() || !validSHA(c.Binding.Runtime) || len(c.Certificate) < 1 || len(c.Certificate) > 64<<10 || len(c.JWT) < 32 || len(c.JWT) > 4096 || len(c.Users) < 1 || len(c.Users) > 8 || !databasePinsSeparate(c.Source, c.Target) {
		return nil, errBehaviorHTTP
	}
	db, e := ParseTarget(c.DatabaseURL)
	if e != nil || db == nil {
		return nil, errBehaviorHTTP
	}
	pin, e := c.Target.fingerprint()
	if e != nil || c.Binding.Database != pin {
		return nil, errBehaviorHTTP
	}
	cert, e := x509.ParseCertificate(c.Certificate)
	if e != nil || cert.VerifyHostname(u.Hostname()) != nil || c.Clock().Before(cert.NotBefore) || !c.Clock().Before(cert.NotAfter) {
		return nil, errBehaviorHTTP
	}
	users := map[uuid.UUID]bool{}
	for _, id := range c.Users {
		if id == uuid.Nil || users[id] {
			return nil, errBehaviorHTTP
		}
		users[id] = true
	}
	expected := sha256.Sum256(c.Certificate)
	address := u.Host
	clock := c.Clock
	tr := &http.Transport{Proxy: nil, DisableKeepAlives: true, DisableCompression: true, ForceAttemptHTTP2: false, TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{}, MaxResponseHeaderBytes: 16 << 10, TLSHandshakeTimeout: 2 * time.Second, ResponseHeaderTimeout: 5 * time.Second}
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if network != "tcp" || addr != address {
			return nil, errBehaviorHTTP
		}
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", address)
	}
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true, NextProtos: []string{"http/1.1"}, VerifyConnection: func(s tls.ConnectionState) error {
		if len(s.PeerCertificates) == 0 {
			return errBehaviorHTTP
		}
		leaf := s.PeerCertificates[0]
		got := sha256.Sum256(leaf.Raw)
		if subtle.ConstantTimeCompare(got[:], expected[:]) != 1 || leaf.VerifyHostname(u.Hostname()) != nil || clock().Before(leaf.NotBefore) || !clock().Before(leaf.NotAfter) {
			return errBehaviorHTTP
		}
		return nil
	}}
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &behaviorHTTPTransport{mu: &sync.Mutex{}, client: client, endpoint: strings.TrimSuffix(u.String(), "/"), binding: c.Binding, certificateSHA: hex.EncodeToString(expected[:]), databaseURL: c.DatabaseURL, source: c.Source, target: c.Target, users: users, clock: clock, secrets: &behaviorHTTPSecrets{key: append([]byte(nil), c.JWT...), known: map[uuid.UUID]behaviorHTTPKnown{}, loginTried: map[uuid.UUID]bool{}, completed: map[uuid.UUID]behaviorSessionResponse{}, oldAccessTried: map[uuid.UUID]bool{}, oldRefreshTried: map[uuid.UUID]bool{}}}, nil
}
func (t *behaviorHTTPTransport) Close() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.secrets.closed = true
	clear(t.secrets.key)
	clear(t.secrets.known)
	clear(t.secrets.loginTried)
	clear(t.secrets.completed)
	clear(t.secrets.oldAccessTried)
	clear(t.secrets.oldRefreshTried)
	t.client.CloseIdleConnections()
}

// Transport may close/read a request body asynchronously after Do returns.
// Own the bytes and serialize erasure instead of sharing the caller's buffer.
type behaviorRequestBuffer struct {
	mu     sync.Mutex
	data   []byte
	offset int
	closed bool
}
type behaviorRequestBody struct{ buffer *behaviorRequestBuffer }

func newBehaviorRequestBody(data []byte) behaviorRequestBody {
	return behaviorRequestBody{&behaviorRequestBuffer{data: append([]byte(nil), data...)}}
}
func (b behaviorRequestBody) Format(s fmt.State, _ rune) {
	io.WriteString(s, "[private behavior request body]")
}
func (b behaviorRequestBody) MarshalJSON() ([]byte, error) { return nil, errBehaviorHTTP }
func (b behaviorRequestBody) Read(p []byte) (int, error) {
	b.buffer.mu.Lock()
	defer b.buffer.mu.Unlock()
	if b.buffer.closed || b.buffer.offset == len(b.buffer.data) {
		return 0, io.EOF
	}
	n := copy(p, b.buffer.data[b.buffer.offset:])
	b.buffer.offset += n
	return n, nil
}
func (b behaviorRequestBody) Close() error {
	b.buffer.mu.Lock()
	defer b.buffer.mu.Unlock()
	clear(b.buffer.data)
	b.buffer.closed = true
	return nil
}

func (t *behaviorHTTPTransport) post(ctx context.Context, path string, payload []byte, access string, status int) (result []byte, err error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, errBehaviorHTTP
	}
	var limit int64
	switch path {
	case "/api/v1/auth/login":
		if status != http.StatusOK || access != "" {
			return nil, errBehaviorHTTP
		}
		limit = 64 << 10
	case "/api/v1/auth/logout":
		if status != http.StatusNoContent || access == "" {
			return nil, errBehaviorHTTP
		}
		limit = 0
	case "/api/v1/auth/refresh":
		if status != http.StatusUnauthorized || access != "" {
			return nil, errBehaviorHTTP
		}
		limit = 8192
	default:
		return nil, errBehaviorHTTP
	}
	body := newBehaviorRequestBody(payload)
	defer body.Close()
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint+path, body)
	if e != nil {
		return nil, errBehaviorHTTP
	}
	req.GetBody = nil
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	if access != "" {
		req.Header.Set("Authorization", "Bearer "+access)
	}
	r, e := t.client.Do(req)
	if e != nil {
		if r != nil && r.Body != nil {
			r.Body.Close()
		}
		return nil, errBehaviorHTTP
	}
	defer func() {
		if r.Body.Close() != nil {
			clear(result)
			result = nil
			err = errBehaviorHTTP
		}
	}()
	if r.StatusCode != status || r.ContentLength > limit || (r.Header.Get("Content-Encoding") != "" && r.Header.Get("Content-Encoding") != "identity") {
		return nil, errBehaviorHTTP
	}
	if status == http.StatusOK || status == http.StatusUnauthorized {
		kind, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if e != nil || kind != "application/json" {
			return nil, errBehaviorHTTP
		}
	}
	data, e := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if e != nil || int64(len(data)) > limit || ctx.Err() != nil {
		clear(data)
		return nil, errBehaviorHTTP
	}
	return data, nil
}

type behaviorHTTPTokenUser struct {
	ID       uuid.UUID  `json:"id"`
	Username string     `json:"username"`
	Role     string     `json:"role"`
	Version  int        `json:"token_version"`
	Created  time.Time  `json:"created_at"`
	Display  *string    `json:"display_name"`
	Last     *time.Time `json:"last_login_at"`
}
type behaviorHTTPTokens struct {
	Access  string                `json:"access_token"`
	Refresh string                `json:"refresh_token"`
	Type    string                `json:"token_type"`
	Expires int                   `json:"expires_in"`
	User    behaviorHTTPTokenUser `json:"user"`
}

func strictHTTPObject(data []byte, required, optional []string) (map[string]json.RawMessage, error) {
	if !utf8.Valid(data) || !pairedUnicodeEscapes(data) {
		return nil, errBehaviorHTTP
	}
	d := json.NewDecoder(bytes.NewReader(data))
	token, e := d.Token()
	if e != nil || token != json.Delim('{') {
		return nil, errBehaviorHTTP
	}
	allowed := map[string]bool{}
	for _, key := range append(append([]string(nil), required...), optional...) {
		allowed[key] = true
	}
	values := map[string]json.RawMessage{}
	for d.More() {
		token, e := d.Token()
		key, ok := token.(string)
		if e != nil || !ok || !allowed[key] || values[key] != nil {
			return nil, errBehaviorHTTP
		}
		var raw json.RawMessage
		if d.Decode(&raw) != nil {
			return nil, errBehaviorHTTP
		}
		values[key] = raw
	}
	if _, e = d.Token(); e != nil || d.Decode(new(any)) != io.EOF {
		return nil, errBehaviorHTTP
	}
	for _, key := range required {
		if values[key] == nil || bytes.Equal(bytes.TrimSpace(values[key]), []byte("null")) {
			return nil, errBehaviorHTTP
		}
	}
	return values, nil
}
func decodeBehaviorHTTPTokens(data []byte) (behaviorHTTPTokens, error) {
	var result behaviorHTTPTokens
	if len(data) > 64<<10 {
		return result, errBehaviorHTTP
	}
	values, e := strictHTTPObject(data, []string{"access_token", "refresh_token", "token_type", "expires_in", "user"}, nil)
	if e != nil {
		return result, e
	}
	if _, e = strictHTTPObject(values["user"], []string{"id", "username", "role", "token_version", "created_at"}, []string{"display_name", "last_login_at"}); e != nil {
		return result, e
	}
	if json.Unmarshal(data, &result) != nil || len(result.Access) < 1 || len(result.Access) > 16384 || !validSHA(result.Refresh) || result.Type != "Bearer" || result.Expires < 1 || result.Expires > 86400 || result.User.ID == uuid.Nil || len(result.User.Username) < 1 || len(result.User.Username) > 256 || (result.User.Role != "admin" && result.User.Role != "user") || result.User.Version < 0 || result.User.Created.IsZero() {
		return behaviorHTTPTokens{}, errBehaviorHTTP
	}
	return result, nil
}
func verifyHTTPAccess(raw string, key []byte, user uuid.UUID, version int, now time.Time) error {
	var claims behaviorClaims
	parsed, e := jwt.ParseWithClaims(raw, &claims, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, errBehaviorHTTP
		}
		return key, nil
	}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired(), jwt.WithIssuedAt(), jwt.WithTimeFunc(func() time.Time { return now }))
	if e != nil || parsed == nil || !parsed.Valid || claims.IssuedAt == nil || claims.Subject != user.String() || claims.TokenVersion != version {
		return errBehaviorHTTP
	}
	return nil
}

func (t *behaviorHTTPTransport) Login(ctx context.Context, c behaviorCredential) (behaviorSessionResponse, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	id, e := uuid.Parse(c.UserID)
	if e != nil || !t.users[id] || t.secrets.closed || t.secrets.loginTried[id] || len(t.secrets.known) >= 8 || c.OldAccess != "" || c.OldRefresh != "" || !utf8.ValidString(c.Username) || !utf8.ValidString(c.Password) || len(c.Username) > 256 || len(c.Password) > 4096 || strings.TrimSpace(c.Username) != c.Username || utf8.RuneCountInString(c.Username) < 1 || utf8.RuneCountInString(c.Username) > 64 || utf8.RuneCountInString(c.Password) < 8 || utf8.RuneCountInString(c.Password) > 128 {
		return behaviorSessionResponse{}, errBehaviorHTTP
	}
	t.secrets.loginTried[id] = true
	started := t.clock()
	body, _ := json.Marshal(struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}{c.Username, c.Password})
	defer clear(body)
	data, e := t.post(ctx, "/api/v1/auth/login", body, "", http.StatusOK)
	defer clear(data)
	if e != nil {
		return behaviorSessionResponse{}, errBehaviorHTTP
	}
	tokens, e := decodeBehaviorHTTPTokens(data)
	if e != nil || tokens.User.ID != id || tokens.User.Username != c.Username || verifyHTTPAccess(tokens.Access, t.secrets.key, id, tokens.User.Version, t.clock()) != nil {
		return behaviorSessionResponse{}, errBehaviorHTTP
	}
	session, e := t.resolve(ctx, tokens.Refresh, id, tokens.User.Version, started, false)
	if e != nil {
		return behaviorSessionResponse{}, errBehaviorHTTP
	}
	if _, exists := t.secrets.known[session.SessionID]; exists {
		return behaviorSessionResponse{}, errBehaviorHTTP
	}
	t.secrets.known[session.SessionID] = behaviorHTTPKnown{user: id, access: tokens.Access, refresh: tokens.Refresh, version: tokens.User.Version, started: started}
	return session, nil
}
func (t *behaviorHTTPTransport) Logout(ctx context.Context, s behaviorSessionResponse) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	known, ok := t.secrets.known[s.SessionID]
	if !ok || t.secrets.closed || known.user != s.UserID || known.attempted {
		return errBehaviorHTTP
	}
	known.attempted = true
	t.secrets.known[s.SessionID] = known
	if verifyHTTPAccess(known.access, t.secrets.key, s.UserID, known.version, t.clock()) != nil {
		return errBehaviorHTTP
	}
	before, e := t.resolve(ctx, known.refresh, known.user, known.version, known.started, false)
	if e != nil || before != s {
		return errBehaviorHTTP
	}
	body, _ := json.Marshal(struct {
		Refresh string `json:"refresh_token"`
	}{known.refresh})
	defer clear(body)
	if _, e = t.post(ctx, "/api/v1/auth/logout", body, known.access, http.StatusNoContent); e != nil {
		return errBehaviorHTTP
	}
	after, e := t.resolve(ctx, known.refresh, known.user, known.version, known.started, true)
	if e != nil || after != s {
		return errBehaviorHTTP
	}
	delete(t.secrets.known, s.SessionID)
	t.secrets.completed[s.SessionID] = s
	return nil
}

func (t *behaviorHTTPTransport) completedWitness(sessions []behaviorSessionResponse, targetJWT []byte) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.secrets.closed || !bytes.Equal(t.secrets.key, targetJWT) || len(t.secrets.known) != 0 || len(t.users) != len(sessions) || len(t.secrets.completed) != len(sessions) || !validSHA(t.certificateSHA) {
		return "", errBehaviorHTTP
	}
	for _, s := range sessions {
		if t.secrets.completed[s.SessionID] != s || s.SessionID == uuid.Nil || !t.users[s.UserID] {
			return "", errBehaviorHTTP
		}
	}
	return t.certificateSHA, nil
}

func (t *behaviorHTTPTransport) resolve(ctx context.Context, refresh string, user uuid.UUID, version int, started time.Time, revoked bool) (result behaviorSessionResponse, err error) {
	if ctx == nil || !validSHA(refresh) {
		return result, errBehaviorHTTP
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cfg, e := ParseTarget(t.databaseURL)
	if e != nil {
		return result, errBehaviorHTTP
	}
	c, e := pgx.ConnectConfig(ctx, cfg)
	if e != nil {
		return result, errBehaviorHTTP
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		if c.Close(cleanup) != nil {
			result = behaviorSessionResponse{}
			err = errBehaviorHTTP
		}
	}()
	tx, e := c.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return result, errBehaviorHTTP
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = tx.Rollback(cleanup)
	}()
	if accountObservationPolicy(ctx, tx, t.source, t.target, cfg, false) != nil {
		return result, errBehaviorHTTP
	}
	sum := sha256.Sum256([]byte(refresh))
	var actualRevoked, disabled bool
	var actualVersion int
	var created, expires time.Time
	e = tx.QueryRow(ctx, `SELECT s.user_id,s.id,s.revoked_at IS NOT NULL,s.created_at,s.expires_at,u.token_version,u.is_disabled FROM public.auth_sessions s JOIN public.users u ON u.id=s.user_id WHERE s.refresh_token_hash=$1`, hex.EncodeToString(sum[:])).Scan(&result.UserID, &result.SessionID, &actualRevoked, &created, &expires, &actualVersion, &disabled)
	now := t.clock()
	if e != nil || result.UserID != user || result.SessionID == uuid.Nil || actualRevoked != revoked || actualVersion != version || disabled || created.Before(started.Add(-time.Second)) || created.After(now.Add(time.Second)) || (!revoked && !expires.After(now)) || ctx.Err() != nil || tx.Commit(ctx) != nil {
		return behaviorSessionResponse{}, errBehaviorHTTP
	}
	return result, nil
}

func verifyBehaviorUnauthorizedError(data []byte) error {
	outer, e := strictHTTPObject(data, []string{"error"}, nil)
	if e != nil {
		return errBehaviorHTTP
	}
	fields, e := strictHTTPObject(outer["error"], []string{"code", "message"}, []string{"request_id", "details"})
	if e != nil {
		return errBehaviorHTTP
	}
	var code, message string
	if json.Unmarshal(fields["code"], &code) != nil || code != "unauthorized" || json.Unmarshal(fields["message"], &message) != nil || len(message) == 0 {
		return errBehaviorHTTP
	}
	if raw, ok := fields["request_id"]; ok {
		var id string
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &id) != nil {
			return errBehaviorHTTP
		}
	}
	if raw, ok := fields["details"]; ok {
		var details map[string]json.RawMessage
		if json.Unmarshal(raw, &details) != nil || details == nil {
			return errBehaviorHTTP
		}
	}
	return nil
}

type oldRefreshSessionSnapshot struct {
	total, revoked, activeForUser int64
	targetFound, targetRevoked    bool
	targetExpires                 time.Time
	digest                        string
}

func (t *behaviorHTTPTransport) observeOldRefresh(ctx context.Context, user uuid.UUID, refresh string) (oldRefreshSessionSnapshot, error) {
	if t.oldRefreshObserver != nil {
		return t.oldRefreshObserver(ctx, user, refresh)
	}
	return t.observeOldRefreshSessions(ctx, user, refresh)
}

func (t *behaviorHTTPTransport) observeOldRefreshSessions(ctx context.Context, user uuid.UUID, refresh string) (result oldRefreshSessionSnapshot, err error) {
	if ctx == nil || user == uuid.Nil || !validSHA(refresh) {
		return oldRefreshSessionSnapshot{}, errBehaviorHTTP
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cfg, e := ParseTarget(t.databaseURL)
	if e != nil {
		return oldRefreshSessionSnapshot{}, errBehaviorHTTP
	}
	c, e := pgx.ConnectConfig(ctx, cfg)
	if e != nil {
		return oldRefreshSessionSnapshot{}, errBehaviorHTTP
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		if c.Close(cleanup) != nil {
			result = oldRefreshSessionSnapshot{}
			err = errBehaviorHTTP
		}
	}()
	tx, e := c.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return oldRefreshSessionSnapshot{}, errBehaviorHTTP
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = tx.Rollback(cleanup)
	}()
	if accountObservationPolicy(ctx, tx, t.source, t.target, cfg, false) != nil {
		return oldRefreshSessionSnapshot{}, errBehaviorHTTP
	}
	var disabled bool
	if tx.QueryRow(ctx, `SELECT is_disabled FROM public.users WHERE id=$1`, user).Scan(&disabled) != nil || disabled {
		return oldRefreshSessionSnapshot{}, errBehaviorHTTP
	}
	tokenHash := sha256.New()
	_, _ = io.WriteString(tokenHash, refresh)
	var owner uuid.UUID
	var revokedAt *time.Time
	var expires time.Time
	e = tx.QueryRow(ctx, `SELECT user_id,revoked_at,expires_at FROM public.auth_sessions WHERE refresh_token_hash=$1`, hex.EncodeToString(tokenHash.Sum(nil))).Scan(&owner, &revokedAt, &expires)
	if e != nil || owner != user || revokedAt == nil || !expires.After(t.clock()) {
		return oldRefreshSessionSnapshot{}, errBehaviorHTTP
	}
	rows, e := tx.Query(ctx, `SELECT id,user_id,revoked_at FROM public.auth_sessions ORDER BY id`)
	if e != nil {
		return oldRefreshSessionSnapshot{}, errBehaviorHTTP
	}
	defer rows.Close()
	snap := oldRefreshSessionSnapshot{targetFound: true, targetRevoked: true, targetExpires: expires}
	tableHash := sha256.New()
	var buf [8]byte
	for rows.Next() {
		var sid, uid uuid.UUID
		var rev *time.Time
		if rows.Scan(&sid, &uid, &rev) != nil {
			return oldRefreshSessionSnapshot{}, errBehaviorHTTP
		}
		snap.total++
		if rev != nil {
			snap.revoked++
			tableHash.Write(sid[:])
			tableHash.Write(uid[:])
			tableHash.Write([]byte{1})
			binary.BigEndian.PutUint64(buf[:], uint64(rev.UTC().UnixNano()))
			tableHash.Write(buf[:])
		} else {
			if uid == user {
				snap.activeForUser++
			}
			tableHash.Write(sid[:])
			tableHash.Write(uid[:])
			tableHash.Write([]byte{0})
		}
	}
	if rows.Err() != nil {
		return oldRefreshSessionSnapshot{}, errBehaviorHTTP
	}
	rows.Close()
	if ctx.Err() != nil || tx.Commit(ctx) != nil {
		return oldRefreshSessionSnapshot{}, errBehaviorHTTP
	}
	snap.digest = hex.EncodeToString(tableHash.Sum(nil))
	return snap, nil
}

// Explicit internal observation only. Never probe while an active or
// unconfirmed login session exists for any whitelisted user.
func (t *behaviorHTTPTransport) rejectOldRefresh(ctx context.Context, proof *verifiedBehaviorAccounts, user uuid.UUID, refresh string) error {
	if t == nil || ctx == nil || ctx.Err() != nil || proof == nil || proof.guard == nil || user == uuid.Nil || !validSHA(refresh) {
		return errBehaviorHTTP
	}
	proof.guard.mu.Lock()
	guardOpen := proof.guard.locations != nil
	guardPlan := proof.guard.plan
	var controlFiles storage.ConditionalFiles
	if guardOpen && proof.guard.locations.files[3] != nil {
		controlFiles = proof.guard.locations.files[3]
	}
	proof.guard.mu.Unlock()
	if !guardOpen || guardPlan.Digest != proof.plan || guardPlan.TargetID != t.binding.TargetID {
		return errBehaviorHTTP
	}
	if controlFiles != nil {
		snap, e := stableHandoverSnapshot(ctx, controlFiles, guardPlan.OperationID)
		if e != nil || snap.tokens != proof.records {
			return errBehaviorHTTP
		}
	}
	if e := t.rejectOldRefreshLocked(ctx, proof, user, refresh); e != nil {
		return e
	}
	if controlFiles != nil {
		after, e := stableHandoverSnapshot(ctx, controlFiles, guardPlan.OperationID)
		if e != nil || after.tokens != proof.records {
			return errBehaviorHTTP
		}
	}
	return nil
}

func (t *behaviorHTTPTransport) rejectOldRefreshLocked(ctx context.Context, proof *verifiedBehaviorAccounts, user uuid.UUID, refresh string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.clock()
	if t.secrets == nil || t.secrets.closed || t.secrets.oldRefreshTried[user] || !databasePinsSeparate(t.source, t.target) || proof.database != t.binding.Database || proof.runtime != t.binding.Runtime || !validStamp(now) || now.Before(proof.checked) || now.Sub(proof.checked) > 20*time.Second || !now.Before(proof.expires) || len(proof.users) == 0 || len(proof.users) != len(t.users) {
		return errBehaviorHTTP
	}
	seenUsers := make(map[uuid.UUID]bool, len(proof.users))
	for _, u := range proof.users {
		if u == uuid.Nil || seenUsers[u] || !t.users[u] {
			return errBehaviorHTTP
		}
		seenUsers[u] = true
	}
	if !seenUsers[user] || len(t.secrets.known) != 0 {
		return errBehaviorHTTP
	}
	if len(t.secrets.loginTried) != 0 {
		completedUsers := make(map[uuid.UUID]bool, len(t.secrets.completed))
		for _, s := range t.secrets.completed {
			completedUsers[s.UserID] = true
		}
		for uid, tried := range t.secrets.loginTried {
			if tried && !completedUsers[uid] {
				return errBehaviorHTTP
			}
		}
	}
	if t.secrets.oldRefreshTried == nil {
		t.secrets.oldRefreshTried = map[uuid.UUID]bool{}
	}
	t.secrets.oldRefreshTried[user] = true
	before, e := t.observeOldRefresh(ctx, user, refresh)
	if e != nil || !before.targetFound || !before.targetRevoked || !before.targetExpires.After(t.clock()) || before.activeForUser != 0 || before.revoked != before.total || !validSHA(before.digest) {
		return errBehaviorHTTP
	}
	body, _ := json.Marshal(struct {
		Refresh string `json:"refresh_token"`
	}{refresh})
	defer clear(body)
	data, e := t.post(ctx, "/api/v1/auth/refresh", body, "", http.StatusUnauthorized)
	defer clear(data)
	if e != nil || verifyBehaviorUnauthorizedError(data) != nil || ctx.Err() != nil {
		return errBehaviorHTTP
	}
	after, e := t.observeOldRefresh(ctx, user, refresh)
	if e != nil || !after.targetFound || !after.targetRevoked || !after.targetExpires.After(t.clock()) || after.activeForUser != 0 || after.revoked != after.total || after.total != before.total || after.revoked != before.revoked || after.digest != before.digest || ctx.Err() != nil {
		return errBehaviorHTTP
	}
	return nil
}
