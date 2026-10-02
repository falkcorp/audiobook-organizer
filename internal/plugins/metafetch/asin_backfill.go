// file: internal/plugins/metafetch/asin_backfill.go
// version: 1.2.0
// guid: c4e9a2f7-1d36-4b85-9a0e-6f2b8d31c7a4
// last-edited: 2026-10-01

package metafetch

// metafetch.asin-backfill: fill the ASIN of every book that has none, from
// Audible ONLY, through the strict gate in asin_match.go -- and the audiobook
// ISBN alongside it. Two paths per book:
//
//   - no ASIN: search + gate. On a match, write the ASIN and, when the book has
//     no ISBN at all, the matched product's audiobook ISBN (same request, it
//     comes back in product_details).
//   - ASIN but no ISBN: look the product up BY THAT ASIN (exact identity, no
//     gate) and write its audiobook ISBN. The ASIN is never changed.
//
// Only Audible's audiobook ISBN is written; print ISBNs from other sources are
// a different edition and are not this op's business.
//
// Why a new op and not maintenance.isbn-enrichment: that op searches every
// configured source (Google Books' 1,000/day key quota included), does one book
// at a time, stops after a 100-book batch, and accepts the first title-prefix
// hit (IsStrictTitleMatch's 60% rule passes "Red Rising" for "Red Rising 2").
// This op touches no provider but Audible, walks the whole library at
// asinBackfillDefaultWorkers concurrent books, and writes an ASIN only when
// exactly one Audible product passes the gate.
//
// Rate: every request goes through providerhttp.Client("audible"), the same
// per-provider token bucket candidate-fetch uses, so the op's workers queue on
// that bucket and can never push Audible past its configured rate. The client
// is rebuilt per book (providerhttp caches it, so this is a map lookup): a
// client held across a provider-limit change would keep the OLD limiter while
// candidate-fetch moved to the new one, and the two would no longer share one
// bucket.
//
// Writes: Book.ASIN and Book.ISBN13 only, each only when empty and not
// user-locked (ISBN-13 only when the book has no ISBN-10 either), through
// repairs.Writer (ModifyBook + one metadata-history row under its own batch id,
// so each write is visible and revertable per book). Nothing here enqueues tag
// write-back or iTunes (ITL) write-back: those are explicit enqueues on the
// metafetch service's apply path (Service.SetWriteBackBatcher), and the store's
// book write has no hook that reaches either. Writing a DB column on a book
// whose files live under books/itunes/** therefore touches no iTunes file.
//
// Scan stand-down: not held. A whole-library pass runs for hours and holding
// the stand-down would park every library scan for that long. Since #3635 a
// scan's book write and this op's ModifyBook serialize on the same per-book
// lock, and ModifyBook re-reads the row under it, so neither can revert the
// other's field.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

const (
	asinBackfillOpID = "metafetch.asin-backfill"

	// asinBackfillDefaultWorkers is the book pool. Audible's bucket is the real
	// throttle (8 req/s configured); four workers keep it full while a worker
	// is busy with DB reads, without queueing so many callers on the bucket
	// that the last one waits past the provider timeout.
	asinBackfillDefaultWorkers = 4
	asinBackfillMaxWorkers     = 8

	// asinBackfillPageSize is small enough that one page (worst case ~3
	// requests per book on the shared bucket) drains well inside
	// asinBackfillCheckpointInterval.
	asinBackfillPageSize = 100

	asinBackfillCheckpointInterval = 15 * time.Minute

	// asinBackfillDefaultRetryDays: a book searched with no confident match is
	// not searched again for this many days.
	asinBackfillDefaultRetryDays = 30

	// asinBackfillMaxConsecutiveErrors aborts the run when this many books in
	// a row ended in a provider error: Audible is down or refusing us, and
	// walking the rest of the library would only send doomed requests.
	asinBackfillMaxConsecutiveErrors = 25

	// asinMissKeyPrefix keys the per-book "searched, no confident match"
	// marker. A raw key, not opstate:, because the retention job prunes
	// opstate: keys that do not belong to a live operation.
	asinMissKeyPrefix = "asinbackfill:miss:"
	// isbnMissKeyPrefix keys the per-book "looked the ASIN up, Audible has no
	// audiobook ISBN for it" marker, so a book that already has an ASIN is not
	// re-fetched every run. Same retry window as the ASIN marker.
	isbnMissKeyPrefix = "asinbackfill:isbnmiss:"

	asinHistorySource     = "Audible (asin-backfill)"
	asinHistoryChangeType = "fetched"
	asinHistoryBatch      = "asin-backfill-"

	// asinProposalSampleCap bounds the would-write list a run keeps in its
	// result. The count is always exact; the cap is logged when it binds.
	asinProposalSampleCap = 2000

	asinISBNSearchResults  = 10
	asinTitleSearchResults = 25
)

// errASINFilledMeanwhile aborts the write when another writer filled the ASIN
// (or a user locked it) between the search and the write.
var errASINFilledMeanwhile = errors.New("asin filled or locked by another writer during the search")

// errISBNFilledMeanwhile aborts an ISBN-only write when another writer filled
// an ISBN (or a user locked ISBN-13) between the lookup and the write.
var errISBNFilledMeanwhile = errors.New("isbn filled or locked by another writer during the lookup")

