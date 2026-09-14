// file: internal/server/batch_apply_one.go
// version: 1.15.0
// guid: 4e91c082-77a3-4d16-b5f8-2c0a9e3d4671
// last-edited: 2026-09-13

package server

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/database"
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
	ValidateCachedIdentityForBook(entry *metafetch.MetadataCandidateCache, book *database.Book) error
	// ApplyMetadataCandidateWithOptions is ApplyMetadataCandidate; opts records
	// an owner-reviewed override of the certainty gate in the change history.
	ApplyMetadataCandidateWithOptions(id string, candidate metafetch.MetadataCandidate, fields []string, opts metafetch.ApplyOptions) (*metafetch.FetchMetadataResponse, error)
	InvalidateCachedCandidates(bookID string) error
	// FinishApplyFileWork is the shared file-side sequel to an apply: cover
	// download, file I/O, and a tag write that happens exactly once.
	// checkpoint, when non-nil, is the caller's scan stand-down check, re-run
	// before each file-writing step; nil means the caller holds none.
	FinishApplyFileWork(id, pendingCoverURL string, fileIO, writeTags bool, checkpoint func() error) error
	// FinishApplyFileWorkTimed is FinishApplyFileWork recording its phases
	// into pt and logging the per-book "apply phase durations" line.
	FinishApplyFileWorkTimed(id, pendingCoverURL string, fileIO, writeTags bool, checkpoint func() error, pt *metafetch.ApplyPhaseTimings) error
	// RenamePreflight reports, before anything is written, that the write-back
	// rename following an apply of candidate is known to fail (wrapping
	// metafetch.ErrApplyFileWorkWouldFail). See applySkipFileWorkWouldFail.
	RenamePreflight(id string, candidate metafetch.MetadataCandidate, fields []string) error
}

// bookReader reads the book the gate judges the candidate against.
type bookReader interface {
	GetBookByID(id string) (*database.Book, error)
}

// itunesEnqueuer mirrors handlers.WriteBackEnqueuer: the iTunes library sync
// batcher, which does NOT touch audio tags. Named explicitly here because
// confusing it for the tag writer is exactly the defect this path once had —
// metadata landed in the database, the iTunes batcher was enqueued, and no audio
// file was ever written.
type itunesEnqueuer interface {
	Enqueue(bookID string)
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
	// SkippedLocked lists the lock keys (database.UserLockableFields) the apply
	// left alone because the user has locked or overridden them. The apply
	// still counts as Applied -- every other field landed -- but an op summary
	// that said "applied" while a locked title was silently dropped would be
	// lying by omission, so the op counts and logs these.
	SkippedLocked []string
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
	// Pinnable: the plan came from a path that accepts an owner-review pin
	// (planCachedApply). The op-results path (planOpResultApply) takes none,
	// so its dry run must never claim an owner review would apply a book.
	Pinnable bool
}

