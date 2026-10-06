// file: internal/plugins/maintenance/lost_candidates_fixer.go
// version: 1.1.0
// guid: 0583655e-5bf4-4704-8046-c880bf7522b6
// last-edited: 2026-10-05

package maintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// lostCandidatesFixerID is the Repairs-lane id of the lost-candidates fixer.
const lostCandidatesFixerID = "maintenance.refetch-lost-candidates"

// Skip kinds of a lost-candidates row.
const (
	lostSkipGone          = "gone"
	lostSkipApplied       = "applied"
	lostSkipNoMatch       = "marked_no_match"
	lostSkipHasCandidates = "has_candidates"
	lostSkipNotMatched    = "no_matched_fetch"
	lostSkipSearchedSince = "searched_since"
)

// Classes of a lost-candidates row: whether the book carries an ASIN now. The
// ASIN backfill filled one on most of these books (and that write dropped the
// candidates); the rest lost theirs to a title or author change.
const (
	lostClassASIN   = "asin_present"
	lostClassNoASIN = "no_asin"
)

// lostCandidatesFixer refetches the metadata candidates of books that had a
// matched candidate and lost it. Until 2026-10-05 the store deleted a book's
// cached candidates on ANY ASIN, ISBN or series change, and the apply paths
// deleted them after every apply; metafetch.asin-backfill fills empty ASINs
// every six hours, so books whose candidate it had just confirmed were left
// with an ASIN, no applied status and nothing to review (1,078 on production,
// plus 421 that lost theirs to the 10-04 author relink and junk-title
// retitles).
//
// A row is a live book that is not applied and not marked "no match", whose
// latest metadata.candidate-fetch outcome is "matched" (the evidence it HAD a
// candidate: the deletion itself left no record), and whose candidate cache
// now holds none. A book whose cache row was searched again after that
// outcome and came back empty is skipped (searched_since): the providers
// already answered it as it is now.
//
// Apply runs the candidate-fetch op's own per-book fetch (unforced, through
// the server's shared provider gate; each provider's token bucket, Audible's
// 8/s included, still paces it) and writes the candidate cache only. It never
// applies metadata and never writes the book, so the iTunes path guard does
// not apply (ITunesDatabaseOnly); Doctor Who / Big Finish / Torchwood stay
// guarded. The engine runs rows on its bounded pool. A row whose refetch
// finds nothing reports failed with the fetch's message.
type lostCandidatesFixer struct{ p *Plugin }

func newLostCandidatesFixer(p *Plugin) *lostCandidatesFixer { return &lostCandidatesFixer{p: p} }

var _ repairs.Fixer = (*lostCandidatesFixer)(nil)

func (f *lostCandidatesFixer) ID() string    { return lostCandidatesFixerID }
func (f *lostCandidatesFixer) Title() string { return "Refetch lost metadata candidates" }
func (f *lostCandidatesFixer) Description() string {
	return "Books a metadata search matched whose candidates were later deleted, and which are not applied. " +
		"Before 2026-10-05 an ASIN/ISBN fill (such as the six-hourly ASIN backfill) deleted them; a title or " +
		"author-name change still does, so books retitled or relinked by a repair land here too. Apply searches the " +
		"providers again for each selected book and stores the candidates for review. It never applies metadata " +
		"and never changes the book."
}

// ITunesDatabaseOnly: the fixer writes no book row and no file, only the
// candidate cache (see the type comment).
func (f *lostCandidatesFixer) ITunesDatabaseOnly() bool { return true }

// lostCandidateInput is everything one row's decision reads.
type lostCandidateInput struct {
	book    database.BookCore
	outcome CandidateFetchOutcome
	known   bool // outcome was found
	entry   *database.MetadataCandidateCache
}

