package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

// Explicit internal observation only. It neither joins the dispatch ledger nor
// upgrades its review. Never send a refresh token to this read-only endpoint.
func (t *behaviorHTTPTransport) rejectOldAccess(ctx context.Context, user uuid.UUID, currentVersion int, access string, sourceKey []byte) (err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if ctx == nil || ctx.Err() != nil || t.secrets.closed || !t.users[user] || t.secrets.oldAccessTried[user] || bytes.Equal(sourceKey, t.secrets.key) || strings.ContainsAny(access, "\r\n") || checkOldBehaviorAccessAt(access, sourceKey, user, currentVersion, t.clock()) != nil || verifyHTTPAccess(access, t.secrets.key, user, currentVersion-1, t.clock()) == nil {
		return errBehaviorHTTP
	}
	if t.secrets.oldAccessTried == nil {
		t.secrets.oldAccessTried = map[uuid.UUID]bool{}
	}
	t.secrets.oldAccessTried[user] = true
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, t.endpoint+"/api/v1/auth/me", nil)
	if e != nil {
		return errBehaviorHTTP
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	r, e := t.client.Do(req)
	if e != nil {
		if r != nil && r.Body != nil {
			r.Body.Close()
		}
		return errBehaviorHTTP
	}
	defer func() {
		if r.Body.Close() != nil {
			err = errBehaviorHTTP
		}
	}()
	kind, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || kind != "application/json" || r.StatusCode != http.StatusUnauthorized || r.ContentLength > 8192 || (r.Header.Get("Content-Encoding") != "" && r.Header.Get("Content-Encoding") != "identity") {
		return errBehaviorHTTP
	}
	data, e := io.ReadAll(io.LimitReader(r.Body, 8193))
	defer clear(data)
	if e != nil || len(data) > 8192 || ctx.Err() != nil {
		return errBehaviorHTTP
	}
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
	if ctx.Err() != nil || checkOldBehaviorAccessAt(access, sourceKey, user, currentVersion, t.clock()) != nil {
		return errBehaviorHTTP
	}
	return nil
}
