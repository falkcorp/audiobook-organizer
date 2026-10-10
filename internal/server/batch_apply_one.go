// file: internal/server/batch_apply_one.go
// version: 1.38.0
// guid: 4e91c082-77a3-4d16-b5f8-2c0a9e3d4671
// last-edited: 2026-10-10

package server

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/authority"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logging"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// cachedApplyService is the narrow slice of *metafetch.Service that applying one
// cached candidate needs. It exists so the per-book logic can be tested against
// fakes without standing up a Server — the four regression tests that used to
// drive this through the HTTP handler now drive applyCachedCandidateForBook
// directly, so the behaviour they pin is still the behaviour production runs.
//
// That equivalence is the whole point of the interface. Before this extraction
// the logic lived inline in the gin handler; moving it to a background op
// without extracting it would have left the tests exercising a code path
// production no longer used.
type cachedApplyService interface {
	GetCachedCandidates(bookID string) (*metafetch.MetadataCandidateCache, bool, error)
	// ValidateCachedIdentityForBook is the identity leg of the bulk-apply gate:
	// the cache row must have been fetched for the book's current title/author.
	// liveAuthors is database.LiveBookAuthorNames for the book.
	ValidateCachedIdentityForBook(entry *metafetch.MetadataCandidateCache, book *database.Book, liveAuthors []string) error
	// CachedQueryMatchesIdentity reports whether the row was fetched for
	// query (a transcribed stand-in title) and the book's current author:
	// the proof that lets the gate accept a transcription-found candidate
	// (cachedTranscribedSearch).
	CachedQueryMatchesIdentity(entry *metafetch.MetadataCandidateCache, book *database.Book, liveAuthors []string, query string) bool
	// ApplyMetadataCandidateWithOptions is ApplyMetadataCandidate; opts records
	// an owner-reviewed override of the certainty gate in the change history.
	ApplyMetadataCandidateWithOptions(id string, candidate metafetch.MetadataCandidate, fields []string, opts metafetch.ApplyOptions) (*metafetch.FetchMetadataResponse, error)
	// There is deliberately no cache-invalidation method here. The apply
	// used to delete the book's cached candidates after every write, the
	// candidate it had just applied included, so an auto-apply that did not
	// mark the book applied left it with neither a status nor a candidate.
	// The store drops the row itself when the apply changes the book's title
	// or author (database candidateSearchIdentityChanged); otherwise the row
	// still answers for the book and stays.
	// FinishApplyFileWork is the shared file-side sequel to an apply: cover
	// download, file I/O, and a tag write that happens exactly once.
	// checkpoint, when non-nil, is the caller's scan stand-down check, re-run
	// before each file-writing step; nil means the caller holds none.
	FinishApplyFileWork(ctx context.Context, id, pendingCoverURL string, fileIO, writeTags bool, checkpoint func() error) error
	// FinishApplyFileWorkTimed is FinishApplyFileWork recording its phases
	// into pt and logging the per-book "apply phase durations" line.
	FinishApplyFileWorkTimed(ctx context.Context, id, pendingCoverURL string, fileIO, writeTags bool, checkpoint func() error, pt *metafetch.ApplyPhaseTimings) error
	// RenamePreflight reports, before anything is written, that the write-back
	// rename following an apply of candidate is known to fail (wrapping
	// metafetch.ErrApplyFileWorkWouldFail). See applySkipFileWorkWouldFail.
	RenamePreflightWithOptions(id string, candidate metafetch.MetadataCandidate, fields []string, opts metafetch.ApplyOptions) error
}

