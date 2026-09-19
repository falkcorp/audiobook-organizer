// file: internal/server/handlers/abs/session_local_all.go
// version: 1.2.0
// guid: fcff98d1-5709-4c26-a345-d79231c02527
// last-edited: 2026-09-19

package abs

import (
	"math"
	"net/http"
	"strings"
	"time"

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
// CONFLICT POLICY (applyLocalSession): (a) forward-only, EXCEPT a session whose
// startedAt is after the server's newest known position may move it either way;
// (b) a session that started before the user's last progress reset is refused;
// (c) a position past the item's duration is rejected, never turned into
// "finished". Background on the forward-only default follows.
//
// Deliberately STRICTER than upstream ABS. Upstream applies a
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
	// StartedAt is when the listen began on the device (ms epoch). Unlike
	// updatedAt, clients do not re-stamp it when they replay a backlog, so it is
	// the one timestamp this endpoint can trust.
	StartedAt *int64 `json:"startedAt"`
}

// absLocalSessionOverrunSec is how far past the item's duration a reported
// position may run before it is treated as garbage rather than "at the end".
const absLocalSessionOverrunSec = 5.0

// absClockSkewTolerance bounds how far a device clock may disagree with the
// server before its startedAt stops being trusted in either direction.
const absClockSkewTolerance = 2 * time.Minute

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
	bookID, rerr := h.bookIDForSyncID(strings.TrimSpace(s.LibraryItemID))
	if rerr != nil {
		res.Error = "could not resolve library item; retry"
		return res
	}
	if bookID == "" {
		res.Error = "library item not found"
		return res
	}
	if h.progress == nil || s.CurrentTime == nil || *s.CurrentTime == 0 {
		// Nothing positional to apply (or nowhere to apply it). The session is
		// accepted; there is simply no progress change.
		res.Success = true
		return res
	}
	ct := *s.CurrentTime
	if math.IsNaN(ct) || math.IsInf(ct, 0) || ct < 0 {
		res.Error = "currentTime is not a valid position"
		return res
	}

	pos, err := h.progress.GetUserPosition(userID, bookID)
	if err != nil {
		// Unknown stored position: writing blind could rewind the listener,
		// which is the one outcome this endpoint must never produce.
		res.Error = "could not read stored progress"
		return res
	}
	state, _ := h.progress.GetUserBookState(userID, bookID)

	now := h.now()
	tol := absClockSkewTolerance.Milliseconds()
	// startedAt is trusted only when present and not implausibly in the future
	// (a device clock far ahead). An untrusted start is treated as absent.
	var startedAt *int64
	if s.StartedAt != nil && *s.StartedAt > 0 && *s.StartedAt <= now.UnixMilli()+tol {
		startedAt = s.StartedAt
	}

	// (b) Reset tombstone. Refuses ONLY a session KNOWN to have started before
	// the user's last "reset progress" — by more than the skew tolerance, so a
	// device clock slightly behind cannot turn a real post-reset listen into a
	// refusal. A session with no (trusted) startedAt is NOT refused: it falls
	// through to the forward-only rule below, because refusing it would refuse
	// that device's uploads for this book forever (the tombstone is never
	// cleared: a known pre-reset session must stay refused even after new
	// listening lands, or the discarded position could be replayed forward).
	if state != nil && state.ProgressResetAt != nil && startedAt != nil &&
		*startedAt+tol < state.ProgressResetAt.UnixMilli() {
		res.Error = "session predates a progress reset"
		return res
	}

	duration := h.durationForBook(bookID, s.Duration)
	// (c) An out-of-range position is rejected, never clamped into "finished":
	// a corrupt or foreign value past the end must not mark the book done.
	if duration > 0 && ct > duration+absLocalSessionOverrunSec {
		res.Error = "currentTime is beyond the item's duration"
		return res
	}
	if duration > 0 && ct > duration {
		ct = duration
	}

	stored := progress.Progress{Duration: duration}
	if pos != nil {
		stored.CurrentTime = pos.PositionSeconds
		stored.UpdatedAtMs = msEpoch(pos.UpdatedAt)
	}
	if state != nil {
		stored.IsFinished = state.Status == database.UserBookStatusFinished
		if ms := msEpoch(state.LastActivityAt); ms > stored.UpdatedAtMs {
			stored.UpdatedAtMs = ms
		}
	}

	var (
		merged   progress.Progress
		accepted bool
	)
	if startedAt != nil && *startedAt > stored.UpdatedAtMs+tol {
		// (a) The listen BEGAN after the server's newest known position — by
		// more than the skew tolerance, so a device clock running ahead cannot
		// make an older listen look newer and rewind a position written after
		// it. Then it may move the position either way (the user re-listened to
		// an earlier chapter while offline). startedAt is not re-stamped on
		// replay, which is what makes this safe where updatedAt is not.
		// Finished stays sticky, exactly as on /sync.
		merged, accepted = progress.MergeIncoming(stored, progress.Progress{
			CurrentTime: ct, Duration: duration, UpdatedAtMs: *startedAt,
		})
	} else {
		// Otherwise forward-only: a backlog entry can never rewind the server.
		merged, accepted = progress.MergeOfflineReplay(stored, progress.Progress{
			CurrentTime: ct, Duration: duration, UpdatedAtMs: now.UnixMilli(),
		})
	}
	res.Success = true
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
	// Read-modify-write so HideFromContinueListening survives, and
	// setDerivedStatus so a status the user pinned by hand (StatusManual) is
	// left alone rather than overwritten by this computed one.
	if err := h.updateUserBookState(userID, bookID, func(st *database.UserBookState) {
		setDerivedStatus(st, status)
		st.ProgressPct = pct
		st.LastSegmentID = absProgressSegmentID
		st.LastActivityAt = now
	}); err != nil {
		res.Error = "position stored; book state not updated"
	}
	res.ProgressSynced = true
	return res
}
