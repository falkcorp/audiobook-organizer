// file: internal/metabatch/candidates_test.go
// version: 1.1.0
// guid: e5f6a7b8-c9d0-1e2f-3a4b-5c6d7e8f9a0b
// last-edited: 2026-08-22

package metabatch_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// ---------------------------------------------------------------------------
// CountByStatus
// ---------------------------------------------------------------------------

func TestCountByStatus_Empty(t *testing.T) {
	if got := metabatch.CountByStatus(nil, "matched"); got != 0 {
		t.Fatalf("expected 0 for nil slice, got %d", got)
	}
}

func TestCountByStatus_Mixed(t *testing.T) {
	results := []metabatch.CandidateResult{
		{Status: "matched"},
		{Status: "matched"},
		{Status: "no_match"},
		{Status: "error"},
		{Status: "matched"},
	}
	if got := metabatch.CountByStatus(results, "matched"); got != 3 {
		t.Errorf("matched: want 3, got %d", got)
	}
	if got := metabatch.CountByStatus(results, "no_match"); got != 1 {
		t.Errorf("no_match: want 1, got %d", got)
	}
	if got := metabatch.CountByStatus(results, "error"); got != 1 {
		t.Errorf("error: want 1, got %d", got)
	}
	if got := metabatch.CountByStatus(results, "unknown"); got != 0 {
		t.Errorf("unknown: want 0, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// BuildCandidateBookInfo
// ---------------------------------------------------------------------------

// mockBookFileStore satisfies database.BookFileStore for testing.
type mockBookFileStore struct {
	files []database.BookFile
	err   error
}

func (m *mockBookFileStore) GetBookFiles(bookID string) ([]database.BookFile, error) {
	return m.files, m.err
}

func TestBuildCandidateBookInfo_BasicFields(t *testing.T) {
	coverURL := "https://example.com/cover.jpg"
	duration := 3600
	fileSize := int64(123456789)
	lang := "en"

	book := &database.Book{
		ID:       "book-001",
		Title:    "The Hobbit",
		FilePath: "/books/hobbit.m4b",
		Format:   "m4b",
		CoverURL: &coverURL,
		Duration: &duration,
		FileSize: &fileSize,
		Language: &lang,
	}
	book.Author = &database.Author{Name: "J.R.R. Tolkien"}

	store := &mockBookFileStore{
		files: []database.BookFile{
			{ITunesPath: "/itunes/hobbit.m4b"},
		},
	}

	info := metabatch.BuildCandidateBookInfo(store, book)

	if info.ID != "book-001" {
		t.Errorf("ID: want book-001, got %s", info.ID)
	}
	if info.Title != "The Hobbit" {
		t.Errorf("Title: want 'The Hobbit', got %s", info.Title)
	}
	if info.Author != "J.R.R. Tolkien" {
		t.Errorf("Author: want 'J.R.R. Tolkien', got %s", info.Author)
	}
	if info.CoverURL != coverURL {
		t.Errorf("CoverURL: want %s, got %s", coverURL, info.CoverURL)
	}
	if info.Duration != duration {
		t.Errorf("Duration: want %d, got %d", duration, info.Duration)
	}
	if info.FileSize != fileSize {
		t.Errorf("FileSize: want %d, got %d", fileSize, info.FileSize)
	}
	if info.Language != lang {
		t.Errorf("Language: want %s, got %s", lang, info.Language)
	}
	if info.ITunesPath != "/itunes/hobbit.m4b" {
		t.Errorf("ITunesPath: want /itunes/hobbit.m4b, got %s", info.ITunesPath)
	}
}

func TestBuildCandidateBookInfo_NilOptionalFields(t *testing.T) {
	book := &database.Book{
		ID:       "book-002",
		Title:    "Dune",
		FilePath: "/books/dune.m4b",
	}
	store := &mockBookFileStore{}

	info := metabatch.BuildCandidateBookInfo(store, book)

	if info.ID != "book-002" {
		t.Errorf("ID: want book-002, got %s", info.ID)
	}
	if info.CoverURL != "" {
		t.Errorf("CoverURL should be empty, got %s", info.CoverURL)
	}
	if info.Author != "" {
		t.Errorf("Author should be empty when nil, got %s", info.Author)
	}
	if info.Language != "" {
		t.Errorf("Language should be empty when nil, got %s", info.Language)
	}
}

// ---------------------------------------------------------------------------
// LoadRejectedCandidateKeys
// ---------------------------------------------------------------------------

// mockRawKVStore satisfies database.RawKVStore for testing.
type mockRawKVStore struct {
	pairs []database.KVPair
	err   error
}

func (m *mockRawKVStore) SetRaw(_ string, _ []byte) error { return nil }
func (m *mockRawKVStore) GetRaw(_ string) ([]byte, error) { return nil, nil }
func (m *mockRawKVStore) DeleteRaw(_ string) error        { return nil }
func (m *mockRawKVStore) CountPrefix(_ string) (int64, error) {
	return int64(len(m.pairs)), nil
}
func (m *mockRawKVStore) ScanPrefix(_ string) ([]database.KVPair, error) {
	return m.pairs, m.err
}

func TestLoadRejectedCandidateKeys_NoRejections(t *testing.T) {
	store := &mockRawKVStore{}
	keys := metabatch.LoadRejectedCandidateKeys(store, "book-001")
	if len(keys) != 0 {
		t.Errorf("want empty map, got %v", keys)
	}
}

func TestLoadRejectedCandidateKeys_WithRejections(t *testing.T) {
	bookID := "book-99"
	prefix := fmt.Sprintf("rejected_candidate:%s:", bookID)
	store := &mockRawKVStore{
		pairs: []database.KVPair{
			{Key: prefix + "audible|Project Hail Mary", Value: []byte("1")},
			{Key: prefix + "hardcover|The Martian", Value: []byte("1")},
		},
	}
	keys := metabatch.LoadRejectedCandidateKeys(store, bookID)
	if !keys["audible|Project Hail Mary"] {
		t.Error("expected 'audible|Project Hail Mary' to be in rejected keys")
	}
	if !keys["hardcover|The Martian"] {
		t.Error("expected 'hardcover|The Martian' to be in rejected keys")
	}
}

func TestLoadRejectedCandidateKeys_StoreError(t *testing.T) {
	store := &mockRawKVStore{err: fmt.Errorf("db error")}
	keys := metabatch.LoadRejectedCandidateKeys(store, "book-001")
	if len(keys) != 0 {
		t.Errorf("want empty map on store error, got %v", keys)
	}
}

// ---------------------------------------------------------------------------
// LatestMatchedBookIDs
// ---------------------------------------------------------------------------

// latestMatchedStore is a minimal Store stub for LatestMatchedBookIDs tests.
type latestMatchedStore struct {
	database.MockStore
	ops     []database.Operation
	opsErr  error
	v2Rows  []database.OperationV2Row
	v2Err   error
	results map[string][]database.OperationResult
}

func (s *latestMatchedStore) GetRecentOperations(limit int) ([]database.Operation, error) {
	return s.ops, s.opsErr
}

func (s *latestMatchedStore) ListOperationsV2Since(since time.Time, limit int) ([]database.OperationV2Row, error) {
	return s.v2Rows, s.v2Err
}

func (s *latestMatchedStore) GetOperationResults(opID string) ([]database.OperationResult, error) {
	if s.results != nil {
		return s.results[opID], nil
	}
	return nil, nil
}

func TestLatestMatchedBookIDs_BothKeyspacesErrorYieldsNothing(t *testing.T) {
	store := &latestMatchedStore{
		opsErr: fmt.Errorf("db error"),
		v2Err:  fmt.Errorf("db error"),
	}
	if result := metabatch.LatestMatchedBookIDs(store); len(result) != 0 {
		t.Errorf("expected no matches when neither keyspace answers, got %v", result)
	}
}

// A failure in ONE keyspace must not hide the other's results. This is the
// whole reason CandidateFetchOps swallows errors per-keyspace instead of
// aborting: OnlyUnmatched is gated on this map, so a v1 hiccup that blanked it
// would re-fetch every book the v2 side already knows is matched.
func TestLatestMatchedBookIDs_LegacyErrorDoesNotHideV2Results(t *testing.T) {
	now := time.Now()
	store := &latestMatchedStore{
		opsErr: fmt.Errorf("v1 unavailable"),
		v2Rows: []database.OperationV2Row{
			{ID: "op-v2", DefID: metabatch.CandidateFetchDefID, Status: "completed", QueuedAt: now},
		},
		results: map[string][]database.OperationResult{
			"op-v2": {{BookID: "book-a", Status: "matched", CreatedAt: now}},
		},
	}
	result := metabatch.LatestMatchedBookIDs(store)
	if !result["book-a"] {
		t.Errorf("v2-keyed match must survive a v1 listing error, got %v", result)
	}
}

// The mirror of the above: a v2 failure must not hide history keyed under v1.
func TestLatestMatchedBookIDs_V2ErrorDoesNotHideLegacyResults(t *testing.T) {
	now := time.Now()
	store := &latestMatchedStore{
		v2Err: fmt.Errorf("v2 unavailable"),
		ops: []database.Operation{
			{ID: "op-v1", Type: "metadata_candidate_fetch", CreatedAt: now},
		},
		results: map[string][]database.OperationResult{
			"op-v1": {{BookID: "book-old", Status: "matched", CreatedAt: now}},
		},
	}
	result := metabatch.LatestMatchedBookIDs(store)
	if !result["book-old"] {
		t.Errorf("v1-keyed match must survive a v2 listing error, got %v", result)
	}
}

func TestLatestMatchedBookIDs_NoOps(t *testing.T) {
	store := &latestMatchedStore{}
	result := metabatch.LatestMatchedBookIDs(store)
	if len(result) != 0 {
		t.Errorf("expected empty map with no ops, got %v", result)
	}
}

func TestLatestMatchedBookIDs_MatchedAndUnmatched(t *testing.T) {
	now := time.Now()
	store := &latestMatchedStore{
		ops: []database.Operation{
			{ID: "op-1", Type: "metadata_candidate_fetch"},
		},
		results: map[string][]database.OperationResult{
			"op-1": {
				{BookID: "book-a", Status: "matched", CreatedAt: now},
				{BookID: "book-b", Status: "no_match", CreatedAt: now},
				{BookID: "book-c", Status: "error", CreatedAt: now},
			},
		},
	}
	result := metabatch.LatestMatchedBookIDs(store)
	if !result["book-a"] {
		t.Error("expected book-a in matched set")
	}
	if result["book-b"] {
		t.Error("expected book-b NOT in matched set (status=no_match)")
	}
	if result["book-c"] {
		t.Error("expected book-c NOT in matched set (status=error)")
	}
}

func TestLatestMatchedBookIDs_LatestWins(t *testing.T) {
	// book-x was matched in op-1 but rejected in op-2 (newer). Should not be matched.
	early := time.Now().Add(-1 * time.Hour)
	late := time.Now()
	store := &latestMatchedStore{
		ops: []database.Operation{
			{ID: "op-1", Type: "metadata_candidate_fetch"},
			{ID: "op-2", Type: "metadata_candidate_fetch"},
		},
		results: map[string][]database.OperationResult{
			"op-1": {
				{BookID: "book-x", Status: "matched", CreatedAt: early},
			},
			"op-2": {
				{BookID: "book-x", Status: "rejected", CreatedAt: late},
			},
		},
	}
	result := metabatch.LatestMatchedBookIDs(store)
	if result["book-x"] {
		t.Error("expected book-x NOT in matched set — latest result is 'rejected'")
	}
}

func TestLatestMatchedBookIDs_IgnoresNonCandidateFetchOps(t *testing.T) {
	now := time.Now()
	store := &latestMatchedStore{
		ops: []database.Operation{
			{ID: "op-scan", Type: "scan"},
			{ID: "op-fetch", Type: "metadata_candidate_fetch"},
		},
		results: map[string][]database.OperationResult{
			"op-scan": {
				{BookID: "book-z", Status: "matched", CreatedAt: now},
			},
			"op-fetch": {
				{BookID: "book-y", Status: "matched", CreatedAt: now},
			},
		},
	}
	result := metabatch.LatestMatchedBookIDs(store)
	if result["book-z"] {
		t.Error("expected book-z NOT in matched set — it came from a non-candidate-fetch op")
	}
	if !result["book-y"] {
		t.Error("expected book-y in matched set — it came from a candidate-fetch op")
	}
}

// ---------------------------------------------------------------------------
// CandidateResult round-trip assertions
// ---------------------------------------------------------------------------

func TestCandidateResult_ZeroValue(t *testing.T) {
	var cr metabatch.CandidateResult
	if cr.Status != "" {
		t.Errorf("expected empty status, got %q", cr.Status)
	}
	if cr.Candidate != nil {
		t.Error("expected nil Candidate on zero value")
	}
}

func TestCandidateResult_WithCandidate(t *testing.T) {
	score := 0.95
	cr := metabatch.CandidateResult{
		Status: "matched",
		Book:   metabatch.CandidateBookInfo{ID: "b1", Title: "Ender's Game"},
		Candidate: &metafetch.MetadataCandidate{
			Source: "audible",
			Title:  "Ender's Game",
			Score:  score,
		},
	}
	if cr.Candidate.Score != score {
		t.Errorf("expected score %.2f, got %.2f", score, cr.Candidate.Score)
	}
}
