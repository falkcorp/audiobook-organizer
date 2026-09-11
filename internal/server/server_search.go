// file: internal/server/server_search.go
// version: 1.7.0
// guid: 12815699-f9ea-4788-9af3-2e854d710315
// last-edited: 2026-09-11

package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/deluge"
	"github.com/falkcorp/audiobook-organizer/internal/search"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
)

func (s *Server) SearchIndex() *search.BleveIndex {
	return s.searchIndex
}

// safeWriteDeps builds a tagger.SafeWriteDeps from the server's wired
// dependencies. Used by movement_atom_cleanup and any other server-package
// code that calls tag-writing functions directly (outside the metadata
// package path that has its own package-level deps).
func (s *Server) safeWriteDeps() tagger.SafeWriteDeps {
	if s.protectedPathCache == nil {
		return tagger.SafeWriteDeps{}
	}
	store := s.storeForWiring()
	importer := deluge.NewLibraryImporterAdapter(store, deluge.GetClient(), &config.AppConfig)
	return tagger.SafeWriteDeps{
		ProtectedCache: s.protectedPathCache,
		Importer:       importer,
	}
}

// searchBackfillStore is the store slice the bulk backfill reads. It is
// paging plus the three BATCH relation reads — deliberately not the
// per-book GetAuthorByID / GetSeriesByID / GetBookTags trio, so the
// compiler forbids the N+1 from coming back.
type searchBackfillStore interface {
	GetAllBooksFullFrom(afterID string, limit int) ([]database.Book, error)
	GetAuthorsByIDs(ids []int) (map[int]*database.Author, error)
	GetSeriesByIDs(ids []int) (map[int]*database.Series, error)
	GetBookTagsByBookIDs(bookIDs []string) (map[string][]string, error)
}

const (
	// searchBackfillPageSize is rows per GetAllBooksFullFrom call. Paging
	// is keyset (afterID), so pages are fetched serially by one producer;
	// the parallelism is across chunks, below.
	searchBackfillPageSize = 500
	// searchBackfillChunkSize is books per worker unit: three batch store
	// reads and one Bleve batch commit per chunk. 100 is inside Bleve's
	// recommended 100–1000 docs per batch and gives 5 chunks per page so
	// a page fans out across cores.
	searchBackfillChunkSize = 100
	// searchBackfillProgressEvery bounds how often a running backfill
	// logs its count, so a 100k-book build is visible without spamming.
	searchBackfillProgressEvery = 30 * time.Second
)

// buildSearchIndexIfEmpty runs a full reindex of the library when
// the search index has zero documents. Honors s.bgCtx so shutdown
// stops the backfill cleanly.
func (s *Server) buildSearchIndexIfEmpty() {
	if s.searchIndex == nil {
		return
	}
	count, err := s.searchIndex.DocCount()
	if err != nil {
		slog.Warn("search index DocCount", "err", err)
		return
	}
	if count > 0 {
		return
	}
	store := s.Ops()
	if store == nil {
		return
	}
	slog.Info("Search index empty — starting full backfill", "workers", runtime.NumCPU())
	start := time.Now()
	indexed, err := s.runSearchBackfill(s.bgCtx, store, runtime.NumCPU(),
		searchBackfillPageSize, searchBackfillChunkSize)
	switch {
	case err == nil:
		slog.Info("Search backfill complete", "indexed", indexed, "time", time.Since(start))
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		slog.Info("Search backfill canceled (bgCtx)", "indexed", indexed, "time", time.Since(start))
	default:
		slog.Warn("Search backfill stopped", "indexed", indexed, "time", time.Since(start), "err", err)
	}
}

