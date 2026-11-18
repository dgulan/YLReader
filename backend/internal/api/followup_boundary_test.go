//go:build integration

package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

func TestFollowupReplaceRuleIntegerBoundariesPersist(t *testing.T) {
	ts, pool := newTestServer(t, nil)
	base := ts.URL + "/api/v1"
	uid, username, password := seedUser(t, pool)
	token := login(t, base, username, password)
	body := func(n int64) []byte {
		return mustJSON(t, map[string]any{"name": "boundary", "pattern": "x", "order_index": n})
	}
	status, raw := doJSON(t, http.MethodPost, base+"/replace-rules", token, body(-2147483648))
	if status != 201 {
		t.Fatal(status, string(raw))
	}
	var rule struct {
		ID uuid.UUID `json:"id"`
	}
	if err := json.Unmarshal(raw, &rule); err != nil {
		t.Fatal(err)
	}
	status, raw = doJSON(t, http.MethodPut, base+"/replace-rules/"+rule.ID.String(), token, body(2147483647))
	if status != 200 {
		t.Fatal(status, string(raw))
	}
	for _, method := range []string{http.MethodPost, http.MethodPut} {
		endpoint := base + "/replace-rules"
		if method == http.MethodPut {
			endpoint += "/" + rule.ID.String()
		}
		for _, n := range []int64{2147483648, -2147483649, 4294967296} {
			status, raw = doJSON(t, method, endpoint, token, body(n))
			if status != 400 {
				t.Fatal(method, n, status, string(raw))
			}
		}
	}
	var order int32
	var count int
	if err := pool.QueryRow(context.Background(), `SELECT order_index FROM replace_rules WHERE id=$1 AND user_id=$2`, rule.ID, uid).Scan(&order); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM replace_rules WHERE user_id=$1`, uid).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if order != 2147483647 || count != 1 {
		t.Fatal("overflow changed persisted rules", order, count)
	}
}