// Plan lists every live, unapplied book whose latest fetch matched and whose
// candidate cache is empty now.
func (f *lostCandidatesFixer) Plan(ctx context.Context, _ json.RawMessage, rep registry.Reporter) ([]repairs.Row, error) {
	store, cache := f.p.deps.OpsStore(), f.p.deps.MetadataCacheStore()
	if store == nil || cache == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	outcomes, err := f.p.deps.LatestCandidateFetchOutcomes()
	if err != nil {
		return nil, fmt.Errorf("read the latest candidate-fetch outcomes: %w", err)
	}
	all, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		return nil, fmt.Errorf("GetAllBooksCore: %w", err)
	}
	// The cheap filters run here; only a book whose latest fetch matched and
	// that nobody has ruled on reads its cache row.
	var cands []lostCandidateInput
	for i := range all {
		b := all[i]
		o, ok := outcomes[b.ID]
		if !ok || o.Status != "matched" || b.IsSoftDeleted() || reviewRuled(&b) {
			continue
		}
		cands = append(cands, lostCandidateInput{book: b, outcome: o, known: true})
	}
	rows := make([]repairs.Row, len(cands))
	keep := make([]bool, len(cands))
	var done atomic.Int64
	// Each worker writes only rows[i] and keep[i] for its own i.
	runErr := registry.RunItems(ctx, rep, indexesOf(len(cands)), func(_ context.Context, i int) error {
		defer done.Add(1)
		in := cands[i]
		entry, gerr := cache.GetMetadataCache(in.book.ID)
		if gerr != nil {
			r := repairs.Row{RowID: in.book.ID, BookIDs: []string{in.book.ID}, Title: in.book.Title,
				Skipped: "error", SkipReason: gerr.Error(), Reason: gerr.Error(), Risk: repairs.RiskReview}
			r.Fingerprint = lostFingerprint(r, "read-error")
			rows[i], keep[i] = r, true
			return nil
		}
		in.entry = entry
		r := lostCandidatesRow(in)
		// A book that still holds candidates is not a row: the plan lists
		// lost candidates only, not the whole matched backlog.
		if r.Skipped == lostSkipHasCandidates {
			return nil
		}
		rows[i], keep[i] = r, true
		return nil
	}, registry.RunItemsOptions{
		Concurrency: titleRepairWorkers(),
		ErrMode:     registry.ErrModeCollect,
		Label:       func(_, total int) string { return fmt.Sprintf("Lost candidates %d/%d", done.Load(), total) },
	})
	if runErr != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	out := make([]repairs.Row, 0, len(rows))
	for i := range rows {
		if keep[i] {
			out = append(out, rows[i])
		}
	}
	return out, nil
}

// Replan re-reads the book, its latest outcome and its cache row. A book that
// was applied, refetched or deleted since comes back skipped with a different
// fingerprint (changed_since_plan).
func (f *lostCandidatesFixer) Replan(_ context.Context, _ json.RawMessage, planned repairs.Row, _ registry.Reporter) (repairs.Row, error) {
	store, cache := f.p.deps.OpsStore(), f.p.deps.MetadataCacheStore()
	if store == nil || cache == nil {
		return repairs.Row{}, fmt.Errorf("database not initialized")
	}
	b, err := store.GetBookByID(planned.RowID)
	if err != nil {
		return repairs.Row{}, fmt.Errorf("read book %s: %w", planned.RowID, err)
	}
	if b == nil || b.IsSoftDeleted() {
		r := repairs.Row{RowID: planned.RowID, BookIDs: []string{planned.RowID}, Skipped: lostSkipGone,
			SkipReason: "the book no longer exists", Risk: repairs.RiskLow}
		r.Reason = r.SkipReason
		r.Fingerprint = lostFingerprint(r, "gone")
		return r, nil
	}
	o, ok, err := f.p.deps.LatestCandidateFetchOutcome(b.ID)
	if err != nil {
		return repairs.Row{}, fmt.Errorf("read the latest candidate-fetch outcome of %s: %w", b.ID, err)
	}
	entry, err := cache.GetMetadataCache(b.ID)
	if err != nil {
		return repairs.Row{}, fmt.Errorf("read the candidate cache of %s: %w", b.ID, err)
	}
	return lostCandidatesRow(lostCandidateInput{book: b.Core(), outcome: o, known: ok, entry: entry}), nil
}

// reviewRuled reports whether the book carries a review verdict that takes it
// out of scope: applied (audiobooks.BookMetadataApplied) or marked no match.
func reviewRuled(b *database.BookCore) bool {
	if b.MetadataReviewStatus == nil {
		return false
	}
	book := b.ToBook()
	return audiobooks.BookMetadataApplied(&book) || *b.MetadataReviewStatus == "no_match"
}

