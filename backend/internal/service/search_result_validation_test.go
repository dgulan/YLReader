package service

import (
	"context"
	"testing"
)

func TestSearchDoesNotReturnEmptyOrUnusableBooks(t *testing.T) {
	s := &SearchService{}
	rules := searchRules{BookList: "$.data[*]", Name: "$.name", BookURL: "$.url"}
	for _, body := range []string{
		`{"code":401,"data":{}}`,
		`{"data":[{}, {"name":" ","url":"/book"}, {"name":"missing URL"}, {"name":"script","url":"javascript:alert(1)"}, {"name":"file","url":"file:///tmp/book"}]}`,
	} {
		got, err := s.parseBookList(context.Background(), body, "https://books.example/search", rules)
		if err != nil || len(got) != 0 {
			t.Fatalf("unusable records returned as books: %#v, %v", got, err)
		}
	}
}

func TestSearchKeepsValidBookAmongMalformedRecords(t *testing.T) {
	s := &SearchService{}
	rules := searchRules{BookList: "$.data[*]", Name: "$.name", BookURL: "/novel/{{$.id}}?read=1"}
	got, err := s.parseBookList(context.Background(), `{"data":[{}, {"name":"  原创验收  ","id":"42"}]}`, "https://books.example/search", rules)
	if err != nil || len(got) != 1 || got[0].Name != "原创验收" || got[0].BookURL != "https://books.example/novel/42?read=1" {
		t.Fatalf("valid book lost or malformed: %#v, %v", got, err)
	}
}