// asinBackfillParams are the op parameters. AfterBookID is the checkpoint
// cursor (the registry merges it back into params on resume); it is the only
// key the op checkpoints, so a resume can never change the mode.
type asinBackfillParams struct {
	DryRun      *bool `json:"dry_run,omitempty"`
	DryRunCamel *bool `json:"dryRun,omitempty"`
	// BookIDs limits the run to these books (no library walk, no checkpoint).
	BookIDs []string `json:"book_ids,omitempty"`
	// RetryAfterDays: skip a book whose no-match marker is younger than this.
	// Omitted = 30; 0 = ignore markers and search every book again.
	RetryAfterDays *int `json:"retry_after_days,omitempty"`
	// Limit caps the books actually SEARCHED this run (0 = no cap).
	Limit int `json:"limit,omitempty"`
	// Workers is the book pool size (default 4, clamped 1..8).
	Workers     int    `json:"workers,omitempty"`
	AfterBookID string `json:"after_book_id,omitempty"`
}

type asinBackfillCheckpoint struct {
	AfterBookID string `json:"after_book_id"`
}

// asinMissMarker is the stored no-match record.
type asinMissMarker struct {
	At      time.Time `json:"at"`
	Outcome string    `json:"outcome"`
	Passing []string  `json:"passing,omitempty"`
}

// audibleIdentitySearcher is the one Audible call the op makes.
type audibleIdentitySearcher interface {
	SearchIdentities(ctx context.Context, q metadata.AudibleIdentityQuery) ([]metadata.AudibleIdentity, error)
	// LookupIdentityByASIN returns (nil, nil) when Audible does not carry the
	// product.
	LookupIdentityByASIN(ctx context.Context, asin string) (*metadata.AudibleIdentity, error)
}

// asinBackfillStore is what the op reads and writes.
type asinBackfillStore interface {
	database.MetadataFieldStateReader
	database.BookAuthorReader
	database.BookFilesGetter
	GetAllBooksFullFrom(afterID string, limit int) ([]database.Book, error)
	GetBookByID(id string) (*database.Book, error)
	GetSeriesByID(id int) (*database.Series, error)
	CountAllBooks() (int, error)
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
	RecordMetadataChange(record *database.MetadataChangeRecord) error
	GetRaw(key string) ([]byte, error)
	SetRaw(key string, value []byte) error
}

var _ asinBackfillStore = (database.Store)(nil)

func (p *Plugin) asinBackfillDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          asinBackfillOpID,
		Liveness:    sdk.LivenessRunItems,
		Plugin:      "metafetch",
		DisplayName: "Backfill missing ASINs and ISBNs from Audible",
		Description: "Walks every book with no ASIN and searches Audible ONLY (never Google Books or Open Library) " +
			"by title+author and, when the book has an ISBN, by that ISBN. Writes an ASIN only when exactly one Audible " +
			"product passes the gate: equal title or a one-sided subtitle difference (volume numbers reject), a matching " +
			"author, no series-position or runtime conflict, and at least one corroboration (audiobook ISBN, runtime within " +
			"10%, or series name+position). Fills only empty, unlocked ASINs and records history per book. A no-match is " +
			"remembered for retry_after_days (default 30). Also fills the audiobook ISBN-13 from Audible: alongside a matched " +
			"ASIN, and for books that already have an ASIN but no ISBN by looking that ASIN up directly. An ISBN is written " +
			"only when the book has none and ISBN-13 is unlocked. Defaults to a dry run; dry_run=false applies. Params: dry_run, " +
			"book_ids, retry_after_days, limit, workers (default 4).",
		// ResumeRestart with a page cursor: a resumed run restarts at the
		// last fully drained page. Books before it that still lack an ASIN
		// were searched (marker written) or skipped, so even a from-zero
		// re-walk is cheap -- reads only, no Audible calls.
		ResumePolicy:          sdk.ResumeRestart,
		MinCheckpointInterval: asinBackfillCheckpointInterval,
		DefaultPriority:       sdk.PriorityLow,
		ConcurrencyKey:        asinBackfillOpID,
		Writes:                []sdk.Resource{sdk.ResBooks},
		Cancellable:           true,
		Timeout:               24 * time.Hour,
		Capabilities: []sdk.Capability{
			sdk.CapLibraryRead, sdk.CapLibraryWrite, sdk.CapNetworkAudible,
		},
		Run: p.runASINBackfill,
	}
}

// asinBackfillTally is the run's counters. Every field is touched from worker
// goroutines AND read by RunItems' Label closure (which also runs inside the
// workers), so all of them are atomic or mutex-guarded.
type asinBackfillTally struct {
	scanned, skippedHasASIN, skippedDeleted, skippedLocked, skippedNoAuthor,
	skippedNoTitle, skippedRecentMiss, skippedLimit, matchedWritten,
	filledMeanwhile, ambiguous, noMatch, errored atomic.Int64

	// ISBN counters. isbnWritten counts every audiobook ISBN written, whether
	// alongside a matched ASIN or from a lookup by an existing ASIN.
	isbnLookups, isbnWritten, isbnNone, skippedRecentISBNMiss atomic.Int64

	// skippedManualOnly: Doctor Who / Big Finish / Torchwood, which the
	// owner applies by hand. skippedMerged: a book merged into another (a
	// merge loser) is not a book of its own and must not join the
	// book:asin index.
	skippedManualOnly, skippedMerged atomic.Int64

	// asinSuspect: a book's EXISTING ASIN looked up to a product whose title
	// or author disagrees with the book (a wrong ASIN from an older writer).
	asinSuspect atomic.Int64

	// proposals is the would-write / written list, capped at
	// asinProposalSampleCap; proposalsDropped counts what the cap left out.
	proposals        []asinProposal
	proposalsDropped int

	searched    atomic.Int64 // books that reached the Audible search (Limit counts these)
	consecutive atomic.Int64 // books in a row ending in a provider error

	mu       sync.Mutex
	rejects  map[string]int
	evidence map[string]int
}

