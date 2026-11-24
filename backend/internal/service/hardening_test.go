package service

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"yreader/backend/internal/engine/fetcher"
	"yreader/backend/internal/repository"
)

type headerRecordingFetcher struct {
	calls []struct {
		url     string
		headers http.Header
	}
	fn func(ctx context.Context, target string, header http.Header) (*fetcher.Response, error)
}

func (f *headerRecordingFetcher) Get(ctx context.Context, target string, header http.Header) (*fetcher.Response, error) {
	f.calls = append(f.calls, struct {
		url     string
		headers http.Header
	}{url: target, headers: header.Clone()})
	return f.fn(ctx, target, header)
}

func writeTestEPUBFile(t *testing.T, entries map[string]string) (*os.File, int64) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "test.epub")
	out, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(out)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := out.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := out.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = out.Close() })
	return out, info.Size()
}

func TestEPUBHardeningLimitsAndDeduplication(t *testing.T) {
	svc := &ImportService{}

	t.Run("rejects entry larger than 8 MiB", func(t *testing.T) {
		f, sz := writeTestEPUBFile(t, map[string]string{
			"META-INF/container.xml": `<?xml version="1.0"?><container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="content.opf"/></rootfiles></container>`,
			"content.opf":            `<?xml version="1.0"?><package><metadata><title>Big</title></metadata><manifest><item id="c1" href="ch1.xhtml"/></manifest><spine><itemref idref="c1"/></spine></package>`,
			"ch1.xhtml":              "<html><body><p>" + strings.Repeat("a", epubMaxEntryBytes+1024) + "</p></body></html>",
		})
		if _, _, err := svc.parseEPUB(context.Background(), f, sz, "fallback"); !errors.Is(err, repository.ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
	})

	t.Run("rejects more than 5000 TOC entries", func(t *testing.T) {
		var nav strings.Builder
		nav.WriteString(`<html><body><nav epub:type="toc"><ol>`)
		for i := 0; i <= epubMaxTOCEntries; i++ {
			fmt.Fprintf(&nav, `<li><a href="ch1.xhtml#s%d">Ch %d</a></li>`, i, i)
		}
		nav.WriteString(`</ol></nav></body></html>`)
		f, sz := writeTestEPUBFile(t, map[string]string{
			"META-INF/container.xml": `<?xml version="1.0"?><container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="content.opf"/></rootfiles></container>`,
			"content.opf":            `<?xml version="1.0"?><package><metadata><title>ManyTOC</title></metadata><manifest><item id="nav" href="nav.xhtml" properties="nav"/><item id="c1" href="ch1.xhtml"/></manifest><spine><itemref idref="c1"/></spine></package>`,
			"nav.xhtml":              nav.String(),
			"ch1.xhtml":              `<html><body><p>Body</p></body></html>`,
		})
		if _, _, err := svc.parseEPUB(context.Background(), f, sz, "fallback"); !errors.Is(err, repository.ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
	})

	t.Run("deduplicates repeated spine targets cleanly", func(t *testing.T) {
		var nav strings.Builder
		nav.WriteString(`<html><body><nav epub:type="toc"><ol>`)
		for i := 0; i < 20; i++ {
			fmt.Fprintf(&nav, `<li><a href="ch1.xhtml#sec%d">Sec %d</a></li>`, i, i)
		}
		nav.WriteString(`</ol></nav></body></html>`)
		f, sz := writeTestEPUBFile(t, map[string]string{
			"META-INF/container.xml": `<?xml version="1.0"?><container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="content.opf"/></rootfiles></container>`,
			"content.opf":            `<?xml version="1.0"?><package><metadata><title>Dedup</title></metadata><manifest><item id="nav" href="nav.xhtml" properties="nav"/><item id="c1" href="ch1.xhtml"/></manifest><spine><itemref idref="c1"/></spine></package>`,
			"nav.xhtml":              nav.String(),
			"ch1.xhtml":              `<html><body><p>Shared content</p></body></html>`,
		})
		pb, _, err := svc.parseEPUB(context.Background(), f, sz, "fallback")
		if err != nil {
			t.Fatalf("parseEPUB: %v", err)
		}
		if len(pb.Chapters) != 20 {
			t.Fatalf("chapters = %d, want 20", len(pb.Chapters))
		}
	})
}

func TestTXTHardeningLimits(t *testing.T) {
	svc := &ImportService{}

	t.Run("rejects file size exceeding 64 MiB", func(t *testing.T) {
		f, err := os.CreateTemp(t.TempDir(), "big-*.txt")
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, _, err := svc.parseTXT(context.Background(), f, txtMaxFileBytes+1, "big"); !errors.Is(err, repository.ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
	})

	t.Run("rejects more than 20000 chapters", func(t *testing.T) {
		var b strings.Builder
		for i := 1; i <= txtMaxChapters+1; i++ {
			fmt.Fprintf(&b, "第%d章 标题\n正文\n", i)
		}
		if _, _, err := parseTXTRaw(context.Background(), b.String(), "many"); !errors.Is(err, repository.ErrInvalidArgument) {
			t.Fatalf("err = %v, want ErrInvalidArgument", err)
		}
	})

	t.Run("splits oversized chapter even with chapter titles present", func(t *testing.T) {
		longBody := strings.Repeat("字", txtSingleChapterMaxRunes+500)
		text := fmt.Sprintf("第1章 超长章\n%s\n第2章 短章\n短正文\n", longBody)
		pb, warnings, err := parseTXTRaw(context.Background(), text, "split")
		if err != nil {
			t.Fatalf("parseTXTRaw: %v", err)
		}
		if len(pb.Chapters) != 3 {
			t.Fatalf("chapters = %d, want 3 (2 split + 1 short)", len(pb.Chapters))
		}
		if utf8.RuneCountInString(pb.Chapters[0].Content) != txtSingleChapterMaxRunes {
			t.Fatalf("first split chunk runes = %d, want %d", utf8.RuneCountInString(pb.Chapters[0].Content), txtSingleChapterMaxRunes)
		}
		if len(warnings) == 0 {
			t.Fatal("expected split warning")
		}
	})
}

func TestReplaceRuleLengthAndContentOutputCap(t *testing.T) {
	if err := validateReplaceRuleInput(&ReplaceRuleInput{
		Name:        "too-long-pattern",
		Pattern:     strings.Repeat("a", maxReplacePatternBytes+1),
		Replacement: "b",
	}); !errors.Is(err, repository.ErrInvalidArgument) {
		t.Fatalf("long pattern err = %v, want ErrInvalidArgument", err)
	}
	if err := validateReplaceRuleInput(&ReplaceRuleInput{
		Name:        "too-long-repl",
		Pattern:     "a",
		Replacement: strings.Repeat("b", maxReplaceReplacementBytes+1),
	}); !errors.Is(err, repository.ErrInvalidArgument) {
		t.Fatalf("long replacement err = %v, want ErrInvalidArgument", err)
	}

	// 50,000 matches * 1,000 byte replacement = 50 MiB > 32 MiB cap: must skip without expanding.
	input := strings.Repeat("a", 50000)
	repl := strings.Repeat("x", 1000)
	ruleID := uuid.New()
	out, applied := applyRuleList([]*repository.ReplaceRule{{
		ID:          ruleID,
		Pattern:     "a",
		Replacement: repl,
	}}, input)
	if out != input || len(applied) != 0 {
		t.Fatalf("amplifying replace rule was applied: len=%d applied=%d", len(out), len(applied))
	}

	out = applySourceReplaceRegex(input, "a##"+repl)
	if out != input {
		t.Fatalf("amplifying source replace regex expanded to %d bytes", len(out))
	}
}

func TestWorkerPoolExecuteSafeRecoversPanic(t *testing.T) {
	wp := NewWorkerPool(nil, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))
	wp.SetExecuteFunc(func(context.Context, uuid.UUID) error {
		panic("boom")
	})
	if err := wp.executeSafe(context.Background(), 1, uuid.New()); err == nil || !strings.Contains(err.Error(), "panic") {
		t.Fatalf("executeSafe err = %v, want panic error", err)
	}
}

func TestCrossHostAndDowngradeHeaderScoping(t *testing.T) {
	srcHeaders := json.RawMessage(`{"Cookie":"sid=secret","Authorization":"Bearer tok","X-Auth-Token":"x-secret","User-Agent":"YLReader"}`)

	t.Run("TOC nextTocUrl strips sensitive headers across host and https->http", func(t *testing.T) {
		ft := &headerRecordingFetcher{}
		ft.fn = func(_ context.Context, target string, _ http.Header) (*fetcher.Response, error) {
			switch target {
			case "https://book.example/toc/1":
				return &fetcher.Response{
					StatusCode: 200,
					FinalURL:   target,
					Body:       []byte(`<ul class="list"><li><a href="/ch/1">Ch 1</a></li></ul><a class="next" href="https://cdn.example/toc/2">Next</a>`),
				}, nil
			case "https://cdn.example/toc/2":
				return &fetcher.Response{
					StatusCode: 200,
					FinalURL:   target,
					Body:       []byte(`<ul class="list"><li><a href="/ch/2">Ch 2</a></li></ul><a class="next" href="http://book.example/toc/3">Next</a>`),
				}, nil
			default:
				return &fetcher.Response{
					StatusCode: 200,
					FinalURL:   target,
					Body:       []byte(`<ul class="list"><li><a href="/ch/3">Ch 3</a></li></ul>`),
				}, nil
			}
		}
		svc := NewTOCService(nil, ft, nil).WithSleep(func(context.Context, time.Duration) error { return nil })
		chapters, err := svc.fetchTOCWithHeaders(context.Background(), tocRules{
			ChapterList: ".list li",
			ChapterName: "a@text",
			ChapterUrl:  "a@href",
			NextTocURL:  "a.next@href",
		}, "https://book.example/toc/1", parseSourceHeaders(srcHeaders))
		if err != nil {
			t.Fatalf("fetchTOCWithHeaders: %v", err)
		}
		if len(chapters) != 3 || len(ft.calls) != 3 {
			t.Fatalf("chapters=%d calls=%d", len(chapters), len(ft.calls))
		}
		if ft.calls[0].headers.Get("Cookie") != "sid=secret" {
			t.Fatalf("same-origin hop 0 missing Cookie: %+v", ft.calls[0].headers)
		}
		for _, hop := range []int{1, 2} {
			for _, k := range []string{"Cookie", "Authorization", "X-Auth-Token"} {
				if got := ft.calls[hop].headers.Get(k); got != "" {
					t.Fatalf("hop %d leaked %s = %q", hop, k, got)
				}
			}
			if ft.calls[hop].headers.Get("User-Agent") != "YLReader" {
				t.Fatalf("hop %d dropped User-Agent", hop)
			}
		}
	})

	t.Run("Content nextContentUrl strips sensitive headers across host and https->http", func(t *testing.T) {
		ft := &headerRecordingFetcher{}
		ft.fn = func(_ context.Context, target string, _ http.Header) (*fetcher.Response, error) {
			switch target {
			case "https://book.example/ch/1":
				return &fetcher.Response{
					StatusCode: 200,
					FinalURL:   target,
					Body:       []byte(`<div id="c">Part 1</div><a class="next" href="http://book.example/ch/1?p=2">Next</a>`),
				}, nil
			default:
				return &fetcher.Response{
					StatusCode: 200,
					FinalURL:   target,
					Body:       []byte(`<div id="c">Part 2</div>`),
				}, nil
			}
		}
		svc := NewContentService(nil, ft, nil, nil, nil, nil)
		text, err := svc.fetchContentWithHeaders(context.Background(), contentRules{
			Content:        "#c@text",
			NextContentURL: "a.next@href",
		}, "https://book.example/ch/1", parseSourceHeaders(srcHeaders), nil)
		if err != nil {
			t.Fatalf("fetchContentWithHeaders: %v", err)
		}
		if !strings.Contains(text, "Part 1") || !strings.Contains(text, "Part 2") {
			t.Fatalf("unexpected content: %q", text)
		}
		if ft.calls[0].headers.Get("Authorization") != "Bearer tok" {
			t.Fatalf("hop 0 missing Authorization: %+v", ft.calls[0].headers)
		}
		for _, k := range []string{"Cookie", "Authorization", "X-Auth-Token", "Referer"} {
			if got := ft.calls[1].headers.Get(k); got != "" {
				t.Fatalf("downgraded hop 1 leaked %s = %q", k, got)
			}
		}
	})

	t.Run("Search and Debug scope headers against BookSourceURL", func(t *testing.T) {
		ft := &headerRecordingFetcher{}
		ft.fn = func(_ context.Context, target string, _ http.Header) (*fetcher.Response, error) {
			return &fetcher.Response{
				StatusCode: 200,
				FinalURL:   target,
				Body:       []byte(`<div class="b"><a href="/1">Title</a></div>`),
			}, nil
		}
		src := &repository.BookSource{
			ID:            uuid.New(),
			BookSourceURL: "https://origin.example",
			Header:        srcHeaders,
			Rules:         json.RawMessage(`{"searchUrl":"https://other.example/find?q={{key}}","ruleSearch":{"bookList":".b","name":"a@text","bookUrl":"a@href"}}`),
		}
		ss := NewSearchService(nil, ft, nil)
		_, _ = ss.searchOne(context.Background(), src, "test", 1, 10)
		if len(ft.calls) != 1 {
			t.Fatalf("expected 1 search call, got %d", len(ft.calls))
		}
		for _, k := range []string{"Cookie", "Authorization", "X-Auth-Token"} {
			if got := ft.calls[0].headers.Get(k); got != "" {
				t.Fatalf("cross-host search leaked %s = %q", k, got)
			}
		}

		ft.calls = nil
		ds := NewDebugService(nil, ft, nil)
		downgrade := "http://origin.example/test"
		if _, err := ds.resolveInput(context.Background(), src, &downgrade, nil); err != nil {
			t.Fatalf("resolveInput: %v", err)
		}
		for _, k := range []string{"Cookie", "Authorization", "X-Auth-Token"} {
			if got := ft.calls[0].headers.Get(k); got != "" {
				t.Fatalf("downgraded debug leaked %s = %q", k, got)
			}
		}
	})

	// Ensure unused bytes import isn't flagged if omitted.
	_ = bytes.Buffer{}
}
