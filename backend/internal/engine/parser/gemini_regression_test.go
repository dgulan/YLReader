package parser

import (
	"context"
	"testing"
)

func TestGeminiMixedCaseJSTags(t *testing.T) {
	e := newTestEngine()
	for _, rule := range []string{`@JS:"MiXeD"`, `<JS>"MiXeD"</jS>`, `<Js>"İ中文MiXeD"</JS>`} {
		got, err := e.Eval(context.Background(), rule, "")
		if err != nil || (got != "MiXeD" && got != "İ中文MiXeD") {
			t.Fatal(rule, got, err)
		}
	}
	segments := splitSourceRule(`İ中文<JS>"A"</JS>`)
	if len(segments) != 2 || segments[0].raw != "İ中文" {
		t.Fatal(segments)
	}
}
func TestGeminiExclusionAndPutTrace(t *testing.T) {
	e := newTestEngine()
	got, err := e.EvalList(context.Background(), "tag.tr!0@text", `<table><tr><th>header</th></tr><tr><td>body</td></tr></table>`)
	if err != nil || len(got) != 1 || got[0] != "body" {
		t.Fatal(got, err)
	}
	_, trace, err := e.EvalListWithTrace(context.Background(), "content", `@put:{v:h1@text}p@text`, `<h1>VALUE</h1><p>BODY</p>`, 128)
	if err != nil || len(trace) < 2 {
		t.Fatal("missing put trace", len(trace), err)
	}
}
