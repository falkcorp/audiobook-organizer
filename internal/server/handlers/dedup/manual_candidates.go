// file: internal/server/handlers/dedup/manual_candidates.go
// version: 1.0.0
// guid: fa18d5e2-e868-4872-866e-cd8bb2b6f9ff
// last-edited: 2026-09-25

package deduphandler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/logging"
	"github.com/gin-gonic/gin"
)

// maxManualCandidatePairs bounds one request. Enqueueing writes nothing to a
// book, so this is not the bulk-apply cap (applycap); it only keeps one call
// from turning into an unbounded loop of store reads.
const maxManualCandidatePairs = 1000

// manualCandidatePair is one requested pair.
type manualCandidatePair struct {
	BookA  string `json:"book_a"`
	BookB  string `json:"book_b"`
	Reason string `json:"reason"`
}

// manualCandidatesRequest is the POST /api/v1/dedup/candidates body.
//
// DryRun is a pointer so a body that omits it reads as a dry run: a plain bool
// would decode a missing key as false and write by default. Writing needs an
// explicit "dry_run": false.
type manualCandidatesRequest struct {
	Pairs  []manualCandidatePair `json:"pairs"`
	DryRun *bool                 `json:"dry_run"`
}

// Per-pair outcomes beyond the store's (created / already_open / reopened /
// decided): the pair was refused before reaching the store, or the store
// write failed.
const (
	manualOutcomeRejected = "rejected"
	manualOutcomeFailed   = "failed"
)

// manualCandidateResult reports one requested pair.
type manualCandidateResult struct {
	BookA   string `json:"book_a"`
	BookB   string `json:"book_b"`
	Reason  string `json:"reason,omitempty"`
	Outcome string `json:"outcome"`
	// Pinned: an existing scanner row gained (or would gain) the manual mark,
	// which exempts it from the automated purge passes.
	Pinned bool `json:"pinned,omitempty"`
	// Code / Error explain a rejected or failed pair.
	Code  string `json:"code,omitempty"`
	Error string `json:"error,omitempty"`
	// VersionGroupID is set for code same_version_group.
	VersionGroupID string                   `json:"version_group_id,omitempty"`
	Candidate      *database.DedupCandidate `json:"candidate,omitempty"`
}

// EnqueueManualDedupCandidates handles POST /api/v1/dedup/candidates.
//
// It puts hand-picked book pairs into the dedup review queue as pending
// candidates with layer "manual" and source "manual", so they appear in the
// same review list and UI as scanner candidates and are decided there. It
// never merges, dismisses or edits a book.
//
// Body:
//
//	{ "pairs": [ {"book_a": "...", "book_b": "...", "reason": "..."} ],
//	  "dry_run": true }
//
// dry_run defaults to TRUE; only an explicit false writes.
//
// Pairs, not clusters: the candidate store holds pairs, and the review UI
// assembles clusters from them by union-find. To put a cluster of N books up
// for review, send its pairs (a star around one book is enough to connect it).
//
// Each pair is validated independently and reported in "results":
//   - rejected (no write): missing_id, same_book, duplicate_in_request,
//     book_not_found, book_deleted, lookup_failed, same_version_group
//   - created: a new pending manual candidate
//   - already_open: a pending row exists and is returned (idempotent); a
//     scanner row is pinned with the manual mark so purge passes keep it
//   - reopened: the row was a machine reclassification (stale-drain /
//     stale-fp) and is pending again, pinned
//   - decided: the row is dismissed or merged; reported, left untouched
//   - failed: the store write errored (the response is then a 500)
//
// Manual candidates are exempt from every automated pass that deletes,
// dismisses, reclassifies, re-scores or auto-merges candidates; see
// database.CandidateSourceManual.
func (h *Handler) EnqueueManualDedupCandidates(c *gin.Context) {
	es := h.embeddingStore
	if es == nil {
		httputil.RespondWithServiceUnavailable(c, "embedding store not available")
		return
	}
	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	var req manualCandidatesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		if errors.Is(err, io.EOF) {
			httputil.RespondWithBadRequest(c, "request body is required: {\"pairs\": [...], \"dry_run\": true}")
			return
		}
		httputil.RespondWithBadRequest(c, "invalid request body: "+err.Error())
		return
	}
	if len(req.Pairs) == 0 {
		httputil.RespondWithBadRequest(c, "pairs is required and must not be empty")
		return
	}
	if len(req.Pairs) > maxManualCandidatePairs {
		httputil.RespondWithBadRequest(c, fmt.Sprintf("too many pairs: %d (max %d per request)", len(req.Pairs), maxManualCandidatePairs))
		return
	}
	dryRun := req.DryRun == nil || *req.DryRun

	bookCache := make(map[string]*database.Book, len(req.Pairs)*2)
	lookupErr := make(map[string]error)
	lookup := func(id string) (*database.Book, error) {
		if b, ok := bookCache[id]; ok {
			return b, lookupErr[id]
		}
		b, err := h.store.GetBookByID(id)
		bookCache[id] = b
		if err != nil {
			lookupErr[id] = err
		}
		return b, err
	}

	counts := map[string]int{}
	pinned := 0
	wrote := false
	seen := make(map[[2]string]bool, len(req.Pairs))
	results := make([]manualCandidateResult, 0, len(req.Pairs))

	for _, p := range req.Pairs {
		r := manualCandidateResult{
			BookA:  strings.TrimSpace(p.BookA),
			BookB:  strings.TrimSpace(p.BookB),
			Reason: strings.TrimSpace(p.Reason),
		}
		reject := func(code, msg string) {
			r.Outcome, r.Code, r.Error = manualOutcomeRejected, code, msg
		}
		switch {
		case r.BookA == "" || r.BookB == "":
			reject("missing_id", "book_a and book_b are both required")
		case r.BookA == r.BookB:
			reject("same_book", "book_a and book_b are the same book")
		default:
			key := [2]string{min(r.BookA, r.BookB), max(r.BookA, r.BookB)}
			if seen[key] {
				reject("duplicate_in_request", "this pair appears earlier in the same request")
				break
			}
			seen[key] = true
			h.checkManualPair(c.Request.Context(), &r, lookup)
		}
		if r.Outcome == "" {
			h.enqueueManualPair(c.Request.Context(), es, &r, dryRun)
		}
		if r.Pinned {
			pinned++
		}
		if !dryRun && r.Candidate != nil && (r.Outcome == string(database.ManualCandidateCreated) ||
			r.Outcome == string(database.ManualCandidateReopened) || r.Pinned) {
			wrote = true
		}
		counts[r.Outcome]++
		results = append(results, r)
	}

	if wrote && h.markDuplicatesFlaggedDirty != nil {
		h.markDuplicatesFlaggedDirty("manual_candidates")
	}

	body := gin.H{
		"dry_run": dryRun,
		"summary": gin.H{
			"requested":    len(req.Pairs),
			"created":      counts[string(database.ManualCandidateCreated)],
			"already_open": counts[string(database.ManualCandidateAlreadyOpen)],
			"reopened":     counts[string(database.ManualCandidateReopened)],
			"decided":      counts[string(database.ManualCandidateDecided)],
			"pinned":       pinned,
			"rejected":     counts[manualOutcomeRejected],
			"failed":       counts[manualOutcomeFailed],
		},
		"results": results,
	}
	if counts[manualOutcomeFailed] > 0 {
		// A partial write must not read as success. The per-pair results say
		// which pairs landed and which did not, so a retry can target the rest
		// (the endpoint is idempotent, so resending the whole batch is safe).
		httputil.RespondWithErrorFields(c, http.StatusInternalServerError,
			fmt.Sprintf("%d of %d pairs failed to enqueue", counts[manualOutcomeFailed], len(req.Pairs)),
			"manual_candidates_partial_failure", body)
		return
	}
	httputil.RespondWithOK(c, body)
}

