package fetcher

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func TestResponseModesPreserveImagesAndDecodeLegacyText(t *testing.T) {
	f := New(Config{})
	read := func(data []byte) *http.Response {
		return &http.Response{Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(data))}
	}
	png := []byte{137, 80, 78, 71, 13, 10, 26, 10, 0, 255, 233}
	ctx := WithResponseCharset(t.Context(), "gb18030")
	binary := responseMode{binaryLimit: int64(len(png))}
	got, err := f.decodeWithContext(context.WithValue(ctx, responseModeKey{}, binary), read(png), "https://example.com/image")
	if err != nil || !bytes.Equal(got.Body, png) {
		t.Fatalf("image bytes changed: %v", err)
	}
	chinese, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte("中文"))
	if err != nil {
		t.Fatal(err)
	}
	got, err = f.decodeWithContext(ctx, read(chinese), "https://example.com/search")
	if err != nil || string(got.Body) != "中文" {
		t.Fatalf("legacy decode failed: %v", err)
	}
}