func newASINBackfillTally() *asinBackfillTally {
	return &asinBackfillTally{rejects: map[string]int{}, evidence: map[string]int{}}
}

func (t *asinBackfillTally) addDecision(d asinDecision) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, v := range d.Rejects {
		t.rejects[k] += v
	}
	for _, e := range d.Evidence {
		t.evidence[e]++
	}
}

func (t *asinBackfillTally) summary(dry bool) string {
	verb := "written"
	if dry {
		verb = "would write"
	}
	return fmt.Sprintf("scanned %d, asin %s %d, isbn %s %d (lookups %d, audible has none %d), ambiguous %d, no match %d, errored %d",
		t.scanned.Load(), verb, t.matchedWritten.Load(), verb, t.isbnWritten.Load(), t.isbnLookups.Load(), t.isbnNone.Load(),
		t.ambiguous.Load(), t.noMatch.Load(), t.errored.Load())
}

// asinProposal is one would-write (dry run) or write: what the owner reviews
// before a live run, and what a live run can be limited to through book_ids.
type asinProposal struct {
	BookID       string   `json:"book_id"`
	Title        string   `json:"title"`
	ASIN         string   `json:"asin,omitempty"`
	ISBN13       string   `json:"isbn13,omitempty"`
	AudibleTitle string   `json:"audible_title,omitempty"`
	Evidence     []string `json:"evidence,omitempty"`
	// Via is "match" (searched + gate), "asin_lookup" (ISBN by an existing
	// ASIN) or "asin_suspect" (the existing ASIN names a different product;
	// nothing written, listed for the owner).
	Via string `json:"via"`
}

func (t *asinBackfillTally) propose(p asinProposal) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.proposals) >= asinProposalSampleCap {
		t.proposalsDropped++
		return
	}
	t.proposals = append(t.proposals, p)
}

// asinBackfillResult is the op's persisted result.
type asinBackfillResult struct {
	DryRun            bool           `json:"dry_run"`
	Scanned           int64          `json:"scanned"`
	SkippedHasASIN    int64          `json:"skipped_has_asin"`
	SkippedDeleted    int64          `json:"skipped_deleted"`
	SkippedLocked     int64          `json:"skipped_locked"`
	SkippedNoAuthor   int64          `json:"skipped_no_author"`
	SkippedNoTitle    int64          `json:"skipped_no_title"`
	SkippedRecentMiss int64          `json:"skipped_recent_miss"`
	SkippedLimit      int64          `json:"skipped_limit"`
	MatchedWritten    int64          `json:"matched_written"`
	FilledMeanwhile   int64          `json:"filled_meanwhile"`
	Ambiguous         int64          `json:"ambiguous"`
	NoMatch           int64          `json:"no_match"`
	Errored           int64          `json:"errored"`
	Searched          int64          `json:"searched"`
	RejectReasons     map[string]int `json:"reject_reasons"`
	Evidence          map[string]int `json:"evidence"`
	HistoryFailed     int            `json:"history_failed"`

	ISBNLookups           int64 `json:"isbn_lookups"`
	ISBNWritten           int64 `json:"isbn_written"`
	ISBNNone              int64 `json:"isbn_none"`
	SkippedRecentISBNMiss int64 `json:"skipped_recent_isbn_miss"`

	SkippedManualOnly int64 `json:"skipped_manual_only"`
	SkippedMerged     int64 `json:"skipped_merged"`
	// ASINSuspect counts books whose existing ASIN names a different
	// product; each is listed in Proposals with via "asin_suspect".
	ASINSuspect int64 `json:"asin_suspect"`

	// Proposals lists the books written (or, in a dry run, that would be),
	// up to asinProposalSampleCap; ProposalsDropped counts the rest.
	Proposals        []asinProposal `json:"proposals,omitempty"`
	ProposalsDropped int            `json:"proposals_dropped,omitempty"`
}

func (t *asinBackfillTally) result(dry bool, historyFailed int) asinBackfillResult {
	t.mu.Lock()
	rejects := make(map[string]int, len(t.rejects))
	for k, v := range t.rejects {
		rejects[k] = v
	}
	evidence := make(map[string]int, len(t.evidence))
	for k, v := range t.evidence {
		evidence[k] = v
	}
	proposals := append([]asinProposal(nil), t.proposals...)
	dropped := t.proposalsDropped
	t.mu.Unlock()
	return asinBackfillResult{
		DryRun: dry, Scanned: t.scanned.Load(), SkippedHasASIN: t.skippedHasASIN.Load(),
		SkippedDeleted: t.skippedDeleted.Load(), SkippedLocked: t.skippedLocked.Load(),
		SkippedNoAuthor: t.skippedNoAuthor.Load(), SkippedNoTitle: t.skippedNoTitle.Load(),
		SkippedRecentMiss: t.skippedRecentMiss.Load(), SkippedLimit: t.skippedLimit.Load(),
		MatchedWritten: t.matchedWritten.Load(), FilledMeanwhile: t.filledMeanwhile.Load(),
		Ambiguous: t.ambiguous.Load(), NoMatch: t.noMatch.Load(), Errored: t.errored.Load(),
		Searched: t.searched.Load(), RejectReasons: rejects, Evidence: evidence, HistoryFailed: historyFailed,
		ISBNLookups: t.isbnLookups.Load(), ISBNWritten: t.isbnWritten.Load(), ISBNNone: t.isbnNone.Load(),
		SkippedRecentISBNMiss: t.skippedRecentISBNMiss.Load(),
		SkippedManualOnly:     t.skippedManualOnly.Load(), SkippedMerged: t.skippedMerged.Load(),
		ASINSuspect: t.asinSuspect.Load(),
		Proposals:   proposals, ProposalsDropped: dropped,
	}
}

