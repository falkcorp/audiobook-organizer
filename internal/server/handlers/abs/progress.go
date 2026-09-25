// file: internal/server/handlers/abs/progress.go
// version: 1.9.0
// guid: 4f0a7d21-9c63-4b58-8e17-52d9a0b3fc84
// last-edited: 2026-09-25

package abs

import (
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/falkcorp/audiobook-organizer/internal/syncapi/progress"
	"github.com/gin-gonic/gin"
)

// Phase 6 — the progress WRITE half.
//
// Every shape here was captured from a real Audiobookshelf 2.36.0 server on
// 2026-08-02 (testdata/abs-fixtures/), not inferred from the published docs, and two
// of the captures contradicted this project's own spec:
//
//  1. DELETE /api/me/progress/:id is keyed by the mediaProgress ROW id, not by the
//     libraryItemId — deleting by item id answers 404 on real ABS.
//  2. POST /api/me/item/:id/remove-from-continue-listening DOES NOT EXIST on ABS
//     2.36.0 (it answers "Cannot POST"). The real mechanism is the
//     hideFromContinueListening field on PATCH /api/me/progress/:id.
//
// Both are handled below, and both are handled as a SUPERSET: we accept the id in
// either form and we serve the POST alias as well as the PATCH field, because §1.8
// makes "where the clients disagree, implement the superset" the standing rule and
// because a 404 here is precisely the symptom the owner reported.

// maxBatchProgressUpdates bounds PATCH /api/me/progress/batch/update.
//
// A client batch is a handful of books — this is not a whole-library collection, so
// the loop is deliberately sequential (CLAUDE.md's worker-pool rule targets
// library-scale work; parallelising a 5-element list would only add a race over the
// same (user, book) rows). The cap exists so a malformed or hostile body cannot turn
// one request into an unbounded write storm.
const maxBatchProgressUpdates = 1000

// progressPatchRequest is the PATCH /api/me/progress/:id body.
//
// Every field is a POINTER because absence and zero mean different things on this
// endpoint: `{"hideFromContinueListening": true}` alone must not be read as
// "currentTime: 0", which would rewind the listener to the start of the book. That
// is the exact shape the remove-from-continue-listening path sends.
//
// `progress` is accepted and IGNORED: it is a derived 0.0-1.0 fraction of
// currentTime/duration, and honouring a client's copy of it would let a stale
// fraction contradict the position we just stored. We recompute it on read.
type progressPatchRequest struct {
	CurrentTime               *float64 `json:"currentTime"`
	Duration                  *float64 `json:"duration"`
	IsFinished                *bool    `json:"isFinished"`
	HideFromContinueListening *bool    `json:"hideFromContinueListening"`
	Progress                  *float64 `json:"progress"`
	// LibraryItemID is only read on the batch path, where the id is in the body
	// instead of the URL.
	LibraryItemID string `json:"libraryItemId"`
}

// ── GET /api/me/progress ────────────────────────────────────────────────────

// MediaProgressList handles GET /api/me/progress.
//
// 🔴 SAME DATA-LOSS RULE AS /api/me (§1.8.1), and easy to miss because this body is
// a bare wrapper rather than a user object: AudioBooth's syncFromAPI DELETES every
// local progress row absent from a server-supplied mediaProgress list. So this
// endpoint returns the COMPLETE list or a 5xx — never a 200 with a short list, never
// paginated. It shares userData.MediaProgress with /api/me rather than enumerating
// again, so the two can never disagree about what "complete" means.
func (h *Handler) MediaProgressList(c *gin.Context) {
	user, ok := servermiddleware.CurrentUser(c)
	if !ok || user == nil {
		respondError(c, http.StatusUnauthorized, "authentication required")
		return
	}
	rows, _, err := h.userPayload(user.ID)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not load user data")
		return
	}
	respondJSON(c, http.StatusOK, gin.H{"mediaProgress": rows})
}

// ── GET /api/me/progress/:id ────────────────────────────────────────────────

