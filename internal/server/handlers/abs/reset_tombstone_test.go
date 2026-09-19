// file: internal/server/handlers/abs/reset_tombstone_test.go
// version: 1.0.0
// guid: 5b0e7c2a-93d1-4f6e-8a27-c4d19e3f6b58
// last-edited: 2026-09-19

package abs_test

import (
	"fmt"
	"math"
	"net/http"
	"slices"
	"testing"
	"time"
)

// resetAt resets w's progress through the real DELETE route, then backdates the
// tombstone by ago so a test can model a replay arriving that long after it.
func resetAt(t *testing.T, w *writeHarness, ago time.Duration) {
	t.Helper()
	if code, _, raw := w.req(t, http.MethodDelete, "/api/me/progress/"+w.rowID(), nil); code != http.StatusOK {
		t.Fatalf("reset = %d %s", code, raw)
	}
	w.seed.lib.mu.Lock()
	defer w.seed.lib.mu.Unlock()
	at := time.Now().Add(-ago)
	w.seed.lib.states[w.userID+"|"+w.bookID].ProgressResetAt = &at
}

// replay sends one offline session re-stamped updatedAt=now, the way a client
// replaying its backlog does (spec §1.8.7).
func replay(t *testing.T, w *writeHarness, id string, ct float64) {
	t.Helper()
	localAll(t, w, map[string]any{"id": id, "userId": w.userID, "libraryItemId": w.syncID,
		"currentTime": ct, "startedAt": time.Now().Add(-3 * time.Hour).UnixMilli(),
		"updatedAt": time.Now().UnixMilli()})
}

// The reset tombstone records the position the user threw away.
func TestProgressReset_TombstoneRecordsTheDiscardedPosition(t *testing.T) {
	w := newWriteHarness(t)
	w.patch(t, map[string]any{"currentTime": 3600.0})
	resetAt(t, w, 0)
	w.patch(t, map[string]any{"currentTime": 1000.0})
	resetAt(t, w, 0)
	st, _ := w.seed.lib.GetUserBookState(w.userID, w.bookID)
	if st == nil || !slices.Equal(st.ProgressResetPositions, []float64{3600, 1000}) {
		t.Fatalf("tombstone positions = %v, want [3600 1000]", st)
	}
}

// Review of #3470 at 9e2e39285: the 4x physical bound only narrowed the hole.
// A pre-reset backlog replayed hours later (the normal offline case) is
// "reachable" by elapsed×4 and brought the discarded position back.
func TestSessionLocalAll_ReStampedBacklogReplayedHoursLaterIsDiscarded(t *testing.T) {
	for _, ct := range []float64{3600, 3620, 3580} {
		w := newWriteHarness(t)
		w.patch(t, map[string]any{"currentTime": 3600.0})
		resetAt(t, w, 2*time.Hour)
		replay(t, w, "backlog", ct)
		if got := storedPosition(t, w); got != 0 {
			t.Fatalf("a backlog at %v replayed 2h after the reset undid it: position %v", ct, got)
		}
	}
}

// Every reset is remembered, not just the latest: a backlog from before the
// FIRST reset stays discarded after a second one.
func TestSessionLocalAll_EarlierResetPositionStaysDiscarded(t *testing.T) {
	w := newWriteHarness(t)
	w.patch(t, map[string]any{"currentTime": 3600.0})
	resetAt(t, w, 3*time.Hour)
	w.patch(t, map[string]any{"currentTime": 1000.0})
	resetAt(t, w, 2*time.Hour)
	replay(t, w, "first-backlog", 3600)
	if got := storedPosition(t, w); got != 0 {
		t.Fatalf("a backlog from before the first reset came back: position %v", got)
	}
}

// Real post-reset listening that SEEKS far ahead is accepted at once — the 4x
// bound refused it until elapsed×4 caught up.
func TestSessionLocalAll_ResetThenSeekAheadIsAccepted(t *testing.T) {
	w := newWriteHarness(t)
	w.patch(t, map[string]any{"currentTime": 1200.0})
	resetAt(t, w, 5*time.Minute) // past the 2 min skew tolerance; 4x would allow only 1320 s
	for _, ct := range []float64{9000, 9020, 9040} {
		replay(t, w, "seek", ct)
		if got := storedPosition(t, w); got != ct {
			t.Fatalf("post-reset seek+listen refused: position %v, want %v", got, ct)
		}
	}
}

// Review of the follow-up: a backlog is a queue of RISING positions applied in
// turn. It must not walk itself onto the discarded position, even within one
// request, however close the entries before it got.
func TestSessionLocalAll_BacklogQueueCannotWalkOntoTheOldPosition(t *testing.T) {
	w := newWriteHarness(t)
	w.patch(t, map[string]any{"currentTime": 3600.0})
	resetAt(t, w, 2*time.Hour)
	var queue []map[string]any
	for i, ct := range []float64{3000, 3200, 3400, 3600} {
		queue = append(queue, map[string]any{"id": fmt.Sprintf("q%d", i), "userId": w.userID,
			"libraryItemId": w.syncID, "currentTime": ct,
			"startedAt": time.Now().Add(-3 * time.Hour).UnixMilli(), "updatedAt": time.Now().UnixMilli()})
	}
	localAll(t, w, queue...)
	if got := storedPosition(t, w); math.Abs(got-3600) <= 36 {
		t.Fatalf("a replayed backlog queue walked back onto the discarded position: %v", got)
	}
}

// Genuine listening that passes THROUGH the old position pays the documented
// cost: the sync landing inside the window is dropped, and the next sync beyond
// it lands normally.
func TestSessionLocalAll_ListeningThroughTheOldPositionResumesPastTheWindow(t *testing.T) {
	w := newWriteHarness(t)
	w.patch(t, map[string]any{"currentTime": 3600.0})
	resetAt(t, w, 2*time.Hour)
	replay(t, w, "listen", 3540)
	replay(t, w, "listen", 3610) // inside ±36 s of 3600: dropped
	if got := storedPosition(t, w); got != 3540 {
		t.Fatalf("sync inside the window: position %v, want 3540 unchanged", got)
	}
	replay(t, w, "listen", 3680)
	if got := storedPosition(t, w); got != 3680 {
		t.Fatalf("sync past the window refused: position %v, want 3680", got)
	}
}

// A tombstone written before positions were recorded keeps the 4x physical
// bound as its only position check.
func TestSessionLocalAll_LegacyTombstoneKeepsThePhysicalBound(t *testing.T) {
	w := newWriteHarness(t)
	w.patch(t, map[string]any{"currentTime": 3600.0})
	resetAt(t, w, 10*time.Minute)
	w.seed.lib.mu.Lock()
	w.seed.lib.states[w.userID+"|"+w.bookID].ProgressResetPositions = nil
	w.seed.lib.mu.Unlock()
	replay(t, w, "legacy", 3600)
	if got := storedPosition(t, w); got != 0 {
		t.Fatalf("legacy tombstone: re-stamped backlog undid the reset: position %v", got)
	}
}
