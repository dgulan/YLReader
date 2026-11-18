package api

import "testing"

func TestSourceURLRejectsCredentialsAndInvalidPorts(t *testing.T) {
	for _, raw := range []string{"https://user:pass@example.com", "https://example.com:65536", "https://example.com:", "https://example.com:0", "https://example.com:abc"} {
		if validateSourceURL(raw) == nil {
			t.Fatal("bad source URL accepted", raw)
		}
	}
	if e := validateSourceURL("https://example.com:443"); e != nil {
		t.Fatal(e)
	}
}
