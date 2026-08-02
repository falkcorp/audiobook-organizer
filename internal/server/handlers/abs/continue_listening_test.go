// file: internal/server/handlers/abs/continue_listening_test.go
// version: 1.0.0
// guid: 2f8c14b7-6039-4e5a-91cd-0847be3d2065
// last-edited: 2026-08-02

package abs_test

import (
	"net/http"
	"testing"
)

// "Remove from now playing" was reported as still not working after the Phase 6
// write half shipped, even though production shows the client's DELETE returning
// 200:
//
//	200 DELETE /api/me/progress/01KYXQ2KNB2X70CGX9N4DV1XJY-44669fab-…
//
// These tests establish exactly how far the SERVER's responsibility gets, so the
// remaining question is narrowed to the client rather than re-argued. Three surfaces
// feed a client's Continue Listening, and all three must forget the book:
//
//	/api/me            — the authoritative mediaProgress list (§1.8.1)
//	/api/me/progress   — the same list under a bare wrapper
//	/personalized      — the server-built "Continue Listening" shelf
//
// If all three drop it and the book still shows on the device, what remains is
// AudioBooth's own local row, which syncFromAPI deliberately SPARES for the
// currently-playing book (§1.8.1) — a client-side exemption no server response can
// override.

// continueListeningIDs returns the libraryItemIds on the Continue Listening shelf.
func continueListeningIDs(t *testing.T, w *writeHarness) []string {
	t.Helper()
	code, body := w.doAny(t, request{
		method:  http.MethodGet,
		path:    "/api/libraries/" + w.libraryID() + "/personalized",
		headers: bearer(w.token),
	})
	if code != http.StatusOK {
		t.Fatalf("personalized = %d", code)
	}
	shelves, ok := body.([]any)
	if !ok {
		t.Fatalf("personalized must be a BARE ARRAY (§1.8.6), got %T", body)
	}
	var out []string
	for _, raw := range shelves {
		shelf, _ := raw.(map[string]any)
		if shelf == nil || shelf["id"] != "continue-listening" {
			continue
		}
		entities, _ := shelf["entities"].([]any)
		for _, e := range entities {
			if item, _ := e.(map[string]any); item != nil {
				if id, _ := item["id"].(string); id != "" {
					out = append(out, id)
				}
			}
		}
	}
	return out
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// progressListIDs returns the libraryItemIds in GET /api/me/progress.
func progressListIDs(t *testing.T, w *writeHarness) []string {
	t.Helper()
	code, body, raw := w.req(t, http.MethodGet, "/api/me/progress", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /api/me/progress = %d %s", code, raw)
	}
	rows, _ := body["mediaProgress"].([]any)
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if row, _ := r.(map[string]any); row != nil {
			if id, _ := row["libraryItemId"].(string); id != "" {
				out = append(out, id)
			}
		}
	}
	return out
}

// 🔴 TestDeleteProgress_RemovesTheBookFromEverySurface is the server half of
// "remove from now playing". If this passes, the server has done everything it can.
func TestDeleteProgress_RemovesTheBookFromEverySurface(t *testing.T) {
	w := newWriteHarness(t)
	w.patch(t, map[string]any{"currentTime": 600.0})

	// Precondition: the book is on every surface.
	if !contains(progressListIDs(t, w), w.syncID) {
		t.Fatal("precondition failed: the book is not in /api/me/progress after a PATCH")
	}
	if !contains(continueListeningIDs(t, w), w.syncID) {
		t.Fatal("precondition failed: the book is not on the Continue Listening shelf after a PATCH")
	}

	// The exact call the client makes: the "<userID>-<syncID>" row-id form.
	code, _, raw := w.req(t, http.MethodDelete, "/api/me/progress/"+w.rowID(), nil)
	if code != http.StatusOK {
		t.Fatalf("DELETE = %d %s", code, raw)
	}

	if ids := progressListIDs(t, w); contains(ids, w.syncID) {
		t.Fatalf("/api/me/progress still lists the book after DELETE: %v", ids)
	}
	if ids := continueListeningIDs(t, w); contains(ids, w.syncID) {
		t.Fatalf("Continue Listening still lists the book after DELETE: %v", ids)
	}
	// /api/me carries the same list and is what syncFromAPI reads.
	code, me, raw := w.req(t, http.MethodGet, "/api/me", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /api/me = %d %s", code, raw)
	}
	rows, _ := me["mediaProgress"].([]any)
	for _, r := range rows {
		if row, _ := r.(map[string]any); row != nil && row["libraryItemId"] == w.syncID {
			t.Fatal("/api/me still lists the book after DELETE — the client will never drop it")
		}
	}
}

// TestHideFromContinueListening_DropsTheBookFromTheShelf covers the OTHER mechanism.
// A hidden book keeps its position — the user asked to tidy the shelf, not to lose
// their place — so it stays in mediaProgress but must leave Continue Listening.
//
// This is the one that would still be broken if the shelf ignored the flag.
func TestHideFromContinueListening_DropsTheBookFromTheShelf(t *testing.T) {
	w := newWriteHarness(t)
	w.patch(t, map[string]any{"currentTime": 600.0})
	if !contains(continueListeningIDs(t, w), w.syncID) {
		t.Fatal("precondition failed: the book is not on the shelf")
	}

	code, _, raw := w.req(t, http.MethodPost,
		"/api/me/item/"+w.syncID+"/remove-from-continue-listening", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("remove-from-continue-listening = %d %s", code, raw)
	}

	if ids := continueListeningIDs(t, w); contains(ids, w.syncID) {
		t.Fatalf("hidden book is STILL on the Continue Listening shelf: %v", ids)
	}
	// ...but the position survives: hiding is not forgetting.
	if !contains(progressListIDs(t, w), w.syncID) {
		t.Fatal("hiding the book also discarded its progress — the user's place must survive")
	}
}
