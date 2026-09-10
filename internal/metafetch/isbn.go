// file: internal/metafetch/isbn.go
// version: 1.9.0
// guid: 34290bd0-745e-4509-ad2d-e237785bb7ef
// last-edited: 2026-09-10

package metafetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
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
}

// NewISBNService creates an enrichment service that will search the
// given metadata sources for ISBN/ASIN data.
func NewISBNService(db isbnEnrichmentStore, sources []metadata.MetadataSource) *ISBNService {
	return &ISBNService{db: db, sources: sources}
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

	// --- ISBN enrichment ---
	if !hasISBN {
		for _, src := range s.sources {
			isbn, isbnLen := s.searchSourceForISBN(src, title, author)
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
	if !hasASIN {
		for _, src := range s.sources {
			if src.Name() != "Audible" {
				continue
			}
			asin := s.searchSourceForASIN(src, title, author)
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
// front. It returns the number of candidate books checked and the number updated.
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
	checked := 0
	updated := 0

	// Load the persisted sweep position. Fail-open: an unreadable cursor starts
	// the sweep from the top rather than aborting enrichment, but we surface the
	// resolved start so a cursor silently stuck at the front is visible.
	afterID := s.loadEnrichCursor(ctx)
	startAfterID := afterID
	logging.Info(ctx, "ISBN enrichment batch starting", "limit", limit, "resume_after_id", afterID)

	wrapped := false
	lastID := afterID
	pages := 0

	for checked < limit {
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
			if ctx != nil && ctx.Err() != nil {
				// Persist how far we got before the cancellation so the next run
				// resumes there rather than repeating this range.
				s.saveEnrichCursor(ctx, lastID)
				return checked, updated, ctx.Err()
			}
			lastID = books[i].ID
			core := books[i].Core()
			if !needsIdentifierEnrichment(&core) {
				continue
			}

			checked++
			found, err := s.EnrichBookISBN(ctx, books[i].ID)
			if err != nil {
				logging.Warn(ctx, "ISBN enrichment failed during batch scan", "id", books[i].ID, "error", err)
			} else if found {
				updated++
				activity.LogBatch(w, opID, "isbn-enrich", "isbn-enrichment",
					activity.BatchItem{Name: books[i].Title, Detail: books[i].ID})
			}
			if checked >= limit {
				break
			}
		}

		// Advance to the next page from the last book we saw.
		afterID = lastID
		// If we stopped because we hit the batch limit, keep the position so the
		// next run resumes mid-library -- do NOT let a short final page wrap us
		// back to the top (that would reintroduce the front-only bug, limit-gated).
		if checked >= limit {
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
	activity.EmitInfo(w, opID, "isbn-enrich", "isbn-enrichment",
		fmt.Sprintf("ISBN enrichment swept %s→%s (wrapped=%t): checked %d, updated %d",
			cursorLabel(startAfterID), cursorLabel(next), wrapped, checked, updated),
		activity.AlwaysShow)

	return checked, updated, nil
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
func (s *ISBNService) searchSourceForISBN(src metadata.MetadataSource, title, author string) (string, int) {
	var results []metadata.BookMetadata
	ctx := context.Background()

	if author != "" {
		results, _ = src.SearchByTitleAndAuthor(ctx, title, author)
	}
	if len(results) == 0 {
		results, _ = src.SearchByTitle(ctx, title)
	}

	for _, r := range results {
		if !IsStrictTitleMatch(title, r.Title) {
			continue
		}
		if r.ISBN != "" {
			return r.ISBN, len(r.ISBN)
		}
	}
	return "", 0
}

// searchSourceForASIN queries a single metadata source and returns the first
// ASIN that matches the title strictly.
func (s *ISBNService) searchSourceForASIN(src metadata.MetadataSource, title, author string) string {
	var results []metadata.BookMetadata
	ctx := context.Background()

	if author != "" {
		results, _ = src.SearchByTitleAndAuthor(ctx, title, author)
	}
	if len(results) == 0 {
		results, _ = src.SearchByTitle(ctx, title)
	}

	for _, r := range results {
		if IsStrictTitleMatch(title, r.Title) && r.ASIN != "" {
			return r.ASIN
		}
	}
	return ""
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
