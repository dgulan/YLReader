package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestFollowupReplaceRuleOrderOverflow(t *testing.T) {
	for _, n := range []string{"2147483648", "-2147483649", "4294967296"} {
		for _, update := range []bool{false, true} {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"rule","pattern":"x","order_index":`+n+`}`))
			s := &Server{}
			if update {
				s.UpdateReplaceRule(w, r, uuid.New())
			} else {
				s.CreateReplaceRule(w, r)
			}
			if w.Code != 400 {
				t.Fatal("overflow reached service", w.Code)
			}
		}
	}
}
