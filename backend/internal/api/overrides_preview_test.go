//go:build integration

package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"
)

func TestSetChapterOverride_TrialFetchPreviewAndCacheRefresh(t *testing.T) {
	const (
		s1BookURL = "https://ov-s1.example.com/book/1"
		s2c1      = "https://ov-s2.example.com/c/1"
		s2Fail    = "https://ov-s2.example.com/c/fail"
	)
	longText := strings.Repeat("试", 600)
	mock := newTOCMock(map[string]string{
		s1BookURL: tocPage(false, []string{"/c/0", "/c/1"}, ""),
		s2c1:      `<html><body><div id="content"><p>` + longText + `</p></div></body></html>`,
		s2Fail:    `<html><body><div class="other">no match</div></body></html>`,
	})
	ts, pool := newTestServer(t, mock)
	base := ts.URL + "/api/v1"
	userID, username, password := seedUser(t, pool)
	token := login(t, base, username, password)
	s1 := seedSource(t, pool, userID, "https://ov-s1.example.com",
		contentRulesJSON("ul.list li", "a@text", "a@href", "", "", "div#content@text", ""))
	s2 := seedSource(t, pool, userID, "https://ov-s2.example.com",
		contentRulesJSON("ul.list li", "a@text", "a@href", "", "", "div#content@text", ""))

	var bookID uuid.UUID
	seedBookWithTOC(t, base, token, &bookID, s1, s1BookURL, "覆盖试抓书")

	// 1. 试抓失败时不落库
	st, _ := doJSON(t, http.MethodPost, base+"/books/"+bookID.String()+"/chapters/1/override", token,
		mustJSON(t, map[string]any{"chapter_url": s2Fail, "source_id": s2.String()}))
	if st != http.StatusUnprocessableEntity {
		t.Fatalf("failed trial fetch status = %d, want 422", st)
	}
	if n := dbInt(t, pool, `SELECT count(*) FROM chapter_source_overrides WHERE book_id = $1 AND chapter_index = 1`, bookID); n != 0 {
		t.Fatalf("override rows = %d, want 0 after failed trial fetch", n)
	}

	// 2. 试抓成功返回 512 字符预览，并刷新缓存
	st, raw := doJSON(t, http.MethodPost, base+"/books/"+bookID.String()+"/chapters/1/override", token,
		mustJSON(t, map[string]any{"chapter_url": s2c1, "source_id": s2.String()}))
	if st != http.StatusOK {
		t.Fatalf("set override status = %d: %s", st, raw)
	}
	var ov struct {
		ContentPreview string `json:"content_preview"`
	}
	if err := json.Unmarshal(raw, &ov); err != nil {
		t.Fatalf("decode override: %v", err)
	}
	if utf8.RuneCountInString(ov.ContentPreview) != 512 {
		t.Fatalf("content_preview rune count = %d, want 512", utf8.RuneCountInString(ov.ContentPreview))
	}

	// 后续读取第 1 章应直接命中缓存（不再重复回源）
	beforeHits := mock.Count()
	st, contentResp := getContent(t, base, token, bookID.String(), "?chapter_index=1")
	if st != http.StatusOK {
		t.Fatalf("get content status = %d", st)
	}
	if !contentResp.Cached {
		t.Fatal("expected cached=true after setChapterOverride cache refresh")
	}
	if mock.Count() != beforeHits {
		t.Fatalf("expected cache hit without refetch, calls before=%d after=%d", beforeHits, mock.Count())
	}
}
