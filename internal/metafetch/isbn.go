// file: internal/metafetch/isbn.go
// version: 1.10.1
// guid: 34290bd0-745e-4509-ad2d-e237785bb7ef
// last-edited: 2026-09-12

package metafetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logging"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// ISBNService searches external metadata sources for ISBN (and ASIN)
// when a book is missing those identifiers after a metadata fetch/apply.
type ISBNService struct {
	db      isbnEnrichmentStore
	sources []metadata.MetadataSource

	// sourceSearchErrSamples rate-limits the WARN in sampleSourceSearchError
	// per source name (map[string]*atomic.Int64, lazily populated). SF-03
	// found isbn.go discarding every provider search error outright, so a
	// throttled or circuit-open provider read identically to a legitimate
	// zero-result search; this field only bounds log volume during a
	// sustained outage across a whole-library sweep, it does not gate
	// correctness -- see sourceSearchError.allErrored for that.
	sourceSearchErrSamples sync.Map
}

// NewISBNService creates an enrichment service that will search the
// given metadata sources for ISBN/ASIN data.
func NewISBNService(db isbnEnrichmentStore, sources []metadata.MetadataSource) *ISBNService {
	return &ISBNService{db: db, sources: sources}
}

// ErrAllSourcesErrored is the sentinel a run-level error from
// EnrichMissingISBNs wraps (via fmt.Errorf's %w, so errors.Is finds it) when
// every book it attempted this run had every provider source error rather
// than return a genuine result. It is also the sentinel sourceSearchError
// represents at the single-book level (see allErrored below). Distinguishing
// this from a real zero-result search is the point of SF-03: without it, a
// provider outage renders identically to "this book genuinely has no
// ISBN/ASIN anywhere".
var ErrAllSourcesErrored = errors.New("isbn enrichment: every provider source errored during search, no genuine result was obtained")

// sourceSearchError carries provider search failures out of EnrichBookISBN
// without changing its (bool, error) signature, so every existing caller
// keeps compiling unchanged. EnrichMissingISBNs unwraps it with errors.As to
// fold per-source error counts into its own run summary and to decide
// whether the book counts as genuinely "checked" or as "errored".
type sourceSearchError struct {
	// bySource counts one entry per source name for every search call that
	// returned an error while enriching this book (a source can appear
	// twice: once from the ISBN loop, once from the ASIN loop).
	bySource map[string]int
	// allErrored is true when this book needed at least one identifier and
	// no source call attempted for it ever returned a genuine (error-free)
	// result -- i.e. this book contributed no information either way.
	allErrored bool
}

func (e *sourceSearchError) Error() string {
	total := 0
	for _, n := range e.bySource {
		total += n
	}
	return fmt.Sprintf("isbn enrichment: %d provider search error(s) across %d source(s) (all_errored=%t)",
		total, len(e.bySource), e.allErrored)
}

// sourceSearchErrorLogSampleEvery bounds how often a repeated provider
// search error gets a WARN log line: the first occurrence per source, then
// every Nth after that -- so an outage spanning thousands of books during a
// nightly sweep does not flood the log.
const sourceSearchErrorLogSampleEvery = 20

// sampleSourceSearchError logs a sampled WARN naming the source and the
// error, instead of the previous behavior (isbn.go discarding the error
// entirely at the `results, _ = src.Search...` call sites).
func (s *ISBNService) sampleSourceSearchError(ctx context.Context, bookID, title, source string, err error) {
	v, _ := s.sourceSearchErrSamples.LoadOrStore(source, new(atomic.Int64))
	counter := v.(*atomic.Int64)
	n := counter.Add(1)
	if n == 1 || n%sourceSearchErrorLogSampleEvery == 0 {
		logging.Warn(ctx, "ISBN enrichment: provider search errored",
			"id", bookID, "title", title, "source", source, "error", err, "occurrence", n)
	}
}

