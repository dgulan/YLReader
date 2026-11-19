package parser

import (
	"context"
	"errors"
	"testing"

	"yreader/backend/internal/engine/sandbox"
)

func TestInvalidXPathReturnsErrorWithoutPanic(t *testing.T) {
	e := New(nil)
	htmlDoc := `<html><body><div class="book"><p>hello</p></div></body></html>`
	for _, expr := range []string{"///", "//div[@id=]"} {
		if _, err := e.Eval(context.Background(), "@XPath:"+expr, htmlDoc); !errors.Is(err, sandbox.ErrRuleExecution) {
			t.Fatalf("Eval(%q) err = %v, want ErrRuleExecution", expr, err)
		}
		if _, err := e.EvalListNodes(context.Background(), "@XPath:"+expr, htmlDoc); !errors.Is(err, sandbox.ErrRuleExecution) {
			t.Fatalf("EvalListNodes(%q) err = %v, want ErrRuleExecution", expr, err)
		}
	}
}

func TestMakeUpNonRescanningAndContextCancel(t *testing.T) {
	sb := sandbox.New(sandbox.Config{}, nil)
	e := New(sb)
	e.SetVariable("a", "@get:{b}")
	e.SetVariable("b", "@get:{a}")
	e.SetVariable("t", "{{1+1}}")

	got, err := e.makeUp(context.Background(), "prefix-@get:{a}-suffix", nil)
	if err != nil {
		t.Fatalf("makeUp self-ref @get failed: %v", err)
	}
	if got != "prefix-@get:{b}-suffix" {
		t.Fatalf("makeUp rescan leaked: got %q, want %q", got, "prefix-@get:{b}-suffix")
	}

	got, err = e.makeUp(context.Background(), `{{t}}`, nil)
	if err != nil {
		t.Fatalf("makeUp nested template failed: %v", err)
	}
	if got != "{{1+1}}" {
		t.Fatalf("makeUp template rescan: got %q, want %q", got, "{{1+1}}")
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.makeUp(canceled, `{{1+1}}`, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("makeUp canceled err = %v, want context.Canceled", err)
	}
}