// runSearchBackfill walks the whole library and indexes every book,
// returning how many documents were written.
//
// Shape (CLAUDE.md "Concurrency — Prefer Multi-Core Design"): one
// producer pages GetAllBooksFullFrom serially (keyset cursor), splits
// each page into chunks of chunkSize, and hands each chunk to an
// errgroup bounded at `workers`. g.Go blocks once every worker is busy,
// so the producer fetches page N+1 while page N is being indexed and
// at most a couple of pages are resident. Each worker does three BATCH
// store reads for its chunk (search.LoadBookRelations), builds the
// documents with zero store calls (search.BookToDocWithRelations), and
// commits them with one Bleve batch. Store traffic is therefore
// O(pages + chunks), never O(books).
//
// This is not a registry op (it runs inside server startup, before the
// registry has anything to resume), so registry.RunItems is not the
// vehicle; errgroup + SetLimit is the CLAUDE.md default for that case.
//
// Concurrent index writes are safe: bleve.Index (scorch) is documented
// goroutine-safe for Index/Batch/Delete — text analysis runs in the
// calling goroutine (that is the CPU we are spreading across cores) and
// segment introduction is serialized by scorch's own introducer. On our
// side BleveIndex.IndexBookBatch holds only the RLock of the open/close
// mutex, so N workers commit in parallel and only Close excludes them.
// Chunks are disjoint slices of one page and every book ID appears in
// exactly one chunk, so two workers never write the same doc ID.
func (s *Server) runSearchBackfill(ctx context.Context, store searchBackfillStore, workers, pageSize, chunkSize int) (int64, error) {
	if workers < 1 {
		workers = 1
	}
	if pageSize < 1 {
		pageSize = searchBackfillPageSize
	}
	if chunkSize < 1 || chunkSize > pageSize {
		chunkSize = pageSize
	}

	var indexed atomic.Int64
	start := time.Now()

	// Progress heartbeat so a long build is visible in the log.
	progressDone := make(chan struct{})
	var progressWG sync.WaitGroup
	progressWG.Add(1)
	go func() {
		defer progressWG.Done()
		t := time.NewTicker(searchBackfillProgressEvery)
		defer t.Stop()
		for {
			select {
			case <-progressDone:
				return
			case <-t.C:
				n := indexed.Load()
				elapsed := time.Since(start)
				rate := float64(n) / elapsed.Seconds()
				slog.Info("Search backfill progress", "indexed", n,
					"elapsed", elapsed.Round(time.Second), "books_per_sec", int(rate))
			}
		}
	}()
	defer func() {
		close(progressDone)
		progressWG.Wait()
	}()

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(workers)

	var pageErr error
	afterID := ""
producer:
	for {
		if err := gctx.Err(); err != nil {
			pageErr = err
			break
		}
		books, err := store.GetAllBooksFullFrom(afterID, pageSize)
		if err != nil {
			pageErr = fmt.Errorf("search backfill GetAllBooksFullFrom after %q: %w", afterID, err)
			break
		}
		if len(books) == 0 {
			break
		}
		for lo := 0; lo < len(books); lo += chunkSize {
			hi := min(lo+chunkSize, len(books))
			chunk := books[lo:hi:hi]
			// Blocks while all workers are busy — that is the back-pressure
			// that keeps paging from running ahead of indexing.
			g.Go(func() error {
				if err := gctx.Err(); err != nil {
					return err
				}
				indexed.Add(s.indexBookChunk(store, chunk))
				return gctx.Err()
			})
			if gctx.Err() != nil {
				break producer
			}
		}
		afterID = books[len(books)-1].ID
		if len(books) < pageSize {
			break
		}
	}
	err := g.Wait()
	if pageErr != nil && !errors.Is(pageErr, context.Canceled) {
		err = pageErr
	} else if err == nil {
		err = pageErr
	}
	return indexed.Load(), err
}

// indexBookChunk resolves one chunk's relations with three batch reads,
// builds its documents and commits them as one Bleve batch. Returns the
// number of documents that made it into the index.
//
// A failed relation read is logged and the chunk is still indexed with
// whatever resolved (the pre-batch code silently indexed a book with no
// author name when GetAuthorByID failed; this keeps that outcome and
// makes the failure visible). A failed batch commit falls back to
// indexing the chunk's documents one by one so a single bad document
// costs one row, not chunkSize rows — the same per-book granularity
// the sequential loop had.
func (s *Server) indexBookChunk(store searchBackfillStore, books []database.Book) int64 {
	if len(books) == 0 {
		return 0
	}
	rel, err := search.LoadBookRelations(store, books)
	if err != nil {
		slog.Warn("search backfill relations (indexing chunk with partial relations)",
			"first", books[0].ID, "last", books[len(books)-1].ID, "n", len(books), "err", err)
	}
	docs := make([]search.BookDocument, 0, len(books))
	for i := range books {
		docs = append(docs, search.BookToDocWithRelations(&books[i], rel))
	}
	err = s.searchIndex.IndexBookBatch(docs)
	if err == nil {
		return int64(len(docs))
	}
	slog.Warn("search backfill batch index failed; retrying chunk per book",
		"first", books[0].ID, "last", books[len(books)-1].ID, "n", len(books), "err", err)
	var ok int64
	for i := range docs {
		if err := s.searchIndex.IndexBook(docs[i]); err != nil {
			slog.Warn("search backfill index", "bookID", docs[i].BookID, "err", err)
			continue
		}
		ok++
	}
	return ok
}

