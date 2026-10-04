// file: internal/server/handlers/metadata_cache_stale.go
// version: 1.10.0
// guid: ba7b75e1-2940-4864-ac78-6a8982bcd9a3
// last-edited: 2026-10-04

package handlers

import (
	"context"
	"encoding/json"
	"sort"
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
// chip shows and the set the button sends are the same computation.
//
// Since 2026-10-02 the chip's count is computed over the review snapshot
// (metadata_cache_snapshot.go) while StaleCachedBookIDs loads fresh. Only the
// book's own row is live in the count (review status, so the "no match" leg);
// everything else the stale rule reads comes from the snapshot: the cache
// row's dates (lastChecked) and the row's searchability, which the resolver
// derived from the book's title, files and folder siblings at build time. The
// two can therefore differ until the next rebuild, which follows the next
// request after any cache write (the write counter), any apply or clear
// no-match, or reviewSnapshotMaxAge -- the last being the only trigger for a
// retitle or a file change that alters searchability without touching the
// cache.

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

// loadedCacheRow is one metadata-cache row whose book still resolves, reduced
// to what the review listing and the stale rule read. The cache entry itself
// is not kept: a whole-cache load holds ~40k of these, and an entry carries
// up to ten candidates with descriptions while only the first is ever read.
type loadedCacheRow struct {
	sum  metafetch.MetadataCacheSummary
	book *database.Book
	// candidateCount is how many candidates the entry holds after the legacy
	// filter; 0 when it holds none or the read failed.
	candidateCount int
	// first is the entry's first candidate, still encoded; nil when
	// candidateCount is 0.
	first json.RawMessage
	// lastChecked is cacheRowLastChecked(entry, sum.FetchedAt): when the
	// book was last searched for.
	lastChecked time.Time
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
	// orphanIDs is every summary whose book no longer resolves.
	orphanIDs []string
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
	if files, ok := c.byBook[bookID]; ok {
		return files, nil
	}
	// Not one of this chunk's books (or no batch): read it from the store.
	return c.cacheRowBookReader.GetBookFiles(bookID)
}

// filesForChunk returns the reader one chunk's rows use for file reads: the
// batch result when the store can batch and the read succeeds, else the store
// itself (per-book reads, the old behaviour).
func filesForChunk(store cacheRowBookReader, rows []loadedCacheRow) chunkFiles {
	batch, ok := store.(bookFilesBatchReader)
	if !ok {
		return chunkFiles{cacheRowBookReader: store}
	}
	ids := make([]string, 0, len(rows))
	for i := range rows {
		if rows[i].book != nil { // an id load's orphans have no files to read
			ids = append(ids, rows[i].book.ID)
		}
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
		// GetBookFiles' order (disc, track, path): the book info takes the
		// first row's iTunes path, and the batch read promises no order.
		sort.Slice(files, func(i, j int) bool {
			if files[i].DiscNumber != files[j].DiscNumber {
				return files[i].DiscNumber < files[j].DiscNumber
			}
			if files[i].TrackNumber != files[j].TrackNumber {
				return files[i].TrackNumber < files[j].TrackNumber
			}
			return files[i].FilePath < files[j].FilePath
		})
		byBook[id] = files
	}
	return chunkFiles{cacheRowBookReader: store, byBook: byBook}
}

// cacheRowLoader is the per-row half of loading cache rows, shared by the
// summary-driven whole-cache load (loadCacheRows) and the explicit-id load the
// review snapshot's incremental build uses (loadCacheRowsByID): the cached
// entry read (through GetCachedCandidatesPreloaded when the service has it),
// the legacy filter, the search-title resolver and the row's file facts.
type cacheRowLoader struct {
	store      cacheRowBookReader
	svc        cacheRowCandidateReader
	preloaded  preloadedCandidateReader
	canPreload bool
	// memo is one folder memo for the pass: a chapter set's rows share one
	// folder listing instead of one each (roots are the resolver's own
	// concern).
	memo *metabatch.FolderMemo
}

func newCacheRowLoader(store cacheRowBookReader, svc cacheRowCandidateReader) *cacheRowLoader {
	l := &cacheRowLoader{store: store, svc: svc, memo: metabatch.NewFolderMemo(store)}
	l.preloaded, l.canPreload = svc.(preloadedCandidateReader)
	return l
}

// readEntry reads r's cache entry (r.book may be nil); (nil, nil) when the
// row is gone.
func (l *cacheRowLoader) readEntry(r *loadedCacheRow, files chunkFiles) (*metafetch.MetadataCandidateCache, error) {
	if l.canPreload {
		entry, _, err := l.preloaded.GetCachedCandidatesPreloaded(r.sum.BookID, r.book, files, l.store)
		return entry, err
	}
	entry, _, err := l.svc.GetCachedCandidates(r.sum.BookID)
	return entry, err
}

// fill completes r (its book set) from its entry, nil when the read failed:
// the row is then dated off its summary and holds no candidate.
func (l *cacheRowLoader) fill(r *loadedCacheRow, entry *metafetch.MetadataCandidateCache, files chunkFiles) {
	r.lastChecked = cacheRowLastChecked(entry, r.sum.FetchedAt)
	if entry != nil && len(entry.Candidates) > 0 {
		r.candidateCount = len(entry.Candidates)
		// json.RawMessage decoding copies each element, so holding the first
		// does not keep the other nine alive.
		r.first = entry.Candidates[0]
	}
	// The same resolver fetchCandidateForBook runs before it searches. For an
	// ordinary title it is a text check; it reads the book's files (and at
	// most its authors) only for a title that needs corroborating or a
	// fallback, which is why it runs here in the pool rather than in the
	// serial pass that applies cacheRowStale.
	r.searchable = metabatch.ResolveCandidateSearchQueryMemo(files, r.book, l.memo).Usable
	r.files = metabatch.ReadBookFileFacts(files, r.book)
}

// forEachChunk runs fn over disjoint loadChunkSize ranges of rows on a bounded
// pool (reviewListConcurrency), with one batch file read per chunk. No two
// workers write the same row. A cancelled ctx stops the work and is returned.
func (l *cacheRowLoader) forEachChunk(ctx context.Context, rows []loadedCacheRow, fn func(lo int, chunk []loadedCacheRow, files chunkFiles)) error {
	var cg errgroup.Group
	cg.SetLimit(reviewListConcurrency)
	for lo := 0; lo < len(rows); lo += loadChunkSize {
		hi := min(lo+loadChunkSize, len(rows))
		cg.Go(func() error {
			if ctx.Err() != nil {
				return nil
			}
			chunk := rows[lo:hi]
			fn(lo, chunk, filesForChunk(l.store, chunk))
			return nil // a per-entry failure skips that row, never the whole batch
		})
	}
	_ = cg.Wait()
	return ctx.Err()
}

// lookupBooks reads the books of ids in ONE batch read, with a point read for
// any id the batch missed (or every id when the batch call itself failed), so
// a partial batch degrades in behavior-preserving fashion rather than
// dropping rows. The lookup returns (nil, nil) for a book that does not
// exist and the error of a point read that failed: the two must not be
// confused, since an orphan is published for as long as the snapshot lives.
// The returned lookup is for the calling goroutine only.
func lookupBooks(store cacheRowBookReader, ids []string) func(id string) (*database.Book, error) {
	booksByID := make(map[string]*database.Book, len(ids))
	if fetched, berr := store.GetBooksByIDs(ids); berr == nil {
		for i := range fetched {
			booksByID[fetched[i].ID] = &fetched[i]
		}
	} else {
		metadataCacheLog.Warn("batch book fetch failed; falling back to per-book reads: %v", berr)
	}
	return func(id string) (*database.Book, error) {
		if b, ok := booksByID[id]; ok {
			return b, nil
		}
		b, err := store.GetBookByID(id)
		if err != nil {
			return nil, err
		}
		if b != nil {
			booksByID[id] = b
		}
		return b, nil
	}
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
	lookupBook := lookupBooks(store, bookIDs)

	set := cacheRowSet{rows: make([]loadedCacheRow, 0, len(summaries))}
	bookReadErrors := 0
	for _, sum := range summaries {
		book, berr := lookupBook(sum.BookID)
		if berr != nil {
			// As before the id loader: counted as an orphan for this load,
			// and said so once.
			bookReadErrors++
		}
		if book == nil {
			set.orphanIDs = append(set.orphanIDs, sum.BookID)
			continue
		}
		set.rows = append(set.rows, loadedCacheRow{sum: sum, book: book})
	}

	if bookReadErrors > 0 {
		metadataCacheLog.Warn("review rows: %d book point reads failed; those rows are counted orphaned until the next build", bookReadErrors)
	}
	l := newCacheRowLoader(store, svc)
	err = l.forEachChunk(ctx, set.rows, func(_ int, chunk []loadedCacheRow, files chunkFiles) {
		for i := range chunk {
			r := &chunk[i]
			entry, cerr := l.readEntry(r, files)
			if cerr != nil {
				entry = nil
			}
			l.fill(r, entry, files)
		}
	})
	if err != nil {
		return cacheRowSet{}, err
	}
	return set, nil
}

// cacheRowsByID is what loadCacheRowsByID read: every asked id lands in
// exactly one of the four.
type cacheRowsByID struct {
	// rows is every id whose cache entry and book both resolve, in asked
	// order, with the summary derived from the entry (BookID, FetchedAt,
	// CandidateCount after the legacy filter).
	rows []loadedCacheRow
	// orphanIDs is every id whose cache entry exists but whose book does not.
	orphanIDs []string
	// goneIDs is every id with no cache entry.
	goneIDs []string
	// failedIDs is every id whose cache entry or book read failed: the
	// caller cannot tell whether the row exists or is an orphan, and must not
	// publish a snapshot that guesses (reviewSnapshotBuilder falls back to a
	// full build).
	failedIDs []string
}

// loadCacheRowsByID is loadCacheRows for an explicit set of book ids: the
// review snapshot's incremental build re-reads only the rows the change logs
// name, and must learn which of them are gone, orphaned, or rows again. The
// same batch book read, chunked file reads and per-row work as the whole-cache
// load, over the ids instead of the key listing.
func loadCacheRowsByID(ctx context.Context, store cacheRowBookReader, svc cacheRowCandidateReader, ids []string) (cacheRowsByID, error) {
	lookupBook := lookupBooks(store, ids)
	rows := make([]loadedCacheRow, len(ids))
	const (
		outcomeRow = iota
		outcomeOrphan
		outcomeGone
		outcomeFailed
	)
	outcome := make([]int, len(ids))
	for i, id := range ids {
		book, berr := lookupBook(id)
		if berr != nil {
			outcome[i] = outcomeFailed
		}
		rows[i] = loadedCacheRow{sum: metafetch.MetadataCacheSummary{BookID: id}, book: book}
	}
	l := newCacheRowLoader(store, svc)
	err := l.forEachChunk(ctx, rows, func(lo int, chunk []loadedCacheRow, files chunkFiles) {
		for i := range chunk {
			r := &chunk[i]
			if outcome[lo+i] == outcomeFailed {
				continue // the book read failed: nothing more to learn
			}
			entry, cerr := l.readEntry(r, files)
			switch {
			case cerr != nil:
				outcome[lo+i] = outcomeFailed
			case entry == nil:
				outcome[lo+i] = outcomeGone
			case r.book == nil:
				outcome[lo+i] = outcomeOrphan
			default:
				r.sum = metafetch.MetadataCacheSummary{BookID: r.sum.BookID, FetchedAt: entry.FetchedAt, CandidateCount: len(entry.Candidates)}
				l.fill(r, entry, files)
			}
		}
	})
	if err != nil {
		return cacheRowsByID{}, err
	}
	var out cacheRowsByID
	for i, id := range ids {
		switch outcome[i] {
		case outcomeRow:
			out.rows = append(out.rows, rows[i])
		case outcomeOrphan:
			out.orphanIDs = append(out.orphanIDs, id)
		case outcomeGone:
			out.goneIDs = append(out.goneIDs, id)
		case outcomeFailed:
			out.failedIDs = append(out.failedIDs, id)
		}
	}
	return out, nil
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
	return !r.lastChecked.After(freshCutoff)
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
