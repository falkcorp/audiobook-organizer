// file: internal/server/handlers/abs/position_read_failclosed_test.go
// version: 1.0.0
// guid: 9d4b2f6a-1e37-4c85-a0b9-e6f3c2d8a514
// last-edited: 2026-09-19

package abs_test

import (
	"errors"
	"net/http"
	"testing"
)

// An unreadable position is NOT "no position yet". Merging against an empty
// stored position let a stale write rewind the listener. Every ABS path that
// reads the position before writing must write nothing and answer 503.

func failPositionReads(w *writeHarness, fail bool) {
	w.seed.lib.mu.Lock()
	defer w.seed.lib.mu.Unlock()
	if fail {
		w.seed.lib.posErr = errors.New("transient read failure")
	} else {
		w.seed.lib.posErr = nil
	}
}

func TestPatchPosition_UnreadablePositionIs503AndDoesNotRewind(t *testing.T) {
	w := newWriteHarness(t)
	w.patch(t, map[string]any{"currentTime": 3600.0})
	failPositionReads(w, true)
	code, _, raw := w.req(t, http.MethodPatch, "/api/me/progress/"+w.syncID, map[string]any{"currentTime": 100.0})
	failPositionReads(w, false)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("PATCH with unreadable position = %d %s, want 503", code, raw)
	}
	if got := storedPosition(t, w); got != 3600 {
		t.Fatalf("a PATCH merged against an unreadable position rewound the listener to %v", got)
	}
}

func TestBatchPatch_UnreadablePositionIs503AndDoesNotRewind(t *testing.T) {
	w := newWriteHarness(t)
	w.patch(t, map[string]any{"currentTime": 3600.0})
	failPositionReads(w, true)
	code, _, raw := w.req(t, http.MethodPatch, "/api/me/progress/batch/update",
		[]map[string]any{{"libraryItemId": w.syncID, "currentTime": 100.0}})
	failPositionReads(w, false)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("batch PATCH with unreadable position = %d %s, want 503", code, raw)
	}
	if got := storedPosition(t, w); got != 3600 {
		t.Fatalf("a batch PATCH rewound the listener to %v", got)
	}
}

func TestSessionSync_UnreadablePositionIs503AndDoesNotRewind(t *testing.T) {
	w := newWriteHarness(t)
	w.patch(t, map[string]any{"currentTime": 3600.0})
	sess := startSession(t, w.harness, w.syncID, w.token)
	id, _ := sess["id"].(string)
	failPositionReads(w, true)
	code, _, raw := w.req(t, http.MethodPost, "/api/session/"+id+"/sync",
		map[string]any{"currentTime": 100.0, "timeListened": 10.0})
	failPositionReads(w, false)
	if code != http.StatusServiceUnavailable || raw == "" {
		t.Fatalf("sync with unreadable position = %d %q, want a non-empty 503", code, raw)
	}
	if got := storedPosition(t, w); got != 3600 {
		t.Fatalf("a sync merged against an unreadable position rewound the listener to %v", got)
	}
}

// Opening a session reports the user's position as currentTime; a 0 from an
// unreadable read rewinds the client to the start (§1.8.7).
func TestPlay_UnreadablePositionIs503(t *testing.T) {
	w := newWriteHarness(t)
	w.patch(t, map[string]any{"currentTime": 3600.0})
	failPositionReads(w, true)
	code, _, raw := w.req(t, http.MethodPost, "/api/items/"+w.syncID+"/play", map[string]any{})
	failPositionReads(w, false)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("play with unreadable position = %d %s, want 503", code, raw)
	}
}

func TestProgressReset_UnreadablePositionIs503(t *testing.T) {
	w := newWriteHarness(t)
	w.patch(t, map[string]any{"currentTime": 3600.0})
	failPositionReads(w, true)
	code, _, raw := w.req(t, http.MethodDelete, "/api/me/progress/"+w.rowID(), nil)
	failPositionReads(w, false)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("reset with unreadable position = %d %s, want 503", code, raw)
	}
	if got := storedPosition(t, w); got != 3600 {
		t.Fatalf("reset changed the position: %v", got)
	}
}
