package parser

import (
	"context"
	"reflect"
	"testing"
)

func TestFollowupCSSNegativeExclusionAndPosition(t *testing.T) {
	e := newTestEngine()
	input := `<ul><li>A</li><li>B</li><li>C</li></ul>`
	for _, tc := range []struct {
		rule string
		want []string
	}{
		{"tag.li!-1@text", []string{"A", "B"}},
		{"tag.li!0.0@text", []string{"B"}},
		{"tag.li!0[0]@text", []string{"B"}},
		{"tag.li.0!0@text", []string{"B"}},
		{"tag.li!-1.-1@text", []string{"B"}},
		{"tag.li!-99@text", []string{"A", "B", "C"}},
		{"tag.li!0.999@text", []string{}},
	} {
		t.Run(tc.rule, func(t *testing.T) {
			got, err := e.EvalList(context.Background(), tc.rule, input)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatal(got, tc.want, err)
			}
		})
	}
}
