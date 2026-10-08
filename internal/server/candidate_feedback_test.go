// file: internal/server/candidate_feedback_test.go
// version: 1.0.0
// guid: 2f919198-8b0e-41f4-886a-3bc7515e7ddb
// last-edited: 2026-10-07

package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

func candidateFeedbackCtx(t *testing.T, method, target, body string, user *database.User) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	if user != nil {
		req = req.WithContext(auth.WithUser(req.Context(), user))
	}
	c.Request = req
	return c, w
}

const feedbackBody = `{
  "book_id": "BOOK1",
  "label": "negative",
  "query": {"title": " Dune ", "author": "Frank Herbert", "browse": true},
  "candidate": {"source": "audible", "asin": "B000TEST01", "title": "Dune Messiah",
                "author": "Frank Herbert", "narrator": "Scott Brick", "series": "Dune",
                "series_position": "2", "year": 2007, "score": 2.13,
                "score_breakdown": {"score": 1.5, "steps": []}},
  "rank": 2,
  "result_count": 7
}`

func TestCandidateFeedback_PostDeleteExport(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()
	user := &database.User{ID: "u-1", Username: "alice"}

	// Record a thumbs-down.
	c, w := candidateFeedbackCtx(t, http.MethodPost, "/metadata/candidate-feedback", feedbackBody, user)
	srv.handlePutCandidateFeedback(c)
	if w.Code != http.StatusOK {
		t.Fatalf("post: %d %s", w.Code, w.Body.String())
	}
	var saved database.CandidateFeedback
	if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	wantHash := metafetch.CandidateSourceHash(metafetch.MetadataCandidate{Source: "audible", ASIN: "B000TEST01"})
	if saved.ID == "" || saved.Label != database.CandidateFeedbackNegative ||
		saved.Query.Mode != database.CandidateFeedbackModeBrowse || saved.Query.Title != "Dune" ||
		saved.UserID != "u-1" || saved.Username != "alice" || saved.Score != 2.13 ||
		saved.Rank != 2 || saved.ResultCount != 7 || saved.Candidate.Narrator != "Scott Brick" ||
		saved.Candidate.SourceHash == "" || saved.Candidate.SourceHash != wantHash ||
		!strings.Contains(string(saved.ScoreBreakdown), `"score":1.5`) {
		t.Fatalf("saved record wrong: %+v", saved)
	}

	// Export carries it as one JSONL line.
	c, w = candidateFeedbackCtx(t, http.MethodGet, "/metadata/candidate-feedback", "", user)
	srv.handleExportCandidateFeedback(c)
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/x-ndjson") {
		t.Fatalf("export: %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	lines := 0
	sc := bufio.NewScanner(bytes.NewReader(w.Body.Bytes()))
	for sc.Scan() {
		var fb database.CandidateFeedback
		if err := json.Unmarshal(sc.Bytes(), &fb); err != nil {
			t.Fatalf("bad jsonl line %q: %v", sc.Text(), err)
		}
		if fb.ID != saved.ID {
			t.Fatalf("export id %s, want %s", fb.ID, saved.ID)
		}
		lines++
	}
	if lines != 1 {
		t.Fatalf("export lines = %d, want 1", lines)
	}

	// Undo with the positive label leaves the negative alone.
	c, w = candidateFeedbackCtx(t, http.MethodDelete, "/metadata/candidate-feedback/x?label=positive", "", user)
	c.Params = gin.Params{{Key: "id", Value: saved.ID}}
	srv.handleDeleteCandidateFeedback(c)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"removed":false`) {
		t.Fatalf("mismatched-label delete: %d %s", w.Code, w.Body.String())
	}
	c, w = candidateFeedbackCtx(t, http.MethodDelete, "/metadata/candidate-feedback/x?label=negative", "", user)
	c.Params = gin.Params{{Key: "id", Value: saved.ID}}
	srv.handleDeleteCandidateFeedback(c)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"removed":true`) {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}

	c, w = candidateFeedbackCtx(t, http.MethodGet, "/metadata/candidate-feedback?book_id=BOOK1", "", user)
	srv.handleExportCandidateFeedback(c)
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "" {
		t.Fatalf("export after delete: %d %q", w.Code, w.Body.String())
	}
}

func TestCandidateFeedback_PostRejectsBadInput(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()
	for name, body := range map[string]string{
		"bad label": strings.Replace(feedbackBody, `"negative"`, `"meh"`, 1),
		"no book":   strings.Replace(feedbackBody, `"BOOK1"`, `""`, 1),
		"not json":  `{`,
	} {
		c, w := candidateFeedbackCtx(t, http.MethodPost, "/metadata/candidate-feedback", body, nil)
		srv.handlePutCandidateFeedback(c)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d %s", name, w.Code, w.Body.String())
		}
	}
	c, w := candidateFeedbackCtx(t, http.MethodDelete, "/metadata/candidate-feedback/x?label=meh", "", nil)
	c.Params = gin.Params{{Key: "id", Value: "x"}}
	srv.handleDeleteCandidateFeedback(c)
	if w.Code != http.StatusBadRequest {
		t.Errorf("bad delete label: got %d", w.Code)
	}
}