// checkManualPair validates that both books exist, are live and are not
// already in the same version group, recording a rejection on r if not.
func (h *Handler) checkManualPair(ctx context.Context, r *manualCandidateResult, lookup func(string) (*database.Book, error)) {
	var books [2]*database.Book
	for i, id := range []string{r.BookA, r.BookB} {
		b, err := lookup(id)
		if err != nil {
			// A read error is not "not found": saying so would invite the
			// caller to treat a transient store failure as a bad ID.
			r.Outcome, r.Code = manualOutcomeRejected, "lookup_failed"
			r.Error = fmt.Sprintf("could not read book %s", id)
			logging.Warn(ctx, "dedup manual candidate: book lookup failed",
				"book_id", logger.SanitizeLogValue(id), "err", logging.SanitizeErr(err))
			return
		}
		if b == nil {
			r.Outcome, r.Code, r.Error = manualOutcomeRejected, "book_not_found", fmt.Sprintf("book %s does not exist", id)
			return
		}
		if b.IsSoftDeleted() {
			r.Outcome, r.Code, r.Error = manualOutcomeRejected, "book_deleted", fmt.Sprintf("book %s is deleted", id)
			return
		}
		books[i] = b
	}
	ga, gb := books[0].VersionGroupID, books[1].VersionGroupID
	if ga != nil && gb != nil && *ga != "" && *ga == *gb {
		r.Outcome, r.Code = manualOutcomeRejected, "same_version_group"
		r.Error = "the books are already versions of each other"
		r.VersionGroupID = *ga
	}
}

// enqueueManualPair plans (dry run) or writes the candidate for a validated
// pair and records the store's outcome on r.
func (h *Handler) enqueueManualPair(ctx context.Context, es *database.EmbeddingStore, r *manualCandidateResult, dryRun bool) {
	var (
		res *database.ManualCandidateResult
		err error
	)
	if dryRun {
		res, err = es.PlanManualCandidate("book", r.BookA, r.BookB, r.Reason)
	} else {
		res, err = es.EnqueueManualCandidate("book", r.BookA, r.BookB, r.Reason)
	}
	if err != nil {
		r.Outcome, r.Code, r.Error = manualOutcomeFailed, "store_error", "failed to enqueue the candidate"
		logging.Error(ctx, "dedup manual candidate: store write failed",
			"book_a", logger.SanitizeLogValue(r.BookA), "book_b", logger.SanitizeLogValue(r.BookB),
			"dry_run", dryRun, "err", logging.SanitizeErr(err))
		return
	}
	r.Outcome = string(res.Outcome)
	r.Pinned = res.Pinned
	cand := res.Candidate
	r.Candidate = &cand
}
