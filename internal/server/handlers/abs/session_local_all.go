// file: internal/server/handlers/abs/session_local_all.go
// version: 1.8.0
// guid: fcff98d1-5709-4c26-a345-d79231c02527
// last-edited: 2026-09-19

package abs

import (
	"math"
	"net/http"
	"slices"
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
// (b) while a progress-reset tombstone exists, a session is discarded unless its
// position's updatedAt provably post-dates the reset AND the position does not
// land back on a position the reset discarded (unless the listener has already
// listened up to it since);
// (c) a position absurdly past the item's duration is refused, one slightly past
// it is clamped to the end. Background on the forward-only default follows.
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
	// StartedAt is when the listen began on the device (ms epoch). It gates the
	// rewind branch (a session that began after the server's newest position).
	// It does NOT date the position: see UpdatedAt.
	StartedAt *int64 `json:"startedAt"`
	// UpdatedAt is when the device last moved this session's position (ms
	// epoch). AudioBooth sets it on every progress sync
	// (SessionManager.syncProgress: session.updatedAt = min(now, end of the
	// session's day)) and sends the stored value on replay, so it dates the
	// POSITION, which is what the reset tombstone must judge. startedAt dates
	// only the session, and AudioBooth REUSES one local session across a reset
	// (a new one starts only when the old one closes or the day changes).
	UpdatedAt *int64 `json:"updatedAt"`
}

// absLocalSessionOverrunSec is how far past the item's duration a reported
// position may run before it is treated as garbage rather than "at the end".
const absLocalSessionOverrunSec = 5.0

// absClockSkewTolerance bounds how far a device clock may disagree with the
// server before its startedAt stops being trusted in either direction.
const absClockSkewTolerance = 2 * time.Minute

// absMaxPlaybackRate is the fastest a listener can move the playhead by
// listening. AudioBooth's speed picker tops out at 3.5x
// (SpeedPickerSheetModel.swift: range 0.5...3.5); 4.0 leaves headroom for other
// clients.
// It bounds only LEGACY tombstones, written before the reset recorded the
// discarded position (see resetPositionResurrected).
const absMaxPlaybackRate = 4.0

// absResetPositionWindowSec / absResetPositionWindowFrac: a replayed position
// within ±max(30 s, 1% of the old position) of a position a reset discarded
// is that discarded position coming back.
const (
	absResetPositionWindowSec  = 30.0
	absResetPositionWindowFrac = 0.01
)

// absEndOfBookMargin is how far past the known duration a reported position may
// fall and still be treated as "at the end" (clamped + finished) rather than as
// garbage.
const absEndOfBookMargin = 0.02

type localAllReq struct {
	Sessions []localSessionReq `json:"sessions"`
}