// bookReader reads the book the gate judges the candidate against, its file
// rows and its author credits: the gate's runtime check compares the
// canonical runtime (database.LoadBookRuntime, the sum over the files), never
// Book.Duration, and its author checks compare the LIVE authors
// (database.LiveBookAuthorNames), never the Book.Author snapshot.
type bookReader interface {
	GetBookByID(id string) (*database.Book, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
	// GetSeriesByID feeds the owner-manual-only guard the book's series name
	// (bulkManualOnlyGuard).
	GetSeriesByID(id int) (*database.Series, error)
	// GetBookTagsDetailed feeds it the book's franchise tags.
	GetBookTagsDetailed(bookID string) ([]database.BookTag, error)
	database.BookAuthorReader
	// BookDirLister: metabatch.ResolveCandidateSearchQuery reads the other
	// rows in a book's folder (metabatch.SkipKindSiblingPart).
	database.BookDirLister
	// ImportPathReader: the resolver reads the import roots from this same
	// store, so an import root is never listed for sibling rows.
	metabatch.ImportPathReader
	// authority.Reader: the resolver refuses a title that is a known
	// person's name (the authority lists).
	authority.Reader
}

// bulkManualOnlyGuard builds the certainty gate's owner-manual-only input
// (applygate.ManualOnlyGuard) for a bulk planner: the store-backed half of
// the check, which the gate cannot do itself -- the book's series name, its
// author credits, its franchise tags and every book_file path
// (applygate.BulkManualOnlyGuard). A read failure goes in ReadErr, which the
// gate refuses as owner_manual_check_failed (hard, not overridable) -- never
// read as "not manual-only".
//
// rowApproval is a single review row the owner approved: one book, chosen by
// the owner, which is how the owner rule says these books ARE applied, so it
// gets no guard. Every other caller -- a review-page bulk button, a script,
// the preview, the batch-apply-candidates op -- is bulk.
//
// searchQuery is the query the candidate was found by (the op result's
// recorded query, or the cached path's current resolver answer), checked too:
// a stand-in the gate is not told about (a folder title) can still name the
// library.
func bulkManualOnlyGuard(books bookReader, book *database.Book, rowApproval bool, searchQuery string) applygate.ManualOnlyGuard {
	if rowApproval {
		return applygate.ManualOnlyGuard{}
	}
	return applygate.BulkManualOnlyGuard(applygate.ManualOnlyReaders{Files: books, Series: books, Authors: books, Tags: books}, book, searchQuery)
}

// applyOutcome is the result of applying one book's cached candidate.
type applyOutcome struct {
	Applied bool
	// Reason is set when Applied is false, using the batchSkip* vocabulary.
	Reason string
	Err    error
	// Gate is the certainty gate's verdict, set whenever the gate ran. When
	// Reason is applySkipGateBlocked, Gate.Reason says which leg refused.
	Gate *applygate.Verdict
	// OwnerReviewed is true when the gate refused but the owner's pinned
	// review overrode it (see applygate.OwnerReviewOverridable). Set on the
	// plan, so it is true whether or not a later step then refused the book.
	OwnerReviewed bool
	// OwnerReplace is true when the request asked a review-page bulk button
	// to replace existing values (metafetch.BulkApplyModeReplace) and this
	// book's review_bulk pin let it overwrite. Set only on an applied
	// outcome: a book that was not applied replaced nothing.
	OwnerReplace bool
	// WriteBackFailed is true when the metadata WAS applied to the database but
	// writing it into the audio files failed. Deliberately separate from
	// !Applied: the database change is real and durable, and reporting the book
	// as "not applied" would send someone re-applying work that succeeded.
	WriteBackFailed bool
	// HistoryFailed is true when an owner-reviewed apply WAS written but its
	// change history was not recorded (metafetch.ErrApplyHistoryIncomplete).
	// Applied stays true for the same reason as WriteBackFailed; the op counts
	// and logs it, because that history is the only record of the override.
	HistoryFailed bool
	// HistoryErr and WriteBackErr hold the error behind HistoryFailed and
	// WriteBackFailed respectively. Both can be set on one book (the history
	// write failed, then the file work failed too), so each flag keeps its own
	// error and Err joins them: before 2026-09-14 the write-back error
	// overwrote the history error in Err, and the owner lost the only signal
	// that the override had no record.
	HistoryErr   error
	WriteBackErr error
	// SkippedLocked lists the lock keys (database.UserLockableFields) the apply
	// left alone because the user has locked or overridden them. The apply
	// still counts as Applied -- every other field landed -- but an op summary
	// that said "applied" while a locked title was silently dropped would be
	// lying by omission, so the op counts and logs these.
	SkippedLocked []string
	// BookTitle / BookAuthor are the book as it was when the plan read it,
	// and Candidate the cached candidate the plan chose (nil when none was
	// reached). Carried only so the op log can say WHICH book got WHAT --
	// "book not applied" with a bare id was the whole trail before.
	BookTitle  string
	BookAuthor string
	Candidate  *metafetch.MetadataCandidate
}

// withPlan stamps the plan's book and candidate onto an outcome.
func (o applyOutcome) withPlan(plan cachedApplyPlan) applyOutcome {
	if plan.Book != nil {
		o.BookTitle = plan.Book.Title
		if plan.Book.Author != nil {
			o.BookAuthor = plan.Book.Author.Name
		}
	}
	o.Candidate = plan.Candidate
	return o
}

// Skip reason vocabulary, shared with the HTTP response shape in
// internal/server/handlers/metadata_cache.go.
const (
	applySkipNoCachedCandidates = "no_cached_candidates"
	applySkipDecodeFailed       = "decode_failed"
	applySkipApplyFailed        = "apply_failed"
	applySkipBookNotFound       = "book_not_found"
	// applySkipGateBlocked: the certainty gate (internal/applygate) refused the
	// candidate. The book is left untouched for manual review.
	applySkipGateBlocked = "gate_blocked"
	// applySkipFileWorkWouldFail: write-back was requested and the rename that
	// follows the apply is known to fail (metafetch.RenamePreflight). The apply
	// is refused so the database and the files never disagree; nothing was
	// written.
	applySkipFileWorkWouldFail = metafetch.ApplyRefusedReasonFileWorkWouldFail
	// applySkipStaleCandidate: the request pinned the candidate the owner was
	// looking at, and the top cached candidate is no longer that one (the cache
	// was refetched after the page loaded). Applying would put a record on the
	// book that nobody reviewed, so nothing is written.
	applySkipStaleCandidate = "stale_candidate"
	// applySkipMarkedNoMatch: the owner marked the book "no match" and nobody
	// picked this candidate, so an automatic apply leaves it alone. A
	// review-lane approval of the candidate (a hash-checked owner-review pin)
	// overrides the mark, as the single-book dialog does; the hashless owner
	// marker does not, since nobody was shown a candidate for that book. The
	// preview drops these books (excludedFromPreview).
	applySkipMarkedNoMatch = "marked_no_match"
	// applySkipAuthorsUnreadable: the book's live author credits (AuthorID
	// and the book_authors join) could not be read, so the certainty gate has
	// nothing true to judge the candidate's author against. Judging it as
	// authorless would LOOSEN the gate (no author to overwrite, so an unknown
	// runtime no longer blocks), so the book is refused and nothing written.
	applySkipAuthorsUnreadable = "authors_unreadable"
	// applySkipAlreadyApplied: the book's metadata has already been applied
	// (database.MetadataApplied: review status "matched" or
	// "audio_confirmed") and nobody approved this candidate for it as a single
	// review row. Nothing is written. Until 2026-10-05 an apply deleted the
	// book's cached candidates, so an applied book had nothing to apply and
	// fell out as no_cached_candidates; now the candidates stay, and without
	// this skip a bulk apply would apply every applied book again -- file
	// work, a tag write, an iTunes write-back and a history entry each time,
	// and in replace mode an overwrite of the owner's later edits. The preview
	// reports these rows as verdict "skipped", reason "already_applied".
	applySkipAlreadyApplied = "already_applied"
)

// cachedApplyPlan is the decision for one book, made BEFORE anything is
// written. The real apply and the dry-run preview both come from
// planCachedApply, so the preview reports exactly the choice the apply makes.
type cachedApplyPlan struct {
	Book      *database.Book
	Candidate *metafetch.MetadataCandidate
	// Reason is empty when the plan is "apply"; otherwise an applySkip* value.
	Reason string
	Err    error
	Gate   *applygate.Verdict
	// OwnerReviewed: the gate refused, the request's pin matched, and every
	// refusing leg is one an owner review overrides. Reason is then "".
	OwnerReviewed bool
	// ReviewApproved: the owner clicked Apply on this ONE row in the review
	// lane (a pin with origin "row" that matched the top cached candidate),
	// whether the gate then passed or refused it. It makes the apply
	// overwrite (owner ruling 2026-09-14, "any row I approve overwrites").
	// A review-page BULK button (origin "review_bulk") lifts the gate like a
	// row (owner ruling 2026-09-27) but overwrites only when the request asks
	// for replace (BulkReplace); otherwise bulk applies stay fill-only (owner
	// decision A3#3). It is kept apart from
	// OwnerReviewed on purpose: OwnerReviewed records a GATE OVERRIDE in the
	// change history, and a row the gate passed overrode nothing.
	ReviewApproved bool
	// BulkReplace: the owner switched the review page's bulk toggle to
	// "Replace existing" (owner ruling 2026-09-27) and this book carries a
	// review_bulk pin (hash-checked or the hashless marker), so the bulk
	// apply overwrites filled fields and records an owner replace. Set only
	// by withBulkMode, which only the cached apply calls: the dry-run preview
	// and planOpResultApply never see a mode, so they never set it.
	BulkReplace bool
	// UnseenBulk: BulkReplace on the hashless marker. Nobody was shown the
	// candidate, so the overwrite keeps the automatic-apply guards
	// (metafetch.ApplyOptions.UnseenCandidate).
	UnseenBulk bool
	// Pinnable: the plan came from a path that accepts an owner-review pin
	// (planCachedApply). The op-results path (planOpResultApply) takes none,
	// so its dry run must never claim an owner review would apply a book.
	Pinnable bool
}

// reviewOnly reports whether a pinless plan refused the book on legs a
// review-page approval lifts, on a path that accepts a pin: the book can land
// only as a review-page apply. Gate is nil on the early skip plans, so it is
// checked explicitly rather than trusted to Reason.
func (p cachedApplyPlan) reviewOnly() bool {
	return p.Pinnable && p.Reason == applySkipGateBlocked && p.Gate != nil && p.Gate.OwnerReviewOverridable()
}

// reviewOnlyUnseen is reviewOnly under the hashless owner marker's rule
// (applygate.Verdict.UnseenOwnerReviewOverridable): the book can land as a
// select-all apply, which sends the marker for a book the lane never loaded.
// planCachedApply lifts the gate for the marker by the same rule, so the
// preview and the apply agree.
func (p cachedApplyPlan) reviewOnlyUnseen() bool {
	return p.Pinnable && p.Reason == applySkipGateBlocked && p.Gate != nil && p.Gate.UnseenOwnerReviewOverridable()
}

// withBulkMode applies the request's bulk mode to a plan planCachedApply made
// for pin. Only replace changes anything, and only on a plan that will apply
// (Reason "") for a book whose pin is a review-page BULK pin: a row pin
// already overwrites, and a book without an owner-review pin (script, API,
// another origin) stays fill-only and fully gated whatever the mode. mode is
// the normalized value (metafetch.NormalizeBulkApplyMode).
func (p cachedApplyPlan) withBulkMode(pin *metafetch.CandidatePin, mode string) cachedApplyPlan {
	if mode != metafetch.BulkApplyModeReplace || p.Reason != "" || pin == nil || !pin.IsOwnerReview() || pin.IsRowReview() {
		return p
	}
	p.BulkReplace = true
	p.UnseenBulk = pin.IsUnseenOwnerReview()
	return p
}

// applyOptions is the ApplyOptions for plan, used by the apply, its rename
// preflight and the dry-run preview alike, so no two of them can disagree
// about a row.
//
// Overwriting is keyed on the pin ORIGIN, never on whether the gate was
// lifted: a single-row approval (ReviewApproved, origin "row", owner ruling
// 2026-09-14) overwrites filled descriptive fields, and so does a review-page
// bulk pin in replace mode (BulkReplace, below). Every other batch row is
// fill-only (owner decision A3#3): no pin, a script's pin, and a review-page
// bulk button's "review_bulk" pin or hashless marker in fill mode. A bulk
// button lifts the certainty gate like a row (owner ruling 2026-09-27) and
// that lift is recorded (OwnerReviewed + GateOverride), but it writes only
// into empty fields, unless the owner switched the bulk toggle to "Replace
// existing" (BulkReplace, owner ruling 2026-09-27): then it overwrites and
// records an owner replace (OwnerReplace).
//
// Preview (the pinless dry run, which knows no button): a gate-passed row
// previews fill-only, which is what a pinless (script or API) apply or a
// review-page bulk apply writes. A row only a review can land (reviewOnly)
// previews as its single-row approval would apply it: overwrite, with the
// override labels (unchanged since 2026-09-14). The same row applied from a
// review-page BULK button gets the same override labels but stays fill-only,
// so it fills where this preview showed an overwrite; and a gate-passed row
// the owner approves with the single-row Apply overwrites where this showed
// a fill.
func (p cachedApplyPlan) applyOptions() metafetch.ApplyOptions {
	overridden := p.OwnerReviewed || p.reviewOnly()
	opts := metafetch.ApplyOptions{FillOnly: !(p.ReviewApproved || p.BulkReplace || p.reviewOnly())}
	if p.BulkReplace {
		opts.OwnerReplace = true
		opts.UnseenCandidate = p.UnseenBulk
	}
	if overridden {
		// OwnerReviewed, not a non-empty summary, is what makes the apply
		// record the override and require its history: an empty
		// RefusingReasons must not turn a reviewed apply into an ordinary one.
		opts.OwnerReviewed = true
		summary := ""
		if p.Gate != nil {
			summary = p.Gate.OverrideSummary()
		}
		opts.GateOverride = cmp.Or(summary, applygate.ReasonOwnerReviewed)
	}
	return opts
}

// planCachedApply picks the top cached candidate and runs the certainty gate
// on it. It reads only. A refused top candidate does NOT fall through to the
// second one: choosing among candidates is what manual review is for.
// claims is the batch's buildClaimIndex result (nil = no batch context).
//
// pin is the candidate the owner was looking at when they clicked an apply
// button on the review page, nil for every other caller (scripts, API
// clients, the dry run). The lane shows the same top cached candidate this
// applies, so a pin that no longer matches means the cache was refetched
// since: stale_candidate, nothing written. A matching owner-review pin (a
// single row or a bulk button, owner ruling 2026-09-27) makes the apply
// owner-reviewed: the gate still runs and its verdict is reported, but a
// refusal from a certainty leg does not block
// (applygate.OwnerReviewOverridable lists which legs still do).
//
// The one pin that is not matched is the hashless owner marker
// (CandidatePin.IsUnseenOwnerReview): a bulk button applied a book the lane
// held no candidate hash for. There is nothing to check for staleness, so it
// is owner-reviewed on the top cached candidate as it stands, and it does not
// lift a "no match" mark. No pin, or a pin of any other origin, means the
// ordinary hard gate.
func planCachedApply(svc cachedApplyService, books bookReader, id string, claims *applygate.ClaimIndex, pin *metafetch.CandidatePin) cachedApplyPlan {
	entry, _, err := svc.GetCachedCandidates(id)
	if err != nil || entry == nil || len(entry.Candidates) == 0 {
		return cachedApplyPlan{Reason: applySkipNoCachedCandidates, Err: err}
	}
	var cand metafetch.MetadataCandidate
	if derr := json.Unmarshal(entry.Candidates[0], &cand); derr != nil {
		return cachedApplyPlan{Reason: applySkipDecodeFailed, Err: derr}
	}
	book, berr := books.GetBookByID(id)
	if berr != nil || book == nil {
		if berr == nil {
			berr = fmt.Errorf("book %s not found", id)
		}
		return cachedApplyPlan{Candidate: &cand, Reason: applySkipBookNotFound, Err: berr}
	}
	// An applied book is left alone unless the owner approved this candidate
	// on its single review row (a row pin, still checked for staleness
	// below): that click is the owner choosing to re-apply. A review-page bulk
	// button, its hashless marker, a script's pin and no pin all skip it.
	// Checked before the stale-pin test, so a bulk pin on an applied book
	// reports already_applied, not stale_candidate.
	if database.MetadataApplied(book.MetadataReviewStatus) && (pin == nil || !pin.IsRowReview()) {
		return cachedApplyPlan{Book: book, Candidate: &cand, Reason: applySkipAlreadyApplied,
			Err: fmt.Errorf("book %s: metadata already applied (review status %q)", id, *book.MetadataReviewStatus)}
	}
	unseen := pin != nil && pin.IsUnseenOwnerReview()
	if pin != nil && !unseen && !pin.Matches(cand) {
		return cachedApplyPlan{Book: book, Candidate: &cand, Reason: applySkipStaleCandidate,
			Err: fmt.Errorf("reviewed candidate %q (%s) is no longer the top cached candidate %q (%s)", pin.Title, pin.Source, cand.Title, cand.Source)}
	}
	// A no-match book is left alone unless the owner approved this candidate
	// in the review lane, which overrides their own mark (the apply then
	// records the match, replacing no_match, like the single-book dialog).
	// The hashless marker does not: nobody was shown this book's candidate.
	if metafetch.IsMarkedNoMatch(book.MetadataReviewStatus) && (pin == nil || !pin.IsOwnerReview() || unseen) {
		return cachedApplyPlan{Book: book, Candidate: &cand, Reason: applySkipMarkedNoMatch,
			Err: fmt.Errorf("book %s: %w", id, metafetch.ErrMarkedNoMatch)}
	}
	authors, aerr := database.LiveBookAuthorNames(books, book)
	if aerr != nil {
		return cachedApplyPlan{Book: book, Candidate: &cand, Reason: applySkipAuthorsUnreadable, Err: aerr}
	}
	idErr := svc.ValidateCachedIdentityForBook(entry, book, authors)
	ts := cachedTranscribedSearch(svc, books, entry, book, authors, idErr)
	// The row is marked stale (the book was retitled or re-credited after the
	// fetch), or the book's ASIN was replaced or cleared after this row was
	// fetched and the candidate does not carry the new one
	// (metafetch.CandidateIdentityStale).
	// A candidate naming another ASIN is refused as asin_conflict anyway; this
	// catches the one that names none (Open Library, Google Books), which the
	// ASIN check passes. It joins the identity leg as identity_stale, and no
	// transcription lifts it: a transcription can explain a stale query, not a
	// book now identified by another record.
	if idStaleErr := metafetch.CandidateIdentityStale(entry, book, &cand); idStaleErr != nil {
		ts.ExplainsStaleIdentity = false
		if idErr == nil {
			idErr = idStaleErr
		} else {
			idErr = fmt.Errorf("%w; %w", idStaleErr, idErr)
		}
	}
	// The owner-manual-only bypass keys on the PIN, not on the request size.
	// Each row pin is an explicit owner approval of this one book's candidate:
	// the owner clicked Apply on that row, which is how the owner rule says
	// Doctor Who / Big Finish books are applied. It deliberately does NOT
	// require len(book_ids)==1: the review page batches per-row Apply clicks
	// made inside its 500ms debounce window into one request
	// (web/src/components/review/lanes/useMetadataLane.ts), so several row
	// approvals arrive together. A bulk button's pin (origin "review_bulk") and the
	// hashless marker are not row pins and get the guard.
	v := applygate.EvaluateTranscribed(book, authors, gateRuntime(books, book), &cand, idErr, claims, ts,
		bulkManualOnlyGuard(books, book, pin != nil && pin.IsRowReview(), metabatch.ResolveCandidateSearchQuery(books, book).Title))
	// Any owner-review pin (row, bulk, or the hashless marker) lifts the
	// certainty gate -- the marker short of review_only_source (below). A single-row pin is an approval that overwrites
	// (ReviewApproved); a bulk button stays fill-only unless the request asks
	// for replace (withBulkMode, applied by the caller). A stale pin never gets
	// here (stale_candidate above, nothing written).
	ownerReview := pin != nil && pin.IsOwnerReview()
	plan := cachedApplyPlan{Book: book, Candidate: &cand, Gate: &v, Pinnable: true, ReviewApproved: pin != nil && pin.IsRowReview()}
	if !v.Allowed {
		// Only an owner-review pin earns the override. A pin of any other
		// origin was still checked for staleness above, and gets the hard gate.
		// The hashless marker lifts everything a pin does except
		// review_only_source: nobody was shown this candidate, and a
		// review-only one is applied only by an owner who saw it
		// (applygate.Verdict.UnseenOwnerReviewOverridable).
		overridable := v.OwnerReviewOverridable()
		if unseen {
			overridable = v.UnseenOwnerReviewOverridable()
		}
		if ownerReview && overridable {
			plan.OwnerReviewed = true
			return plan
		}
		plan.Reason = applySkipGateBlocked
		plan.Err = fmt.Errorf("%s: %s", v.Reason, v.Detail)
	}
	return plan
}

// planOpResultApply is planCachedApply for /metadata/batch-apply-candidates,
// whose candidate comes from a candidate-fetch OperationResult rather than the
// cache, so ValidateCachedIdentity has no row to check. Its identity leg is
// the equivalent staleness check the transcription path uses: the book's
// title and author NOW must still be the ones the candidate was fetched for
// (CandidateResult.Book, recorded at fetch time). A rename or re-author since
// the fetch means the candidate answers a question the book no longer asks.
func planOpResultApply(books bookReader, id string, cr CandidateResult, claims *applygate.ClaimIndex) cachedApplyPlan {
	if cr.Candidate == nil {
		return cachedApplyPlan{Reason: applySkipNoCachedCandidates}
	}
	cand := *cr.Candidate
	book, berr := books.GetBookByID(id)
	if berr != nil || book == nil {
		if berr == nil {
			berr = fmt.Errorf("book %s not found", id)
		}
		return cachedApplyPlan{Candidate: &cand, Reason: applySkipBookNotFound, Err: berr}
	}
	// Nobody picks candidates on this path, so a no-match book is always left.
	if metafetch.IsMarkedNoMatch(book.MetadataReviewStatus) {
		return cachedApplyPlan{Book: book, Candidate: &cand, Reason: applySkipMarkedNoMatch,
			Err: fmt.Errorf("book %s: %w", id, metafetch.ErrMarkedNoMatch)}
	}
	authors, aerr := database.LiveBookAuthorNames(books, book)
	if aerr != nil {
		return cachedApplyPlan{Book: book, Candidate: &cand, Reason: applySkipAuthorsUnreadable, Err: aerr}
	}
	idErr := fetchTimeIdentity(cr.Book.Title, cr.Book.Author, cr.SearchQuery, book, authors)
	ts := opResultTranscribedSearch(books, book, cr)
	if idErr == nil && searchedByStandIn(cr) {
		// The book was searched by a stand-in, not its own title, so an
		// unchanged title proves nothing about the candidate: the question the
		// fetch asked was the stand-in. This is the op path's equivalent of the
		// cached path's hash mismatch. Only a transcription the book still
		// resolves to may explain it, and the gate lifts it only when the
		// candidate's title matches that transcription; a folder-name search
		// stays identity_stale.
		idErr = fmt.Errorf("%w: book %s was searched by its %s %q, not its title",
			metafetch.ErrStaleMetadataCache, book.ID, cr.SearchQuerySource, cr.SearchQuery)
		ts.ExplainsStaleIdentity = ts.Query != ""
	}
	// Nobody picks candidates on this path: it is always bulk.
	v := applygate.EvaluateTranscribed(book, authors, gateRuntime(books, book), &cand, idErr, claims, ts,
		bulkManualOnlyGuard(books, book, false, cr.SearchQuery))
	plan := cachedApplyPlan{Book: book, Candidate: &cand, Gate: &v}
	if !v.Allowed {
		plan.Reason = applySkipGateBlocked
		plan.Err = fmt.Errorf("%s: %s", v.Reason, v.Detail)
	}
	return plan
}

// cachedTranscribedSearch tells the gate whether the cached candidate was
// found by searching the book's transcribed title, with the proof
// EvaluateTranscribed needs to lift identity_stale. It is consulted only when
// the identity check failed as stale (a row matching the book's own title
// was searched by that title, so there is nothing to explain).
//
// The proof is two facts, both about the book as it is NOW:
//   - the batch fetch would search it by a transcribed title today
//     (metabatch.ResolveCandidateSearchQuery: its stored title is still
//     unsearchable and that transcription is still the first usable one);
//   - the cache row was written for exactly that query and a current author
//     (metafetch.Service.CachedQueryMatchesIdentity).
//
// A row fetched for another query, another author, or before the book got a
// real title fails one of them, and the refusal stays identity_stale.
//
// The search also carries the book's first present file path
// (transcribedFirstFile), from which the gate reads the work folder that can
// contradict the transcription.
func cachedTranscribedSearch(svc cachedApplyService, books bookReader, entry *metafetch.MetadataCandidateCache, book *database.Book, authors []string, idErr error) applygate.TranscribedSearch {
	if idErr == nil || !errors.Is(idErr, metafetch.ErrStaleMetadataCache) {
		return applygate.TranscribedSearch{}
	}
	cur := metabatch.ResolveCandidateSearchQuery(books, book)
	if !cur.Usable || !metabatch.IsTranscribedSource(cur.Source) {
		return applygate.TranscribedSearch{}
	}
	if !svc.CachedQueryMatchesIdentity(entry, book, authors, cur.Title) {
		return applygate.TranscribedSearch{}
	}
	return applygate.TranscribedSearch{Query: cur.Title, Author: cur.Author, Source: cur.Source, ExplainsStaleIdentity: true,
		FirstFilePath: transcribedFirstFile(books, book)}
}

// transcribedFirstFile is the book's first present file path for
// applygate.TranscribedSearch.FirstFilePath (metabatch.FirstPresentFilePath).
// A read failure answers "": the gate then reads the work folder from the
// book's own FilePath (metadata.WorkFolderTitle, which handles a directory),
// so the folder check still runs.
func transcribedFirstFile(books bookReader, book *database.Book) string {
	p, err := metabatch.FirstPresentFilePath(books, book.ID)
	if err != nil {
		return ""
	}
	return p
}

// searchedByStandIn reports whether an op-result candidate's fetch searched a
// stand-in for the book's title (a transcription or the folder name,
// metabatch.ResolveCandidateSearchQuery) rather than the title itself.
func searchedByStandIn(cr CandidateResult) bool {
	return cr.SearchQuerySource != "" && cr.SearchQuerySource != metabatch.SearchQuerySourceTitle
}

// opResultTranscribedSearch is cachedTranscribedSearch for an op-result
// candidate, whose fetch recorded the query it searched
// (CandidateResult.SearchQuery). It names the transcribed query only when the
// book would still be searched by exactly it. It does not set
// ExplainsStaleIdentity: planOpResultApply does, and only when the stand-in
// search is the sole reason the identity is stale.
func opResultTranscribedSearch(books bookReader, book *database.Book, cr CandidateResult) applygate.TranscribedSearch {
	if cr.SearchQuery == "" || !metabatch.IsTranscribedSource(cr.SearchQuerySource) {
		return applygate.TranscribedSearch{}
	}
	cur := metabatch.ResolveCandidateSearchQuery(books, book)
	if !cur.Usable || cur.Title != cr.SearchQuery || !metabatch.IsTranscribedSource(cur.Source) {
		return applygate.TranscribedSearch{}
	}
	return applygate.TranscribedSearch{Query: cur.Title, Author: cur.Author, Source: cur.Source,
		FirstFilePath: transcribedFirstFile(books, book)}
}

// excludedFromPreview reports whether the bulk-apply preview leaves this book
// out entirely (no row, not counted): the owner rejected every match for it.
func excludedFromPreview(plan cachedApplyPlan) bool {
	return plan.Reason == applySkipMarkedNoMatch
}

// fetchTimeIdentity fails closed when the book's current title or author is
// not the one recorded when its candidates were fetched.
//
// The fetch recorded CandidateBookInfo.Author, which is the Book.Author
// snapshot (metabatch.BuildCandidateBookInfo), not the live author. So the
// recorded author is accepted when it is the book's author in any form a
// fetch could have recorded: the snapshot (what the batch fetch records
// today, so no row that passed before fails now), the live primary author, or
// every live author joined as the API's author_name joins them. Anything else
// was fetched for an author the book no longer has.
//
// searchQuery is the query the fetch recorded (CandidateResult.SearchQuery).
// A row with no title AND no query is a legacy row that recorded nothing and
// fails closed; a row that recorded its query but no title is a book whose
// stored title was blank when fetched (it was searched by a stand-in), and it
// is judged like any other: is the title still what it was. Passing here is
// not enough for a stand-in search: planOpResultApply then fails it as stale
// unless a matching transcription explains it (searchedByStandIn).
func fetchTimeIdentity(fetchedTitle, fetchedAuthor, searchQuery string, book *database.Book, liveAuthors []string) error {
	if strings.TrimSpace(fetchedTitle) == "" && strings.TrimSpace(searchQuery) == "" {
		return fmt.Errorf("%w: fetch result for book %s recorded no title", metafetch.ErrStaleMetadataCache, book.ID)
	}
	if util.NormalizeTitle(fetchedTitle) != util.NormalizeTitle(book.Title) {
		return fmt.Errorf("%w: book %s title changed since the fetch (%q -> %q)", metafetch.ErrStaleMetadataCache, book.ID, fetchedTitle, book.Title)
	}
	forms := metafetch.CurrentAuthorForms(book, liveAuthors)
	for _, cur := range forms {
		if util.NormalizeAuthor(fetchedAuthor) == util.NormalizeAuthor(cur) {
			return nil
		}
	}
	return fmt.Errorf("%w: book %s author changed since the fetch (%q -> %q)", metafetch.ErrStaleMetadataCache, book.ID, fetchedAuthor, strings.Join(forms, " | "))
}

// applyCachedCandidateForBook applies the highest-scored cached candidate for
// one book — only if the certainty gate allows it — and, when writeBack is
// true, writes the result into the audio files.
//
// FILE I/O — KEEP IN STEP WITH THE SINGLE-BOOK PATH. The sibling is
// applyAudiobookMetadataImpl in internal/server/handlers/metadata/handler.go.
// The two drifted apart once: the sibling wrote tags and embedded cover art
// while this path only updated the database and enqueued the iTunes batcher, so
// applied metadata never reached the files and nothing logged a failure. If you
// add file-side work to either path, add it to both.
//
// The caller supplies concurrency; this function does the work for exactly one
// book and never spawns goroutines. It takes no path lock, and the caller must
// not hold one around it: FinishApplyFileWork locks each write itself, on the
// path it is about to write (the library copy's for a protected book, the
// post-rename path for the tags), and the lock is not reentrant. Until
// 2026-09-12 this re-read the book and locked its path around the whole
// sequel, which for a protected book was not the path the sequel wrote, and
// which held one key across the rename.
//
// checkpoint is the op's scan stand-down check (nil in tests); the file-side
// sequel re-runs it before each file-writing step.
func applyCachedCandidateForBook(
	svc cachedApplyService,
	books bookReader,
	id string,
	writeBack bool,
	checkpoint func() error,
) applyOutcome {
	return applyCachedCandidateForBookTimed(context.Background(), svc, books, id, writeBack, checkpoint, metafetch.NewApplyPhaseTimings(), nil, nil, "")
}

// applyCachedCandidateForBookTimed is applyCachedCandidateForBook recording
// its phases into pt (which the caller may already have started, e.g. with the
// write-back gate wait). The file-side sequel logs the one per-book
// "apply phase durations" line; a book that is not applied logs none.
//
// mode is the request's normalized bulk mode (metafetch.NormalizeBulkApplyMode;
// "" is fill). It reaches the plan through withBulkMode only, so the dry-run
// preview, which calls planCachedApply directly, can never honour it.
func applyCachedCandidateForBookTimed(
	ctx context.Context,
	svc cachedApplyService,
	books bookReader,
	id string,
	writeBack bool,
	checkpoint func() error,
	pt *metafetch.ApplyPhaseTimings,
	claims *applygate.ClaimIndex,
	pin *metafetch.CandidatePin,
	mode string,
) applyOutcome {
	applyStart := time.Now()
	plan := planCachedApply(svc, books, id, claims, pin).withBulkMode(pin, mode)
	if plan.Reason != "" {
		return applyOutcome{Reason: plan.Reason, Err: plan.Err, Gate: plan.Gate, OwnerReviewed: plan.OwnerReviewed}.withPlan(plan)
	}

	// The apply below writes the database first and the files after. On
	// 2026-09-13 three books got their new metadata and then failed the rename
	// ("link <tmp> <dest>: file already exists"), leaving the database and the
	// disk disagreeing. When the file side is known to fail, refuse here,
	// before any write. Only with writeBack: without it there is no rename.
	// An owner review does not lift this: it is not a certainty judgement.
	//
	// The preflight plans the apply's own options: a fill-only and an
	// overwriting apply can rename to different targets.
	opts := plan.applyOptions()
	if writeBack {
		if err := svc.RenamePreflightWithOptions(id, *plan.Candidate, nil, opts); err != nil {
			return applyOutcome{Reason: applySkipFileWorkWouldFail, Err: err, Gate: plan.Gate, OwnerReviewed: plan.OwnerReviewed}.withPlan(plan)
		}
	}

	// fields nil: every field the candidate provides, minus the user's locks.
	// An unreviewed row is fill-only: it never overwrites a filled descriptive
	// field (owner decision A3#3). An owner-reviewed row was hand-picked in the
	// review lane, so it may overwrite, like the single-book apply (owner
	// decision 2026-09-14). opts is the value the preflight above planned.
	resp, aerr := svc.ApplyMetadataCandidateWithOptions(id, *plan.Candidate, nil, opts)
	// A response with ErrApplyHistoryIncomplete means the write stands and
	// only its history is missing: finish the apply (so the database and the
	// files agree) and report the history failure, rather than calling a
	// written book unapplied.
	historyErr := error(nil)
	if aerr != nil && resp != nil && errors.Is(aerr, metafetch.ErrApplyHistoryIncomplete) {
		historyErr, aerr = aerr, nil
	}
	if aerr != nil && errors.Is(aerr, metafetch.ErrMarkedNoMatch) {
		// Marked "no match" between the plan and the apply: nothing written,
		// reported as the skip it is rather than a failure.
		return applyOutcome{Reason: applySkipMarkedNoMatch, Err: aerr, Gate: plan.Gate}.withPlan(plan)
	}
	if aerr != nil {
		return applyOutcome{Reason: applySkipApplyFailed, Err: aerr, Gate: plan.Gate, OwnerReviewed: plan.OwnerReviewed}.withPlan(plan)
	}
	pt.Since(metafetch.PhaseApplyDB, applyStart)

	// Every later return is an applied outcome; carry the skipped locks on all
	// of them. resp is non-nil on a nil error (ApplyMetadataCandidate's
	// contract), but the mocks in this package's tests return (nil, nil), and a
	// nil deref here would turn "no response" into a crashed op.
	out := applyOutcome{Applied: true, Gate: plan.Gate, OwnerReviewed: plan.OwnerReviewed, OwnerReplace: plan.BulkReplace}.withPlan(plan)
	// Err is always errors.Join(HistoryErr, WriteBackErr): a later failure
	// must never replace an earlier one, and errors.Is still finds either.
	failWriteBack := func(err error) {
		out.WriteBackFailed, out.WriteBackErr = true, err
		out.Err = errors.Join(out.HistoryErr, out.WriteBackErr)
	}
	if historyErr != nil {
		out.HistoryFailed, out.HistoryErr, out.Err = true, historyErr, historyErr
	}
	pendingCover := ""
	if resp != nil {
		out.SkippedLocked = resp.SkippedLockedFields
		pendingCover = resp.PendingCoverURL
	}

	if !writeBack {
		// No file I/O and no tags were asked for, but the new cover is still
		// downloaded: ApplyMetadataCandidate kept the previous cover_url until
		// the image is on disk, and until 2026-09-12 nothing on this path ever
		// fetched it, so a batch-applied book kept its old cover forever.
		if err := svc.FinishApplyFileWorkTimed(ctx, id, pendingCover, false, false, checkpoint, pt); err != nil {
			failWriteBack(err)
		}
		return out
	}

	// Cover download, then the file I/O (the rename lives in there), then the
	// tags -- once. This used to call ApplyMetadataFileIO and then
	// WriteBackMetadataForBook, and with auto_write_tags_on_apply on the first
	// already wrote the tags, so every file was tagged twice.
	//
	// Applied stays true on a failure -- the database change is real and
	// durable -- but the file side is flagged, which is exactly why
	// WriteBackFailed is separate from !Applied. The core still writes tags
	// after a rename failure and reports the rename error first, because
	// "rename failed" localises the fault better than what it causes.
	if err := svc.FinishApplyFileWorkTimed(ctx, id, pendingCover, true, true, checkpoint, pt); err != nil {
		failWriteBack(err)
	}
	return out
}

// gateRuntime is the book's canonical runtime for the certainty gate. A failed
// file read is logged and yields an UNKNOWN runtime (the gate then treats the
// runtime as missing evidence), never Book.Duration promoted to a total.
func gateRuntime(books bookReader, book *database.Book) database.BookRuntime {
	rt, err := database.LoadBookRuntime(books, book)
	if err != nil {
		logging.Warn(context.Background(), "apply gate: book files unreadable; runtime treated as unknown",
			"book_id", book.ID, "err", err)
	}
	return rt
}