// MediaProgressGet handles GET /api/me/progress/:id.
//
// This is the call AudioBooth made on 2026-08-02 that 404'd and stalled its
// reset-progress flow. Real ABS answers a BARE mediaProgress object (not wrapped)
// when a row exists and a plain-text 404 when it does not.
func (h *Handler) MediaProgressGet(c *gin.Context) {
	user, ref, ok := h.resolveTarget(c)
	if !ok {
		return
	}
	bookID := ref.BookID
	if h.userData == nil {
		respondError(c, http.StatusInternalServerError, "progress is unavailable")
		return
	}
	// The row is READ from the canonical book but RENDERED under the id the
	// client asked with. AudioBooth keys its local progress row by the id of
	// the page it opened, so a body naming the canonical id after the client
	// asked with a merge loser's id lands on a row that page never reads, and
	// "marked finished" reads unfinished on the next visit (owner, 2026-09-25).
	row, found, err := h.userData.MediaProgressFor(user.ID, bookID, ref.RequestedID)
	if err != nil {
		// 5xx, never a 404: a 404 we are not sure about reads to the client as
		// "you have no progress in this book", and acting on that costs a place.
		respondError(c, http.StatusInternalServerError, "could not load progress")
		return
	}
	if !found {
		respondNotFoundPlain(c)
		return
	}
	respondJSON(c, http.StatusOK, row)
}

// ── PATCH /api/me/progress/:id ──────────────────────────────────────────────

// MediaProgressPatch handles PATCH /api/me/progress/:id.
//
// Real ABS answers `200 text/plain "OK"` here, so this returns respondPlainOK rather
// than a JSON body — matched to the oracle capture rather than "improved", because
// neither client reads the body and deviating buys nothing.
func (h *Handler) MediaProgressPatch(c *gin.Context) {
	user, ref, ok := h.resolveTarget(c)
	if !ok {
		return
	}
	bookID := ref.BookID
	var req progressPatchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// An unparseable body on a write is a 400, unlike on /sync: /sync must never
		// 4xx because a 4xx wedges an offline replay queue permanently (§1.7.3 #2),
		// but this endpoint is interactive and a silent 200 would tell the user their
		// change was saved when nothing was written.
		respondError(c, http.StatusBadRequest, "invalid progress body")
		return
	}
	// Logged because a 200 here does not prove a write: applyProgressUpdate
	// returns nil when the stored record wins the merge. The owner's "mark as
	// finished doesn't stick" (2026-09-25) could not be diagnosed without the
	// body the app actually sent.
	progressLog.Info("abs: progress patch: user=%s item=%s book=%s currentTime=%v duration=%v isFinished=%v progress=%v hide=%v",
		logger.SanitizeLogValue(user.ID), logger.SanitizeLogValue(c.Param("id")), logger.SanitizeLogValue(bookID),
		fptr(req.CurrentTime), fptr(req.Duration), bptr(req.IsFinished),
		fptr(req.Progress), bptr(req.HideFromContinueListening))
	if err := h.applyProgressUpdate(user.ID, bookID, req); err != nil {
		respondProgressWriteError(c, "PATCH progress", user.ID, bookID, err, "could not save progress")
		return
	}
	respondPlainOK(c)
}

