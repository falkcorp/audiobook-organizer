// file: internal/server/handlers/abs/review_3478_failclosed_test.go
// version: 1.0.0
// guid: 6a1e8c3d-4f27-4b95-b0d6-c9e2f7a3d148
// last-edited: 2026-09-19

package abs_test

import (
	"errors"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble/v2"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// plantCorruptPosition backs w's positions with a REAL PebbleStore holding a
// valid row at 3600 plus one undecodable upos: row for the same book, so the
// store's own decode path is exercised end to end.
func plantCorruptPosition(t *testing.T, w *writeHarness) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "positions")
	ps, err := database.NewPebbleStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := ps.SetUserPosition(w.userID, w.bookID, "abs", 3600); err != nil {
		t.Fatal(err)
	}
	ps.Close()
	db, err := pebble.Open(dir, &pebble.Options{FormatMajorVersion: pebble.FormatNewest})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Set([]byte("upos:"+w.userID+":"+w.bookID+":zz"), []byte("{not json"), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	db.Close()
	ps, err = database.NewPebbleStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ps.Close() })
	w.seed.lib.mu.Lock()
	w.seed.lib.realPositions = ps
	w.seed.lib.mu.Unlock()
}

// H1 end to end: a corrupt position row used to read as "no position", so a
// PATCH merged against 0 and play answered currentTime 0.
func TestPatch_CorruptPositionRowIs503(t *testing.T) {
	w := newWriteHarness(t)
	plantCorruptPosition(t, w)
	if code, _, raw := w.req(t, http.MethodPatch, "/api/me/progress/"+w.syncID, map[string]any{"currentTime": 100.0}); code != http.StatusServiceUnavailable {
		t.Fatalf("PATCH over a corrupt position row = %d %s, want 503", code, raw)
	}
}

func TestPlay_CorruptPositionRowIs503(t *testing.T) {
	w := newWriteHarness(t)
	plantCorruptPosition(t, w)
	if code, _, raw := w.req(t, http.MethodPost, "/api/items/"+w.syncID+"/play", map[string]any{}); code != http.StatusServiceUnavailable {
		t.Fatalf("play over a corrupt position row = %d %.200s, want 503", code, raw)
	}
}

// M1: the tombstone is written BEFORE positions are cleared, so a reset whose
// state write fails leaves the position in place and the retry records it.
func TestProgressReset_FailedTombstoneWriteIsRetryable(t *testing.T) {
	w := newWriteHarness(t)
	w.patch(t, map[string]any{"currentTime": 3600.0})
	w.seed.lib.mu.Lock()
	w.seed.lib.setStateErrOnce = errors.New("transient write failure")
	w.seed.lib.mu.Unlock()
	if code, _, _ := w.req(t, http.MethodDelete, "/api/me/progress/"+w.rowID(), nil); code == http.StatusOK {
		t.Fatal("reset whose tombstone write failed answered 200")
	}
	if got := storedPosition(t, w); got != 3600 {
		t.Fatalf("failed reset cleared the position (now %v): the retry cannot record it", got)
	}
	if code, _, raw := w.req(t, http.MethodDelete, "/api/me/progress/"+w.rowID(), nil); code != http.StatusOK {
		t.Fatalf("retry = %d %s", code, raw)
	}
	st, _ := w.seed.lib.GetUserBookState(w.userID, w.bookID)
	if st == nil || st.ProgressResetAt == nil || len(st.ProgressResetPositions) != 1 || st.ProgressResetPositions[0] != 3600 {
		t.Fatalf("retry did not write the tombstone with the discarded position: %+v", st)
	}
	if got := storedPosition(t, w); got != 0 {
		t.Fatalf("retry left the position at %v", got)
	}
}

// M2: a sync answered 503 must not have changed the in-memory session, or the
// client's retry counts timeListened twice.
func TestSessionSync_RetryAfter503DoesNotDoubleCountListening(t *testing.T) {
	w := newWriteHarness(t)
	sess := startSession(t, w.harness, w.syncID, w.token)
	id, _ := sess["id"].(string)
	body := map[string]any{"currentTime": 100.0, "timeListened": 10.0}
	failPositionReads(w, true)
	code, _, _ := w.req(t, http.MethodPost, "/api/session/"+id+"/sync", body)
	failPositionReads(w, false)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("sync with unreadable position = %d, want 503", code)
	}
	if code, _, raw := w.req(t, http.MethodPost, "/api/session/"+id+"/sync", body); code != http.StatusOK {
		t.Fatalf("retry = %d %s", code, raw)
	}
	st, _ := w.seed.lib.GetUserBookState(w.userID, w.bookID)
	if st == nil || st.TotalListenedSeconds != 10 {
		t.Fatalf("listened seconds after a 503 + retry = %+v, want 10", st)
	}
}

