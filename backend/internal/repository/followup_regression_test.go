package repository

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

func TestFollowupImportedIntegerBounds(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want int32
	}{
		{"2147483647", 2147483647}, {"-2147483648", -2147483648}, {"2147483648", 7}, {"4294967296", 7}, {"1e100", 7},
		{"12.9", 12}, {`"123"`, 123}, {`"123oops"`, 7}, {`"2147483648"`, 7},
	} {
		if got := rawInt32(json.RawMessage(tc.raw), 7); got != tc.want {
			t.Fatal(tc.raw, got, tc.want)
		}
	}
	if got := rawInt16(json.RawMessage(`65537`), 0); got != 0 {
		t.Fatal("source type wrapped", got)
	}
}

func TestFollowupImportedIdentityAndRulesPreserved(t *testing.T) {
	for _, raw := range []string{"file:///book", "DQuestQBall"} {
		data, _ := json.Marshal(map[string]string{"bookSourceUrl": raw})
		if _, err := parseSource(data, uuid.New()); err == nil {
			t.Fatal("invalid URL accepted", raw)
		}
	}
	// Existing Legado identity values are not all canonical request URLs.
	for _, identity := range []string{"https://www.qidian.com       ", "http://www.630book.cc-By Dark"} {
		data, _ := json.Marshal(map[string]string{"bookSourceUrl": identity})
		src, err := parseSource(data, uuid.New())
		if err != nil || src.BookSourceURL != identity {
			t.Fatal("legacy identity changed", src, err)
		}
	}
	raw := json.RawMessage(`{"bookSourceUrl":"https://example.com:8443/path?q=1","weight":4294967296,"bookSourceType":65537,"extra":{"x":1}}`)
	src, err := parseSource(raw, uuid.New())
	if err != nil || src.Weight != 0 || src.BookSourceType != 0 || string(src.Rules) != string(raw) {
		t.Fatal(src, err)
	}
}