// EnrichBookISBN searches external sources for ISBN if the book doesn't have one.
// It also back-fills ASIN from Audible when missing. Returns true if any
// identifier was found and saved.
//
// A user lock on isbn10, isbn13 or asin is honoured: a locked identifier is
// never searched for or written, even when the column is blank -- a blank
// the user locked is a deliberate blank. Lock reads fail closed: when the
// locks cannot be read the book is left untouched and the error is returned.
func (s *ISBNService) EnrichBookISBN(ctx context.Context, bookID string) (bool, error) {
	book, err := s.db.GetBookByID(bookID)
	if err != nil || book == nil {
		return false, err
	}

	locks, err := database.LoadFieldLocks(s.db, bookID)
	if err != nil {
		return false, fmt.Errorf("refusing to enrich identifiers for %s: %w", bookID, err)
	}

	hasISBN := (book.ISBN10 != nil && *book.ISBN10 != "") || (book.ISBN13 != nil && *book.ISBN13 != "")
	hasASIN := book.ASIN != nil && *book.ASIN != ""

	// A locked identifier counts as "present": whatever is there is what the
	// user wants there.
	isbnLocked := locks.Locked(database.FieldKeyISBN10) && locks.Locked(database.FieldKeyISBN13)
	asinLocked := locks.Locked(database.FieldKeyASIN)
	if isbnLocked || asinLocked {
		logging.Info(ctx, "ISBN enrichment leaving user-locked identifiers alone",
			"id", bookID, "isbn_locked", isbnLocked, "asin_locked", asinLocked)
	}
	hasISBN = hasISBN || isbnLocked
	hasASIN = hasASIN || asinLocked

	// Nothing to do if both are already present.
	if hasISBN && hasASIN {
		return false, nil
	}

	// Build search query from book title + author. When the canonical title is
	// blank, fall back to the Whisper-parsed intro title: a real title that can
	// pass the strict-title-match gate below. We deliberately do NOT fall back to
	// the author name the way SearchMetadataCore does -- there the goal is to get
	// results, but here every hit must satisfy IsStrictTitleMatch(title, r.Title),
	// which an author-name query fails by construction. A book with neither a
	// title nor a transcribed title simply cannot be enriched.
	title := effectiveEnrichTitle(book)
	author := s.resolveAuthor(book)

	updated := false
	bookErrBySource := map[string]int{}

	// --- ISBN enrichment ---
	isbnAttempted := false
	isbnGenuine := false
	if !hasISBN {
		isbnAttempted = true
		for _, src := range s.sources {
			isbn, isbnLen, serr := s.searchSourceForISBN(src, title, author)
			if serr != nil {
				bookErrBySource[src.Name()]++
				s.sampleSourceSearchError(ctx, bookID, title, src.Name(), serr)
				continue
			}
			isbnGenuine = true
			if isbn == "" {
				continue
			}
			// Only one of the two ISBN columns may be locked here (both locked
			// was handled above); Apply restores it if the hit lands there.
			restored := locks.Apply(book, func(b *database.Book) {
				if isbnLen == 13 {
					b.ISBN13 = &isbn
				} else {
					b.ISBN10 = &isbn
				}
			})
			if len(restored) > 0 {
				logging.Info(ctx, "ISBN enrichment hit a user-locked column; not written",
					"isbn", isbn, "title", title, "source", src.Name(), "locked", restored)
				continue
			}
			if _, err := s.db.UpdateBook(bookID, book); err != nil {
				return false, err
			}
			logging.Info(ctx, "ISBN enrichment found", "isbn", isbn, "title", title, "source", src.Name())
			updated = true
			break
		}
	}

	// --- ASIN enrichment ---
	asinAttempted := false
	asinGenuine := false
	if !hasASIN {
		for _, src := range s.sources {
			if src.Name() != "Audible" {
				continue
			}
			asinAttempted = true
			asin, serr := s.searchSourceForASIN(src, title, author)
			if serr != nil {
				bookErrBySource[src.Name()]++
				s.sampleSourceSearchError(ctx, bookID, title, src.Name(), serr)
				break
			}
			asinGenuine = true
			if asin == "" {
				break
			}
			book.ASIN = &asin
			if _, err := s.db.UpdateBook(bookID, book); err != nil {
				return updated, err
			}
			logging.Info(ctx, "ASIN enrichment found", "asin", asin, "title", title)
			updated = true
			break
		}
	}

	if len(bookErrBySource) > 0 {
		// allErrored: this book needed at least one identifier, and neither
		// loop it ran ever got a genuine (error-free) result -- the book
		// contributed zero information, so the caller must not count it as
		// a real "checked, found nothing" outcome.
		attemptedAny := isbnAttempted || asinAttempted
		gotGenuine := (isbnAttempted && isbnGenuine) || (asinAttempted && asinGenuine)
		return updated, &sourceSearchError{bySource: bookErrBySource, allErrored: attemptedAny && !gotGenuine}
	}

	return updated, nil
}

