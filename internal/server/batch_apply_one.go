// file: internal/server/batch_apply_one.go
// version: 1.7.0
// guid: 4e91c082-77a3-4d16-b5f8-2c0a9e3d4671
// last-edited: 2026-09-13

package server

import (
	"encoding/json"
	"fmt"
	"strings"

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
	ApplyMetadataCandidate(id string, candidate metafetch.MetadataCandidate, fields []string) (*metafetch.FetchMetadataResponse, error)
	InvalidateCachedCandidates(bookID string) error
	// FinishApplyFileWork is the shared file-side sequel to an apply: cover
	// download, file I/O, and a tag write that happens exactly once.
	// checkpoint, when non-nil, is the caller's scan stand-down check, re-run
	// before each file-writing step; nil means the caller holds none.
	FinishApplyFileWork(id, pendingCoverURL string, fileIO, writeTags bool, checkpoint func() error) error
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
	// WriteBackFailed is true when the metadata WAS applied to the database but
	// writing it into the audio files failed. Deliberately separate from
	// !Applied: the database change is real and durable, and reporting the book
	// as "not applied" would send someone re-applying work that succeeded.
	WriteBackFailed bool
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
}

// planCachedApply picks the top cached candidate and runs the certainty gate
// on it. It reads only. A refused top candidate does NOT fall through to the
// second one: choosing among candidates is what manual review is for.
func planCachedApply(svc cachedApplyService, books bookReader, id string) cachedApplyPlan {
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
	v := applygate.Evaluate(book, &cand, svc.ValidateCachedIdentityForBook(entry, book))
	plan := cachedApplyPlan{Book: book, Candidate: &cand, Gate: &v}
	if !v.Allowed {
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
func planOpResultApply(books bookReader, id string, cr CandidateResult) cachedApplyPlan {
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
	v := applygate.Evaluate(book, &cand, fetchTimeIdentity(cr.Book.Title, cr.Book.Author, book))
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
	plan := planCachedApply(svc, books, id)
	if plan.Reason != "" {
		return applyOutcome{Reason: plan.Reason, Err: plan.Err, Gate: plan.Gate}
	}

	resp, aerr := svc.ApplyMetadataCandidate(id, *plan.Candidate, nil)
	if aerr != nil {
		return applyOutcome{Reason: applySkipApplyFailed, Err: aerr, Gate: plan.Gate}
	}
	_ = svc.InvalidateCachedCandidates(id)

	// Every later return is an applied outcome; carry the skipped locks on all
	// of them. resp is non-nil on a nil error (ApplyMetadataCandidate's
	// contract), but the mocks in this package's tests return (nil, nil), and a
	// nil deref here would turn "no response" into a crashed op.
	out := applyOutcome{Applied: true, Gate: plan.Gate}
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
		if err := svc.FinishApplyFileWork(id, pendingCover, false, false, checkpoint); err != nil {
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
	if err := svc.FinishApplyFileWork(id, pendingCover, true, true, checkpoint); err != nil {
		out.WriteBackFailed, out.Err = true, err
	}
	return out
}
