// file: internal/server/signals_coverage_handler.go
// version: 1.4.0
// guid: ca3e529b-ef05-451d-8a67-8a643e16c176
// last-edited: 2026-09-19

package server

import (
	"context"
	"errors"
	"runtime"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
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
	// SignatureEra splits live books by book-signature era (deep=true only:
	// memdb strips the signature fields, so it is a Pebble read).
	SignatureEra      *database.BookSignatureEraCoverage `json:"signature_era,omitempty"`
	SignatureEraError string                             `json:"signature_era_error,omitempty"`
}

// SignalCoverageResponse is the payload of GET /api/v1/signals/coverage.
type SignalCoverageResponse struct {
	Files *database.BookFileSignalCoverage `json:"files"`
	Books BookSignalCoverage               `json:"books"`
	// Windows is the windowed-fingerprint census, present only with
	// ?windows=true. WindowsError says why it is absent when it was asked for.
	Windows      *database.FingerprintWindowCoverage `json:"windows,omitempty"`
	WindowsError string                              `json:"windows_error,omitempty"`
	ElapsedMS    int64                               `json:"elapsed_ms"`
}

// windowToolVersionTimeout bounds the two `-version` execs a deep window
// census makes to learn which tool versions count as current.
const windowToolVersionTimeout = 15 * time.Second

// handleGetSignalCoverage reports per-signal coverage of book_file rows plus
// book-level embedding presence. Read-only: it never stats a file.
//
// GET /api/v1/signals/coverage[?deep=true][&windows=true]
//
// Default reads memdb row pointers across a NumCPU worker pool (sub-second on
// 742k rows); raw fingerprint and Seg0..6 are not in memdb, so they are listed
// under files.unavailable and the fingerprint-duration proxy stands in. deep=true
// scans Pebble for exact raw / Seg0..6 / failure-reason counts and takes minutes
// on a full library — opt-in only, never a silent fallback.
//
// windows=true adds the windowed-fingerprint census (present files with a
// window, a tombstone, or neither, split by backfill tier). It walks the fpwin:
// keyspace, so it is opt-in too: keys only by default, and with deep=true the
// window values are decoded to tell current windows from stale ones.
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
	// 409, not a shared result: a second caller would otherwise sit inside
	// its request for the minutes the first scan takes.
	if errors.Is(err, database.ErrDeepCoverageBusy) {
		httputil.RespondWithConflict(c, err.Error())
		return
	}
	if err != nil {
		httputil.InternalError(c, "failed to compute signal coverage", err)
		return
	}

	resp := SignalCoverageResponse{Files: files}
	if c.Query("windows") == "true" || c.Query("windows") == "1" {
		ps := database.AsPebbleStore(store)
		if ps == nil {
			resp.WindowsError = "fingerprint windows are stored only in the Pebble store"
		} else {
			crit, toolErr := s.windowCoverageCriteria(c.Request.Context(), deep)
			win, werr := ps.GetFingerprintWindowCoverage(c.Request.Context(), deep, crit, workers)
			switch {
			case errors.Is(werr, database.ErrMemDBNotReady):
				httputil.RespondWithServiceUnavailable(c, werr.Error())
				return
			case errors.Is(werr, database.ErrDeepCoverageBusy):
				httputil.RespondWithConflict(c, werr.Error())
				return
			case werr != nil:
				httputil.InternalError(c, "failed to compute fingerprint window coverage", werr)
				return
			}
			if toolErr != "" && win.Unavailable != nil {
				win.Unavailable["tool_version_currency"] = "current tool versions are unknown (" + toolErr + "), so currency compares pipeline and window set only"
			}
			resp.Windows = win
		}
	}
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
	switch ps := database.AsPebbleStore(store); {
	case ps == nil:
		resp.Books.SignatureEraError = "book signatures are counted only on the Pebble store"
	case !deep:
		resp.Books.SignatureEraError = "book signature fields are stripped from memdb; pass deep=true"
	default:
		era, eerr := ps.CountBookSignatureEras(c.Request.Context(), workers)
		switch {
		case errors.Is(eerr, database.ErrDeepCoverageBusy):
			httputil.RespondWithConflict(c, eerr.Error())
			return
		case eerr != nil:
			httputil.InternalError(c, "failed to count book signature eras", eerr)
			return
		}
		resp.Books.SignatureEra = era
	}
	resp.ElapsedMS = time.Since(started).Milliseconds()
	httputil.RespondWithOK(c, resp)
}

// windowCoverageCriteria is what a current window must match: this build's
// pipeline and window set and, on the deep path, the tool versions the server
// would stamp today. A failed version lookup is returned as a reason and the
// versions are left empty, which the census reports as pipeline-only currency
// rather than as every window being stale.
func (s *Server) windowCoverageCriteria(ctx context.Context, deep bool) (database.WindowCoverageCriteria, string) {
	crit := database.WindowCoverageCriteria{
		Pipeline:  fingerprint.WindowPipelineID,
		WindowSet: fingerprint.WindowSetWS1,
	}
	if !deep {
		return crit, ""
	}
	if s.toolRegistry == nil {
		return crit, "tool registry not configured"
	}
	ctx, cancel := context.WithTimeout(ctx, windowToolVersionTimeout)
	defer cancel()
	tools, err := fingerprint.ResolveWindowTools(ctx, s.toolRegistry)
	if err != nil {
		return crit, err.Error()
	}
	crit.FpcalcVersion = tools.Versions.Fpcalc
	crit.FFmpegVersion = tools.Versions.FFmpeg
	return crit, ""
}
