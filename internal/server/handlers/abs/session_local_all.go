// file: internal/server/handlers/abs/session_local_all.go
// version: 1.0.0
// guid: fcff98d1-5709-4c26-a345-d79231c02527
// last-edited: 2026-09-19

package abs

import (
	"net/http"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/falkcorp/audiobook-organizer/internal/syncapi/progress"
	"github.com/gin-gonic/gin"
)

// ── POST /api/session/local-all ─────────────────────────────────────────────
//
// 🔴 THIS 404'd ON PRODUCTION until 2026-09-19 (item-6 decode matrix), so every
// offline listening session the app queued was dropped on the floor: listen on
// a plane, reconnect, and the server never learned where you got to.
//
// Body: {"sessions":[PlaybackSession…]}. Answer (upstream ABS shape):
// {"results":[{"id","success","progressSynced","error"?}]}. AudioBooth types the
// response as Data, so the body only has to be a non-empty 2xx.
//
// CONFLICT POLICY — deliberately STRICTER than upstream ABS. Upstream applies a
// local session when session.updatedAt > the stored progress's lastUpdate. We use
// progress.MergeOfflineReplay, which ignores timestamps and is forward-only on
// position, because offline clients RE-STAMP backlog entries with updatedAt=now
// before replaying them (spec §1.8.7; abs-shim src/index.ts:534-541). A timestamp
// comparison would therefore let a stale backlog entry that is far BEHIND the
// server rewind the listener. Forward-only means a replayed session can advance
// the position and can never move it back.
//
// IDEMPOTENT ON SESSION ID. The forward-only merge makes a replay a no-op: the
// second time the same session arrives its currentTime is not greater than what
// the first application stored. Duplicate ids inside ONE request are collapsed
// to a single result row. No session-id ledger is persisted — it would add a
// keyspace to guarantee something the merge already guarantees.
//
// ALWAYS 200. A 4xx on an offline-replay endpoint wedges the client's replay
// queue permanently (§1.7.3 item 2), so a session we cannot place is reported in
// its result row, never as an HTTP error.

// localSessionReq is the subset of an ABS PlaybackSession this endpoint reads.
type localSessionReq struct {
	ID            string   `json:"id"`
	UserID        string   `json:"userId"`
	LibraryItemID string   `json:"libraryItemId"`
	EpisodeID     *string  `json:"episodeId"`
	CurrentTime   *float64 `json:"currentTime"`
	Duration      *float64 `json:"duration"`
}

type localAllReq struct {
	Sessions []localSessionReq `json:"sessions"`
}

// localSessionResult is one row of the upstream ABS response.
type localSessionResult struct {
	ID             string `json:"id"`
	Success        bool   `json:"success"`
	ProgressSynced bool   `json:"progressSynced"`
	Error          string `json:"error,omitempty"`
}

// SessionLocalAll handles POST /api/session/local-all.
func (h *Handler) SessionLocalAll(c *gin.Context) {
	results := []localSessionResult{}
	user, ok := servermiddleware.CurrentUser(c)
	if !ok || user == nil {
		// ABSRequireAuth already refused an unauthenticated caller; this is the
		// nil guard, answered in the endpoint's own always-200 shape.
		respondJSON(c, http.StatusOK, gin.H{"results": results})
		return
	}
	var req localAllReq
	if err := c.ShouldBindJSON(&req); err != nil {
		respondJSON(c, http.StatusOK, gin.H{"results": results})
		return
	}

	seen := make(map[string]int, len(req.Sessions))
	for _, s := range req.Sessions {
		res := h.applyLocalSession(user.ID, s)
		if i, dup := seen[res.ID]; dup && res.ID != "" {
			// Same session twice in one upload: one result row. Either
			// application is safe (forward-only), so report success if any did.
			prev := &results[i]
			prev.Success = prev.Success || res.Success
			prev.ProgressSynced = prev.ProgressSynced || res.ProgressSynced
			if prev.Success {
				prev.Error = ""
			}
			continue
		}
		seen[res.ID] = len(results)
		results = append(results, res)
	}
	respondJSON(c, http.StatusOK, gin.H{"results": results})
}

// applyLocalSession writes one offline session's position through the
// offline-replay merge and reports what happened.
func (h *Handler) applyLocalSession(userID string, s localSessionReq) localSessionResult {
	res := localSessionResult{ID: strings.TrimSpace(s.ID)}
	switch {
	case res.ID == "":
		res.Error = "session id is required"
		return res
	case s.UserID != "" && s.UserID != userID:
		// Upstream ABS rejects a session belonging to another user the same way.
		res.Error = "session belongs to a different user"
		return res
	case s.EpisodeID != nil && strings.TrimSpace(*s.EpisodeID) != "":
		res.Error = "podcast episodes are not supported"
		return res
	}
	bookID := h.bookIDForSyncID(strings.TrimSpace(s.LibraryItemID))
	if bookID == "" {
		res.Error = "library item not found"
		return res
	}
	res.Success = true
	if h.progress == nil || s.CurrentTime == nil || *s.CurrentTime <= 0 {
		// Nothing positional to apply (or nowhere to apply it). The session is
		// accepted; there is simply no progress change.
		return res
	}

	stored := progress.Progress{}
	if pos, err := h.progress.GetUserPosition(userID, bookID); err == nil && pos != nil {
		stored.CurrentTime = pos.PositionSeconds
		stored.UpdatedAtMs = msEpoch(pos.UpdatedAt)
	} else if err != nil {
		// Unknown stored position: writing blind could rewind the listener,
		// which is the one outcome this endpoint must never produce.
		res.Success = false
		res.Error = "could not read stored progress"
		return res
	}
	if state, err := h.progress.GetUserBookState(userID, bookID); err == nil && state != nil {
		stored.IsFinished = state.Status == database.UserBookStatusFinished
	}
	duration := h.durationForBook(bookID, s.Duration)
	stored.Duration = duration

	now := h.now()
	merged, accepted := progress.MergeOfflineReplay(stored, progress.Progress{
		CurrentTime: *s.CurrentTime,
		Duration:    duration,
		UpdatedAtMs: now.UnixMilli(),
	})
	if !accepted {
		// The server is already at or past this session. Success, no change.
		return res
	}
	if err := h.progress.SetUserPosition(userID, bookID, absProgressSegmentID, merged.CurrentTime); err != nil {
		res.Success = false
		res.Error = "could not store progress"
		return res
	}
	status := database.UserBookStatusInProgress
	if merged.IsFinished {
		status = database.UserBookStatusFinished
	}
	pct := 0
	if merged.Duration > 0 {
		pct = min(int(merged.CurrentTime/merged.Duration*100), 100)
	}
	// Read-modify-write through the shared helper so user intent
	// (HideFromContinueListening, StatusManual) survives, exactly as on /sync.
	if err := h.updateUserBookState(userID, bookID, func(state *database.UserBookState) {
		state.Status = status
		state.ProgressPct = pct
		state.LastSegmentID = absProgressSegmentID
		state.LastActivityAt = now
	}); err != nil {
		res.Error = "position stored; book state not updated"
	}
	res.ProgressSynced = true
	return res
}
