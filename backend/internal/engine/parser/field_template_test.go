package parser

import (
	"context"
	"errors"
	"testing"

	"yreader/backend/internal/engine/sandbox"
)

func TestJSONFieldRelativeURLTemplate(t *testing.T) {
	e := New(nil)
	nodes, err := e.EvalListNodes(context.Background(), "$.data[*]", `{"data":[{"id":"42"}]}`)
	if err != nil || len(nodes) != 1 {
		t.Fatal(nodes, err)
	}
	got, err := e.EvalField(context.Background(), nodes[0], "/novel/{{$.id}}?read=1")
	if err != nil || got != "/novel/42?read=1" {
		t.Fatal(got, err)
	}
	// Actual XPath remains incompatible with a JSON node.
	if _, err := e.EvalField(context.Background(), nodes[0], "/novel/id"); !errors.Is(err, sandbox.ErrUnsupportedRule) {
		t.Fatal("XPath compatibility check was bypassed", err)
	}
}
