// file: internal/server/metadata_candidate_refetch_test.go
// version: 1.0.0
// guid: 53254e8b-33e1-4370-80d0-245b21ecc8f8
// last-edited: 2026-09-19

package server

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// countingSource counts every outbound search call and answers with results
// (nil = "nothing") or err.
type countingSource struct {
	name    string
	calls   atomic.Int64
	results []metadata.BookMetadata
	err     error
}

func (c *countingSource) Name() string { return c.name }
func (c *countingSource) SearchByTitle(_ context.Context, _ string) ([]metadata.BookMetadata, error) {
	c.calls.Add(1)
	return c.results, c.err
}
func (c *countingSource) SearchByTitleAndAuthor(_ context.Context, _, _ string) ([]metadata.BookMetadata, error) {
	c.calls.Add(1)
	return c.results, c.err
}

// runCandidateFetch runs metadata.candidate-fetch once under opID and returns
// its result rows keyed by book id.
func runCandidateFetch(t *testing.T, s *Server, opID string, ids []string, force bool) map[string]CandidateResult {
	t.Helper()
	params, err := json.Marshal(metadataCandidateFetchOpParams{BookIDs: ids, Force: force})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	if err := s.runMetadataCandidateFetchOp(context.Background(), params, &resumeRecorder{opID: opID}); err != nil {
		t.Fatalf("run %s: %v", opID, err)
	}
	rows, err := s.storeForWiring().GetOperationResults(opID)
	if err != nil {
		t.Fatalf("GetOperationResults(%s): %v", opID, err)
	}
	out := make(map[string]CandidateResult, len(rows))
	for _, r := range rows {
		var cr CandidateResult
		if err := json.Unmarshal([]byte(r.ResultJSON), &cr); err != nil {
			t.Fatalf("decode result for %s: %v", r.BookID, err)
		}
		out[r.BookID] = cr
	}
	return out
}

func sourceCalls(srcs ...*countingSource) int64 {
	var n int64
	for _, s := range srcs {
		n += s.calls.Load()
	}
	return n
}

