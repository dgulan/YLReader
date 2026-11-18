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
	"yreader/backend/internal/api"
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
type oldTokenProbeWitness struct {
	plan, consent string
	records       [3]string
	database      string
	runtime       string
	certificate   string
	observedAt    time.Time
	tokenExpires  time.Time
	confirmed     bool
}

func (oldTokenProbeWitness) Format(s fmt.State, _ rune) {
	io.WriteString(s, "[private old-token probe witness]")
}
func (oldTokenProbeWitness) MarshalJSON() ([]byte, error) { return nil, errBehaviorHTTP }

type readerRoundtripWitness struct {
	plan, consent string
	records       [3]string
	database      string
	runtime       string
	certificate   string
	sessionID     uuid.UUID
	bookID        uuid.UUID
	otherBookID   uuid.UUID
	observedAt    time.Time
	beforeDigest  string
	afterDigest   string
	confirmed     bool
}

func (readerRoundtripWitness) Format(s fmt.State, _ rune) {
	io.WriteString(s, "[private reader roundtrip witness]")
}
func (readerRoundtripWitness) MarshalJSON() ([]byte, error) { return nil, errBehaviorHTTP }

type readerExpectedBookmark struct {
	id           uuid.UUID
	chapterIndex int
	charOffset   int
	note         *string
}

type readerDBSnapshot struct {
	sessionActive      bool
	activeJobs         int64
	bookID             uuid.UUID
	chapterIndex       int
	otherBookID        uuid.UUID
	otherChapterIndex  int
	progressChapter    int
	progressCharOffset int
	progressVersion    int64
	bookmarks          []readerExpectedBookmark
	digest             string
}

type behaviorHTTPSecrets struct {
	key               []byte
	sourceKey         []byte
	known             map[uuid.UUID]behaviorHTTPKnown
	loginTried        map[uuid.UUID]bool
	completed         map[uuid.UUID]behaviorSessionResponse
	oldAccessTried    map[uuid.UUID]bool
	oldRefreshTried   map[uuid.UUID]bool
	oldAccessWitness  map[uuid.UUID]oldTokenProbeWitness
	oldRefreshWitness map[uuid.UUID]oldTokenProbeWitness
	oldTokenDrifted   bool
	readerTried       map[uuid.UUID]bool
	readerWitness     map[uuid.UUID]readerRoundtripWitness
	readerDrifted     bool
	closed            bool
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
	readerObserver     func(context.Context, uuid.UUID, uuid.UUID) (readerDBSnapshot, error)
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
	return &behaviorHTTPTransport{mu: &sync.Mutex{}, client: client, endpoint: strings.TrimSuffix(u.String(), "/"), binding: c.Binding, certificateSHA: hex.EncodeToString(expected[:]), databaseURL: c.DatabaseURL, source: c.Source, target: c.Target, users: users, clock: clock, secrets: &behaviorHTTPSecrets{key: append([]byte(nil), c.JWT...), known: map[uuid.UUID]behaviorHTTPKnown{}, loginTried: map[uuid.UUID]bool{}, completed: map[uuid.UUID]behaviorSessionResponse{}, oldAccessTried: map[uuid.UUID]bool{}, oldRefreshTried: map[uuid.UUID]bool{}, oldAccessWitness: map[uuid.UUID]oldTokenProbeWitness{}, oldRefreshWitness: map[uuid.UUID]oldTokenProbeWitness{}, readerTried: map[uuid.UUID]bool{}, readerWitness: map[uuid.UUID]readerRoundtripWitness{}}}, nil
}
func (t *behaviorHTTPTransport) Close() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.secrets.closed = true
	clear(t.secrets.key)
	clear(t.secrets.sourceKey)
	t.secrets.sourceKey = nil
	clear(t.secrets.known)
	clear(t.secrets.loginTried)
	clear(t.secrets.completed)
	clear(t.secrets.oldAccessTried)
	clear(t.secrets.oldRefreshTried)
	clear(t.secrets.oldAccessWitness)
	clear(t.secrets.oldRefreshWitness)
	clear(t.secrets.readerTried)
	clear(t.secrets.readerWitness)
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

