// file: internal/server/handlers/abs/progress.go
// version: 1.0.0
// guid: 4f0a7d21-9c63-4b58-8e17-52d9a0b3fc84
// last-edited: 2026-08-02

package abs

import (
	"errors"
	"net/http"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
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
	user, bookID, ok := h.resolveProgressTarget(c)
	if !ok {
		return
	}
	if h.userData == nil {
		respondError(c, http.StatusInternalServerError, "progress is unavailable")
		return
	}
	row, found, err := h.userData.MediaProgressFor(user.ID, bookID)
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
	user, bookID, ok := h.resolveProgressTarget(c)
	if !ok {
		return
	}
	var req progressPatchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// An unparseable body on a write is a 400, unlike on /sync: /sync must never
		// 4xx because a 4xx wedges an offline replay queue permanently (§1.7.3 #2),
		// but this endpoint is interactive and a silent 200 would tell the user their
		// change was saved when nothing was written.
		respondError(c, http.StatusBadRequest, "invalid progress body")
		return
	}
	if err := h.applyProgressUpdate(user.ID, bookID, req); err != nil {
		respondError(c, http.StatusInternalServerError, "could not save progress")
		return
	}
	respondPlainOK(c)
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
		bookID, _, ok := h.resolveBookID(user.ID, item.LibraryItemID)
		if !ok {
			continue
		}
		if err := h.applyProgressUpdate(user.ID, bookID, item); err != nil {
			respondError(c, http.StatusInternalServerError, "could not save progress")
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
	user, bookID, ok := h.resolveProgressTarget(c)
	if !ok {
		return
	}
	if h.progress == nil {
		respondError(c, http.StatusInternalServerError, "progress is unavailable")
		return
	}

	// 404 when there was nothing to reset, matching real ABS. Checked BEFORE the
	// delete so the answer describes what actually happened.
	pos, err := h.progress.GetUserPosition(user.ID, bookID)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not load progress")
		return
	}
	if pos == nil {
		respondNotFoundPlain(c)
		return
	}

	if err := h.progress.ClearUserPositions(user.ID, bookID); err != nil {
		respondError(c, http.StatusInternalServerError, "could not reset progress")
		return
	}
	if err := h.updateUserBookState(user.ID, bookID, func(state *database.UserBookState) {
		state.Status = database.UserBookStatusUnstarted
		state.StatusManual = false
		state.ProgressPct = 0
		state.TotalListenedSeconds = 0
		state.LastSegmentID = ""
		state.HideFromContinueListening = false
		state.LastActivityAt = h.now()
	}); err != nil {
		respondError(c, http.StatusInternalServerError, "could not reset progress")
		return
	}
	respondPlainOK(c)
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
	user, bookID, ok := h.resolveProgressTarget(c)
	if !ok {
		return
	}
	if h.progress == nil {
		respondError(c, http.StatusInternalServerError, "progress is unavailable")
		return
	}
	if err := h.updateUserBookState(user.ID, bookID, func(state *database.UserBookState) {
		state.HideFromContinueListening = true
	}); err != nil {
		respondError(c, http.StatusInternalServerError, "could not update progress")
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
	if pos, err := h.progress.GetUserPosition(userID, bookID); err == nil && pos != nil {
		stored.CurrentTime = pos.PositionSeconds
		stored.UpdatedAtMs = msEpoch(pos.UpdatedAt)
	}
	state, _ := h.progress.GetUserBookState(userID, bookID)
	if state != nil {
		stored.IsFinished = state.Status == database.UserBookStatusFinished
		if ms := msEpoch(state.LastActivityAt); ms > stored.UpdatedAtMs {
			stored.UpdatedAtMs = ms
		}
	}
	stored.Duration = h.durationForBook(bookID, req.Duration)

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
		pct = int(merged.CurrentTime / merged.Duration * 100)
		if pct > 100 {
			pct = 100
		}
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
func (h *Handler) durationForBook(bookID string, clientDuration *float64) float64 {
	if h.library != nil {
		if files, err := h.library.GetBookFiles(bookID); err == nil && len(files) > 0 {
			total := 0.0
			for i := range files {
				total += float64(files[i].Duration)
			}
			if total > 0 {
				return total
			}
		}
		if book, err := h.library.GetBookByID(bookID); err == nil && book != nil && book.Duration != nil {
			return float64(*book.Duration)
		}
	}
	if clientDuration != nil && *clientDuration > 0 {
		return *clientDuration
	}
	return 0
}

// ── id resolution ───────────────────────────────────────────────────────────

// resolveProgressTarget authenticates the caller and turns the `:id` path parameter
// into an internal book id, writing the error response itself when it cannot.
//
// 🔴 THE `:id` ARRIVES IN TWO DIFFERENT FORMS and both are accepted:
//
//   - the libraryItemId (the 36-char sync UUID) — what a client holds from a browse
//     response, and what our own /api/me/progress/:id GET is addressed with;
//   - the mediaProgress ROW id, which our read half renders as "<userID>-<syncID>"
//     (userdata.go / item.go). Real ABS keys DELETE /api/me/progress/:id by the ROW
//     id — verified against the oracle 2026-08-02, where deleting by libraryItemId
//     answers 404 — so a client that read the list and handed the row id back must
//     be understood here or reset-progress silently does nothing.
//
// The row-id form is stripped by matching the AUTHENTICATED caller's own user id as
// a prefix rather than by splitting on a separator. That is not defensiveness for its
// own sake: it makes the parse independent of whether a user id can ever contain the
// separator, and it means one user can never address another user's row by
// constructing an id — the prefix that gets stripped is always their own.
func (h *Handler) resolveProgressTarget(c *gin.Context) (*database.User, string, bool) {
	user, _, bookID, ok := h.resolveTarget(c)
	return user, bookID, ok
}

// resolveTarget is resolveProgressTarget plus the canonical syncID, which the
// bookmark handlers need because the bookmark keyspace is keyed by the
// CLIENT-VISIBLE libraryItemId rather than by the internal book id.
func (h *Handler) resolveTarget(c *gin.Context) (user *database.User, syncID, bookID string, ok bool) {
	u, found := servermiddleware.CurrentUser(c)
	if !found || u == nil {
		respondError(c, http.StatusUnauthorized, "authentication required")
		return nil, "", "", false
	}
	bookID, syncID, found = h.resolveBookID(u.ID, c.Param("id"))
	if !found {
		respondNotFoundPlain(c)
		return nil, "", "", false
	}
	return u, syncID, bookID, true
}

// resolveBookID maps a client-visible id to an internal Book id AND the canonical
// syncID, accepting both the libraryItemId and the "<userID>-<syncID>" mediaProgress
// row id. See resolveProgressTarget for why both forms exist.
//
// The syncID it returns is the CANONICAL one (ResolveSyncItem follows merge
// redirects, spec §4.2), not the one the client sent. Callers that key storage by it
// therefore converge on one id per book even when a client is still holding the
// syncID of a book that has since lost a dedup merge.
func (h *Handler) resolveBookID(userID, raw string) (bookID, syncID string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || h.identity == nil {
		return "", "", false
	}
	candidates := []string{raw}
	if prefix := userID + "-"; userID != "" && strings.HasPrefix(raw, prefix) {
		// Tried FIRST: the row-id form is the more specific reading, and a bare
		// syncID can never carry this prefix.
		candidates = []string{strings.TrimPrefix(raw, prefix), raw}
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		item, err := h.identity.ResolveSyncItem(candidate)
		if err == nil && item != nil && item.CurrentBookID != "" {
			id := item.SyncID
			if id == "" {
				id = candidate
			}
			return item.CurrentBookID, id, true
		}
	}
	return "", "", false
}

// errNoProgressStore is returned by the shared write path when the server booted
// without a listening-progress store. Unreachable in production (wireABSRoutes
// asserts the capability), but an explicit error beats a silent no-op that would
// report a saved position the server never wrote.
var errNoProgressStore = errors.New("abs: no listening-progress store is wired")

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