// TestCandidateFetch_UnchangedEmptyBookIsNotRefetched is the owner's complaint
// as a test: a book every provider answered with nothing is handed back by the
// "only unmatched" selection on every run, and each run used to re-ask every
// provider the whole query ladder. The second run over an unchanged book must
// make ZERO provider calls, and say why in its result row.
func TestCandidateFetch_UnchangedEmptyBookIsNotRefetched(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	store := s.storeForWiring()

	book, err := store.CreateBook(&database.Book{Title: "A Book Nobody Catalogued", FilePath: "/lib/nobody/book.m4b"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	a, b := &countingSource{name: "SrcA"}, &countingSource{name: "SrcB"}
	mfs := metafetch.NewService(store)
	mfs.SetOverrideSources([]metadata.MetadataSource{a, b})
	s.metadataFetchService = mfs

	first := runCandidateFetch(t, s, "op-refetch-1", []string{book.ID}, false)
	if first[book.ID].Status != "no_match" {
		t.Fatalf("first run status = %q, want no_match", first[book.ID].Status)
	}
	firstCalls := sourceCalls(a, b)
	if firstCalls == 0 {
		t.Fatal("first run made no provider calls; the test is not exercising a live search")
	}

	second := runCandidateFetch(t, s, "op-refetch-2", []string{book.ID}, false)
	if got := sourceCalls(a, b) - firstCalls; got != 0 {
		t.Fatalf("second run over an unchanged, already-empty book made %d provider calls, want 0", got)
	}
	if r := second[book.ID]; r.Status != "no_match" || r.Cached != candidateCachedKnownEmpty {
		t.Fatalf("second run result = {status %q, cached %q}, want {no_match, %q}", r.Status, r.Cached, candidateCachedKnownEmpty)
	}

	// Force asks again.
	before := sourceCalls(a, b)
	runCandidateFetch(t, s, "op-refetch-force", []string{book.ID}, true)
	if sourceCalls(a, b) == before {
		t.Fatal("a forced run made no provider calls")
	}

	// An input change re-opens the question.
	book.Title = "A Book Somebody Retitled"
	if _, err := store.UpdateBook(book.ID, book); err != nil {
		t.Fatalf("UpdateBook: %v", err)
	}
	before = sourceCalls(a, b)
	third := runCandidateFetch(t, s, "op-refetch-3", []string{book.ID}, false)
	if sourceCalls(a, b) == before {
		t.Fatal("a run after the title changed made no provider calls; a stale verdict was reused")
	}
	if third[book.ID].Cached != "" {
		t.Fatalf("retitled book was served from cache (%q)", third[book.ID].Cached)
	}

	// A newly enabled provider re-opens it too: SrcC never answered.
	c := &countingSource{name: "SrcC"}
	mfs.SetOverrideSources([]metadata.MetadataSource{a, b, c})
	runCandidateFetch(t, s, "op-refetch-4", []string{book.ID}, false)
	if c.calls.Load() == 0 {
		t.Fatal("a newly enabled provider was never asked; the verdict ignored the source set")
	}
}

// TestCandidateFetch_FailedProviderDoesNotMakeADurableVerdict: a provider that
// errored (quota, outage) never answered, so "nothing found" is not a verdict
// yet and the next run must ask it again -- but only while it keeps failing.
func TestCandidateFetch_FailedProviderDoesNotMakeADurableVerdict(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	store := s.storeForWiring()

	book, err := store.CreateBook(&database.Book{Title: "Quota Victim", FilePath: "/lib/quota/book.m4b"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	ok := &countingSource{name: "Answers"}
	down := &countingSource{name: "QuotaExhausted", err: errors.New("429 daily quota exceeded")}
	mfs := metafetch.NewService(store)
	mfs.SetOverrideSources([]metadata.MetadataSource{ok, down})
	s.metadataFetchService = mfs

	runCandidateFetch(t, s, "op-quota-1", []string{book.ID}, false)
	before := sourceCalls(down)
	second := runCandidateFetch(t, s, "op-quota-2", []string{book.ID}, false)
	if sourceCalls(down) == before {
		t.Fatal("a provider that failed was not retried; its failure was recorded as 'nothing found'")
	}
	if second[book.ID].Cached != "" {
		t.Fatalf("book with a failed provider was served from cache (%q)", second[book.ID].Cached)
	}

	// The provider recovers and answers "nothing": now every provider has
	// answered, and the run after that is free. The one that answered earlier
	// is remembered rather than needing to answer on the same run.
	down.err = nil
	runCandidateFetch(t, s, "op-quota-3", []string{book.ID}, false)
	before = sourceCalls(ok, down)
	fourth := runCandidateFetch(t, s, "op-quota-4", []string{book.ID}, false)
	if got := sourceCalls(ok, down) - before; got != 0 {
		t.Fatalf("run after every provider answered made %d provider calls, want 0", got)
	}
	if fourth[book.ID].Cached != candidateCachedKnownEmpty {
		t.Fatalf("fourth run cached = %q, want %q", fourth[book.ID].Cached, candidateCachedKnownEmpty)
	}
}

// TestCandidateFetch_FreshCachedCandidatesAreServedFromCache: a book whose
// candidates were fetched for its current inputs is answered from the
// candidate cache. The per-provider fetch cache is emptied between runs so it
// cannot be what saves the calls.
func TestCandidateFetch_FreshCachedCandidatesAreServedFromCache(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	store := s.storeForWiring()

	book, err := store.CreateBook(&database.Book{Title: "The Hobbit", FilePath: "/lib/hobbit/book.m4b"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	src := &countingSource{name: "Finds", results: []metadata.BookMetadata{{Title: "The Hobbit", Author: "J.R.R. Tolkien"}}}
	mfs := metafetch.NewService(store)
	mfs.SetOverrideSources([]metadata.MetadataSource{src})
	s.metadataFetchService = mfs

	first := runCandidateFetch(t, s, "op-cached-1", []string{book.ID}, false)
	if first[book.ID].Status != "matched" {
		t.Fatalf("first run status = %q, want matched", first[book.ID].Status)
	}
	if err := database.InvalidateAllCachedMetadataFetchesForBook(store, book.ID); err != nil {
		t.Fatalf("clear fetch cache: %v", err)
	}
	before := src.calls.Load()
	second := runCandidateFetch(t, s, "op-cached-2", []string{book.ID}, false)
	if got := src.calls.Load() - before; got != 0 {
		t.Fatalf("second run over a book with fresh cached candidates made %d provider calls, want 0", got)
	}
	r := second[book.ID]
	if r.Status != "matched" || r.Cached != candidateCachedCandidates || r.Candidate == nil || r.Candidate.Title != "The Hobbit" {
		t.Fatalf("second run result = {status %q, cached %q, candidate %+v}, want matched The Hobbit from cache", r.Status, r.Cached, r.Candidate)
	}
}

// TestCandidateFetch_KnownEmptyExpiresAfterBackstop: provider catalogs add
// releases, so a "known empty" verdict is re-asked once it is older than
// database.MetadataKnownEmptyTTL, even with unchanged inputs and sources.
func TestCandidateFetch_KnownEmptyExpiresAfterBackstop(t *testing.T) {
	s, cleanup := setupTestServer(t)
	defer cleanup()
	store := s.storeForWiring()

	book, err := store.CreateBook(&database.Book{Title: "Old Empty Verdict", FilePath: "/lib/old/book.m4b"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	src := &countingSource{name: "Empty"}
	mfs := metafetch.NewService(store)
	mfs.SetOverrideSources([]metadata.MetadataSource{src})
	s.metadataFetchService = mfs

	runCandidateFetch(t, s, "op-backstop-1", []string{book.ID}, false)
	entry, err := store.GetMetadataCache(book.ID)
	if err != nil || entry == nil || entry.LastEmptyFetchAt == nil || len(entry.EmptySources) == 0 {
		t.Fatalf("first run did not record a known-empty verdict: %+v (err %v)", entry, err)
	}
	old := time.Now().UTC().Add(-91 * 24 * time.Hour)
	entry.LastEmptyFetchAt = &old
	entry.FetchedAt = old
	if err := store.PutMetadataCache(entry); err != nil {
		t.Fatalf("PutMetadataCache: %v", err)
	}

	before := src.calls.Load()
	second := runCandidateFetch(t, s, "op-backstop-2", []string{book.ID}, false)
	if src.calls.Load() == before {
		t.Fatal("a known-empty verdict older than the 90-day backstop was reused; providers were not asked again")
	}
	if second[book.ID].Cached != "" {
		t.Fatalf("expired verdict was served from cache (%q)", second[book.ID].Cached)
	}
}
