// file: internal/server/handlers/abs/state_read_failclosed_test.go
// version: 1.0.0
// guid: 7c3f1a9e-2d84-4b6a-9e05-b18c6d2f4a73
// last-edited: 2026-09-19

package abs_test

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

// An unreadable book-state row is NOT "no row yet". Treating it as one wrote a
// fresh row over it and wiped the reset tombstone (and hide/manual status).
// Every ABS write path must write nothing and answer 503.

func failStateReads(w *writeHarness, fail bool) {
	w.seed.lib.mu.Lock()
	defer w.seed.lib.mu.Unlock()
	if fail {
		w.seed.lib.stateErr = errors.New("transient read failure")
	} else {
		w.seed.lib.stateErr = nil
	}
}

func tombstoneOf(t *testing.T, w *writeHarness) *time.Time {
	t.Helper()
	st, err := w.seed.lib.GetUserBookState(w.userID, w.bookID)
	if err != nil || st == nil {
		t.Fatalf("state = %v, %v", st, err)
	}
	return st.ProgressResetAt
}

func TestHideOnly_UnreadableStateIs503AndKeepsTheTombstone(t *testing.T) {
	w := newWriteHarness(t)
	w.patch(t, map[string]any{"currentTime": 3600.0})
	resetAt(t, w, time.Hour)
	failStateReads(w, true)
	code, _, raw := w.req(t, http.MethodPatch, "/api/me/progress/"+w.syncID, map[string]any{"hideFromContinueListening": true})
	failStateReads(w, false)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("hide with unreadable state = %d %s, want 503", code, raw)
	}
	if tombstoneOf(t, w) == nil {
		t.Fatal("the reset tombstone was wiped by a write over an unreadable row")
	}
}

func TestRemoveFromContinueListening_UnreadableStateIs503AndKeepsTheTombstone(t *testing.T) {
	w := newWriteHarness(t)
	w.patch(t, map[string]any{"currentTime": 3600.0})
	resetAt(t, w, time.Hour)
	failStateReads(w, true)
	code, _, raw := w.req(t, http.MethodPost, "/api/me/item/"+w.syncID+"/remove-from-continue-listening", nil)
	failStateReads(w, false)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("remove-from-continue-listening with unreadable state = %d %s, want 503", code, raw)
	}
	if tombstoneOf(t, w) == nil {
		t.Fatal("the reset tombstone was wiped by a write over an unreadable row")
	}
}

func TestPatchPosition_UnreadableStateIs503AndWritesNothing(t *testing.T) {
	w := newWriteHarness(t)
	w.patch(t, map[string]any{"currentTime": 3600.0})
	resetAt(t, w, time.Hour)
	failStateReads(w, true)
	code, _, raw := w.req(t, http.MethodPatch, "/api/me/progress/"+w.syncID, map[string]any{"currentTime": 500.0})
	failStateReads(w, false)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("PATCH with unreadable state = %d %s, want 503", code, raw)
	}
	if got := storedPosition(t, w); got != 0 {
		t.Fatalf("PATCH wrote a position without reading the state: %v", got)
	}
	if tombstoneOf(t, w) == nil {
		t.Fatal("the reset tombstone was wiped")
	}
}

// A reset whose tombstone cannot be written must not clear the positions:
// positions cleared with no tombstone let any backlog replay undo the reset.
func TestProgressReset_UnreadableStateIs503AndClearsNothing(t *testing.T) {
	w := newWriteHarness(t)
	w.patch(t, map[string]any{"currentTime": 3600.0})
	failStateReads(w, true)
	code, _, raw := w.req(t, http.MethodDelete, "/api/me/progress/"+w.rowID(), nil)
	failStateReads(w, false)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("reset with unreadable state = %d %s, want 503", code, raw)
	}
	if got := storedPosition(t, w); got != 3600 {
		t.Fatalf("reset cleared positions without writing its tombstone: position %v", got)
	}
}

// Offline replay with an unreadable state cannot see the tombstone, so it must
// not apply: 503 (AudioBooth retries 5xx) and nothing written.
func TestSessionLocal_UnreadableStateIs503AndCannotBypassTheTombstone(t *testing.T) {
	w := newWriteHarness(t)
	w.patch(t, map[string]any{"currentTime": 3600.0})
	resetAt(t, w, 2*time.Hour)
	failStateReads(w, true)
	code, _, raw := w.req(t, http.MethodPost, "/api/session/local", map[string]any{"id": "s", "userId": w.userID,
		"libraryItemId": w.syncID, "currentTime": 3600.0,
		"startedAt": time.Now().Add(-3 * time.Hour).UnixMilli(), "updatedAt": time.Now().UnixMilli()})
	failStateReads(w, false)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("session/local with unreadable state = %d %s, want 503", code, raw)
	}
	if got := storedPosition(t, w); got != 0 {
		t.Fatalf("replay bypassed the reset tombstone: position %v", got)
	}
}
