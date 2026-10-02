// file: internal/server/handlers/metadata_cache_stale.go
// version: 1.4.0
// guid: ba7b75e1-2940-4864-ac78-6a8982bcd9a3
// last-edited: 2026-10-02

package handlers

import (
	"context"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// This file holds the ONE definition of "a stale metadata-cache row", shared by
// the review summary's `stale` count (GetCacheReviewResults) and the
// refetch-all-stale resolver (StaleCachedBookIDs, called by POST
// /metadata/batch-fetch-candidates with {stale:true}).
//
// They were two computations until 2026-09-30, and they drifted: the chip
// counted every non-orphaned row past the TTL (3,511 on production) while the
// refetch button derived its set on the client from the reviewable bucket
// alone, so its confirm dialog read "Refetch 10 stale books?" and refetched
// 10. Both now go through loadCacheRows + cacheRowStale, so the number the
// chip shows and the set the button sends cannot disagree.

var metadataCacheLog = logger.New("handlers.metadata-cache")

// cacheRowBookReader is the book-store slice loadCacheRows needs: book reads,
// plus what metabatch.ResolveCandidateSearchQuery reads (files and live
// authors) to decide whether a row is searchable at all.
type cacheRowBookReader interface {
	GetBookByID(id string) (*database.Book, error)
	GetBooksByIDs(ids []string) ([]database.Book, error)
	metabatch.SearchQueryReader
}

// cacheRowCandidateReader is the metadata-cache slice loadCacheRows needs.
type cacheRowCandidateReader interface {
	ListCachedSummaries(ctx context.Context) ([]metafetch.MetadataCacheSummary, error)
	GetCachedCandidates(bookID string) (*metafetch.MetadataCandidateCache, bool, error)
}

// loadedCacheRow is one metadata-cache row whose book still resolves, with its
// cached candidate entry (nil when that read failed).
type loadedCacheRow struct {
	sum   metafetch.MetadataCacheSummary
	book  *database.Book
	entry *metafetch.MetadataCandidateCache
	// searchable is metabatch.ResolveCandidateSearchQuery(...).Usable: false
	// when the candidate fetch would skip the book with "no usable title"
	// instead of searching it.
	searchable bool
	// files is what the review row's book info takes from the book's file
	// rows, read in the same pass so the page never reads them again.
	files metabatch.BookFileFacts
}

// cacheRowSet is what loadCacheRows read.
type cacheRowSet struct {
	// rows is every summary whose book resolved, in summary order.
	rows []loadedCacheRow
	// orphaned counts summaries whose book no longer resolves.
	orphaned int
	// lookupBook serves books from the batch read, falling back to a point
	// read (and caching it) when the batch missed the row.
	lookupBook func(id string) *database.Book
}

// bookFilesBatchReader is the batch file read loadCacheRows uses when the
// store has it (database.Store does; with memdb published it is an index
// lookup per book, not a Pebble range scan). A store without it is read per
// book, as before.
type bookFilesBatchReader interface {
	GetBookFilesForIDsCore(bookIDs []string) (map[string][]database.BookFileCore, error)
}

// preloadedCandidateReader is metafetch.(*Service).GetCachedCandidatesPreloaded:
// GetCachedCandidates with the book and its file rows already in hand. A
// service without it is read through GetCachedCandidates, as before.
type preloadedCandidateReader interface {
	GetCachedCandidatesPreloaded(bookID string, book *database.Book, files database.BookFilesGetter, authors database.BookAuthorReader) (*metafetch.MetadataCandidateCache, bool, error)
}

// loadChunkSize is how many rows one worker of loadCacheRows takes at a
// time: one batch file read per chunk. Bounded so the rows held at once stay
// small (a whole-cache batch is ~40k books and hundreds of thousands of file
// rows), large enough that the per-call overhead vanishes.
const loadChunkSize = 256

// chunkFiles serves GetBookFiles for one chunk's books from a single batch
// read, and delegates everything else (authors, folder listings) to the
// store. A book the batch covered but holds no rows for has no files: that is
// an answer, not a miss.
type chunkFiles struct {
	cacheRowBookReader
	byBook map[string][]database.BookFile
}

func (c chunkFiles) GetBookFiles(bookID string) ([]database.BookFile, error) {
	if c.byBook == nil {
		return c.cacheRowBookReader.GetBookFiles(bookID)
	}
	return c.byBook[bookID], nil
}

// filesForChunk returns the reader one chunk's rows use for file reads: the
// batch result when the store can batch and the read succeeds, else the store
// itself (per-book reads, the old behaviour).
func filesForChunk(store cacheRowBookReader, rows []loadedCacheRow) chunkFiles {
	batch, ok := store.(bookFilesBatchReader)
	if !ok {
		return chunkFiles{cacheRowBookReader: store}
	}
	ids := make([]string, len(rows))
	for i := range rows {
		ids[i] = rows[i].book.ID
	}
	cores, err := batch.GetBookFilesForIDsCore(ids)
	if err != nil {
		metadataCacheLog.Warn("batch book-file read failed; falling back to per-book reads: %v", err)
		return chunkFiles{cacheRowBookReader: store}
	}
	byBook := make(map[string][]database.BookFile, len(ids))
	for _, id := range ids {
		byBook[id] = nil
	}
	for id, list := range cores {
		files := make([]database.BookFile, len(list))
		for i := range list {
			files[i] = list[i].AsBookFile()
		}
		byBook[id] = files
	}
	return chunkFiles{cacheRowBookReader: store, byBook: byBook}
}

// loadCacheRows lists every metadata-cache summary, resolves each one's book
// in ONE batch read (with a per-book fallback), drops the orphans, and reads
// every surviving row's cached candidates over a bounded worker pool.
//
// The batch book read replaced a GetBookByID per summary (2N point reads,
// measured at 21.7s and 35.2s on production). The candidate reads run over
// every row whatever page a caller asked for, because the counts span the
// whole set; reviewListConcurrency bounds that fan-out.
//
// Book files are read ONCE per row, in batches of loadChunkSize, and shared by
// the three consumers that each used to read them: the legacy candidate filter
// (its runtime), the search-title resolver (presentFiles), and the row's book
// info (metabatch.BookFileFacts, kept on the row). On production those were
// three Pebble range scans per book and most of a 119 s request.
func loadCacheRows(ctx context.Context, store cacheRowBookReader, svc cacheRowCandidateReader) (cacheRowSet, error) {
	summaries, err := svc.ListCachedSummaries(ctx)
	if err != nil {
		return cacheRowSet{}, err
	}

	bookIDs := make([]string, 0, len(summaries))
	for _, sum := range summaries {
		bookIDs = append(bookIDs, sum.BookID)
	}
	booksByID := make(map[string]*database.Book, len(summaries))
	if fetched, berr := store.GetBooksByIDs(bookIDs); berr == nil {
		for i := range fetched {
			booksByID[fetched[i].ID] = &fetched[i]
		}
	} else {
		metadataCacheLog.Warn("batch book fetch failed; falling back to per-book reads: %v", berr)
	}
	// Serves from the batch result and falls back to a point read only when
	// the batch missed the row (or the batch call itself failed), so a partial
	// batch degrades in behavior-preserving fashion rather than dropping rows.
	// Only called from this goroutine.
	lookupBook := func(id string) *database.Book {
		if b, ok := booksByID[id]; ok {
			return b
		}
		b, err := store.GetBookByID(id)
		if err != nil || b == nil {
			return nil
		}
		booksByID[id] = b
		return b
	}

	set := cacheRowSet{rows: make([]loadedCacheRow, 0, len(summaries)), lookupBook: lookupBook}
	for _, sum := range summaries {
		book := lookupBook(sum.BookID)
		if book == nil {
			set.orphaned++
			continue
		}
		set.rows = append(set.rows, loadedCacheRow{sum: sum, book: book})
	}

	preloaded, canPreload := svc.(preloadedCandidateReader)
	// One folder memo for the pass: a chapter set's rows share one folder
	// listing instead of one each (roots are the resolver's own concern).
	memo := metabatch.NewFolderMemo()
	var cg errgroup.Group
	cg.SetLimit(reviewListConcurrency)
	// Chunks are disjoint index ranges of set.rows, so no two workers write
	// the same row.
	for lo := 0; lo < len(set.rows); lo += loadChunkSize {
		hi := min(lo+loadChunkSize, len(set.rows))
		cg.Go(func() error {
			if ctx.Err() != nil {
				return nil
			}
			chunk := set.rows[lo:hi]
			files := filesForChunk(store, chunk)
			for i := range chunk {
				r := &chunk[i]
				var entry *metafetch.MetadataCandidateCache
				var cerr error
				if canPreload {
					entry, _, cerr = preloaded.GetCachedCandidatesPreloaded(r.sum.BookID, r.book, files, store)
				} else {
					entry, _, cerr = svc.GetCachedCandidates(r.sum.BookID)
				}
				if cerr == nil {
					r.entry = entry
				}
				// The same resolver fetchCandidateForBook runs before it searches.
				// For an ordinary title it is a text check; it reads the book's
				// files (and at most its authors) only for a title that needs
				// corroborating or a fallback, which is why it runs here in the
				// pool rather than in the serial pass that applies cacheRowStale.
				r.searchable = metabatch.ResolveCandidateSearchQueryMemo(files, r.book, memo).Usable
				r.files = metabatch.ReadBookFileFacts(files, r.book)
			}
			return nil // a per-entry failure skips that row, never the whole batch
		})
	}
	_ = cg.Wait()
	if err := ctx.Err(); err != nil {
		return cacheRowSet{}, err
	}
	return set, nil
}

// cacheRowLastChecked is when this book was last SEARCHED FOR, which is not
// the same as when its candidates were fetched. A book whose providers
// returned nothing an hour ago keeps its older candidates and their older
// FetchedAt (see metafetch.cacheSearchResponse), so dating staleness off
// FetchedAt alone would leave it permanently overdue and every refetch pass
// would pick it again forever -- "we pressed the stale button yesterday and
// they are still marked stale". LastEmptyFetchAt records that fruitless look.
func cacheRowLastChecked(entry *metafetch.MetadataCandidateCache, fetchedAt time.Time) time.Time {
	if entry != nil && entry.LastEmptyFetchAt != nil && entry.LastEmptyFetchAt.After(fetchedAt) {
		return *entry.LastEmptyFetchAt
	}
	return fetchedAt
}

// cacheRowStale reports whether a loaded row counts as stale: its last search
// is at or before freshCutoff (now - MetadataCacheTTL), AND a refetch would
// actually search for it. It is the ONE stale rule: the summary's `stale`
// count, each row's `stale` flag (which the web's stale chip view reads and
// does not re-derive), and the refetch-all-stale set all come from it.
//
// The second half excludes the two kinds of book the candidate fetch skips
// before it asks any provider (fetchCandidateForBook). Counting them made a
// backlog no refetch could ever clear -- the chip never reached zero and every
// click re-queued them:
//
//   - Books the owner marked "no match" (metafetch.IsMarkedNoMatch -- "do not
//     spend provider quota searching for, or offer, a match they rejected").
//     ClearMetadataNoMatch makes such a book eligible again.
//   - Books with no usable search title (metabatch.ResolveCandidateSearchQuery
//     not Usable: an empty, placeholder or chapter-fragment title with no
//     transcribed or folder fallback). A retitle or a transcription makes it
//     searchable, and it is counted from then on.
//
// A row with no readable cache entry is dated off its summary's FetchedAt.
func cacheRowStale(r loadedCacheRow, freshCutoff time.Time) bool {
	if metafetch.IsMarkedNoMatch(r.book.MetadataReviewStatus) || !r.searchable {
		return false
	}
	return !cacheRowLastChecked(r.entry, r.sum.FetchedAt).After(freshCutoff)
}

// StaleCacheBookReader is the book-store slice StaleCachedBookIDs needs.
type StaleCacheBookReader = cacheRowBookReader

// StaleCacheCandidateReader is the metadata-cache slice StaleCachedBookIDs
// needs.
type StaleCacheCandidateReader = cacheRowCandidateReader

// StaleCachedBookIDs returns exactly the books GetCacheReviewResults counts in
// its summary's `stale`: every non-orphaned cache row, reviewable or not
// (no_candidates, resolved_no_candidates and decode-error rows included),
// whose last search is past MetadataCacheTTL and that a refetch would search.
// Same loader, same predicate, one clock read.
func StaleCachedBookIDs(ctx context.Context, store StaleCacheBookReader, svc StaleCacheCandidateReader) ([]string, error) {
	set, err := loadCacheRows(ctx, store, svc)
	if err != nil {
		return nil, err
	}
	freshCutoff := time.Now().Add(-database.MetadataCacheTTL)
	ids := make([]string, 0)
	for _, r := range set.rows {
		if cacheRowStale(r, freshCutoff) {
			ids = append(ids, r.sum.BookID)
		}
	}
	return ids, nil
}