func (t *behaviorHTTPTransport) doJSON(ctx context.Context, req *http.Request, expectJSON bool, status int, limit int64) (result []byte, err error) {
	if ctx == nil || ctx.Err() != nil || req == nil {
		return nil, errBehaviorHTTP
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
	if expectJSON {
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

func (t *behaviorHTTPTransport) post(ctx context.Context, path string, payload []byte, access string, status int) ([]byte, error) {
	if ctx == nil || ctx.Err() != nil || strings.ContainsAny(access, "\r\n") {
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
	return t.doJSON(ctx, req, status == http.StatusOK || status == http.StatusUnauthorized, status, limit)
}

func allowedReaderGETPath(path string, status int) (int64, bool) {
	if path == "/api/v1/books" && status == http.StatusOK {
		return 1 << 20, true
	}
	const prefix = "/api/v1/books/"
	if !strings.HasPrefix(path, prefix) {
		return 0, false
	}
	rest := strings.TrimPrefix(path, prefix)
	slash := strings.IndexByte(rest, '/')
	if slash <= 0 {
		return 0, false
	}
	if !validUUID(rest[:slash]) {
		return 0, false
	}
	tail := rest[slash:]
	switch {
	case tail == "/chapters" && status == http.StatusOK:
		return 1 << 20, true
	case tail == "/progress" && status == http.StatusOK:
		return 64 << 10, true
	case tail == "/bookmarks" && status == http.StatusOK:
		return 256 << 10, true
	case strings.HasPrefix(tail, "/content?chapter_index="):
		idxRaw := strings.TrimPrefix(tail, "/content?chapter_index=")
		idx, e := strconv.Atoi(idxRaw)
		if e != nil || idx < 0 || strconv.Itoa(idx) != idxRaw {
			return 0, false
		}
		if status == http.StatusOK {
			return 1 << 20, true
		}
		if status == http.StatusNotFound {
			return 8192, true
		}
	}
	return 0, false
}

func (t *behaviorHTTPTransport) get(ctx context.Context, path, access string, status int) ([]byte, error) {
	limit, ok := allowedReaderGETPath(path, status)
	if !ok || ctx == nil || ctx.Err() != nil || access == "" || strings.ContainsAny(access, "\r\n") {
		return nil, errBehaviorHTTP
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, t.endpoint+path, nil)
	if e != nil {
		return nil, errBehaviorHTTP
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	return t.doJSON(ctx, req, true, status, limit)
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
	return verifyBehaviorError(data, "unauthorized")
}

func verifyBehaviorError(data []byte, wantCode string) error {
	outer, e := strictHTTPObject(data, []string{"error"}, nil)
	if e != nil {
		return errBehaviorHTTP
	}
	fields, e := strictHTTPObject(outer["error"], []string{"code", "message"}, []string{"request_id", "details"})
	if e != nil {
		return errBehaviorHTTP
	}
	var code, message string
	if json.Unmarshal(fields["code"], &code) != nil || code != wantCode || json.Unmarshal(fields["message"], &message) != nil || len(message) == 0 {
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

func checkGuardProof(proof *verifiedBehaviorAccounts, targetID string) (storage.ConditionalFiles, Plan, error) {
	if proof == nil || proof.guard == nil {
		return nil, Plan{}, errBehaviorHTTP
	}
	proof.guard.mu.Lock()
	guardOpen := proof.guard.locations != nil
	guardPlan := proof.guard.plan
	var controlFiles storage.ConditionalFiles
	if guardOpen && proof.guard.locations.files[3] != nil {
		controlFiles = proof.guard.locations.files[3]
	}
	proof.guard.mu.Unlock()
	if !guardOpen || guardPlan.Digest != proof.plan || guardPlan.TargetID != targetID {
		return nil, Plan{}, errBehaviorHTTP
	}
	return controlFiles, guardPlan, nil
}

func (t *behaviorHTTPTransport) checkProofWhitelistLocked(proof *verifiedBehaviorAccounts, user uuid.UUID) error {
	now := t.clock()
	if t.secrets == nil || t.secrets.closed || !databasePinsSeparate(t.source, t.target) || proof.database != t.binding.Database || proof.runtime != t.binding.Runtime || !validStamp(now) || now.Before(proof.checked) || now.Sub(proof.checked) > 20*time.Second || !now.Before(proof.expires) || len(proof.users) == 0 || len(proof.users) != len(t.users) {
		return errBehaviorHTTP
	}
	seenUsers := make(map[uuid.UUID]bool, len(proof.users))
	for _, u := range proof.users {
		if u == uuid.Nil || seenUsers[u] || !t.users[u] {
			return errBehaviorHTTP
		}
		seenUsers[u] = true
	}
	if !seenUsers[user] {
		return errBehaviorHTTP
	}
	return nil
}

func parseOldAccessExpiry(raw string, sourceKey []byte, user uuid.UUID, currentVersion int, now time.Time) (time.Time, error) {
	if checkOldBehaviorAccessAt(raw, sourceKey, user, currentVersion, now) != nil {
		return time.Time{}, errBehaviorHTTP
	}
	var claims behaviorClaims
	parsed, e := jwt.ParseWithClaims(raw, &claims, func(tok *jwt.Token) (any, error) {
		if tok.Method != jwt.SigningMethodHS256 {
			return nil, errBehaviorHTTP
		}
		return sourceKey, nil
	}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired(), jwt.WithIssuedAt(), jwt.WithTimeFunc(func() time.Time { return now }))
	if e != nil || parsed == nil || !parsed.Valid || claims.ExpiresAt == nil {
		return time.Time{}, errBehaviorHTTP
	}
	return claims.ExpiresAt.Time.UTC(), nil
}

// Explicit internal observation bound to a live verifiedBehaviorAccounts proof.
// Records the access rejection witness only on confirmed 401 and stable control state.
func (t *behaviorHTTPTransport) confirmOldAccess(ctx context.Context, proof *verifiedBehaviorAccounts, user uuid.UUID, currentVersion int, access string, sourceKey []byte) error {
	if t == nil || ctx == nil || ctx.Err() != nil || user == uuid.Nil || len(sourceKey) < 32 || len(sourceKey) > 4096 {
		return errBehaviorHTTP
	}
	controlFiles, guardPlan, e := checkGuardProof(proof, t.binding.TargetID)
	if e != nil {
		return errBehaviorHTTP
	}
	t.mu.Lock()
	if e := t.checkProofWhitelistLocked(proof, user); e != nil || t.secrets.oldAccessTried[user] || bytes.Equal(sourceKey, t.secrets.key) || (len(t.secrets.sourceKey) > 0 && !bytes.Equal(t.secrets.sourceKey, sourceKey)) {
		t.mu.Unlock()
		return errBehaviorHTTP
	}
	tokenExp, e := parseOldAccessExpiry(access, sourceKey, user, currentVersion, t.clock())
	t.mu.Unlock()
	if e != nil {
		return errBehaviorHTTP
	}
	if controlFiles != nil {
		snap, e := stableHandoverSnapshot(ctx, controlFiles, guardPlan.OperationID)
		if e != nil || snap.tokens != proof.records {
			return errBehaviorHTTP
		}
	}
	if e := t.rejectOldAccess(ctx, user, currentVersion, access, sourceKey); e != nil {
		return e
	}
	if controlFiles != nil {
		after, e := stableHandoverSnapshot(ctx, controlFiles, guardPlan.OperationID)
		if e != nil || after.tokens != proof.records {
			return errBehaviorHTTP
		}
	}
	t.mu.Lock()
	if t.secrets == nil || t.secrets.closed {
		t.mu.Unlock()
		return errBehaviorHTTP
	}
	if len(t.secrets.sourceKey) > 0 && !bytes.Equal(t.secrets.sourceKey, sourceKey) {
		t.secrets.oldTokenDrifted = true
		t.mu.Unlock()
		return errBehaviorHTTP
	}
	if len(t.secrets.sourceKey) == 0 {
		t.secrets.sourceKey = append([]byte(nil), sourceKey...)
	}
	w := oldTokenProbeWitness{plan: proof.plan, consent: proof.consent, records: proof.records, database: proof.database, runtime: proof.runtime, certificate: t.certificateSHA, observedAt: t.clock(), tokenExpires: tokenExp, confirmed: true}
	if t.secrets.oldAccessWitness == nil {
		t.secrets.oldAccessWitness = map[uuid.UUID]oldTokenProbeWitness{}
	}
	t.secrets.oldAccessWitness[user] = w
	targetKey := append([]byte(nil), t.secrets.key...)
	t.mu.Unlock()
	defer clear(targetKey)
	proof.guard.attachOldTokenWitness(user, true, w, targetKey, sourceKey)
	return nil
}

// Explicit internal observation only. Never probe while an active or
// unconfirmed login session exists for any whitelisted user.
func (t *behaviorHTTPTransport) rejectOldRefresh(ctx context.Context, proof *verifiedBehaviorAccounts, user uuid.UUID, refresh string) error {
	if t == nil || ctx == nil || ctx.Err() != nil || user == uuid.Nil || !validSHA(refresh) {
		return errBehaviorHTTP
	}
	controlFiles, guardPlan, e := checkGuardProof(proof, t.binding.TargetID)
	if e != nil {
		return errBehaviorHTTP
	}
	if controlFiles != nil {
		snap, e := stableHandoverSnapshot(ctx, controlFiles, guardPlan.OperationID)
		if e != nil || snap.tokens != proof.records {
			return errBehaviorHTTP
		}
	}
	w, e := t.rejectOldRefreshLocked(ctx, proof, user, refresh)
	if e != nil {
		return e
	}
	if controlFiles != nil {
		after, e := stableHandoverSnapshot(ctx, controlFiles, guardPlan.OperationID)
		if e != nil || after.tokens != proof.records {
			return errBehaviorHTTP
		}
	}
	t.mu.Lock()
	if t.secrets == nil || t.secrets.closed {
		t.mu.Unlock()
		return errBehaviorHTTP
	}
	if t.secrets.oldRefreshWitness == nil {
		t.secrets.oldRefreshWitness = map[uuid.UUID]oldTokenProbeWitness{}
	}
	t.secrets.oldRefreshWitness[user] = w
	targetKey := append([]byte(nil), t.secrets.key...)
	t.mu.Unlock()
	defer clear(targetKey)
	proof.guard.attachOldTokenWitness(user, false, w, targetKey, nil)
	return nil
}

func (t *behaviorHTTPTransport) rejectOldRefreshLocked(ctx context.Context, proof *verifiedBehaviorAccounts, user uuid.UUID, refresh string) (oldTokenProbeWitness, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.checkProofWhitelistLocked(proof, user) != nil || t.secrets.oldRefreshTried[user] || len(t.secrets.known) != 0 {
		return oldTokenProbeWitness{}, errBehaviorHTTP
	}
	if len(t.secrets.loginTried) != 0 {
		completedUsers := make(map[uuid.UUID]bool, len(t.secrets.completed))
		for _, s := range t.secrets.completed {
			completedUsers[s.UserID] = true
		}
		for uid, tried := range t.secrets.loginTried {
			if tried && !completedUsers[uid] {
				return oldTokenProbeWitness{}, errBehaviorHTTP
			}
		}
	}
	if t.secrets.oldRefreshTried == nil {
		t.secrets.oldRefreshTried = map[uuid.UUID]bool{}
	}
	t.secrets.oldRefreshTried[user] = true
	before, e := t.observeOldRefresh(ctx, user, refresh)
	if e != nil || !before.targetFound || !before.targetRevoked || !before.targetExpires.After(t.clock()) || before.activeForUser != 0 || before.revoked != before.total || !validSHA(before.digest) {
		return oldTokenProbeWitness{}, errBehaviorHTTP
	}
	body, _ := json.Marshal(struct {
		Refresh string `json:"refresh_token"`
	}{refresh})
	defer clear(body)
	data, e := t.post(ctx, "/api/v1/auth/refresh", body, "", http.StatusUnauthorized)
	defer clear(data)
	if e != nil || verifyBehaviorUnauthorizedError(data) != nil || ctx.Err() != nil {
		return oldTokenProbeWitness{}, errBehaviorHTTP
	}
	after, e := t.observeOldRefresh(ctx, user, refresh)
	if e != nil || !after.targetFound || !after.targetRevoked || !after.targetExpires.After(t.clock()) || after.activeForUser != 0 || after.revoked != after.total || after.total != before.total || after.revoked != before.revoked || after.digest != before.digest || ctx.Err() != nil {
		return oldTokenProbeWitness{}, errBehaviorHTTP
	}
	return oldTokenProbeWitness{plan: proof.plan, consent: proof.consent, records: proof.records, database: proof.database, runtime: proof.runtime, certificate: t.certificateSHA, observedAt: t.clock(), tokenExpires: after.targetExpires.UTC(), confirmed: true}, nil
}

func (t *behaviorHTTPTransport) oldTokenWitnessSnapshot(sourceKey, targetKey []byte) confirmedOldTokenSet {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := confirmedOldTokenSet{
		access:  map[uuid.UUID]oldTokenProbeWitness{},
		refresh: map[uuid.UUID]oldTokenProbeWitness{},
	}
	if t.secrets == nil || t.secrets.closed || t.secrets.oldTokenDrifted || !bytes.Equal(t.secrets.key, targetKey) {
		out.drifted = true
		return out
	}
	if len(t.secrets.sourceKey) > 0 && !bytes.Equal(t.secrets.sourceKey, sourceKey) {
		out.drifted = true
		return out
	}
	for u, w := range t.secrets.oldAccessWitness {
		if w.confirmed {
			out.access[u] = w
		}
	}
	for u, w := range t.secrets.oldRefreshWitness {
		if w.confirmed {
			out.refresh[u] = w
		}
	}
	return out
}

func checkUniqueJSONToken(d *json.Decoder, first json.Token, depth int) error {
	if depth > 64 {
		return errBehaviorHTTP
	}
	delim, ok := first.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			tok, e := d.Token()
			key, ok := tok.(string)
			if e != nil || !ok || seen[key] {
				return errBehaviorHTTP
			}
			seen[key] = true
			val, e := d.Token()
			if e != nil || checkUniqueJSONToken(d, val, depth+1) != nil {
				return errBehaviorHTTP
			}
		}
		end, e := d.Token()
		if e != nil || end != json.Delim('}') {
			return errBehaviorHTTP
		}
	case '[':
		for d.More() {
			val, e := d.Token()
			if e != nil || checkUniqueJSONToken(d, val, depth+1) != nil {
				return errBehaviorHTTP
			}
		}
		end, e := d.Token()
		if e != nil || end != json.Delim(']') {
			return errBehaviorHTTP
		}
	default:
		return errBehaviorHTTP
	}
	return nil
}

func decodeStrictReaderJSON(data []byte, out any) error {
	if len(data) == 0 || !utf8.Valid(data) || !pairedUnicodeEscapes(data) || bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return errBehaviorHTTP
	}
	d := json.NewDecoder(bytes.NewReader(data))
	first, e := d.Token()
	if e != nil || checkUniqueJSONToken(d, first, 0) != nil {
		return errBehaviorHTTP
	}
	if _, e = d.Token(); e != io.EOF {
		return errBehaviorHTTP
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(out) != nil {
		return errBehaviorHTTP
	}
	if _, e = dec.Token(); e != io.EOF {
		return errBehaviorHTTP
	}
	return nil
}

func (t *behaviorHTTPTransport) observeReader(ctx context.Context, user, sessionID uuid.UUID) (readerDBSnapshot, error) {
	if t.readerObserver != nil {
		return t.readerObserver(ctx, user, sessionID)
	}
	return t.observeReaderDB(ctx, user, sessionID)
}

func (t *behaviorHTTPTransport) observeReaderDB(ctx context.Context, user, sessionID uuid.UUID) (result readerDBSnapshot, err error) {
	if ctx == nil || user == uuid.Nil || sessionID == uuid.Nil {
		return readerDBSnapshot{}, errBehaviorHTTP
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cfg, e := ParseTarget(t.databaseURL)
	if e != nil {
		return readerDBSnapshot{}, errBehaviorHTTP
	}
	c, e := pgx.ConnectConfig(ctx, cfg)
	if e != nil {
		return readerDBSnapshot{}, errBehaviorHTTP
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		if c.Close(cleanup) != nil {
			result = readerDBSnapshot{}
			err = errBehaviorHTTP
		}
	}()
	tx, e := c.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return readerDBSnapshot{}, errBehaviorHTTP
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = tx.Rollback(cleanup)
	}()
	if accountObservationPolicy(ctx, tx, t.source, t.target, cfg, false) != nil {
		return readerDBSnapshot{}, errBehaviorHTTP
	}
	var disabled bool
	if tx.QueryRow(ctx, `SELECT is_disabled FROM public.users WHERE id=$1`, user).Scan(&disabled) != nil || disabled {
		return readerDBSnapshot{}, errBehaviorHTTP
	}
	var sessOwner uuid.UUID
	var sessActive bool
	var sessExpires time.Time
	if tx.QueryRow(ctx, `SELECT user_id,revoked_at IS NULL,expires_at FROM public.auth_sessions WHERE id=$1`, sessionID).Scan(&sessOwner, &sessActive, &sessExpires) != nil || sessOwner != user || !sessActive || !sessExpires.After(t.clock()) {
		return readerDBSnapshot{}, errBehaviorHTTP
	}
	snap := readerDBSnapshot{sessionActive: true}
	if tx.QueryRow(ctx, `SELECT count(*) FROM public.jobs WHERE status IN ('queued','running')`).Scan(&snap.activeJobs) != nil || snap.activeJobs != 0 {
		return readerDBSnapshot{}, errBehaviorHTTP
	}
	var activeSource uuid.UUID
	if tx.QueryRow(ctx, `SELECT id,active_source_id FROM public.books WHERE user_id=$1 AND book_type='local' AND active_source_id IS NOT NULL ORDER BY id LIMIT 1`, user).Scan(&snap.bookID, &activeSource) != nil || snap.bookID == uuid.Nil || activeSource == uuid.Nil {
		return readerDBSnapshot{}, errBehaviorHTTP
	}
	if tx.QueryRow(ctx, `SELECT chapter_index FROM public.chapters WHERE book_id=$1 AND source_id=$2 AND is_volume=false AND chapter_index IS NOT NULL ORDER BY sort_order ASC,id ASC LIMIT 1`, snap.bookID, activeSource).Scan(&snap.chapterIndex) != nil || snap.chapterIndex < 0 {
		return readerDBSnapshot{}, errBehaviorHTTP
	}
	if tx.QueryRow(ctx, `SELECT b.id,COALESCE((SELECT MIN(c.chapter_index) FROM public.chapters c WHERE c.book_id=b.id AND c.is_volume=false AND c.chapter_index IS NOT NULL),0) FROM public.books b WHERE b.user_id<>$1 ORDER BY b.id LIMIT 1`, user).Scan(&snap.otherBookID, &snap.otherChapterIndex) != nil || snap.otherBookID == uuid.Nil || snap.otherBookID == snap.bookID || snap.otherChapterIndex < 0 {
		return readerDBSnapshot{}, errBehaviorHTTP
	}
	e = tx.QueryRow(ctx, `SELECT chapter_index,char_offset,version FROM public.reading_progress WHERE user_id=$1 AND book_id=$2`, user, snap.bookID).Scan(&snap.progressChapter, &snap.progressCharOffset, &snap.progressVersion)
	if errors.Is(e, pgx.ErrNoRows) {
		snap.progressChapter, snap.progressCharOffset, snap.progressVersion = 0, 0, 1
	} else if e != nil {
		return readerDBSnapshot{}, errBehaviorHTTP
	}
	rows, e := tx.Query(ctx, `SELECT id,chapter_index,char_offset,note FROM public.bookmarks WHERE user_id=$1 AND book_id=$2 ORDER BY chapter_index ASC,char_offset ASC,id ASC`, user, snap.bookID)
	if e != nil {
		return readerDBSnapshot{}, errBehaviorHTTP
	}
	defer rows.Close()
	for rows.Next() {
		var bm readerExpectedBookmark
		if rows.Scan(&bm.id, &bm.chapterIndex, &bm.charOffset, &bm.note) != nil || bm.id == uuid.Nil {
			return readerDBSnapshot{}, errBehaviorHTTP
		}
		snap.bookmarks = append(snap.bookmarks, bm)
	}
	if rows.Err() != nil {
		return readerDBSnapshot{}, errBehaviorHTTP
	}
	rows.Close()
	var payload string
	e = tx.QueryRow(ctx, `SELECT json_build_array(
		(SELECT coalesce(json_agg(t ORDER BY id),'[]'::json) FROM (SELECT id,username,token_version,is_disabled,updated_at FROM public.users) t),
		(SELECT coalesce(json_agg(t ORDER BY id),'[]'::json) FROM (SELECT id,user_id,revoked_at,expires_at FROM public.auth_sessions) t),
		(SELECT coalesce(json_agg(t ORDER BY id),'[]'::json) FROM (SELECT id,user_id,book_type,name,active_source_id,total_chapters,last_read_at,version,updated_at FROM public.books) t),
		(SELECT coalesce(json_agg(t ORDER BY id),'[]'::json) FROM (SELECT id,book_id,source_id,chapter_index,sort_order,title,is_volume,updated_at FROM public.chapters) t),
		(SELECT coalesce(json_agg(t ORDER BY id),'[]'::json) FROM (SELECT id,user_id,book_id,chapter_index,char_offset,version,server_updated_at FROM public.reading_progress) t),
		(SELECT coalesce(json_agg(t ORDER BY id),'[]'::json) FROM (SELECT id,user_id,book_id,chapter_index,char_offset,note,created_at FROM public.bookmarks) t),
		(SELECT coalesce(json_agg(t ORDER BY id),'[]'::json) FROM (SELECT id,user_id,book_source_url,version,updated_at FROM public.book_sources) t),
		(SELECT coalesce(json_agg(t ORDER BY id),'[]'::json) FROM (SELECT id,book_id,source_id,sync_status,last_synced_at,updated_at FROM public.book_source_mappings) t),
		(SELECT coalesce(json_agg(t ORDER BY id),'[]'::json) FROM (SELECT id,user_id,book_id,chapter_index,source_id,status,updated_at FROM public.chapter_source_overrides) t),
		(SELECT coalesce(json_agg(t ORDER BY id),'[]'::json) FROM (SELECT id,user_id,is_enabled,updated_at FROM public.replace_rules) t),
		(SELECT coalesce(json_agg(t ORDER BY id),'[]'::json) FROM (SELECT id,user_id,type,status,attempts,created_at,started_at,finished_at FROM public.jobs) t),
		(SELECT coalesce(json_agg(t ORDER BY user_id,key),'[]'::json) FROM (SELECT user_id,key,value,updated_at FROM public.app_settings) t)
	)::text`).Scan(&payload)
	if e != nil || ctx.Err() != nil || tx.Commit(ctx) != nil {
		return readerDBSnapshot{}, errBehaviorHTTP
	}
	sum := sha256.Sum256([]byte(payload))
	snap.digest = hex.EncodeToString(sum[:])
	return snap, nil
}

func (t *behaviorHTTPTransport) verifyReaderRoundtripHTTP(ctx context.Context, access string, expected readerDBSnapshot) error {
	booksRaw, e := t.get(ctx, "/api/v1/books", access, http.StatusOK)
	defer clear(booksRaw)
	if e != nil {
		return errBehaviorHTTP
	}
	var shelf api.BookPage
	if decodeStrictReaderJSON(booksRaw, &shelf) != nil || shelf.Total < 1 || len(shelf.Items) < 1 {
		return errBehaviorHTTP
	}
	foundBook := false
	for _, b := range shelf.Items {
		if b.Id == expected.bookID && b.BookType == api.BookBookType("local") {
			foundBook = true
			break
		}
	}
	if !foundBook {
		return errBehaviorHTTP
	}

	bookStr := expected.bookID.String()
	chaptersRaw, e := t.get(ctx, "/api/v1/books/"+bookStr+"/chapters", access, http.StatusOK)
	defer clear(chaptersRaw)
	if e != nil {
		return errBehaviorHTTP
	}
	var chapters api.ChapterList
	if decodeStrictReaderJSON(chaptersRaw, &chapters) != nil || chapters.BookId != expected.bookID || chapters.Total < 1 || len(chapters.Items) < 1 {
		return errBehaviorHTTP
	}
	foundChapter := false
	for _, ch := range chapters.Items {
		if ch.ChapterIndex != nil && *ch.ChapterIndex == expected.chapterIndex {
			foundChapter = true
			break
		}
	}
	if !foundChapter {
		return errBehaviorHTTP
	}

	contentRaw, e := t.get(ctx, "/api/v1/books/"+bookStr+"/content?chapter_index="+strconv.Itoa(expected.chapterIndex), access, http.StatusOK)
	defer clear(contentRaw)
	if e != nil {
		return errBehaviorHTTP
	}
	var content api.ChapterContent
	if decodeStrictReaderJSON(contentRaw, &content) != nil || content.Cached || content.TraceId != nil || content.Chapter.ChapterIndex == nil || *content.Chapter.ChapterIndex != expected.chapterIndex || strings.TrimSpace(content.Content) == "" || content.WordCount < 1 {
		return errBehaviorHTTP
	}

	progressRaw, e := t.get(ctx, "/api/v1/books/"+bookStr+"/progress", access, http.StatusOK)
	defer clear(progressRaw)
	if e != nil {
		return errBehaviorHTTP
	}
	var progress api.Progress
	if decodeStrictReaderJSON(progressRaw, &progress) != nil || progress.BookId != expected.bookID || progress.ChapterIndex != expected.progressChapter || progress.CharOffset != expected.progressCharOffset || int64(progress.Version) != expected.progressVersion {
		return errBehaviorHTTP
	}

	bookmarksRaw, e := t.get(ctx, "/api/v1/books/"+bookStr+"/bookmarks", access, http.StatusOK)
	defer clear(bookmarksRaw)
	if e != nil {
		return errBehaviorHTTP
	}
	var bookmarks []api.Bookmark
	if decodeStrictReaderJSON(bookmarksRaw, &bookmarks) != nil || len(bookmarks) != len(expected.bookmarks) {
		return errBehaviorHTTP
	}
	expMap := make(map[uuid.UUID]readerExpectedBookmark, len(expected.bookmarks))
	for _, bm := range expected.bookmarks {
		expMap[bm.id] = bm
	}
	for _, bm := range bookmarks {
		exp, ok := expMap[bm.Id]
		if !ok || bm.BookId != expected.bookID || bm.ChapterIndex != exp.chapterIndex || bm.CharOffset != exp.charOffset || (bm.Note == nil) != (exp.note == nil) || (bm.Note != nil && *bm.Note != *exp.note) {
			return errBehaviorHTTP
		}
		delete(expMap, bm.Id)
	}
	if len(expMap) != 0 {
		return errBehaviorHTTP
	}

	crossRaw, e := t.get(ctx, "/api/v1/books/"+expected.otherBookID.String()+"/content?chapter_index="+strconv.Itoa(expected.otherChapterIndex), access, http.StatusNotFound)
	defer clear(crossRaw)
	if e != nil || verifyBehaviorError(crossRaw, "not_found") != nil {
		return errBehaviorHTTP
	}
	return nil
}

// Explicit bounded read-only reader verification during a live authenticated session
// (after Login confirmation and before Logout revocation).
func (t *behaviorHTTPTransport) readerRoundtrip(ctx context.Context, consent behaviorConsent, proof *verifiedBehaviorAccounts, s behaviorSessionResponse) error {
	if t == nil || ctx == nil || ctx.Err() != nil || proof == nil || proof.guard == nil || s.UserID == uuid.Nil || s.SessionID == uuid.Nil {
		return errBehaviorHTTP
	}
	if consent.validate() != nil || !consent.allowsReaderRoundtrip() || consent.ID != proof.consent || consent.PlanDigest != proof.plan || consent.TargetID != t.binding.TargetID || consent.RuntimeSHA != proof.runtime || [3]string{consent.OperationSHA, consent.AttemptSHA, consent.PublicationSHA} != proof.records {
		return errBehaviorHTTP
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.checkProofWhitelistLocked(proof, s.UserID) != nil {
		return errBehaviorHTTP
	}
	now := t.clock()
	if now.Before(consent.Issued) || !now.Before(consent.Expires) {
		return errBehaviorHTTP
	}
	known, ok := t.secrets.known[s.SessionID]
	if !ok || known.user != s.UserID || known.attempted || t.secrets.readerTried[s.UserID] || verifyHTTPAccess(known.access, t.secrets.key, s.UserID, known.version, now) != nil {
		return errBehaviorHTTP
	}
	if t.secrets.readerTried == nil {
		t.secrets.readerTried = map[uuid.UUID]bool{}
	}
	t.secrets.readerTried[s.UserID] = true

	before, e := t.observeReader(ctx, s.UserID, s.SessionID)
	if e != nil || !before.sessionActive || before.activeJobs != 0 || before.bookID == uuid.Nil || before.otherBookID == uuid.Nil || before.bookID == before.otherBookID || before.chapterIndex < 0 || before.otherChapterIndex < 0 || !validSHA(before.digest) {
		return errBehaviorHTTP
	}
	if t.verifyReaderRoundtripHTTP(ctx, known.access, before) != nil {
		return errBehaviorHTTP
	}
	after, e := t.observeReader(ctx, s.UserID, s.SessionID)
	if e != nil || !after.sessionActive || after.activeJobs != 0 || after.bookID != before.bookID || after.chapterIndex != before.chapterIndex || after.otherBookID != before.otherBookID || after.digest != before.digest || ctx.Err() != nil {
		return errBehaviorHTTP
	}
	nowAfter := t.clock()
	if !validStamp(nowAfter) || nowAfter.Before(consent.Issued) || !nowAfter.Before(consent.Expires) || !nowAfter.Before(proof.expires) {
		return errBehaviorHTTP
	}
	if t.secrets.readerWitness == nil {
		t.secrets.readerWitness = map[uuid.UUID]readerRoundtripWitness{}
	}
	t.secrets.readerWitness[s.UserID] = readerRoundtripWitness{
		plan:         proof.plan,
		consent:      proof.consent,
		records:      proof.records,
		database:     proof.database,
		runtime:      proof.runtime,
		certificate:  t.certificateSHA,
		sessionID:    s.SessionID,
		bookID:       before.bookID,
		otherBookID:  before.otherBookID,
		observedAt:   nowAfter,
		beforeDigest: before.digest,
		afterDigest:  after.digest,
		confirmed:    true,
	}
	return nil
}

func (t *behaviorHTTPTransport) readerWitnessSnapshot(consent behaviorConsent, targetKey []byte) confirmedReaderSet {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !consent.allowsReaderRoundtrip() {
		drifted := t.secrets == nil || t.secrets.closed || t.secrets.readerDrifted || len(t.secrets.readerTried) != 0 || len(t.secrets.readerWitness) != 0
		return confirmedReaderSet{authorized: false, drifted: drifted}
	}
	out := confirmedReaderSet{
		authorized: true,
		users:      map[uuid.UUID]readerRoundtripWitness{},
	}
	if t.secrets == nil || t.secrets.closed || t.secrets.readerDrifted || !bytes.Equal(t.secrets.key, targetKey) {
		out.drifted = true
		return out
	}
	for u, w := range t.secrets.readerWitness {
		if w.confirmed {
			if w.consent != consent.ID || w.plan != consent.PlanDigest {
				out.drifted = true
				return out
			}
			out.users[u] = w
		}
	}
	return out
}
