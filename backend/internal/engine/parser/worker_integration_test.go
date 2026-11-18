//go:build integration && linux

package parser

import (
	"context"
	"math"
	"os"
	"testing"

	"yreader/backend/internal/engine/sandbox"
)

func TestWorkerParserIntegration(t *testing.T) {
	path := os.Getenv("TEST_SANDBOX_WORKER_SOCKET")
	if path == "" {
		t.Skip("requires separately isolated worker container")
	}
	remote, err := sandbox.NewRemote(sandbox.Config{}, nil, []string{path})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, tc := range []struct{ rule, input string }{
		{`@js:result.toUpperCase()`, "chapter"},
		{`@js:java.md5Encode("abc")`, ""},
		{`@js:source.setVariable("k","v");source.getVariable("k")`, ""},
		{`{{1+2}}`, ""},
	} {
		localEngine, remoteEngine := New(sandbox.New(sandbox.Config{}, nil)), New(remote)
		want, e := localEngine.Eval(ctx, tc.rule, tc.input)
		if e != nil {
			t.Fatal(e)
		}
		got, e := remoteEngine.Eval(ctx, tc.rule, tc.input)
		if e != nil || got != want {
			t.Fatal(tc.rule, got, want, e)
		}
	}
	e := New(remote)
	nodes, err := e.EvalListNodes(ctx, `@js:[{name:"book",index:1000000000}]`, "")
	if err != nil || len(nodes) != 1 {
		t.Fatal(nodes, err)
	}
	for _, field := range []struct{ rule, want string }{{`@json:$.name`, "book"}, {`@json:$.index`, "1000000000"}} {
		got, e := e.EvalField(ctx, nodes[0], field.rule)
		if e != nil || got != field.want {
			t.Fatal(got, field.want, e)
		}
	}
	got, err := remote.Eval(ctx, `1/x`, map[string]any{"x": math.Copysign(0, -1)})
	if err != nil || got != "-Infinity" {
		t.Fatal("negative zero changed across IPC", got, err)
	}
}
