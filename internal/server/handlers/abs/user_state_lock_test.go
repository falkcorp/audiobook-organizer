// file: internal/server/handlers/abs/user_state_lock_test.go
// version: 1.0.0
// guid: e8c7567d-47ae-49e1-a1f5-b1089ec27e34
// last-edited: 2026-10-05

package abs_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// waitsForUserStateLock sends one request while the test holds the
// per-(user, book) user-state stripe and fails unless the request waits for
// it: every ABS write path takes database.LockUserBookState across its
// read-merge-write, so the Repairs writer and the revert cannot interleave.
func waitsForUserStateLock(t *testing.T, w *writeHarness, method, path string, body any) int {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	unlock := database.LockUserBookState(w.userID, w.bookID)
	done := make(chan int, 1)
	go func() {
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+w.token)
		rec := httptest.NewRecorder()
		w.router.ServeHTTP(rec, r)
		done <- rec.Code
	}()
	select {
	case code := <-done:
		unlock()
		t.Fatalf("%s %s finished (%d) while the user-state lock was held", method, path, code)
	case <-time.After(150 * time.Millisecond):
	}
	unlock()
	select {
	case code := <-done:
		return code
	case <-time.After(10 * time.Second):
		t.Fatalf("%s %s never finished after the lock was released", method, path)
	}
	return 0
}

func TestUserStateLock_ProgressPatch(t *testing.T) {
	w := newWriteHarness(t)
	if code := waitsForUserStateLock(t, w, http.MethodPatch, "/api/me/progress/"+w.syncID, map[string]any{"currentTime": 120.0}); code != http.StatusOK {
		t.Fatalf("PATCH = %d", code)
	}
}

func TestUserStateLock_ProgressReset(t *testing.T) {
	w := newWriteHarness(t)
	if err := w.seed.lib.SetUserPosition(w.userID, w.bookID, "abs", 120); err != nil {
		t.Fatal(err)
	}
	if code := waitsForUserStateLock(t, w, http.MethodDelete, "/api/me/progress/"+w.syncID, nil); code != http.StatusOK {
		t.Fatalf("DELETE = %d", code)
	}
}

func TestUserStateLock_LocalSessionReplay(t *testing.T) {
	w := newWriteHarness(t)
	body := map[string]any{"sessions": []map[string]any{{
		"id": "offline-lock", "userId": w.userID, "libraryItemId": w.syncID, "currentTime": 321.5, "timeListening": 300,
	}}}
	if code := waitsForUserStateLock(t, w, http.MethodPost, "/api/session/local-all", body); code != http.StatusOK {
		t.Fatalf("local-all = %d", code)
	}
}

// The session sync takes the stripe before it reads the stored position, so
// the merge compares against the position as it is when the write lands.
func TestUserStateLock_SessionSync(t *testing.T) {
	w := newWriteHarness(t)
	code, play, raw := w.req(t, http.MethodPost, "/api/items/"+w.syncID+"/play", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("play = %d %s", code, raw)
	}
	sid := str(t, play, "id")
	if code := waitsForUserStateLock(t, w, http.MethodPost, "/api/session/"+sid+"/sync",
		map[string]any{"currentTime": 200.0, "timeListened": 1.0}); code != http.StatusOK {
		t.Fatalf("sync = %d", code)
	}
}

func TestUserStateLock_RemoveFromContinueListening(t *testing.T) {
	w := newWriteHarness(t)
	if code := waitsForUserStateLock(t, w, http.MethodGet, "/api/me/progress/"+w.syncID+"/remove-from-continue-listening", nil); code != http.StatusOK {
		t.Fatalf("remove-from-continue-listening = %d", code)
	}
}
