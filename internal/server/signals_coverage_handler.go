// file: internal/server/signals_coverage_handler.go
// version: 1.1.0
// guid: ca3e529b-ef05-451d-8a67-8a643e16c176
// last-edited: 2026-09-12

package server

import (
	"errors"
	"runtime"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/gin-gonic/gin"
)

// BookSignalCoverage is the book-level half of the coverage report.
type BookSignalCoverage struct {
	PrimaryBooks      int    `json:"primary_books"`
	PrimaryBooksError string `json:"primary_books_error,omitempty"`
	// WithEmbedding counts stored book embedding vectors (any model). It can
	// exceed PrimaryBooks when vectors of since-merged books were never pruned.
	WithEmbedding  int    `json:"with_embedding"`
	EmbeddingError string `json:"embedding_error,omitempty"`
}

// SignalCoverageResponse is the payload of GET /api/v1/signals/coverage.
type SignalCoverageResponse struct {
	Files     *database.BookFileSignalCoverage `json:"files"`
	Books     BookSignalCoverage               `json:"books"`
	ElapsedMS int64                            `json:"elapsed_ms"`
}

// handleGetSignalCoverage reports per-signal coverage of book_file rows plus
// book-level embedding presence. Read-only: it never stats a file.
//
// GET /api/v1/signals/coverage[?deep=true]
//
// Default reads memdb row pointers across a NumCPU worker pool (sub-second on
// 742k rows); raw fingerprint and Seg0..6 are not in memdb, so they are listed
// under files.unavailable and the fingerprint-duration proxy stands in. deep=true
// scans Pebble for exact raw / Seg0..6 / failure-reason counts and takes minutes
// on a full library — opt-in only, never a silent fallback.
func (s *Server) handleGetSignalCoverage(c *gin.Context) {
	store := s.Ops()
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	deep := c.Query("deep") == "true" || c.Query("deep") == "1"
	workers := runtime.NumCPU()
	started := time.Now()

	var (
		files *database.BookFileSignalCoverage
		err   error
	)
	if ps := database.AsPebbleStore(store); ps != nil {
		files, err = ps.GetBookFileSignalCoverage(c.Request.Context(), deep, workers)
	} else {
		var cores []database.BookFileCore
		cores, err = store.GetAllBookFilesCore()
		if err == nil {
			files, err = database.CountBookFileSignalsFromCores(c.Request.Context(), cores, workers)
		}
	}
	if errors.Is(err, database.ErrMemDBNotReady) {
		httputil.RespondWithServiceUnavailable(c, err.Error())
		return
	}
	if err != nil {
		httputil.InternalError(c, "failed to compute signal coverage", err)
		return
	}

	resp := SignalCoverageResponse{Files: files}
	// A failed count is reported, not rendered as a zero denominator.
	if n, perr := store.CountPrimaryBooks(); perr != nil {
		resp.Books.PrimaryBooksError = perr.Error()
	} else {
		resp.Books.PrimaryBooks = n
	}
	if s.embeddingStore != nil {
		// A prefix walk over emb:v:book: — O(embeddings), not O(book_files).
		if n, eerr := s.embeddingStore.CountByType("book"); eerr != nil {
			resp.Books.EmbeddingError = eerr.Error()
		} else {
			resp.Books.WithEmbedding = n
		}
	} else {
		resp.Books.EmbeddingError = "embedding store not configured"
	}
	resp.ElapsedMS = time.Since(started).Milliseconds()
	httputil.RespondWithOK(c, resp)
}