func fptr(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

func bptr(v *bool) any {
	if v == nil {
		return nil
	}
	return *v
}

// ── PATCH /api/me/progress/batch/update ─────────────────────────────────────

// MediaProgressBatchUpdate handles PATCH /api/me/progress/batch/update.
//
// The body is a BARE ARRAY (verified against the oracle), each element carrying its
// own libraryItemId. Elements that name an unknown item are SKIPPED rather than
// failing the request: a batch is a best-effort catch-up from a client that may hold
// ids for books since merged or removed, and rejecting the whole array would strand
// every other element in it.
func (h *Handler) MediaProgressBatchUpdate(c *gin.Context) {
	user, ok := servermiddleware.CurrentUser(c)
	if !ok || user == nil {
		respondError(c, http.StatusUnauthorized, "authentication required")
		return
	}
	var batch []progressPatchRequest
	if err := c.ShouldBindJSON(&batch); err != nil {
		respondError(c, http.StatusBadRequest, "invalid progress batch body")
		return
	}
	if len(batch) > maxBatchProgressUpdates {
		respondError(c, http.StatusBadRequest, "progress batch too large")
		return
	}

	for _, item := range batch {
		ref, err := h.resolveBodyItem(user.ID, item.LibraryItemID, true)
		if errors.Is(err, errItemNotFound) {
			continue
		}
		if err != nil {
			// Not skipped: a skipped element is answered "OK" with nothing
			// written, and the client drops its pending update.
			progressLog.Warn("abs: PATCH progress batch: could not resolve item: user_id=%s id=%s: %v",
				logger.SanitizeLogValue(user.ID), logger.SanitizeLogValue(item.LibraryItemID), err)
			respondError(c, http.StatusServiceUnavailable, "could not resolve library item")
			return
		}
		bookID := ref.BookID
		if err := h.applyProgressUpdate(user.ID, bookID, item); err != nil {
			respondProgressWriteError(c, "PATCH progress batch", user.ID, bookID, err, "could not save progress")
			return
		}
	}
	respondPlainOK(c)
}

// ── DELETE /api/me/progress/:id ─────────────────────────────────────────────

// MediaProgressDelete handles DELETE /api/me/progress/:id — "reset progress".
//
// It clears the stored positions AND resets the derived book state, because a
// position row without its state leaves the book reading "finished" at 0:00. The
// user's hide-from-continue-listening choice is deliberately cleared too: resetting a
// book to unstarted and leaving it hidden would make it invisible everywhere with no
// way to get it back from the client UI.
func (h *Handler) MediaProgressDelete(c *gin.Context) {
	user, ref, ok := h.resolveTarget(c)
	if !ok {
		return
	}
	bookID := ref.BookID
	if h.progress == nil {
		respondError(c, http.StatusInternalServerError, "progress is unavailable")
		return
	}

	// 404 when there was nothing to reset, matching real ABS. Checked BEFORE the
	// delete so the answer describes what actually happened.
	pos, err := h.progress.GetUserPosition(user.ID, bookID)
	if err != nil {
		// Transient: nothing was changed, a retry can succeed.
		logProgressUnavailable("reset: read position", user.ID, bookID, err)
		respondError(c, http.StatusServiceUnavailable, "could not load progress")
		return
	}
	if pos == nil {
		respondNotFoundPlain(c)
		return
	}

	// ORDER MATTERS. The tombstone is written FIRST, positions cleared second:
	//   - tombstone write fails → nothing changed; the retry finds the
	//     position and records it (503 for an unreadable state row);
	//   - clear fails → the tombstone already guards replay; the retry finds
	//     the position still there and clears it (appendResetPosition does not
	//     record the same position twice).
	// The reverse order left positions cleared with no tombstone, and the
	// retry then answered 404 with nothing recorded.
	if err := h.updateUserBookState(user.ID, bookID, func(state *database.UserBookState) {
		state.Status = database.UserBookStatusUnstarted
		state.StatusManual = false
		state.ProgressPct = 0
		state.TotalListenedSeconds = 0
		state.LastSegmentID = ""
		state.HideFromContinueListening = false
		state.LastActivityAt = h.now()
		// Tombstone: offline replay must not bring the discarded position back
		// (session_local_all.go). It records WHEN (a replayed position last
		// moved before the reset is stale) and WHERE (a re-stamped backlog
		// that lands on the old position is stale whatever its timestamp).
		resetAt := h.now()
		state.ProgressResetAt = &resetAt
		state.ProgressResetPositions = appendResetPosition(state.ProgressResetPositions, pos.PositionSeconds)
	}); err != nil {
		respondProgressWriteError(c, "reset: write tombstone", user.ID, bookID, err, "could not reset progress")
		return
	}
	if err := h.progress.ClearUserPositions(user.ID, bookID); err != nil {
		logProgressUnavailable("reset: clear positions (tombstone already written)", user.ID, bookID, err)
		respondError(c, http.StatusServiceUnavailable, "could not reset progress")
		return
	}
	respondPlainOK(c)
}

// appendResetPosition records a discarded position in the reset tombstone,
// keeping the newest database.MaxProgressResetPositions. It returns a new
// slice, never aliasing the caller's.
func appendResetPosition(positions []float64, discarded float64) []float64 {
	if n := len(positions); n > 0 && positions[n-1] == discarded {
		// A retried reset (the earlier attempt wrote the tombstone but could
		// not clear the positions) records the same position once.
		return slices.Clone(positions)
	}
	out := append(slices.Clone(positions), discarded)
	if n := len(out) - database.MaxProgressResetPositions; n > 0 {
		out = out[n:]
	}
	return out
}

// ── POST /api/me/item/:id/remove-from-continue-listening ────────────────────

// RemoveFromContinueListening handles the POST alias for hiding a book from Continue
// Listening.
//
// Real ABS 2.36.0 has no such route. It is served here because a client calls it and
// took a 404 in production, and because the operation is unambiguous: it is exactly
// PATCH /api/me/progress/:id with {"hideFromContinueListening": true}, which the
// oracle confirmed is the actual mechanism.
//
// 🔴 The body must be NON-EMPTY (§1.8.6 / spec:318) — an empty 200 is fatal to these
// decoders — so it answers `{}` rather than a bare 200 or a plain-text "OK".
func (h *Handler) RemoveFromContinueListening(c *gin.Context) {
	user, ref, ok := h.resolveTarget(c)
	if !ok {
		return
	}
	bookID := ref.BookID
	if h.progress == nil {
		respondError(c, http.StatusInternalServerError, "progress is unavailable")
		return
	}
	if err := h.updateUserBookState(user.ID, bookID, func(state *database.UserBookState) {
		state.HideFromContinueListening = true
	}); err != nil {
		respondProgressWriteError(c, "remove-from-continue-listening", user.ID, bookID, err, "could not update progress")
		return
	}
	respondJSON(c, http.StatusOK, gin.H{})
}

// ── shared write path ───────────────────────────────────────────────────────

// applyProgressUpdate is the single merge+persist path behind PATCH and batch.
//
// It applies §5's conflict policy through progress.MergeExplicit — the PATCH variant,
// where the client DOES state isFinished and the server must honour rather than
// contradict it, including re-opening a finished book. The alternative (last write
// wins) is what silently rewinds a listener when a stale device wakes up.
//
// A body carrying ONLY hideFromContinueListening skips the merge entirely: with no
// currentTime there is no position to reconcile, and running it through the merge
// would compare the client's absent position (0) against the stored one and either
// reject the whole update or, worse, write a 0.
func (h *Handler) applyProgressUpdate(userID, bookID string, req progressPatchRequest) error {
	if h.progress == nil {
		return errNoProgressStore
	}

	if req.HideFromContinueListening != nil {
		hide := *req.HideFromContinueListening
		if err := h.updateUserBookState(userID, bookID, func(state *database.UserBookState) {
			state.HideFromContinueListening = hide
		}); err != nil {
			return err
		}
	}
	if req.CurrentTime == nil && req.IsFinished == nil {
		// Nothing position-related to do. Not an error: a hide-only PATCH is a
		// legitimate, complete request.
		return nil
	}

	stored := progress.Progress{}
	pos, err := h.progress.GetUserPosition(userID, bookID)
	if err != nil {
		// Fail closed: an unreadable position is not "no position yet".
		// Merging against an empty stored value lets a stale PATCH rewind
		// the listener.
		return fmt.Errorf("%w: %s/%s: %w", errProgressPositionUnreadable, userID, bookID, err)
	}
	if pos != nil {
		stored.CurrentTime = pos.PositionSeconds
		stored.UpdatedAtMs = msEpoch(pos.UpdatedAt)
	}
	state, err := h.progress.GetUserBookState(userID, bookID)
	if err != nil {
		// Fail closed: without the state the finished flag and the reset
		// tombstone are unknown, and updateUserBookState would refuse the
		// follow-up write anyway, leaving a position with no state.
		return fmt.Errorf("%w: %s/%s: %w", errProgressStateUnreadable, userID, bookID, err)
	}
	if state != nil {
		stored.IsFinished = state.Status == database.UserBookStatusFinished
		if ms := msEpoch(state.LastActivityAt); ms > stored.UpdatedAtMs {
			stored.UpdatedAtMs = ms
		}
	}
	if stored.Duration, err = h.durationForBook(bookID, req.Duration); err != nil {
		return err
	}

	now := h.now().UnixMilli()
	incoming := progress.Progress{
		CurrentTime: stored.CurrentTime,
		Duration:    stored.Duration,
		IsFinished:  stored.IsFinished,
		// The client does not send a timestamp on this endpoint, so the write is
		// stamped server-side and must be guaranteed to BEAT the client's own
		// tie-break: AudioBooth truncates both sides to whole seconds and compares
		// with strict >, so a same-second write is silently discarded (§1.8.7).
		UpdatedAtMs: progress.NextServerTimestampMs(stored.UpdatedAtMs, now),
	}
	if req.CurrentTime != nil && *req.CurrentTime >= 0 {
		incoming.CurrentTime = *req.CurrentTime
	}
	if req.IsFinished != nil {
		incoming.IsFinished = *req.IsFinished
	}

	merged, accepted := progress.MergeExplicit(stored, incoming)
	if !accepted {
		progressLog.Info("abs: progress patch not applied, stored record wins: user=%s book=%s stored_time=%v stored_finished=%v stored_updated_ms=%v incoming_time=%v incoming_finished=%v incoming_updated_ms=%v",
			logger.SanitizeLogValue(userID), logger.SanitizeLogValue(bookID),
			stored.CurrentTime, stored.IsFinished, stored.UpdatedAtMs,
			incoming.CurrentTime, incoming.IsFinished, incoming.UpdatedAtMs)
		// The stored record already wins. Reporting success is correct: the client's
		// intent ("this book is at position X") is satisfied by a server value that
		// is at or ahead of X.
		return nil
	}

	if err := h.progress.SetUserPosition(userID, bookID, absProgressSegmentID, merged.CurrentTime); err != nil {
		return err
	}
	status := database.UserBookStatusInProgress
	switch {
	case merged.IsFinished:
		status = database.UserBookStatusFinished
	case merged.CurrentTime <= 0:
		status = database.UserBookStatusUnstarted
	}
	pct := 0
	if merged.Duration > 0 {
		pct = min(int(merged.CurrentTime/merged.Duration*100), 100)
	}
	return h.updateUserBookState(userID, bookID, func(state *database.UserBookState) {
		state.Status = status
		state.ProgressPct = pct
		state.LastSegmentID = absProgressSegmentID
		state.LastActivityAt = h.now()
	})
}

// durationForBook reproduces §5b's ONE-authoritative-duration rule: the sum of the
// per-file durations, with Book.Duration used only for a book that has no file rows.
// It matches userdata.go's durationFor and the mapper's loadOneItemView exactly —
// mixing sources is what leaves a fully-listened book stuck at 99% forever.
//
// A client-supplied duration is a last-resort fallback only, never a preference.
func (h *Handler) durationForBook(bookID string, clientDuration *float64) (float64, error) {
	if h.library != nil {
		// countedBookFiles: the same rows (copies excluded) the
		// mapper lists.
		book, files, err := countedBookFiles(h.library, bookID)
		if err != nil {
			// Fail closed: the duration decides "finished" and the percent;
			// silently substituting the client's value is not a fallback, it
			// is a different answer.
			return 0, fmt.Errorf("%w: %w", errBookDurationUnreadable, err)
		}
		if len(files) > 0 {
			total := 0.0
			for i := range files {
				total += float64(files[i].Duration)
			}
			if total > 0 {
				return total, nil
			}
		}
		if book != nil && book.Duration != nil {
			return float64(*book.Duration), nil
		}
	}
	if clientDuration != nil && *clientDuration > 0 {
		return *clientDuration, nil
	}
	return 0, nil
}

// durationBoundsForBook is durationForBook for code that must JUDGE a position
// against the duration (offline replay). known=false when any file of the book
// has no duration: the sum is then an undercount, and the caller must neither
// refuse nor clamp a position against it. For an unknown duration it returns the
// client's duration when given, else 0, so an uncertain value never auto-finishes
// a book. fileCount is the number of book_file rows, for the per-file rounding
// slack. A read error is returned (errBookDurationUnreadable), never guessed past.
func (h *Handler) durationBoundsForBook(bookID string, clientDuration *float64) (duration float64, known bool, fileCount int, err error) {
	if h.library != nil {
		_, files, err := countedBookFiles(h.library, bookID)
		if err != nil {
			return 0, false, 0, fmt.Errorf("%w: %w", errBookDurationUnreadable, err)
		}
		if len(files) > 0 {
			total := 0.0
			for i := range files {
				if files[i].Duration <= 0 {
					if clientDuration != nil && *clientDuration > 0 {
						return *clientDuration, false, len(files), nil
					}
					return 0, false, len(files), nil
				}
				total += float64(files[i].Duration)
			}
			return total, true, len(files), nil
		}
	}
	d, err := h.durationForBook(bookID, clientDuration)
	if err != nil {
		return 0, false, 0, err
	}
	return d, d > 0, 0, nil
}

// errNoProgressStore is returned by the shared write path when the server booted
// without a listening-progress store. Unreachable in production (wireABSRoutes
// asserts the capability), but an explicit error beats a silent no-op that would
// report a saved position the server never wrote.
var errNoProgressStore = errors.New("abs: no listening-progress store is wired")

// errProgressStateUnreadable: the stored user_book_state row could not be read
// (I/O or decode). Nothing was written; the handler answers 503 (retryable).
var errProgressStateUnreadable = errors.New("abs: stored book state is unreadable")

// errBookDurationUnreadable: the book's files (or row) could not be read, so
// its authoritative duration is unknown. Nothing is written; 503.
var errBookDurationUnreadable = errors.New("abs: book duration is unreadable")

// errProgressPositionUnreadable: the stored listening position could not be
// read. Not "no position yet" (that is nil, nil); nothing was written and the
// handler answers 503, exactly as for errProgressStateUnreadable.
var errProgressPositionUnreadable = errors.New("abs: stored listening position is unreadable")

// progressLog records every fail-closed 503 on the progress paths with the
// wrapped cause, so an unreadable row is visible to the operator instead of
// only to the client.
var progressLog = logger.New("abs")

// logProgressUnavailable logs a fail-closed refusal: op, user, book and cause.
func logProgressUnavailable(op, userID, bookID string, err error) {
	progressLog.Warn("abs: %s: user_id=%s book_id=%s: nothing written, answering 503: %v",
		op, logger.SanitizeLogValue(userID), logger.SanitizeLogValue(bookID), err)
}

// respondProgressWriteError answers a progress write error with
// progressWriteStatus, logging it first (Warn for an unreadable row, Error
// otherwise).
func respondProgressWriteError(c *gin.Context, op, userID, bookID string, err error, msg string) {
	status := progressWriteStatus(err)
	if status == http.StatusServiceUnavailable {
		logProgressUnavailable(op, userID, bookID, err)
	} else {
		progressLog.Error("abs: %s: user_id=%s book_id=%s: %v",
			op, logger.SanitizeLogValue(userID), logger.SanitizeLogValue(bookID), err)
	}
	respondError(c, status, msg)
}

// progressWriteStatus maps a progress write error to its HTTP status: 503 for
// an unreadable state row (transient, nothing written), 500 otherwise.
func progressWriteStatus(err error) int {
	if errors.Is(err, errProgressStateUnreadable) || errors.Is(err, errProgressPositionUnreadable) ||
		errors.Is(err, errBookDurationUnreadable) {
		return http.StatusServiceUnavailable
	}
	return http.StatusInternalServerError
}

// respondNotFoundPlain answers exactly what real ABS answers for a missing progress
// row or bookmark: 404 with the plain-text body "Not Found".
//
// Plain text rather than JSON is matched to the oracle. Neither target client parses
// a non-200 body, and the body is non-empty either way, so following the captured
// shape costs nothing and removes one more place our surface could differ.
func respondNotFoundPlain(c *gin.Context) {
	c.String(http.StatusNotFound, "Not Found")
	c.Abort()
}