// asinBackfillRun is one run's resolved state.
type asinBackfillRun struct {
	store     asinBackfillStore
	newClient func() audibleIdentitySearcher
	writer    *repairs.Writer
	log       logger.LevelLogger
	tally     *asinBackfillTally
	dry       bool
	retry     time.Duration // 0 = ignore markers
	limit     int64
	now       func() time.Time
}

func (p *Plugin) runASINBackfill(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	var params asinBackfillParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("%s: invalid params: %w", asinBackfillOpID, err)
		}
	}
	dry, err := opmode.ResolveDryRun(asinBackfillOpID, params.DryRun, params.DryRunCamel)
	if err != nil {
		return err
	}
	store, ok := p.store.(asinBackfillStore)
	if !ok || store == nil {
		return fmt.Errorf("%s: store does not support the ASIN backfill", asinBackfillOpID)
	}
	newClient := p.newAudible
	if newClient == nil {
		// Build the configured source chain first: that is what installs the
		// configured Audible budget into providerhttp before the client that
		// reads it is constructed.
		if p.mfs != nil {
			p.mfs.BuildSourceChain()
		}
		newClient = func() audibleIdentitySearcher { return metadata.NewAudibleClient() }
	}

	retryDays := asinBackfillDefaultRetryDays
	if params.RetryAfterDays != nil {
		retryDays = max(*params.RetryAfterDays, 0)
	}
	workers := params.Workers
	if workers <= 0 {
		workers = asinBackfillDefaultWorkers
	}
	workers = min(workers, asinBackfillMaxWorkers)

	run := &asinBackfillRun{
		store:     store,
		newClient: newClient,
		writer:    repairs.NewWriter(store, store, asinHistorySource, asinHistoryChangeType, asinHistoryBatch),
		log:       logger.New("asin-backfill"),
		tally:     newASINBackfillTally(),
		dry:       dry,
		retry:     time.Duration(retryDays) * 24 * time.Hour,
		limit:     int64(max(params.Limit, 0)),
		now:       time.Now,
	}
	run.log.Info("starting: dry_run=%t workers=%d retry_after_days=%d limit=%d book_ids=%d resume_after=%s",
		dry, workers, retryDays, params.Limit, len(params.BookIDs), logger.SanitizeLogValue(params.AfterBookID))

	var runErr error
	if len(params.BookIDs) > 0 {
		runErr = run.runBookIDs(ctx, reporter, params.BookIDs, workers)
	} else {
		runErr = run.runLibrary(ctx, reporter, params.AfterBookID, workers)
	}

	res := run.tally.result(dry, run.writer.HistoryFailed())
	if err := registry.ReporterSetResult(reporter, res); err != nil {
		run.log.Warn("persisting the result failed: %s", logger.SanitizeLogValue(err.Error()))
	}
	run.log.Info("finished (dry_run=%t): %s; rejects=%v evidence=%v manual_only=%d merged=%d proposals_kept=%d proposals_dropped_by_cap=%d err=%v",
		dry, run.tally.summary(dry), res.RejectReasons, res.Evidence, res.SkippedManualOnly, res.SkippedMerged,
		len(res.Proposals), res.ProposalsDropped, runErr)
	return runErr
}

func (r *asinBackfillRun) options(offset, total, workers int) registry.RunItemsOptions {
	return registry.RunItemsOptions{
		Concurrency:    workers,
		ProgressOffset: offset,
		ProgressTotal:  total,
		// Runs inside the workers: reads only atomics (summary).
		Label: func(i, t int) string {
			return fmt.Sprintf("Books %d/%d (%s)", offset+i+1, t, r.tally.summary(r.dry))
		},
	}
}

// runLibrary walks the whole library one page at a time from the cursor,
// checkpointing after each fully drained page.
func (r *asinBackfillRun) runLibrary(ctx context.Context, reporter sdk.Reporter, cursor string, workers int) error {
	total, err := r.store.CountAllBooks()
	if err != nil {
		r.log.Warn("counting books failed; progress is unsized: %s", logger.SanitizeLogValue(err.Error()))
		total = 0
	}
	done := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		page, err := r.store.GetAllBooksFullFrom(cursor, asinBackfillPageSize)
		if err != nil {
			return fmt.Errorf("load books after %q: %w", cursor, err)
		}
		if len(page) == 0 {
			return nil
		}
		if err := registry.RunItems(ctx, reporter, page, r.processBook, r.options(done, max(total, done+len(page)), workers)); err != nil {
			return err
		}
		done += len(page)
		cursor = page[len(page)-1].ID
		if err := reporter.Checkpoint(asinBackfillCheckpoint{AfterBookID: cursor}); err != nil {
			r.log.Warn("checkpoint at %s failed: %s", logger.SanitizeLogValue(cursor), logger.SanitizeLogValue(err.Error()))
		}
		if r.limit > 0 && r.tally.searched.Load() >= r.limit {
			return nil
		}
	}
}