// isbnEnrichCursorKey is the STABLE storage key for the batch sweep's resume
// point. It is deliberately not the per-run opID: the sweep must carry its
// position from one nightly run to the next, and a per-run opID would reset it
// every run -- the exact bug this cursor fixes (the loop used to restart at
// offset 0 every run and never reach past the first `limit` no-ASIN books).
const isbnEnrichCursorKey = "metafetch:isbn-enrich-cursor"

// isbnEnrichBatchLimitSetting names the setting that overrides the batch limit.
// Absent/blank/unparseable -> defaultISBNEnrichBatchLimit. Reading it from a
// setting (not a code const) lets the throughput be tuned in prod without a
// deploy, which this repo gates on HEAD...origin/main == 0 0.
const isbnEnrichBatchLimitSetting = "isbn_enrichment_batch_limit"

const defaultISBNEnrichBatchLimit = 100

// isbnEnrichCursor is the persisted sweep position: the ID of the last book the
// previous run examined. The next run resumes strictly after it and wraps to the
// start of the library (AfterID == "") once the end is reached.
type isbnEnrichCursor struct {
	AfterID   string    `json:"after_id"`
	UpdatedAt time.Time `json:"updated_at"`
}

// EnrichMissingISBNs scans books missing ISBN/ASIN data and enriches up to a
// batch limit of them, resuming across runs via a persistent last-seen-book-ID
// cursor so successive runs sweep the whole library rather than re-walking the
// front. It returns the number of candidate books genuinely checked (a real,
// error-free search happened) and the number updated.
//
// checked deliberately excludes books where every provider source errored
// during search (throttled or circuit-open) -- those contributed no real
// information and would otherwise be indistinguishable from a book that
// genuinely has no ISBN/ASIN anywhere (SF-03). See the returned error: if
// every book attempted this run fell into that bucket, EnrichMissingISBNs
// returns a non-nil error wrapping ErrAllSourcesErrored so the run reports as
// failed through the caller's existing reporter instead of only logging.
//
// limit <= 0 means "resolve the limit from the isbn_enrichment_batch_limit
// setting, falling back to the default"; a positive limit is honoured verbatim
// (used by tests and any direct caller that wants an explicit bound).
//
// w and opID are optional -- if provided, each enriched book is submitted to the
// activity batcher instead of emitting a per-book log line. (opID is the op's
// per-run id for activity batching; it is NOT the cursor key.)
func (s *ISBNService) EnrichMissingISBNs(ctx context.Context, limit int, w *activity.Writer, opID string) (int, int, error) {
	if limit <= 0 {
		limit = s.resolveBatchLimit(ctx)
	}

	const pageSize = 250
	// attempted paces the batch (loop bound + resume position) and includes
	// every book EnrichBookISBN was called for, whether or not it got a
	// genuine result. checked is the subset that actually got a genuine
	// (error-free) result from at least one source -- what a caller should
	// read as "we searched and this is the true state". errored is the
	// complement: every source call for that book errored, so nothing was
	// learned about it (SF-03). attempted, not checked, bounds the loop so a
	// full provider outage still stops after `limit` books per run rather
	// than free-running through the whole library retrying nothing.
	attempted := 0
	checked := 0
	errored := 0
	updated := 0
	sourceErrTotals := map[string]int{}

	// Load the persisted sweep position. Fail-open: an unreadable cursor starts
	// the sweep from the top rather than aborting enrichment, but we surface the
	// resolved start so a cursor silently stuck at the front is visible.
	afterID := s.loadEnrichCursor(ctx)
	startAfterID := afterID
	logging.Info(ctx, "ISBN enrichment batch starting", "limit", limit, "resume_after_id", afterID)

	wrapped := false
	lastID := afterID
	pages := 0

	for attempted < limit {
		if ctx != nil && ctx.Err() != nil {
			return checked, updated, ctx.Err()
		}

		books, err := s.db.GetAllBooksFullFrom(afterID, pageSize)
		if err != nil {
			return checked, updated, err
		}
		if len(books) == 0 {
			// The first page came back empty. If we started from a non-empty
			// cursor, the cursored book is either the last in the library (a
			// legitimate end-of-library wrap) or it was deleted since we saved it
			// (GetAllBooksFullFrom ends iteration on an unknown afterID). Both wrap
			// to the top -- but a deleted-cursor case that recurs every run leaves
			// the sweep permanently stuck at the front while looking healthy, so
			// flag it distinctly rather than as an ordinary wrap.
			if pages == 0 && startAfterID != "" {
				logging.Warn(ctx, "ISBN enrichment cursor resolved to no books; wrapping to the start (cursored book at end-of-library or deleted)",
					"resume_after_id", startAfterID)
			}
			wrapped = true
			break
		}
		pages++

		for i := range books {
			// Per-book scan stand-down beat (a no-op beyond the ctx check when
			// the caller holds none): renews the hold and stops before the
			// book's write once it is lost.
			if ctx != nil {
				if err := opsregistry.ScanStandDownCheckpoint(ctx); err != nil {
					// Persist how far we got before the cancellation so the next
					// run resumes there rather than repeating this range.
					s.saveEnrichCursor(ctx, lastID)
					return checked, updated, err
				}
			}
			lastID = books[i].ID
			core := books[i].Core()
			if !needsIdentifierEnrichment(&core) {
				continue
			}

			attempted++
			found, err := s.EnrichBookISBN(ctx, books[i].ID)
			if found {
				updated++
				activity.LogBatch(w, opID, "isbn-enrich", "isbn-enrichment",
					activity.BatchItem{Name: books[i].Title, Detail: books[i].ID})
			}
			var se *sourceSearchError
			switch {
			case err == nil:
				checked++
			case errors.As(err, &se):
				// Real provider search errors, not discarded (SF-03): fold
				// into the run's per-source tally, and only count the book
				// as genuinely "checked" if some source call for it did
				// return a real (possibly empty) result.
				for name, n := range se.bySource {
					sourceErrTotals[name] += n
				}
				if se.allErrored {
					errored++
				} else {
					checked++
				}
			default:
				logging.Warn(ctx, "ISBN enrichment failed during batch scan", "id", books[i].ID, "error", err)
				checked++
			}
			if attempted >= limit {
				break
			}
		}

		// Advance to the next page from the last book we saw.
		afterID = lastID
		// If we stopped because we hit the batch limit, keep the position so the
		// next run resumes mid-library -- do NOT let a short final page wrap us
		// back to the top (that would reintroduce the front-only bug, limit-gated).
		if attempted >= limit {
			break
		}
		// A short page with budget to spare means the library is exhausted -> wrap.
		if len(books) < pageSize {
			wrapped = true
			break
		}
	}

	// Persist the new sweep position: "" to wrap to the top next run, else the
	// last book examined this run.
	next := lastID
	if wrapped {
		next = ""
	}
	s.saveEnrichCursor(ctx, next)

	// Record the swept window in the activity timeline, not just the log. Under
	// the cross-run cursor a run legitimately reports checked=0/updated=0 when it
	// swept a region that was already fully identified; without the window that
	// reads as "nothing to do" (and the scheduler tags updated==0 as a no-op).
	// The window makes it clear the sweep advanced.
	summary := fmt.Sprintf("ISBN enrichment swept %s→%s (wrapped=%t): checked %d, updated %d",
		cursorLabel(startAfterID), cursorLabel(next), wrapped, checked, updated)
	if errored > 0 {
		// SF-03: this is what makes a provider outage visible in the sweep's
		// own summary instead of reading identically to a legitimate
		// zero-result search. errored books are excluded from `checked`
		// above -- they never got a real determination.
		summary += fmt.Sprintf(", errored %d (every source errored, not counted as checked; per-source error counts %v)",
			errored, sourceErrTotals)
	}
	activity.EmitInfo(w, opID, "isbn-enrich", "isbn-enrichment", summary, activity.AlwaysShow)

	var runErr error
	if attempted > 0 && errored == attempted {
		// Every book attempted this run had every provider source error --
		// nothing was actually searched. Report the run as failed through
		// the existing reporter (both callers already do `if err != nil {
		// return err }`), rather than leaving a healthy-looking log line as
		// the only trace of a total provider outage.
		runErr = fmt.Errorf("%w: all %d book(s) attempted this run had every provider source error (per-source error counts: %v)",
			ErrAllSourcesErrored, errored, sourceErrTotals)
	}

	return checked, updated, runErr
}

