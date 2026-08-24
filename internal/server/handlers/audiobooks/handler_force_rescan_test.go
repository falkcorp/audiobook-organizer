// file: internal/server/handlers/audiobooks/handler_force_rescan_test.go
// version: 1.0.0
// guid: 8e2b4a17-6c3d-4f9a-b105-7d2e4f8c6a03
// last-edited: 2026-08-24

// Tests for ForceRescanAudiobook, the only per-book forced re-read that is
// precise.
//
// Why this handler exists at all: the alternative is triggering library.scan
// with folder_path + force_update, which nils the scan cache for everything in
// scope. Folders are not uniformly small -- measured 2026-08-24,
// /mnt/bigdata/books/newbooks/audiobooks holds 1,458 files directly -- so that
// route re-reads 1,458 files to rescan one book. Setting NeedsRescan re-reads
// exactly one.

package audiobookshandler_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func TestForceRescanAudiobook_FlagsTheBook(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1"}, nil)
	d.store.EXPECT().MarkNeedsRescan("b1").Return(nil)

	c, w := newCtx("POST", "/audiobooks/b1/force-rescan", nil, p("id", "b1"))
	h.ForceRescanAudiobook(c)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (body %s)", w.Code, w.Body.String())
	}
	// The mock is strict: if MarkNeedsRescan were never called the expectation
	// would fail. Assert the response too, because a handler that returns 200
	// without flagging anything is the failure mode that matters -- the caller
	// believes the book will be re-read and it never will be.
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	body, _ := resp["data"].(map[string]any)
	if body == nil {
		body = resp
	}
	if body["needs_rescan"] != true {
		t.Errorf("response must confirm the flag was set, got %v", body["needs_rescan"])
	}
}

// The load-bearing guard. A swallowed MarkNeedsRescan error is indistinguishable
// from success at the call site: the caller is told the book is queued and it
// simply never gets re-read. Mutation-tested by replacing the error branch with
// a fallthrough to RespondWithOK -- this test then fails, the happy-path test
// does not.
func TestForceRescanAudiobook_StoreErrorIsNotSwallowed(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1"}, nil)
	d.store.EXPECT().MarkNeedsRescan("b1").Return(errString("pebble write failed"))

	c, w := newCtx("POST", "/audiobooks/b1/force-rescan", nil, p("id", "b1"))
	h.ForceRescanAudiobook(c)

	if w.Code == http.StatusOK {
		t.Fatal("a failed MarkNeedsRescan returned 200: the caller is told the " +
			"book is queued for a rescan that will never happen")
	}
	if w.Code != http.StatusInternalServerError {
		t.Errorf("want 500, got %d", w.Code)
	}
}

// A missing book must not be flagged -- MarkNeedsRescan has no expectation here,
// so the strict mock fails the test if the handler calls it anyway.
func TestForceRescanAudiobook_NotFoundDoesNotFlag(t *testing.T) {
	h, d := newHandler(t)
	d.store.EXPECT().GetBookByID("nope").Return(nil, nil)

	c, w := newCtx("POST", "/audiobooks/nope/force-rescan", nil, p("id", "nope"))
	h.ForceRescanAudiobook(c)

	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", w.Code)
	}
}