// runBookIDs processes exactly the named books.
func (r *asinBackfillRun) runBookIDs(ctx context.Context, reporter sdk.Reporter, ids []string, workers int) error {
	books := make([]database.Book, 0, len(ids))
	for _, id := range ids {
		b, err := r.store.GetBookByID(strings.TrimSpace(id))
		if err != nil {
			return fmt.Errorf("load book %s: %w", id, err)
		}
		if b == nil {
			r.log.Warn("book %s not found; skipped", logger.SanitizeLogValue(id))
			continue
		}
		books = append(books, *b)
	}
	return registry.RunItems(ctx, reporter, books, r.processBook, r.options(0, len(books), workers))
}

// processBook is one book. It returns an error only for cancellation or a run
// that must stop (a provider outage); every per-book outcome, including a
// per-book failure, is counted and returns nil so RunItems' fail-fast mode
// does not cancel the whole library over one book.
func (r *asinBackfillRun) processBook(ctx context.Context, b database.Book) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	t := r.tally
	t.scanned.Add(1)

	hasASIN := b.ASIN != nil && strings.TrimSpace(*b.ASIN) != ""
	switch {
	case b.MarkedForDeletion != nil && *b.MarkedForDeletion:
		t.skippedDeleted.Add(1)
		return nil
	case b.MergedIntoBookID != nil && strings.TrimSpace(*b.MergedIntoBookID) != "":
		t.skippedMerged.Add(1)
		return nil
	case hasASIN && !bookHasNoISBN(&b):
		t.skippedHasASIN.Add(1)
		return nil
	}

	searchTitle := strings.TrimSpace(b.Title)
	if searchTitle == "" && b.TranscribedTitle != nil {
		searchTitle = strings.TrimSpace(*b.TranscribedTitle)
	}
	// Owner rule: Doctor Who / Big Finish / Torchwood are applied by hand,
	// never by a bulk op. Checked on the book (path, title, the transcribed
	// title it would be searched by, series row, every file path) before any
	// Audible call; the candidate side is checked after the search.
	if skip, err := r.manualOnly(&b, searchTitle); err != nil {
		r.bookError(b.ID, "owner-manual check", err)
		return nil
	} else if skip {
		t.skippedManualOnly.Add(1)
		return nil
	}
	if hasASIN {
		if searchTitle == "" {
			t.skippedNoTitle.Add(1)
			return nil
		}
		authors, err := database.LiveBookAuthorNames(r.store, &b)
		if err != nil {
			r.bookError(b.ID, "read authors", err)
			return nil
		}
		if len(authors) == 0 {
			t.skippedNoAuthor.Add(1)
			return nil
		}
		return r.fillISBNByASIN(ctx, &b, searchTitle, authors)
	}

	locks, err := database.LoadFieldLocks(r.store, b.ID)
	if err != nil {
		// Fail closed: an unreadable lock set is never a write.
		r.bookError(b.ID, "read field locks", err)
		return nil
	}
	if locks.Locked(database.FieldKeyASIN) {
		t.skippedLocked.Add(1)
		return nil
	}

	if r.retry > 0 {
		recent, err := r.recentMiss(b.ID)
		if err != nil {
			r.bookError(b.ID, "read no-match marker", err)
			return nil
		}
		if recent {
			t.skippedRecentMiss.Add(1)
			return nil
		}
	}

	title := searchTitle
	if title == "" {
		t.skippedNoTitle.Add(1)
		return nil
	}
	authors, err := database.LiveBookAuthorNames(r.store, &b)
	if err != nil {
		r.bookError(b.ID, "read authors", err)
		return nil
	}
	if len(authors) == 0 {
		t.skippedNoAuthor.Add(1)
		return nil
	}

	facts, err := r.bookFacts(&b, title, authors)
	if err != nil {
		r.bookError(b.ID, "read book facts", err)
		return nil
	}

	if n := t.searched.Add(1); r.limit > 0 && n > r.limit {
		t.searched.Add(-1)
		t.skippedLimit.Add(1)
		return nil
	}

	cands, err := r.search(ctx, facts)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// A request Audible refuses as malformed for THIS book (400, 404,
		// 422) will fail the same way every run: remember it like a miss so
		// it is not retried every run, and do not let a run of them abort
		// the op as an outage would.
		if isPermanentRequestError(err) {
			t.errored.Add(1)
			r.log.Warn("Audible refused the search for this book; remembered as a miss: book_id=%s err=%s",
				logger.SanitizeLogValue(b.ID), logger.SanitizeLogValue(err.Error()))
			r.markRaw(asinMissKey(b.ID), asinMissMarker{At: r.now().UTC(), Outcome: "bad_request"})
			return nil
		}
		// Any other failed search leaves the candidate set incomplete -- a
		// second passer could be the one we did not see -- so no decision is
		// taken and no marker written.
		r.bookError(b.ID, "Audible search", err)
		if t.consecutive.Add(1) >= asinBackfillMaxConsecutiveErrors {
			return fmt.Errorf("%s: %d books in a row failed their Audible search; stopping (last: %w)",
				asinBackfillOpID, asinBackfillMaxConsecutiveErrors, err)
		}
		return nil
	}
	t.consecutive.Store(0)

	for _, c := range cands {
		if candidateManualOnly(c) {
			// Audible answering with a Doctor Who / Big Finish / Torchwood
			// product means this book is one, whatever its own row says.
			t.skippedManualOnly.Add(1)
			return nil
		}
	}

	d := decideASIN(facts, cands)
	t.addDecision(d)
	switch d.Outcome {
	case asinOutcomeMatched:
		r.apply(&b, locks, title, d)
	case asinOutcomeAmbiguous:
		t.ambiguous.Add(1)
		r.log.Info("ambiguous, nothing written: book_id=%s title=%s passing=%v",
			logger.SanitizeLogValue(b.ID), logger.SanitizeLogValue(title), d.Passing)
		r.markMiss(b.ID, d)
	default:
		t.noMatch.Add(1)
		r.markMiss(b.ID, d)
	}
	return nil
}