// cursorLabel renders a sweep cursor for a human: the empty cursor is the start
// of the library, not a blank.
func cursorLabel(id string) string {
	if id == "" {
		return "start"
	}
	return id
}

// resolveBatchLimit reads the batch limit from settings, defaulting when the
// setting is absent (ErrSettingNotFound in prod, nil in the mock), blank, or
// unparseable. A store without GetSetting or an outright error also falls back to
// the default -- the limit is a tuning knob, never a reason to skip enrichment.
func (s *ISBNService) resolveBatchLimit(ctx context.Context) int {
	setting, err := s.db.GetSetting(isbnEnrichBatchLimitSetting)
	if err != nil {
		if !errors.Is(err, database.ErrSettingNotFound) {
			logging.Warn(ctx, "reading ISBN enrichment batch limit setting failed; using default",
				"error", err, "default", defaultISBNEnrichBatchLimit)
		}
		return defaultISBNEnrichBatchLimit
	}
	if setting == nil || strings.TrimSpace(setting.Value) == "" {
		return defaultISBNEnrichBatchLimit
	}
	n, err := strconv.Atoi(strings.TrimSpace(setting.Value))
	if err != nil || n <= 0 {
		logging.Warn(ctx, "ISBN enrichment batch limit setting is not a positive integer; using default",
			"value", setting.Value, "default", defaultISBNEnrichBatchLimit)
		return defaultISBNEnrichBatchLimit
	}
	return n
}

