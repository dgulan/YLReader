package api

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
	"yreader/backend/internal/service"
)

type importFunc func(context.Context) (*service.ImportBookResult, error)

func (f importFunc) ImportBook(c context.Context, _ uuid.UUID, _ *os.File, _ int64, _, _, _ string, _, _ *string) (*service.ImportBookResult, error) {
	return f(c)
}
func TestImportBudgetExpiration(t *testing.T) {
	t.Setenv("TMP", t.TempDir())
	s := NewServer(Deps{ImportTimeout: time.Millisecond, Import: importFunc(func(ctx context.Context) (*service.ImportBookResult, error) {
		<-ctx.Done()
		return nil, fmt.Errorf("%w", service.ErrCommitFailed)
	})})
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("第一章 测试\n正文"))
	w := httptest.NewRecorder()
	s.ImportLocalBook(w, r, ImportLocalBookParams{Filename: "test.txt"})
	var body ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != 504 || string(body.Error.Code) != "import_timeout" || w.Header().Get("Retry-After") != "10" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}
func TestImportDefaultBudgetAndNonTimeoutError(t *testing.T) {
	s := NewServer(Deps{Import: importFunc(func(ctx context.Context) (*service.ImportBookResult, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("missing deadline")
		}
		return nil, service.ErrDRMProtected
	})})
	if s.importTimeout != 90*time.Second {
		t.Fatal("default changed")
	}
	w := httptest.NewRecorder()
	s.ImportLocalBook(w, httptest.NewRequest("POST", "/", strings.NewReader("第一章 测试\n正文")), ImportLocalBookParams{Filename: "test.txt"})
	if w.Code != 422 || w.Header().Get("Retry-After") != "" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}
func TestClientCancellationIsNotImportBudgetExpiration(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := NewServer(Deps{Import: importFunc(func(ctx context.Context) (*service.ImportBookResult, error) { return nil, ctx.Err() })})
	w := httptest.NewRecorder()
	s.ImportLocalBook(w, httptest.NewRequest("POST", "/", strings.NewReader("第一章 测试\n正文")).WithContext(ctx), ImportLocalBookParams{Filename: "test.txt"})
	if strings.Contains(w.Body.String(), "import_timeout") || w.Header().Get("Retry-After") != "" {
		t.Fatal("client cancellation misclassified")
	}
}
