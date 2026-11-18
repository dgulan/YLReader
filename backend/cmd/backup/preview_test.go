package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPreviewArgumentsAndPrivateFailures(t *testing.T) {
	_, _, clock := commandFixture()
	root := t.TempDir()
	for _, args := range [][]string{
		{"inspect"}, {"inspect", "--input-root", "relative", "--request-key", "r"},
		{"inspect", "--input-root", root, "--request-key", "r", "--config-key", "SECRET"},
		{"plan", "--input-root", root, "--request-key", "r"},
		{"inspect", "--input-root", root, "--request-key", "r", "--request-key", "SECRET"},
	} {
		called := false
		var out, errs bytes.Buffer
		if code := run(context.Background(), args, &out, &errs, func(string) (controlReader, error) { called = true; return nil, nil }, clock); code != 2 || called || out.Len() != 0 || strings.Contains(errs.String(), "SECRET") {
			t.Fatal("unsafe arguments")
		}
	}
	args := []string{"inspect", "--input-root", root, "--request-key", "request.json"}
	var out, errs bytes.Buffer
	if code := run(context.Background(), args, &out, &errs, func(string) (controlReader, error) { return nil, errors.New("SECRET") }, clock); code != 3 || strings.Contains(errs.String(), "SECRET") {
		t.Fatal("open error exposed")
	}
	r := &commandReader{data: map[string][]byte{"request.json": []byte(`{"SECRET":true}`)}}
	errs.Reset()
	if code := run(context.Background(), args, &out, &errs, func(string) (controlReader, error) { return r, nil }, clock); code != 4 || !r.closed || out.Len() != 0 || strings.Contains(errs.String(), "SECRET") {
		t.Fatal("invalid request leaked")
	}
	r.closeErr = errors.New("SECRET")
	errs.Reset()
	if code := run(context.Background(), args, &out, &errs, func(string) (controlReader, error) { return r, nil }, clock); code != 5 || out.Len() != 0 || strings.Contains(errs.String(), "SECRET") {
		t.Fatal("close failure ignored")
	}
	for _, key := range []string{"../SECRET", "/SECRET", "a//b", "a\\b", "a/./b", "C:SECRET"} {
		if validPreviewKey(key) {
			t.Fatal("unsafe key")
		}
	}
}
