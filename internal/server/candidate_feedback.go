// file: internal/server/candidate_feedback.go
// version: 1.0.0
// guid: 432e499b-32d5-468b-9799-b4874d2e0f55
// last-edited: 2026-10-07

package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// Metadata-candidate feedback endpoints (owner request 2026-10-07): the
// Candidates view's per-row thumbs-down ("not the best match") and the
// implicit thumbs-up an Apply records, kept as scoring training data. See
// internal/database/candidate_feedback.go for what is stored and why.
//
//	POST   /metadata/candidate-feedback       upsert one label
//	DELETE /metadata/candidate-feedback/:id   undo (?label= limits it to that label)
//	GET    /metadata/candidate-feedback       JSONL export (?book_id= filters)

// candidateFeedbackStore wraps the main store's raw key space.
func (s *Server) candidateFeedbackStore() *database.CandidateFeedbackStore {
	return database.NewCandidateFeedbackStore(s.storeForWiring())
}

type candidateFeedbackRequest struct {
	BookID string `json:"book_id"`
	Label  string `json:"label"`
	Query  struct {
		Title  string `json:"title"`
		Author string `json:"author"`
		Browse bool   `json:"browse"`
	} `json:"query"`
	Candidate   metafetch.MetadataCandidate `json:"candidate"`
	Rank        int                         `json:"rank"`
	ResultCount int                         `json:"result_count"`
}

func (s *Server) handlePutCandidateFeedback(c *gin.Context) {
	var req candidateFeedbackRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	mode := database.CandidateFeedbackModeBook
	if req.Query.Browse {
		mode = database.CandidateFeedbackModeBrowse
	}
	cand := req.Candidate
	fb := database.CandidateFeedback{
		BookID: req.BookID,
		Label:  database.CandidateFeedbackLabel(req.Label),
		Query: database.CandidateFeedbackQuery{
			Title:  strings.TrimSpace(req.Query.Title),
			Author: strings.TrimSpace(req.Query.Author),
			Mode:   mode,
		},
		Candidate: database.CandidateFeedbackCandidate{
			Source:         cand.Source,
			ASIN:           cand.ASIN,
			ISBN:           cand.ISBN,
			ISBN10:         cand.ISBN10,
			ISBN13:         cand.ISBN13,
			Title:          cand.Title,
			Author:         cand.Author,
			Narrator:       cand.Narrator,
			Series:         cand.Series,
			SeriesPosition: cand.SeriesPosition,
			Publisher:      cand.Publisher,
			Year:           cand.Year,
			DurationSec:    cand.DurationSec,
			FromCatalog:    cand.FromCatalog,
			SourceHash:     metafetch.CandidateSourceHash(cand),
		},
		Score:       cand.Score,
		Rank:        req.Rank,
		ResultCount: req.ResultCount,
	}
	if cand.ScoreBreakdown != nil {
		raw, err := json.Marshal(cand.ScoreBreakdown)
		if err != nil {
			httputil.RespondWithBadRequest(c, "score_breakdown: "+err.Error())
			return
		}
		fb.ScoreBreakdown = raw
	}
	if u, ok := auth.UserFromContext(c.Request.Context()); ok && u != nil {
		fb.UserID, fb.Username = u.ID, u.Username
	}
	saved, err := s.candidateFeedbackStore().Put(fb, time.Now())
	if errors.Is(err, database.ErrCandidateFeedbackInvalid) {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	if err != nil {
		slog.Error("candidate feedback write failed", "book", logger.SanitizeLogValue(req.BookID), "error", err)
		httputil.RespondWithInternalError(c, "could not record candidate feedback")
		return
	}
	c.JSON(http.StatusOK, saved)
}

func (s *Server) handleDeleteCandidateFeedback(c *gin.Context) {
	label := database.CandidateFeedbackLabel(c.Query("label"))
	if label != "" && label != database.CandidateFeedbackNegative && label != database.CandidateFeedbackPositive {
		httputil.RespondWithBadRequest(c, "label must be negative or positive")
		return
	}
	id := c.Param("id")
	removed, err := s.candidateFeedbackStore().Delete(id, label)
	if err != nil {
		slog.Error("candidate feedback delete failed", "id", logger.SanitizeLogValue(id), "error", err)
		httputil.RespondWithInternalError(c, "could not remove candidate feedback")
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "removed": removed})
}

// handleExportCandidateFeedback streams every label as JSON Lines, one record
// per line, paging the key space so memory does not grow with the dataset.
func (s *Server) handleExportCandidateFeedback(c *gin.Context) {
	bookID := c.Query("book_id")
	if strings.ContainsAny(bookID, ":/") {
		// Checked before the stream headers go on, so the 400 is plain JSON.
		httputil.RespondWithBadRequest(c, "book_id may not contain ':' or '/'")
		return
	}
	c.Header("Content-Type", "application/x-ndjson")
	c.Header("Content-Disposition", `attachment; filename="candidate-feedback.jsonl"`)
	c.Status(http.StatusOK)
	enc := json.NewEncoder(c.Writer)
	n := 0
	err := s.candidateFeedbackStore().Each(bookID, func(fb *database.CandidateFeedback) error {
		n++
		return enc.Encode(fb)
	})
	if err == nil {
		return
	}
	slog.Error("candidate feedback export failed", "records_written", n, "error", err)
	if c.Writer.Written() {
		// Mid-stream: the status is sent, so the only signal left is a
		// truncated body and the log line above.
		return
	}
	c.Header("Content-Type", "application/json")
	c.Header("Content-Disposition", "")
	httputil.RespondWithInternalError(c, "could not export candidate feedback")
}
