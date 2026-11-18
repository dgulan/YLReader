package parser

import (
	"context"
	"testing"
)

func TestNextURLCSSSelectorChain(t *testing.T) {
	e := New(nil)
	body := `<a class="next" href="/wrong">outside</a><nav class="pager" data-owner="a@b"><a href="/previous">上一页</a><a class="next" href="/page/2">下一页</a></nav>`
	for _, rule := range []string{`.pager@a.next@href`, `.pager[data-owner='a@b']@a.next@href`, `class.pager@tag.a.1@href`} {
		got, err := e.EvalNextURL(context.Background(), rule, body)
		if err != nil || got != "/page/2" {
			t.Fatal(rule, got, err)
		}
	}
}
