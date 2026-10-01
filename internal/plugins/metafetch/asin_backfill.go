// file: internal/plugins/metafetch/asin_backfill.go
// version: 1.0.0
// guid: c4e9a2f7-1d36-4b85-9a0e-6f2b8d31c7a4
// last-edited: 2026-10-01

package metafetch

// metafetch.asin-backfill: fill the ASIN of every book that has none, from
// Audible ONLY, through the strict gate in asin_match.go.
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
// Writes: Book.ASIN only, only when empty and not user-locked, through
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

	asinHistorySource     = "Audible (asin-backfill)"
	asinHistoryChangeType = "fetched"
	asinHistoryBatch      = "asin-backfill-"

	asinISBNSearchResults  = 10
	asinTitleSearchResults = 25
)

// errASINFilledMeanwhile aborts the write when another writer filled the ASIN
// (or a user locked it) between the search and the write.
var errASINFilledMeanwhile = errors.New("asin filled or locked by another writer during the search")

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
		DisplayName: "Backfill missing ASINs from Audible",
		Description: "Walks every book with no ASIN and searches Audible ONLY (never Google Books or Open Library) " +
			"by title+author and, when the book has an ISBN, by that ISBN. Writes an ASIN only when exactly one Audible " +
			"product passes the gate: equal title or a one-sided subtitle difference (volume numbers reject), a matching " +
			"author, no series-position or runtime conflict, and at least one corroboration (audiobook ISBN, runtime within " +
			"10%, or series name+position). Fills only empty, unlocked ASINs and records history per book. A no-match is " +
			"remembered for retry_after_days (default 30). Defaults to a dry run; dry_run=false applies. Params: dry_run, " +
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
	return fmt.Sprintf("scanned %d, %s %d, ambiguous %d, no match %d, errored %d",
		t.scanned.Load(), verb, t.matchedWritten.Load(), t.ambiguous.Load(), t.noMatch.Load(), t.errored.Load())
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
	t.mu.Unlock()
	return asinBackfillResult{
		DryRun: dry, Scanned: t.scanned.Load(), SkippedHasASIN: t.skippedHasASIN.Load(),
		SkippedDeleted: t.skippedDeleted.Load(), SkippedLocked: t.skippedLocked.Load(),
		SkippedNoAuthor: t.skippedNoAuthor.Load(), SkippedNoTitle: t.skippedNoTitle.Load(),
		SkippedRecentMiss: t.skippedRecentMiss.Load(), SkippedLimit: t.skippedLimit.Load(),
		MatchedWritten: t.matchedWritten.Load(), FilledMeanwhile: t.filledMeanwhile.Load(),
		Ambiguous: t.ambiguous.Load(), NoMatch: t.noMatch.Load(), Errored: t.errored.Load(),
		Searched: t.searched.Load(), RejectReasons: rejects, Evidence: evidence, HistoryFailed: historyFailed,
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
	run.log.Info("finished (dry_run=%t): %s; rejects=%v evidence=%v err=%v",
		dry, run.tally.summary(dry), res.RejectReasons, res.Evidence, runErr)
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

	switch {
	case b.MarkedForDeletion != nil && *b.MarkedForDeletion:
		t.skippedDeleted.Add(1)
		return nil
	case b.ASIN != nil && strings.TrimSpace(*b.ASIN) != "":
		t.skippedHasASIN.Add(1)
		return nil
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

	title := strings.TrimSpace(b.Title)
	if title == "" && b.TranscribedTitle != nil {
		title = strings.TrimSpace(*b.TranscribedTitle)
	}
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
		// Any failed search leaves the candidate set incomplete -- a second
		// passer could be the one we did not see -- so no decision is taken
		// and no marker written.
		r.bookError(b.ID, "Audible search", err)
		if t.consecutive.Add(1) >= asinBackfillMaxConsecutiveErrors {
			return fmt.Errorf("%s: %d books in a row failed their Audible search; stopping (last: %w)",
				asinBackfillOpID, asinBackfillMaxConsecutiveErrors, err)
		}
		return nil
	}
	t.consecutive.Store(0)

	d := decideASIN(facts, cands)
	t.addDecision(d)
	switch d.Outcome {
	case asinOutcomeMatched:
		r.apply(b.ID, title, d)
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
func (r *asinBackfillRun) apply(bookID, title string, d asinDecision) {
	t := r.tally
	evidence := strings.Join(d.Evidence, "+")
	if r.dry {
		t.matchedWritten.Add(1)
		r.log.Info("would write asin=%s book_id=%s title=%s audible_title=%s evidence=%s",
			d.ASIN, logger.SanitizeLogValue(bookID), logger.SanitizeLogValue(title), logger.SanitizeLogValue(d.Title), evidence)
		return
	}
	asin := d.ASIN
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
		r.log.Info("wrote asin=%s book_id=%s title=%s audible_title=%s evidence=%s",
			asin, logger.SanitizeLogValue(bookID), logger.SanitizeLogValue(title), logger.SanitizeLogValue(d.Title), evidence)
	}
}

func asinMissKey(bookID string) string { return asinMissKeyPrefix + bookID }

// recentMiss reports whether the book has a no-match marker younger than the
// retry window. A malformed marker is treated as absent (the book is simply
// searched again); a read error is returned.
func (r *asinBackfillRun) recentMiss(bookID string) (bool, error) {
	raw, err := r.store.GetRaw(asinMissKey(bookID))
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
	if r.dry {
		return
	}
	passing := append([]string(nil), d.Passing...)
	sort.Strings(passing)
	data, err := json.Marshal(asinMissMarker{At: r.now().UTC(), Outcome: d.Outcome, Passing: passing})
	if err == nil {
		err = r.store.SetRaw(asinMissKey(bookID), data)
	}
	if err != nil {
		r.log.Warn("recording the no-match marker failed (book will be searched again next run): book_id=%s err=%s",
			logger.SanitizeLogValue(bookID), logger.SanitizeLogValue(err.Error()))
	}
}