func (r *asinBackfillRun) bookError(bookID, what string, err error) {
	r.tally.errored.Add(1)
	r.log.Warn("%s failed; book left alone: book_id=%s err=%s",
		what, logger.SanitizeLogValue(bookID), logger.SanitizeLogValue(err.Error()))
}

// bookFacts gathers what the gate compares. A series that cannot be read is an
// error, not "no series": a missing series removes the series-conflict veto.
func (r *asinBackfillRun) bookFacts(b *database.Book, title string, authors []string) (asinBookFacts, error) {
	f := asinBookFacts{Title: title, Authors: authors}
	for _, v := range []*string{b.ISBN13, b.ISBN10} {
		if v != nil && strings.TrimSpace(*v) != "" {
			f.ISBNs = append(f.ISBNs, strings.TrimSpace(*v))
		}
	}
	// An unreadable or partial runtime is simply unknown: it neither
	// corroborates nor vetoes.
	if rt, err := database.LoadBookRuntime(r.store, b); err == nil {
		if sec, ok := rt.KnownSeconds(); ok {
			f.RuntimeSec = sec
		}
	}
	if b.SeriesID != nil && *b.SeriesID > 0 {
		s, err := r.store.GetSeriesByID(*b.SeriesID)
		if err != nil {
			return f, fmt.Errorf("series %d: %w", *b.SeriesID, err)
		}
		if s != nil {
			f.SeriesName = s.Name
			if b.SeriesSequence != nil {
				f.SeriesSeq = *b.SeriesSequence
			}
		}
	}
	return f, nil
}

// search runs the ISBN-anchored searches first, then title+author. There is
// deliberately no title-only search: every candidate must match an author, and
// a search without one only adds strangers.
func (r *asinBackfillRun) search(ctx context.Context, f asinBookFacts) ([]metadata.AudibleIdentity, error) {
	client := r.newClient()
	var out []metadata.AudibleIdentity
	seenISBN := map[string]bool{}
	for _, isbn := range f.ISBNs {
		n := normISBN13(isbn)
		if n == "" || seenISBN[n] {
			continue
		}
		seenISBN[n] = true
		res, err := client.SearchIdentities(ctx, metadata.AudibleIdentityQuery{Keywords: n, NumResults: asinISBNSearchResults})
		if err != nil {
			return nil, fmt.Errorf("isbn %s: %w", n, err)
		}
		out = append(out, res...)
	}
	res, err := client.SearchIdentities(ctx, metadata.AudibleIdentityQuery{
		Title: f.Title, Author: f.Authors[0], NumResults: asinTitleSearchResults,
	})
	if err != nil {
		return nil, fmt.Errorf("title+author: %w", err)
	}
	out = append(out, res...)
	if len(res) == 0 {
		// Audible's title filter can miss on a long "Title: Subtitle"; retry
		// with the head. Still with the author.
		if head, _, ok := splitSubtitle(stripSeriesParen(f.Title)); ok {
			res, err = client.SearchIdentities(ctx, metadata.AudibleIdentityQuery{
				Title: head, Author: f.Authors[0], NumResults: asinTitleSearchResults,
			})
			if err != nil {
				return nil, fmt.Errorf("title head+author: %w", err)
			}
			out = append(out, res...)
		}
	}
	return out, nil
}