// M3: session sync treats an unreadable state like PATCH does: 503, and the
// position is not written first.
func TestSessionSync_UnreadableStateIs503AndWritesNothing(t *testing.T) {
	w := newWriteHarness(t)
	w.patch(t, map[string]any{"currentTime": 50.0})
	sess := startSession(t, w.harness, w.syncID, w.token)
	id, _ := sess["id"].(string)
	failStateReads(w, true)
	code, _, raw := w.req(t, http.MethodPost, "/api/session/"+id+"/sync", map[string]any{"currentTime": 100.0, "timeListened": 10.0})
	failStateReads(w, false)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("sync with unreadable state = %d %s, want 503", code, raw)
	}
	if got := storedPosition(t, w); got != 50 {
		t.Fatalf("sync wrote the position (%v) without a readable state", got)
	}
}

func failBookFiles(w *writeHarness, fail bool) {
	w.seed.lib.mu.Lock()
	defer w.seed.lib.mu.Unlock()
	if w.seed.lib.bookFilesErr == nil {
		w.seed.lib.bookFilesErr = map[string]error{}
	}
	if fail {
		w.seed.lib.bookFilesErr[w.bookID] = errors.New("transient read failure")
	} else {
		delete(w.seed.lib.bookFilesErr, w.bookID)
	}
}

// The authoritative duration decides "finished" and the progress percent; a
// failed file read used to fall back to the client's duration silently.
func TestPatch_UnreadableBookFilesIs503(t *testing.T) {
	w := newWriteHarness(t)
	w.patch(t, map[string]any{"currentTime": 50.0})
	failBookFiles(w, true)
	code, _, raw := w.req(t, http.MethodPatch, "/api/me/progress/"+w.syncID, map[string]any{"currentTime": 9970.0, "duration": 9975.0})
	failBookFiles(w, false)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("PATCH with unreadable book files = %d %s, want 503", code, raw)
	}
	if got := storedPosition(t, w); got != 50 {
		t.Fatalf("PATCH wrote %v without a readable duration", got)
	}
}

func TestSessionLocal_UnreadableBookFilesIs503(t *testing.T) {
	w := newWriteHarness(t)
	failBookFiles(w, true)
	code, _, raw := w.req(t, http.MethodPost, "/api/session/local", map[string]any{"id": "s", "userId": w.userID,
		"libraryItemId": w.syncID, "currentTime": 500.0, "duration": 9975.0})
	failBookFiles(w, false)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("session/local with unreadable book files = %d %s, want 503", code, raw)
	}
	if got := storedPosition(t, w); got != 0 {
		t.Fatalf("session/local wrote %v without a readable duration", got)
	}
}

// Display paths stay fail-OPEN (they write nothing), but one corrupt segment
// row must not hide the readable abs row: the item still shows its progress
// and the book stays on Continue Listening.
func TestItemProgress_CorruptSegmentRowFallsBackToReadableRows(t *testing.T) {
	w := newWriteHarness(t)
	plantCorruptPosition(t, w)
	code, body, raw := w.req(t, http.MethodGet, "/api/items/"+w.syncID, nil)
	if code != http.StatusOK {
		t.Fatalf("GET item = %d %.200s", code, raw)
	}
	mp, _ := body["userMediaProgress"].(map[string]any)
	if mp == nil || mp["currentTime"] != 3600.0 {
		t.Fatalf("userMediaProgress = %v, want currentTime 3600 from the readable row", mp)
	}
}

func TestContinueListening_CorruptSegmentRowKeepsTheBook(t *testing.T) {
	w := newWriteHarness(t)
	plantCorruptPosition(t, w)
	if ids := continueListeningIDs(t, w); !contains(ids, w.syncID) {
		t.Fatalf("continue listening = %v; a corrupt segment row dropped the book", ids)
	}
}
