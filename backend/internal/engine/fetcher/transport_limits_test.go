package fetcher

import (
	"net/http"
	"testing"
)

// TestNewCapsResponseHeaderBytes 确保主抓取器对响应头设置硬上限，
// 避免恶意书源用超大响应头耗尽内存。
func TestNewCapsResponseHeaderBytes(t *testing.T) {
	f := New(Config{})
	tr, ok := f.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", f.client.Transport)
	}
	if tr.MaxResponseHeaderBytes != 64<<10 {
		t.Fatalf("MaxResponseHeaderBytes = %d, want %d", tr.MaxResponseHeaderBytes, 64<<10)
	}
}
