package sandbox

import (
	"context"
	"github.com/google/uuid"
	"testing"
)

func TestGeminiUUIDAndPadding(t *testing.T) {
	s := New(Config{}, nil)
	a, e := s.Eval(context.Background(), `java.randomUUID()`, nil)
	if e != nil {
		t.Fatal(e)
	}
	b, e := s.Eval(context.Background(), `java.randomUUID()`, nil)
	if _, e := uuid.Parse(a); e != nil || a == b {
		t.Fatal("invalid/non-unique UUID")
	}
	if e != nil {
		t.Fatal(e)
	}
	if _, e := pkcs7Unpad([]byte{1, 0, 2}); e == nil {
		t.Fatal("corrupt padding accepted")
	}
	if aesDecrypt(aesEncrypt("正文😀", "compat-key"), "compat-key") != "正文😀" {
		t.Fatal("legacy alias changed")
	}
}