// lostCandidatesRow decides one row from what in read.
func lostCandidatesRow(in lostCandidateInput) repairs.Row {
	b := in.book
	asin := ""
	if b.ASIN != nil {
		asin = strings.TrimSpace(*b.ASIN)
	}
	status := ""
	if b.MetadataReviewStatus != nil {
		status = *b.MetadataReviewStatus
	}
	r := repairs.Row{RowID: b.ID, BookIDs: []string{b.ID}, Title: b.Title, Risk: repairs.RiskLow,
		Class: lostClassNoASIN, Current: map[string]string{"candidates": "0"}}
	if asin != "" {
		r.Class = lostClassASIN
		r.Current["asin"] = asin
	}
	if in.known {
		r.Current["last_fetch"] = in.outcome.Status + " " + in.outcome.At.UTC().Format("2006-01-02")
	}
	cacheState := "none"
	count := 0
	if in.entry != nil {
		count = len(in.entry.Candidates)
		r.Current["candidates"] = strconv.Itoa(count)
		cacheState = fmt.Sprintf("%d|%s|%s", count, in.entry.FetchedAt.UTC().Format(time.RFC3339Nano), lastEmpty(in.entry))
	}
	extra := strings.Join([]string{asin, status, in.outcome.Status, in.outcome.At.UTC().Format(time.RFC3339Nano), cacheState}, "\n")
	skip := func(kind, why string) repairs.Row {
		r.Skipped, r.SkipReason, r.Reason = kind, why, why
		r.Fingerprint = lostFingerprint(r, extra)
		return r
	}
	switch {
	case database.MetadataApplied(b.MetadataReviewStatus):
		return skip(lostSkipApplied, "the book's metadata has been applied")
	case status == "no_match":
		return skip(lostSkipNoMatch, "the book is marked \"no match\"")
	case count > 0:
		return skip(lostSkipHasCandidates, "the book has cached candidates")
	case !in.known || in.outcome.Status != "matched":
		return skip(lostSkipNotMatched, "the book's latest metadata search did not match")
	case in.entry != nil && searchedAfter(in.entry, in.outcome.At):
		return skip(lostSkipSearchedSince, "the book was searched again after the match and the providers returned nothing")
	}
	r.Proposed = map[string]string{"candidates": "refetch"}
	r.Reason = "a metadata search matched this book on " + in.outcome.At.UTC().Format("2006-01-02") +
		", but its candidates are gone; a later change to the book deleted them"
	r.Evidence = []string{"latest candidate fetch: matched", "cached candidates now: " + strconv.Itoa(count)}
	r.Fingerprint = lostFingerprint(r, extra)
	return r
}

// lastEmpty formats entry.LastEmptyFetchAt for the fingerprint.
func lastEmpty(e *database.MetadataCandidateCache) string {
	if e.LastEmptyFetchAt == nil {
		return ""
	}
	return e.LastEmptyFetchAt.UTC().Format(time.RFC3339Nano)
}

// searchedAfter reports whether the cache row records a search after t.
func searchedAfter(e *database.MetadataCandidateCache, t time.Time) bool {
	if e.FetchedAt.After(t) {
		return true
	}
	return e.LastEmptyFetchAt != nil && e.LastEmptyFetchAt.After(t)
}

func lostFingerprint(r repairs.Row, extra string) string {
	sum := sha256.Sum256([]byte(r.RowID + "\n" + r.Title + "\n" + r.Skipped + "\n" + extra))
	return hex.EncodeToString(sum[:])[:32]
}

// errRefetchFoundNothing marks a refetch that left the book with no
// candidates.
var errRefetchFoundNothing = errors.New("refetch found no candidates")

// Apply refetches the book's candidates. It writes nothing through w: the
// candidate cache is not book data, and nothing is applied.
func (f *lostCandidatesFixer) Apply(ctx context.Context, _ *repairs.Writer, fresh repairs.Row) error {
	res, err := f.p.deps.RefetchMetadataCandidates(ctx, fresh.RowID)
	if err != nil {
		return fmt.Errorf("refetch candidates of %s: %w", fresh.RowID, err)
	}
	if res.Candidates == 0 {
		return fmt.Errorf("%w (fetch status %s): %s", errRefetchFoundNothing, res.Status, res.Detail)
	}
	return nil
}
