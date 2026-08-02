// file: internal/server/handlers/abs/remove_continue_listening_test.go
// version: 1.0.0
// guid: 6c07d13a-b284-4e59-90f7-1a53e2c8b046
// last-edited: 2026-08-02

package abs_test

import (
	"net/http"
	"testing"
)

// "Remove from Continue Listening" stayed broken after the Phase 6 write half
// shipped, and neither the reference oracle nor this project's spec explained why:
// ABS 2.36.0 has no such route at all, and §1.8.6 recorded the wrong shape.
//
// AudioBooth's own source settled it (SessionService.swift:181-193, MPL-2.0):
//
//	NetworkRequest(path: "/api/me/progress/\(progressID)/remove-from-continue-listening",
//	               method: .get)
//
// A **GET**, under **/api/me/progress/:id** — not the POST-on-/api/me/item form we
// had registered. Every tap 404'd before reaching a handler, which is why nothing
// appeared in the audit log for it.
//
// These tests pin the client's exact call so a future "cleanup" cannot quietly drop
// it back to the shape the spec claims.

// TestRemoveFromContinueListening_ClientsExactCall is the regression that matters:
// the precise method and path AudioBooth sends.
func TestRemoveFromContinueListening_ClientsExactCall(t *testing.T) {
	w := newWriteHarness(t)
	w.patch(t, map[string]any{"currentTime": 600.0})

	// progressID is the mediaProgress row id — what the client holds.
	code, _, raw := w.req(t, http.MethodGet,
		"/api/me/progress/"+w.rowID()+"/remove-from-continue-listening", nil)
	if code != http.StatusOK {
		t.Fatalf("GET .../remove-from-continue-listening = %d %s — this is the exact call "+
			"AudioBooth makes; a non-200 means the feature is dead on the device", code, raw)
	}
	// 🔴 NetworkService treats an empty body as a decoding error even on a 2xx, and
	// the client decodes into an empty `struct Response: Codable {}` — so the body
	// must be a non-empty JSON object (§1.8.6).
	if raw == "" {
		t.Fatal("empty 200 body — the client's decoder fails on it (§1.8.6)")
	}

	row := w.getRow(t)
	if row["hideFromContinueListening"] != true {
		t.Fatalf("hideFromContinueListening = %#v, want true", row["hideFromContinueListening"])
	}
	// Hiding is not forgetting: the position must survive.
	if got := num(t, row, "currentTime"); got != 600 {
		t.Fatalf("currentTime = %v, want 600 — hiding must not discard the position", got)
	}
	if ids := continueListeningIDs(t, w); contains(ids, w.syncID) {
		t.Fatalf("book is still on the Continue Listening shelf: %v", ids)
	}
}

// TestRemoveFromContinueListening_AcceptsTheLibraryItemIDToo — the client sends the
// row id, but the bare libraryItemId is the other id a client can plausibly hold,
// and resolveBookID accepts both. Pinned so the tolerance is not lost.
func TestRemoveFromContinueListening_AcceptsTheLibraryItemIDToo(t *testing.T) {
	w := newWriteHarness(t)
	w.patch(t, map[string]any{"currentTime": 600.0})

	code, _, raw := w.req(t, http.MethodGet,
		"/api/me/progress/"+w.syncID+"/remove-from-continue-listening", nil)
	if code != http.StatusOK {
		t.Fatalf("got %d %s", code, raw)
	}
	if w.getRow(t)["hideFromContinueListening"] != true {
		t.Fatal("the libraryItemId form did not hide the book")
	}
}

// TestRemoveFromContinueListening_AllToleratedShapesWork covers the aliases. Each is
// a shape some ABS client — or a future AudioBooth version — could plausibly send,
// and a 404 on any of them is user-visible breakage for zero benefit.
func TestRemoveFromContinueListening_AllToleratedShapesWork(t *testing.T) {
	for name, tc := range map[string]struct{ method, prefix string }{
		"GET  /api/me/progress": {http.MethodGet, "/api/me/progress/"},
		"POST /api/me/progress": {http.MethodPost, "/api/me/progress/"},
		"POST /api/me/item":     {http.MethodPost, "/api/me/item/"},
		"GET  /api/me/item":     {http.MethodGet, "/api/me/item/"},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWriteHarness(t)
			w.patch(t, map[string]any{"currentTime": 600.0})

			code, _, raw := w.req(t, tc.method,
				tc.prefix+w.syncID+"/remove-from-continue-listening", map[string]any{})
			if code != http.StatusOK {
				t.Fatalf("%s = %d %s", name, code, raw)
			}
			if w.getRow(t)["hideFromContinueListening"] != true {
				t.Fatalf("%s returned 200 but did not hide the book", name)
			}
		})
	}
}

// TestRemoveFromContinueListening_RequiresAuth — the route must sit behind the
// fail-closed identity middleware like every other /api/me path.
func TestRemoveFromContinueListening_RequiresAuth(t *testing.T) {
	w := newWriteHarness(t)
	rec, _ := w.do(t, request{
		method: http.MethodGet,
		path:   "/api/me/progress/" + w.syncID + "/remove-from-continue-listening",
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", rec.Code)
	}
}

// TestRemoveFromContinueListening_DoesNotShadowTheProgressRead proves the two GETs
// coexist: gin must route /api/me/progress/:id and
// /api/me/progress/:id/remove-from-continue-listening independently. A mis-registered
// tree would make one swallow the other, and the failure would look like a data bug.
func TestRemoveFromContinueListening_DoesNotShadowTheProgressRead(t *testing.T) {
	w := newWriteHarness(t)
	w.patch(t, map[string]any{"currentTime": 600.0})

	row := w.getRow(t) // GET /api/me/progress/:id — must still return the row
	if got := num(t, row, "currentTime"); got != 600 {
		t.Fatalf("GET /api/me/progress/:id returned currentTime %v, want 600", got)
	}
	if row["libraryItemId"] != w.syncID {
		t.Fatalf("progress read returned the wrong item: %#v", row["libraryItemId"])
	}
}