// apply writes the matched ASIN (or, in a dry run, logs it).
func (r *asinBackfillRun) apply(b *database.Book, locks database.FieldLocks, title string, d asinDecision) {
	bookID := b.ID
	t := r.tally
	evidence := strings.Join(d.Evidence, "+")
	if r.dry {
		t.matchedWritten.Add(1)
		// The same conditions the live write applies, on the row and locks
		// this run already read, so the preview is what a live run would do.
		wouldISBN := d.ISBN != "" && bookHasNoISBN(b) && !locks.Locked(database.FieldKeyISBN13)
		pr := asinProposal{BookID: bookID, Title: title, ASIN: d.ASIN, AudibleTitle: d.Title, Evidence: d.Evidence, Via: "match"}
		if wouldISBN {
			t.isbnWritten.Add(1)
			pr.ISBN13 = d.ISBN
		}
		t.propose(pr)
		r.log.Info("would write asin=%s isbn13=%s book_id=%s title=%s audible_title=%s evidence=%s",
			d.ASIN, writtenOrNone(wouldISBN, d.ISBN), logger.SanitizeLogValue(bookID), logger.SanitizeLogValue(title), logger.SanitizeLogValue(d.Title), evidence)
		return
	}
	asin := d.ASIN
	isbn := d.ISBN
	isbnWritten := false
	_, err := r.writer.Modify(bookID, func(fresh *database.Book) error {
		if fresh.ASIN != nil && strings.TrimSpace(*fresh.ASIN) != "" {
			return errASINFilledMeanwhile
		}
		// Re-read the locks under the book's write lock: a lock taken during
		// the search must win.
		locks, lerr := database.LoadFieldLocks(r.store, bookID)
		if lerr != nil {
			return fmt.Errorf("read field locks: %w", lerr)
		}
		if locks.Locked(database.FieldKeyASIN) {
			return errASINFilledMeanwhile
		}
		fresh.ASIN = &asin
		// The matched product's audiobook ISBN rides along, under the same
		// rules: only when the book has no ISBN at all (an ISBN-10 already
		// there may be a print edition, and pairing it with a different
		// ISBN-13 would leave the book disagreeing with itself) and ISBN-13
		// is not locked.
		isbnWritten = false
		if isbn != "" && bookHasNoISBN(fresh) && !locks.Locked(database.FieldKeyISBN13) {
			v := isbn
			fresh.ISBN13 = &v
			isbnWritten = true
		}
		return nil
	})
	switch {
	case errors.Is(err, errASINFilledMeanwhile):
		t.filledMeanwhile.Add(1)
		r.log.Info("asin filled or locked during the search; left alone: book_id=%s", logger.SanitizeLogValue(bookID))
	case err != nil:
		r.bookError(bookID, "write asin", err)
	default:
		t.matchedWritten.Add(1)
		p := asinProposal{BookID: bookID, Title: title, ASIN: asin, AudibleTitle: d.Title, Evidence: d.Evidence, Via: "match"}
		if isbnWritten {
			t.isbnWritten.Add(1)
			p.ISBN13 = isbn
		}
		t.propose(p)
		r.log.Info("wrote asin=%s isbn13=%s book_id=%s title=%s audible_title=%s evidence=%s",
			asin, writtenOrNone(isbnWritten, isbn), logger.SanitizeLogValue(bookID), logger.SanitizeLogValue(title), logger.SanitizeLogValue(d.Title), evidence)
	}
}

func asinMissKey(bookID string) string { return asinMissKeyPrefix + bookID }

// recentMiss reports whether the book has an ASIN no-match marker younger
// than the retry window.
func (r *asinBackfillRun) recentMiss(bookID string) (bool, error) {
	return r.recentMarker(asinMissKey(bookID))
}

// recentMarker reports whether the marker at key is younger than the retry
// window. A malformed marker is treated as absent (the book is simply looked
// at again); a read error is returned.
func (r *asinBackfillRun) recentMarker(key string) (bool, error) {
	raw, err := r.store.GetRaw(key)
	if err != nil {
		return false, err
	}
	if len(raw) == 0 {
		return false, nil
	}
	var m asinMissMarker
	if json.Unmarshal(raw, &m) != nil || m.At.IsZero() {
		return false, nil
	}
	return r.now().Sub(m.At) < r.retry, nil
}

// markMiss records a genuine (error-free) no-match or ambiguous search. A dry
// run writes nothing, markers included.
func (r *asinBackfillRun) markMiss(bookID string, d asinDecision) {
	passing := append([]string(nil), d.Passing...)
	sort.Strings(passing)
	r.markRaw(asinMissKey(bookID), asinMissMarker{At: r.now().UTC(), Outcome: d.Outcome, Passing: passing})
}

// markRaw stores one marker. A dry run writes nothing. A failed write only
// means the book is looked at again next run.
func (r *asinBackfillRun) markRaw(key string, m asinMissMarker) {
	if r.dry {
		return
	}
	data, err := json.Marshal(m)
	if err == nil {
		err = r.store.SetRaw(key, data)
	}
	if err != nil {
		r.log.Warn("recording the no-match marker failed (book will be looked at again next run): key=%s err=%s",
			logger.SanitizeLogValue(key), logger.SanitizeLogValue(err.Error()))
	}
}

// bookHasNoISBN reports whether the book carries neither ISBN-13 nor ISBN-10.
func bookHasNoISBN(b *database.Book) bool {
	for _, v := range []*string{b.ISBN13, b.ISBN10} {
		if v != nil && strings.TrimSpace(*v) != "" {
			return false
		}
	}
	return true
}

func writtenOrNone(written bool, isbn string) string {
	if !written {
		return "-"
	}
	return isbn
}

func isbnMissKey(bookID string) string { return isbnMissKeyPrefix + bookID }