// planCachedApply picks the top cached candidate and runs the certainty gate
// on it. It reads only. A refused top candidate does NOT fall through to the
// second one: choosing among candidates is what manual review is for.
// claims is the batch's buildClaimIndex result (nil = no batch context).
//
// pin is the candidate the owner was looking at when they clicked Apply in the
// review lane, nil for every other caller (scripts, API clients, the dry run).
// The lane shows the same top cached candidate this applies, so a pin that no
// longer matches means the cache was refetched since: stale_candidate, nothing
// written. A matching pin makes the apply owner-reviewed: the gate still runs
// and its verdict is reported, but a refusal from a certainty leg does not
// block (applygate.OwnerReviewOverridable lists which legs still do). No pin
// means the ordinary hard gate.
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
	if pin != nil && !pin.Matches(cand) {
		return cachedApplyPlan{Book: book, Candidate: &cand, Reason: applySkipStaleCandidate,
			Err: fmt.Errorf("reviewed candidate %q (%s) is no longer the top cached candidate %q (%s)", pin.Title, pin.Source, cand.Title, cand.Source)}
	}
	v := applygate.EvaluateInBatch(book, &cand, svc.ValidateCachedIdentityForBook(entry, book), claims)
	plan := cachedApplyPlan{Book: book, Candidate: &cand, Gate: &v, Pinnable: true}
	if !v.Allowed {
		// Only a single-row review earns the override. A pin of any other
		// origin was still checked for staleness above, and gets the hard gate.
		if pin != nil && pin.IsRowReview() && v.OwnerReviewOverridable() {
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
	v := applygate.EvaluateInBatch(book, &cand, fetchTimeIdentity(cr.Book.Title, cr.Book.Author, book), claims)
	plan := cachedApplyPlan{Book: book, Candidate: &cand, Gate: &v}
	if !v.Allowed {
		plan.Reason = applySkipGateBlocked
		plan.Err = fmt.Errorf("%s: %s", v.Reason, v.Detail)
	}
	return plan
}

// fetchTimeIdentity fails closed when the book's current title or author is
// not the one recorded when its candidates were fetched.
func fetchTimeIdentity(fetchedTitle, fetchedAuthor string, book *database.Book) error {
	if strings.TrimSpace(fetchedTitle) == "" {
		return fmt.Errorf("%w: fetch result for book %s recorded no title", metafetch.ErrStaleMetadataCache, book.ID)
	}
	if util.NormalizeTitle(fetchedTitle) != util.NormalizeTitle(book.Title) {
		return fmt.Errorf("%w: book %s title changed since the fetch (%q -> %q)", metafetch.ErrStaleMetadataCache, book.ID, fetchedTitle, book.Title)
	}
	curAuthor := ""
	if book.Author != nil {
		curAuthor = book.Author.Name
	}
	if util.NormalizeAuthor(fetchedAuthor) != util.NormalizeAuthor(curAuthor) {
		return fmt.Errorf("%w: book %s author changed since the fetch (%q -> %q)", metafetch.ErrStaleMetadataCache, book.ID, fetchedAuthor, curAuthor)
	}
	return nil
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
	itunes itunesEnqueuer,
	id string,
	writeBack bool,
	checkpoint func() error,
) applyOutcome {
	return applyCachedCandidateForBookTimed(svc, books, itunes, id, writeBack, checkpoint, metafetch.NewApplyPhaseTimings(), nil, nil)
}

// applyCachedCandidateForBookTimed is applyCachedCandidateForBook recording
// its phases into pt (which the caller may already have started, e.g. with the
// write-back gate wait). The file-side sequel logs the one per-book
// "apply phase durations" line; a book that is not applied logs none.
func applyCachedCandidateForBookTimed(
	svc cachedApplyService,
	books bookReader,
	itunes itunesEnqueuer,
	id string,
	writeBack bool,
	checkpoint func() error,
	pt *metafetch.ApplyPhaseTimings,
	claims *applygate.ClaimIndex,
	pin *metafetch.CandidatePin,
) applyOutcome {
	applyStart := time.Now()
	plan := planCachedApply(svc, books, id, claims, pin)
	if plan.Reason != "" {
		return applyOutcome{Reason: plan.Reason, Err: plan.Err, Gate: plan.Gate, OwnerReviewed: plan.OwnerReviewed}
	}

	// The apply below writes the database first and the files after. On
	// 2026-09-13 three books got their new metadata and then failed the rename
	// ("link <tmp> <dest>: file already exists"), leaving the database and the
	// disk disagreeing. When the file side is known to fail, refuse here,
	// before any write. Only with writeBack: without it there is no rename.
	// An owner review does not lift this: it is not a certainty judgement.
	if writeBack {
		if err := svc.RenamePreflight(id, *plan.Candidate, nil); err != nil {
			return applyOutcome{Reason: applySkipFileWorkWouldFail, Err: err, Gate: plan.Gate, OwnerReviewed: plan.OwnerReviewed}
		}
	}

	// Field policy is identical for a reviewed and an unreviewed apply (fields
	// nil: every field the candidate provides, minus the user's locks). The
	// options only record the override in the change history.
	var opts metafetch.ApplyOptions
	if plan.OwnerReviewed {
		// OwnerReviewed, not a non-empty summary, is what makes the apply
		// record the override and require its history: an empty
		// RefusingReasons must not turn a reviewed apply into an ordinary one.
		opts.OwnerReviewed = true
		opts.GateOverride = cmp.Or(plan.Gate.OverrideSummary(), applygate.ReasonOwnerReviewed)
	}
	resp, aerr := svc.ApplyMetadataCandidateWithOptions(id, *plan.Candidate, nil, opts)
	// A response with ErrApplyHistoryIncomplete means the write stands and
	// only its history is missing: finish the apply (so the database and the
	// files agree) and report the history failure, rather than calling a
	// written book unapplied.
	historyErr := error(nil)
	if aerr != nil && resp != nil && errors.Is(aerr, metafetch.ErrApplyHistoryIncomplete) {
		historyErr, aerr = aerr, nil
	}
	if aerr != nil {
		return applyOutcome{Reason: applySkipApplyFailed, Err: aerr, Gate: plan.Gate, OwnerReviewed: plan.OwnerReviewed}
	}
	_ = svc.InvalidateCachedCandidates(id)
	pt.Since(metafetch.PhaseApplyDB, applyStart)

	// Every later return is an applied outcome; carry the skipped locks on all
	// of them. resp is non-nil on a nil error (ApplyMetadataCandidate's
	// contract), but the mocks in this package's tests return (nil, nil), and a
	// nil deref here would turn "no response" into a crashed op.
	out := applyOutcome{Applied: true, Gate: plan.Gate, OwnerReviewed: plan.OwnerReviewed}
	if historyErr != nil {
		out.HistoryFailed, out.Err = true, historyErr
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
		if err := svc.FinishApplyFileWorkTimed(id, pendingCover, false, false, checkpoint, pt); err != nil {
			out.WriteBackFailed, out.Err = true, err
		}
		return out
	}

	// iTunes library sync is enqueued BEFORE the file work (matching the
	// single-book path) so a failure writing tags cannot lose it.
	if itunes != nil {
		itunes.Enqueue(id)
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
	if err := svc.FinishApplyFileWorkTimed(id, pendingCover, true, true, checkpoint, pt); err != nil {
		out.WriteBackFailed, out.Err = true, err
	}
	return out
}