// loadEnrichCursor reads the persisted sweep position. Fail-open: any error or
// malformed blob starts the sweep from the top.
func (s *ISBNService) loadEnrichCursor(ctx context.Context) string {
	data, err := s.db.GetOperationState(isbnEnrichCursorKey)
	if err != nil {
		logging.Warn(ctx, "reading ISBN enrichment cursor failed; sweeping from the start", "error", err)
		return ""
	}
	if len(data) == 0 {
		return ""
	}
	var cur isbnEnrichCursor
	if err := json.Unmarshal(data, &cur); err != nil {
		logging.Warn(ctx, "ISBN enrichment cursor is malformed; sweeping from the start", "error", err)
		return ""
	}
	return cur.AfterID
}

// saveEnrichCursor persists the sweep position. A write error is logged but never
// propagated: failing to checkpoint should not fail the enrichment run.
func (s *ISBNService) saveEnrichCursor(ctx context.Context, afterID string) {
	data, err := json.Marshal(isbnEnrichCursor{AfterID: afterID, UpdatedAt: time.Now()})
	if err != nil {
		logging.Warn(ctx, "marshalling ISBN enrichment cursor failed", "error", err)
		return
	}
	if err := s.db.SaveOperationState(isbnEnrichCursorKey, data); err != nil {
		logging.Warn(ctx, "persisting ISBN enrichment cursor failed", "error", err)
	}
}

// effectiveEnrichTitle returns the title to search with: the canonical title if
// present, else the Whisper-parsed intro title. Returns "" only when neither
// exists (such a book cannot be enriched via title search).
func effectiveEnrichTitle(book *database.Book) string {
	if strings.TrimSpace(book.Title) != "" {
		return book.Title
	}
	if book.TranscribedTitle != nil && strings.TrimSpace(*book.TranscribedTitle) != "" {
		return *book.TranscribedTitle
	}
	return ""
}

// resolveAuthor returns the author name for the book, or "" if unknown.
func (s *ISBNService) resolveAuthor(book *database.Book) string {
	if book.AuthorID == nil {
		return ""
	}
	a, err := s.db.GetAuthorByID(*book.AuthorID)
	if err != nil || a == nil {
		return ""
	}
	return a.Name
}