// IndexBookByID reads a book (plus its related rows) and upserts
// the flat BookDocument into the search index. Best-effort: logs
// and returns nil if the index isn't open or the book is missing.
// Callers: handlers that create or update a book, plus the startup
// full-build goroutine.
func (s *Server) IndexBookByID(bookID string) error {
	if s.searchIndex == nil || bookID == "" {
		return nil
	}
	book, err := s.Ops().GetBookByID(bookID)
	if err != nil || book == nil {
		return err
	}
	return s.searchIndex.IndexBook(search.BookToDoc(s.Ops(), book))
}

// DeleteIndexedBook removes a book from the search index. Called
// after a book delete (soft or hard). Safe when the index isn't
// open.
func (s *Server) DeleteIndexedBook(bookID string) error {
	if s.searchIndex == nil || bookID == "" {
		return nil
	}
	return s.searchIndex.DeleteBook(bookID)
}

func (h *serverScanHooks) OnBookScanned(bookID, title string) {
	if h.activityService != nil {
		_ = h.activityService.Record(database.ActivityEntry{
			Tier:    "change",
			Type:    "scan",
			Level:   "info",
			Source:  "background",
			BookID:  bookID,
			Summary: fmt.Sprintf("Scan found: %s", title),
		})
	}
}

func (h *serverScanHooks) OnImportDedup(bookID string) {
	if h.dedupFn != nil {
		h.dedupFn(bookID)
	}
}

func (h *serverOrganizeHooks) OnCollision(currentBookID, occupantPath string) {
	if h.server.embeddingStore == nil || h.server.store == nil {
		return
	}
	h.server.bgWG.Go("organize-collision-hook", func() {
		occupant, err := h.server.store.GetBookByFilePath(occupantPath)
		if err != nil {
			slog.Warn("organize-collision hook lookup failed", "occupantPath", occupantPath, "err", err)
			return
		}
		if occupant == nil || occupant.ID == currentBookID {
			return
		}
		sim := 1.0
		if err := h.server.embeddingStore.UpsertCandidate(database.DedupCandidate{
			EntityType: "book",
			EntityAID:  currentBookID,
			EntityBID:  occupant.ID,
			Layer:      "exact",
			Similarity: &sim,
			Status:     "pending",
		}); err != nil {
			slog.Warn("organize-collision hook upsert candidate / failed", "currentBookID", currentBookID, "occupant", occupant.ID, "err", err)
			return
		}
		slog.Info("organize-collision created dedup candidate between and (occupant of )", "currentBookID", currentBookID, "occupant", occupant.ID, "occupantPath", occupantPath)
		h.server.markDuplicatesFlaggedDirty("upsert_candidate")
	})
}

// fireDedupOnImport runs the dedup engine's Layer 1 + Layer 2 checks for
// a freshly created book, in a bgWG-tracked goroutine so it doesn't
// block the caller and shutdown drains it before closing Pebble.
//
// This is the single entry point used by every CreateBook path —
// scanner imports (via ScanHooks.OnImportDedup), iTunes sync, manual
// book creation, etc. Having every create path fire the hook means new
// books get exact-match hash/ISBN/title checks against the whole
// library immediately, instead of waiting for a user-triggered Re-scan.
//
// In particular this catches the "iTunes sync creates a parallel row
// for a book we already have under audiobook-organizer/" bug — the
// Layer 1 file-hash check fires inside CheckBook, sees the match, and
// records a pending dedup candidate that surfaces in the UI.
//
// Safe to call even when the dedup engine is disabled — it's a no-op.
func (s *Server) fireDedupOnImport(bookID string) {
	if s.dedupEngine == nil || bookID == "" {
		return
	}
	s.bgWG.Go("dedup-on-import", func() {
		if _, err := s.dedupEngine.CheckBook(s.bgCtx, bookID); err != nil {
			slog.Warn("dedup-on-import CheckBook()", "bookID", bookID, "err", err)
		}
	})
}
