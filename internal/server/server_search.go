// file: internal/server/server_search.go
// version: 1.5.1
// guid: 12815699-f9ea-4788-9af3-2e854d710315
// last-edited: 2026-07-03

package server

import (
	"fmt"
	"log/slog"
	"time"

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
	store := s.Store()
	importer := deluge.NewLibraryImporterAdapter(store, deluge.GetClient(), &config.AppConfig)
	return tagger.SafeWriteDeps{
		ProtectedCache: s.protectedPathCache,
		Importer:       importer,
	}
}

// buildSearchIndexIfEmpty runs a full reindex of the library when
// the search index has zero documents. Honors s.bgCtx so shutdown
// stops the backfill cleanly. Page size matches the existing
// backfill code to keep memory bounded.
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
	store := s.Store()
	if store == nil {
		return
	}
	slog.Info("Search index empty — starting full backfill")
	start := time.Now()
	indexed := 0
	const pageSize = 500
	afterID := ""
	for {
		select {
		case <-s.bgCtx.Done():
			slog.Info("Search backfill canceled at books (bgCtx)", "indexed", indexed)
			return
		default:
		}
		books, err := store.GetAllBooksFrom(afterID, pageSize)
		if err != nil {
			slog.Warn("search backfill GetAllBooksFrom", "err", err)
			return
		}
		if len(books) == 0 {
			break
		}
		for i := range books {
			select {
			case <-s.bgCtx.Done():
				slog.Info("Search backfill canceled at books", "indexed", indexed)
				return
			default:
			}
			doc := search.BookToDoc(store, &books[i])
			if err := s.searchIndex.IndexBook(doc); err != nil {
				slog.Warn("search backfill index", "bookID", books[i].ID, "err", err)
				continue
			}
			indexed++
		}
		afterID = books[len(books)-1].ID
		if len(books) < pageSize {
			break
		}
	}
	slog.Info("Search backfill complete books in", "indexed", indexed, "time", time.Since(start))
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
	book, err := s.Store().GetBookByID(bookID)
	if err != nil || book == nil {
		return err
	}
	return s.searchIndex.IndexBook(search.BookToDoc(s.Store(), book))
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
	h.server.bgWG.Add("organize-collision-hook")
	go func() {
		defer h.server.bgWG.Done("organize-collision-hook")
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
	}()
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
	s.bgWG.Add("dedup-on-import")
	go func() {
		defer s.bgWG.Done("dedup-on-import")
		if _, err := s.dedupEngine.CheckBook(s.bgCtx, bookID); err != nil {
			slog.Warn("dedup-on-import CheckBook()", "bookID", bookID, "err", err)
		}
	}()
}