// needsIdentifierEnrichment reports whether the batch enrichment scan should hand
// a book to EnrichBookISBN. A book qualifies when it is missing EITHER an ISBN or
// an ASIN — matching the per-book gate in queueISBNEnrichment (service_fetch.go)
// and the internal logic of EnrichBookISBN, which enriches ISBN and ASIN
// independently. Previously this ANDed only the two ISBN-empty checks and never
// looked at ASIN, so a book that had an ISBN but no ASIN was skipped before
// EnrichBookISBN (which would fetch the ASIN) was ever called — the batch path
// could never mint an ASIN for the ~2/3 of books that already carry an ISBN.
func needsIdentifierEnrichment(book *database.BookCore) bool {
	if book == nil {
		return false
	}
	needsISBN := (book.ISBN10 == nil || strings.TrimSpace(*book.ISBN10) == "") &&
		(book.ISBN13 == nil || strings.TrimSpace(*book.ISBN13) == "")
	needsASIN := book.ASIN == nil || strings.TrimSpace(*book.ASIN) == ""
	return needsISBN || needsASIN
}

// searchSourceForISBN queries a single metadata source and returns the first
// ISBN that matches the title strictly, along with its length (10 or 13).
//
// The returned error is non-nil only when no genuine result was obtained at
// all -- i.e. every call attempted for this source errored. A source that
// returns a real (possibly empty) result set at any point in the
// title+author -> title fallback is reported with a nil error, even if an
// earlier call in that fallback errored, because we did get a genuine
// determination. Previously this discarded the error entirely (SF-03),
// making "provider throttled/circuit-open" indistinguishable from "no such
// book at this source".
func (s *ISBNService) searchSourceForISBN(src metadata.MetadataSource, title, author string) (string, int, error) {
	var results []metadata.BookMetadata
	var err error
	ctx := context.Background()

	if author != "" {
		results, err = src.SearchByTitleAndAuthor(ctx, title, author)
	}
	if len(results) == 0 {
		results, err = src.SearchByTitle(ctx, title)
	}
	if err != nil {
		return "", 0, err
	}

	for _, r := range results {
		if !IsStrictTitleMatch(title, r.Title) {
			continue
		}
		if r.ISBN != "" {
			return r.ISBN, len(r.ISBN), nil
		}
	}
	return "", 0, nil
}

// searchSourceForASIN queries a single metadata source and returns the first
// ASIN that matches the title strictly. See searchSourceForISBN for how the
// returned error is derived.
func (s *ISBNService) searchSourceForASIN(src metadata.MetadataSource, title, author string) (string, error) {
	var results []metadata.BookMetadata
	var err error
	ctx := context.Background()

	if author != "" {
		results, err = src.SearchByTitleAndAuthor(ctx, title, author)
	}
	if len(results) == 0 {
		results, err = src.SearchByTitle(ctx, title)
	}
	if err != nil {
		return "", err
	}

	for _, r := range results {
		if IsStrictTitleMatch(title, r.Title) && r.ASIN != "" {
			return r.ASIN, nil
		}
	}
	return "", nil
}

// isStrictTitleMatch returns true if titles are close enough to be the same book.
func IsStrictTitleMatch(dbTitle, searchTitle string) bool {
	a := strings.ToLower(strings.TrimSpace(dbTitle))
	b := strings.ToLower(strings.TrimSpace(searchTitle))
	if a == "" || b == "" {
		return false
	}
	// Exact match
	if a == b {
		return true
	}
	// One is a prefix of the other (e.g., "Shadows of Self" matches "Shadows of Self: A Mistborn Novel")
	if strings.HasPrefix(a, b) || strings.HasPrefix(b, a) {
		shorterText := a
		longerText := b
		if len(shorterText) > len(longerText) {
			shorterText, longerText = longerText, shorterText
		}
		remainder := strings.TrimSpace(strings.TrimPrefix(longerText, shorterText))
		if remainder == "" {
			return true
		}
		if strings.HasPrefix(remainder, ":") ||
			strings.HasPrefix(remainder, "-") ||
			strings.HasPrefix(remainder, "—") {
			return true
		}

		// But only if the shorter one is at least 60% of the longer one's length
		shorter := len(a)
		longer := len(b)
		if shorter > longer {
			shorter, longer = longer, shorter
		}
		return float64(shorter)/float64(longer) >= 0.6
	}
	return false
}