// fillISBNByASIN handles a book that already has an ASIN but no ISBN: it looks
// the product up BY THAT ASIN (an exact identity, so there is no matching gate
// to pass) and writes the audiobook ISBN when Audible has one. The ASIN itself
// is never changed. The existing ASIN is NOT trusted blindly: some were
// written by the old isbn-enrichment job's title-prefix matching. The
// looked-up product's title and author must agree with the book (the gate's
// title and author rules); a product that disagrees is reported as
// asin_suspect, listed for the owner, and nothing is written. A product
// Audible does not carry, one without an ISBN, or a suspect one is recorded as
// an ISBN miss so the next run does not fetch it again inside the retry window.
func (r *asinBackfillRun) fillISBNByASIN(ctx context.Context, b *database.Book, title string, authors []string) error {
	t := r.tally
	locks, err := database.LoadFieldLocks(r.store, b.ID)
	if err != nil {
		r.bookError(b.ID, "read field locks", err)
		return nil
	}
	if locks.Locked(database.FieldKeyISBN13) {
		t.skippedLocked.Add(1)
		return nil
	}
	if r.retry > 0 {
		recent, err := r.recentMarker(isbnMissKey(b.ID))
		if err != nil {
			r.bookError(b.ID, "read isbn no-match marker", err)
			return nil
		}
		if recent {
			t.skippedRecentISBNMiss.Add(1)
			return nil
		}
	}
	if n := t.searched.Add(1); r.limit > 0 && n > r.limit {
		t.searched.Add(-1)
		t.skippedLimit.Add(1)
		return nil
	}

	asin := strings.TrimSpace(*b.ASIN)
	t.isbnLookups.Add(1)
	id, err := r.newClient().LookupIdentityByASIN(ctx, asin)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		r.bookError(b.ID, "Audible ASIN lookup", err)
		if t.consecutive.Add(1) >= asinBackfillMaxConsecutiveErrors {
			return fmt.Errorf("%s: %d books in a row failed their Audible request; stopping (last: %w)",
				asinBackfillOpID, asinBackfillMaxConsecutiveErrors, err)
		}
		return nil
	}
	t.consecutive.Store(0)

	isbn := ""
	// The product must be the one asked for: Audible can answer an old ASIN
	// with its replacement, and that product's ISBN is not this book's.
	if id != nil && candidateManualOnly(*id) {
		t.skippedManualOnly.Add(1)
		return nil
	}
	if id != nil && strings.EqualFold(strings.TrimSpace(id.ASIN), asin) {
		_, titleOK := asinTitleMatch(title, id.Title, id.Subtitle)
		if !titleOK || !asinAuthorMatches(authors, id.Authors) {
			t.asinSuspect.Add(1)
			t.propose(asinProposal{BookID: b.ID, Title: title, ASIN: asin, AudibleTitle: id.Title, Via: "asin_suspect"})
			r.log.Info("existing asin names a different product; nothing written: book_id=%s asin=%s title=%s audible_title=%s",
				logger.SanitizeLogValue(b.ID), asin, logger.SanitizeLogValue(title), logger.SanitizeLogValue(id.Title))
			r.markRaw(isbnMissKey(b.ID), asinMissMarker{At: r.now().UTC(), Outcome: "asin_suspect"})
			return nil
		}
		isbn = normISBN13(id.ISBN)
	}
	if isbn == "" {
		t.isbnNone.Add(1)
		r.markRaw(isbnMissKey(b.ID), asinMissMarker{At: r.now().UTC(), Outcome: "no_isbn"})
		return nil
	}

	if r.dry {
		t.isbnWritten.Add(1)
		t.propose(asinProposal{BookID: b.ID, Title: b.Title, ASIN: asin, ISBN13: isbn, AudibleTitle: id.Title, Via: "asin_lookup"})
		r.log.Info("would write isbn13=%s from asin=%s book_id=%s",
			isbn, asin, logger.SanitizeLogValue(b.ID))
		return nil
	}
	_, err = r.writer.Modify(b.ID, func(fresh *database.Book) error {
		if fresh.ASIN == nil || !strings.EqualFold(strings.TrimSpace(*fresh.ASIN), asin) || !bookHasNoISBN(fresh) {
			return errISBNFilledMeanwhile
		}
		locks, lerr := database.LoadFieldLocks(r.store, b.ID)
		if lerr != nil {
			return fmt.Errorf("read field locks: %w", lerr)
		}
		if locks.Locked(database.FieldKeyISBN13) {
			return errISBNFilledMeanwhile
		}
		v := isbn
		fresh.ISBN13 = &v
		return nil
	})
	switch {
	case errors.Is(err, errISBNFilledMeanwhile):
		t.filledMeanwhile.Add(1)
		r.log.Info("isbn filled, locked or asin changed during the lookup; left alone: book_id=%s", logger.SanitizeLogValue(b.ID))
	case err != nil:
		r.bookError(b.ID, "write isbn", err)
	default:
		t.isbnWritten.Add(1)
		t.propose(asinProposal{BookID: b.ID, Title: b.Title, ASIN: asin, ISBN13: isbn, AudibleTitle: id.Title, Via: "asin_lookup"})
		r.log.Info("wrote isbn13=%s from asin=%s book_id=%s", isbn, asin, logger.SanitizeLogValue(b.ID))
	}
	return nil
}

// manualOnly reports whether the book belongs to a library the owner applies
// by hand (applygate's Doctor Who / Big Finish / Torchwood rule). A store
// read failure is an error, never "not manual-only".
func (r *asinBackfillRun) manualOnly(b *database.Book, searchTitle string) (bool, error) {
	g := applygate.BulkManualOnlyGuard(r.store, r.store, b, searchTitle)
	if g.ReadErr != "" {
		return false, errors.New(g.ReadErr)
	}
	reason, _ := applygate.ManualOnlyDetail(b, nil, applygate.TranscribedSearch{Query: searchTitle}, g)
	return reason != "", nil
}

// candidateManualOnly reports whether an Audible product names a manual-only
// library in its title, subtitle or any series.
func candidateManualOnly(c metadata.AudibleIdentity) bool {
	if applygate.IsOwnerManualOnly(c.Title, c.Subtitle) {
		return true
	}
	for _, s := range c.Series {
		if applygate.IsOwnerManualOnly("", s.Title) {
			return true
		}
	}
	return false
}

// isPermanentRequestError reports whether err is Audible refusing the request
// itself (400, 404, 422), as opposed to throttling, auth or an outage.
func isPermanentRequestError(err error) bool {
	var pe *metadata.ProviderStatusError
	if !errors.As(err, &pe) {
		return false
	}
	switch pe.Status {
	case 400, 404, 422:
		return true
	}
	return false
}
