// file: internal/database/candidate_feedback_test.go
// version: 1.0.0
// guid: 93b2cfa7-7495-466b-a970-4131e54d66e4
// last-edited: 2026-10-07

package database

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func newFeedbackStore(t *testing.T) *CandidateFeedbackStore {
	t.Helper()
	ps, err := NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	return NewCandidateFeedbackStore(ps)
}

func sampleFeedback(book string, label CandidateFeedbackLabel) CandidateFeedback {
	return CandidateFeedback{
		BookID: book,
		Label:  label,
		Query:  CandidateFeedbackQuery{Title: "Sample Saga", Author: "A. Writer", Mode: CandidateFeedbackModeBrowse},
		Candidate: CandidateFeedbackCandidate{
			Source: "audible", ASIN: "B0EXAMPLE1", Title: "Sample Sequel", Author: "A. Writer",
			SourceHash: "abc123",
		},
		Score:          2.13,
		ScoreBreakdown: json.RawMessage(`{"base":1.5,"steps":[]}`),
		Rank:           2,
		ResultCount:    7,
		UserID:         "u1",
	}
}

func TestCandidateFeedback_PutGetUpsertKeepsCreatedAt(t *testing.T) {
	s := newFeedbackStore(t)
	t0 := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

	neg, err := s.Put(sampleFeedback("book1", CandidateFeedbackNegative), t0)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if neg.ID == "" || neg.CreatedAt != t0 {
		t.Fatalf("id/created not set: %+v", neg)
	}

	// Apply of the same candidate for the same query turns it positive in place.
	pos, err := s.Put(sampleFeedback("book1", CandidateFeedbackPositive), t0.Add(time.Minute))
	if err != nil {
		t.Fatalf("put positive: %v", err)
	}
	if pos.ID != neg.ID {
		t.Fatalf("same book+query+candidate gave two ids: %s vs %s", neg.ID, pos.ID)
	}
	got, err := s.Get(neg.ID)
	if err != nil || got == nil {
		t.Fatalf("get: %v %v", got, err)
	}
	if got.Label != CandidateFeedbackPositive || !got.CreatedAt.Equal(t0) || !got.UpdatedAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("upsert wrong: %+v", got)
	}
	if string(got.ScoreBreakdown) != `{"base":1.5,"steps":[]}` || got.Score != 2.13 || got.Rank != 2 {
		t.Fatalf("payload not round-tripped: %+v", got)
	}

	// A different query is a different example.
	other := sampleFeedback("book1", CandidateFeedbackNegative)
	other.Query.Mode = CandidateFeedbackModeBook
	o, err := s.Put(other, t0)
	if err != nil {
		t.Fatalf("put other: %v", err)
	}
	if o.ID == neg.ID {
		t.Fatal("per-book and browse queries share an id")
	}
}

func TestCandidateFeedback_DeleteOnlyMatchingLabel(t *testing.T) {
	s := newFeedbackStore(t)
	now := time.Now()
	fb, err := s.Put(sampleFeedback("book1", CandidateFeedbackPositive), now)
	if err != nil {
		t.Fatal(err)
	}
	// Undoing a thumbs-down must not erase the positive written over it.
	removed, err := s.Delete(fb.ID, CandidateFeedbackNegative)
	if err != nil || removed {
		t.Fatalf("negative undo removed a positive: %v %v", removed, err)
	}
	removed, err = s.Delete(fb.ID, CandidateFeedbackPositive)
	if err != nil || !removed {
		t.Fatalf("delete: %v %v", removed, err)
	}
	if got, _ := s.Get(fb.ID); got != nil {
		t.Fatal("record survived delete")
	}
	removed, err = s.Delete(fb.ID, "")
	if err != nil || removed {
		t.Fatalf("delete of absent: %v %v", removed, err)
	}
}

func TestCandidateFeedback_EachPagesAndFiltersByBook(t *testing.T) {
	s := newFeedbackStore(t)
	now := time.Now()
	// More than one page, across two books.
	for i := 0; i < candidateFeedbackPage+3; i++ {
		fb := sampleFeedback("bookA", CandidateFeedbackNegative)
		fb.Candidate.SourceHash = ""
		fb.Candidate.Title = "T" + string(rune('a'+i%26)) + time.Duration(i).String()
		if _, err := s.Put(fb, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Put(sampleFeedback("bookB", CandidateFeedbackNegative), now); err != nil {
		t.Fatal(err)
	}
	// "bookA" must not match "bookAB"-style neighbours: the filter includes the ':'.
	if _, err := s.Put(sampleFeedback("bookAB", CandidateFeedbackNegative), now); err != nil {
		t.Fatal(err)
	}

	count := func(book string) int {
		n := 0
		if err := s.Each(book, func(*CandidateFeedback) error { n++; return nil }); err != nil {
			t.Fatalf("each %q: %v", book, err)
		}
		return n
	}
	if got := count(""); got != candidateFeedbackPage+5 {
		t.Fatalf("all: got %d", got)
	}
	if got := count("bookA"); got != candidateFeedbackPage+3 {
		t.Fatalf("bookA: got %d", got)
	}
	if got := count("bookB"); got != 1 {
		t.Fatalf("bookB: got %d", got)
	}
}

func TestCandidateFeedback_Validation(t *testing.T) {
	s := newFeedbackStore(t)
	cases := map[string]func(*CandidateFeedback){
		"no book":      func(f *CandidateFeedback) { f.BookID = "" },
		"colon book":   func(f *CandidateFeedback) { f.BookID = "a:b" },
		"bad label":    func(f *CandidateFeedback) { f.Label = "meh" },
		"bad mode":     func(f *CandidateFeedback) { f.Query.Mode = "" },
		"no source":    func(f *CandidateFeedback) { f.Candidate.Source = "" },
		"no title":     func(f *CandidateFeedback) { f.Candidate.Title = " " },
		"huge breakdn": func(f *CandidateFeedback) { f.ScoreBreakdown = make(json.RawMessage, maxCandidateFeedbackBreakdown+1) },
	}
	for name, mut := range cases {
		fb := sampleFeedback("book1", CandidateFeedbackNegative)
		mut(&fb)
		if _, err := s.Put(fb, time.Now()); !errors.Is(err, ErrCandidateFeedbackInvalid) {
			t.Errorf("%s: want ErrCandidateFeedbackInvalid, got %v", name, err)
		}
	}
}
