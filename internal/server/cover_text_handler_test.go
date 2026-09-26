// file: internal/server/cover_text_handler_test.go
// version: 1.0.0
// guid: 5a0c7e39-4d2b-4f81-96e3-1b8d2f6a9c04
// last-edited: 2026-09-26

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/covertext"
)

func TestHandleGetCoverText(t *testing.T) {
	srv, store, _ := setupCoverHistoryServer(t)

	get := func(id string) (int, map[string]any) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks/"+id+"/cover-text", nil)
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, req)
		var body map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		return w.Code, body
	}

	code, body := get("b1")
	if code != http.StatusOK {
		t.Fatalf("unindexed book: %d %v", code, body)
	}

	h := strings.Repeat("ab", 32)
	if err := covertext.Put(store, &covertext.Record{Hash: h, Status: covertext.StatusOK, PromptVersion: covertext.PromptVersion,
		Model: "m", ReadAt: time.Now(), Text: &covertext.Text{Title: "Printed Title"}}); err != nil {
		t.Fatal(err)
	}
	if err := covertext.PutBook(store, &covertext.BookIndex{BookID: "b1", Images: []covertext.ImageRef{
		{Hash: h, Source: covertext.SourceFolder, Path: "/secret/path/cover.jpg"}}}); err != nil {
		t.Fatal(err)
	}
	code, body = get("b1")
	if code != http.StatusOK {
		t.Fatalf("indexed book: %d %v", code, body)
	}
	raw, _ := json.Marshal(body)
	if !strings.Contains(string(raw), "Printed Title") || !strings.Contains(string(raw), `"source":"folder"`) {
		t.Fatalf("response = %s", raw)
	}
	if strings.Contains(string(raw), "/secret/path") {
		t.Fatal("response leaked the on-disk path")
	}
}