// localSessionResult is one row of the upstream ABS response.
type localSessionResult struct {
	ID             string `json:"id"`
	Success        bool   `json:"success"`
	ProgressSynced bool   `json:"progressSynced"`
	Error          string `json:"error,omitempty"`
	// transient marks a failure a retry can fix (a read/resolve/write error),
	// as opposed to a refusal of the session itself. /api/session/local maps
	// it to 503 (AudioBooth retries 5xx) vs 409 (AudioBooth skips a 4xx).
	transient bool
	// refused marks a deliberate refusal of the session's DATA (it would undo
	// a progress reset, carries an impossible position, or belongs to another
	// user). /api/session/local answers 409 for these. A session that names
	// nothing we can apply (no id, unknown item, podcast) is not "refused":
	// there is nothing to misreport, and ShelfPlayer reads a failure on this
	// path as the connection being offline (§1.8.8 item 1).
	refused bool
	// discarded marks a session deliberately dropped because it would undo a
	// progress reset. It can never become valid, so /api/session/local answers
	// 200 for it (AudioBooth re-sends a 4xx'd session on every activation,
	// forever); the discard is logged at Info with the reason.
	discarded bool
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
		res.refused = true
		return res
	case s.EpisodeID != nil && strings.TrimSpace(*s.EpisodeID) != "":
		res.Error = "podcast episodes are not supported"
		return res
	}
	bookID, rerr := h.bookIDForSyncID(strings.TrimSpace(s.LibraryItemID))
	if rerr != nil {
		res.Error = "could not resolve library item; retry"
		res.transient = true
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
		res.refused = true
		return res
	}

	pos, err := h.progress.GetUserPosition(userID, bookID)
	if err != nil {
		// Unknown stored position: writing blind could rewind the listener,
		// which is the one outcome this endpoint must never produce.
		logProgressUnavailable("session/local: read position", userID, bookID, err)
		res.Error = "could not read stored progress"
		res.transient = true
		return res
	}
	state, err := h.progress.GetUserBookState(userID, bookID)
	if err != nil {
		// Unreadable state: the reset tombstone cannot be checked, and
		// applying blind could undo a reset. Transient: retry (503).
		logProgressUnavailable("session/local: read state", userID, bookID, err)
		res.Error = "could not read book state"
		res.transient = true
		return res
	}

	now := h.now()
	tol := absClockSkewTolerance.Milliseconds()
	// startedAt is trusted only when present and not implausibly in the future
	// (a device clock far ahead). An untrusted start is treated as absent.
	var startedAt *int64
	if s.StartedAt != nil && *s.StartedAt > 0 && *s.StartedAt <= now.UnixMilli()+tol {
		startedAt = s.StartedAt
	}

	// (b) Reset tombstone. While one exists, a session is accepted ONLY if its
	// POSITION provably post-dates the reset: a present, trusted updatedAt (not
	// beyond server now + tolerance) at or after reset + tolerance. The
	// tolerance widens only the REFUSE side. Refused:
	//   - a position last moved before, or within the tolerance of, the reset:
	//     the stored position is 0 after a reset, so forward-only would bring
	//     the discarded position back;
	//   - a missing, zero or implausibly-future updatedAt: it cannot prove the
	//     position came after the reset.
	// Judged on updatedAt, NOT startedAt (review of #3470 at 0690ce2d6):
	// AudioBooth keeps the same local session across a reset, so real
	// listening after a 10:00 reset arrives on a session with startedAt 09:00
	// and was refused. AudioBooth always sends updatedAt (non-optional Int in
	// its SessionSync model). Without a tombstone none of this applies.
	//
	// ⚠️ KNOWN LIMIT: a device clock running FAST by less than the tolerance
	// (2 min) can make a position moved shortly BEFORE the reset look like it
	// came after it, and that position would be accepted. Closing it needs a
	// server-side record of when each device's position moved, which offline
	// replay by definition does not have. Clocks more than 2 min fast are
	// untrusted and refused. Dating the position (updatedAt) bounds this better
	// than dating the session did: a startedAt-based check let ANY position a
	// long-running session reached after the reset through, however old its
	// last update.
	//
	// ⚠️ KNOWN LIMIT (midnight clamp): AudioBooth caps updatedAt at the end of
	// the session's day. A session paused before midnight and resumed after a
	// reset at, say, 00:10 reports an updatedAt of 23:59:59 for its first
	// post-reset sync, which is refused; about one sync interval (~20 s) of
	// listening is lost, and the playhead carries over on the next sync.
	//
	// DISCARDED POSITIONS (review of #3470 at 9e2e39285): some clients
	// RE-STAMP updatedAt=now when replaying a backlog (spec §1.8.7), so a
	// pre-reset backlog replayed after the reset passes the updatedAt test.
	// The tombstone records every position a reset discarded; a replay that
	// lands within the window of one is discarded whatever its timestamps.
	// There is deliberately no "already listened up to it" exemption: a
	// backlog is a queue of rising positions and each is applied in turn, so
	// such an exemption lets the queue walk itself onto the old position.
	// Everything else is accepted, including a seek far ahead right after the
	// reset. An earlier 4x "physical bound" narrowed
	// the hole but let a backlog replayed hours later (the normal offline
	// case) through, and refused real seeks; it now applies only to legacy
	// tombstones that recorded no position.
	//
	// ⚠️ KNOWN LIMIT (deliberate seek back): a user who resets and then SEEKS
	// straight to (within the window of) the position they discarded, without
	// listening up to it, is indistinguishable from the stale backlog and is
	// discarded; the playhead lands on their next sync outside the window.
	// Likewise genuine post-reset listening that passes THROUGH the old
	// position loses the syncs inside the window (at most ~2 × window of
	// position unsynced); the next sync beyond it lands normally.
	//
	// ⚠️ KNOWN LIMIT (backlog body): only a replay landing ON a discarded
	// position is recognisable. Re-stamped backlog entries short of it look
	// exactly like real post-reset listening and are accepted (forward-only),
	// so a replayed backlog can still move the playhead up to just before the
	// window. Entries that are not re-stamped are caught by the updatedAt rule.
	if state != nil && state.ProgressResetAt != nil {
		resetMs := state.ProgressResetAt.UnixMilli()
		trusted := s.UpdatedAt != nil && *s.UpdatedAt > 0 && *s.UpdatedAt <= now.UnixMilli()+tol
		if !trusted || *s.UpdatedAt < resetMs+tol {
			res.Error = "session position is not provably after a progress reset"
			res.discarded = true
			return res
		}
		if len(state.ProgressResetPositions) == 0 {
			// Legacy tombstone: no position recorded, keep the physical bound.
			reachable := float64(*s.UpdatedAt-resetMs)/1000*absMaxPlaybackRate + absClockSkewTolerance.Seconds()
			if ct > reachable {
				res.Error = "position cannot have been reached by listening since the progress reset"
				res.discarded = true
				return res
			}
		} else if slices.ContainsFunc(state.ProgressResetPositions, func(old float64) bool {
			return resetPositionResurrected(ct, old)
		}) {
			res.Error = "session position is the position a progress reset discarded"
			res.discarded = true
			return res
		}
	}

	// (c) Duration sanity. Per-file durations are whole seconds (up to ~1 s
	// lost per file) and a file may have none at all, so the summed duration
	// is only approximate:
	//   - any file with an unknown (0) duration → the duration is unknown and
	//     nothing is refused or clamped on that ground;
	//   - otherwise tolerance = max(5 s, files × 1 s + 0.5% of the duration);
	//     a position up to 2% (+ tolerance) past the end is the end — clamped
	//     to the duration, which marks the book finished; only a position
	//     beyond that is refused as impossible.
	// Before this, a 60-file 36,000 s book summed to ~35,970 s and a finish at
	// 35,999 s was refused, so the finish never synced.
	duration, known, files, derr := h.durationBoundsForBook(bookID, s.Duration)
	if derr != nil {
		logProgressUnavailable("session/local: read duration", userID, bookID, derr)
		res.Error = "could not read the item's duration"
		res.transient = true
		return res
	}
	if known && duration > 0 {
		slack := max(absLocalSessionOverrunSec, float64(files)+0.005*duration)
		if ct > duration*(1+absEndOfBookMargin)+slack {
			res.Error = "currentTime is beyond the item's duration"
			res.refused = true
			return res
		}
		if ct > duration {
			ct = duration
		}
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
		res.transient = true
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

// resetPositionResurrected reports whether a replayed position ct would bring
// back old, a position a reset discarded: it lands within ±max(30 s, 1%) of
// it. A discarded position within the window of 0 resurrects nothing.
func resetPositionResurrected(ct, old float64) bool {
	window := max(absResetPositionWindowSec, absResetPositionWindowFrac*old)
	return old > window && math.Abs(ct-old) <= window
}
