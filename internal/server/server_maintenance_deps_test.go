// file: internal/server/server_maintenance_deps_test.go
// version: 1.1.0
// guid: 9c1e4f6a-2b7d-4a3e-8f5c-6d1a9b2e4c7f
// last-edited: 2026-07-11

// Package server tests for TASK-23 (MATCH-6/BUG-3/QUAL-3): ApplyTranscriptionCandidate
// must verify the identity of the re-read cached candidate against the
// candTitle/candAuthor that was actually gated by runAutoMatchTranscribed,
// closing the TOCTOU window where a cache refresh between the gate
// (SearchTranscriptionCandidate) and the apply (ApplyTranscriptionCandidate)
// could otherwise cause an ungated candidate to be applied.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// newTOCTOUCacheStore builds a MockStore whose GetMetadataCacheFunc returns
// firstEntry on its first invocation and secondEntry on every subsequent
// invocation, simulating a cache refresh between the gate read (via
// SearchTranscriptionCandidate) and the apply read (inside
// ApplyTranscriptionCandidate). updateCalls records every UpdateBook
// invocation so tests can assert whether the write path was reached.
func newTOCTOUCacheStore(t *testing.T, book *database.Book, firstEntry, secondEntry *database.MetadataCandidateCache) (store *database.MockStore, updateCalls *[]*database.Book) {
	t.Helper()
	calls := 0
	updateCalls = &[]*database.Book{}
	store = &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return book, nil
		},
		GetBookTagsFunc: func(bookID string) ([]string, error) {
			return nil, nil
		},
		GetMetadataCacheFunc: func(bookID string) (*database.MetadataCandidateCache, error) {
			calls++
			if calls == 1 {
				return firstEntry, nil
			}
			return secondEntry, nil
		},
		UpdateBookFunc: func(id string, b *database.Book) (*database.Book, error) {
			*updateCalls = append(*updateCalls, b)
			return b, nil
		},
	}
	return store, updateCalls
}

// mustCandidateCache marshals a single candidate into a
// database.MetadataCandidateCache the way the real cache layer stores it.
func mustCandidateCache(t *testing.T, bookID string, cand metafetch.MetadataCandidate) *database.MetadataCandidateCache {
	t.Helper()
	raw, err := json.Marshal(cand)
	if err != nil {
		t.Fatalf("marshal candidate: %v", err)
	}
	return &database.MetadataCandidateCache{
		BookID:     bookID,
		Candidates: []json.RawMessage{raw},
	}
}

// TestApplyTranscriptionCandidate_TOCTOU_CacheChangedBetweenGateAndApply
// reproduces the TOCTOU window: the cache holds one candidate when
// SearchTranscriptionCandidate (the gate read) runs, but a different
// candidate by the time ApplyTranscriptionCandidate (the apply read) runs.
// The apply must detect the mismatch and refuse to apply the ungated
// candidate.
func TestApplyTranscriptionCandidate_TOCTOU_CacheChangedBetweenGateAndApply(t *testing.T) {
	bookID := "book-1"
	book := &database.Book{ID: bookID, Title: "Old Title"}

	gatedCand := metafetch.MetadataCandidate{Title: "The Gated Book", Author: "Gated Author", Score: 0.9}
	refreshedCand := metafetch.MetadataCandidate{Title: "A Totally Different Book", Author: "Someone Else", Score: 0.95}

	store, updateCalls := newTOCTOUCacheStore(t, book,
		mustCandidateCache(t, bookID, gatedCand),
		mustCandidateCache(t, bookID, refreshedCand),
	)

	s := &Server{store: store, metadataFetchService: metafetch.NewService(store)}
	ctx := context.Background()

	candTitle, candAuthor, _, found, err := s.SearchTranscriptionCandidate(ctx, bookID, "irrelevant", "irrelevant")
	if err != nil || !found {
		t.Fatalf("SearchTranscriptionCandidate() = (found=%v, err=%v), want found=true, err=nil", found, err)
	}
	if candTitle != gatedCand.Title || candAuthor != gatedCand.Author {
		t.Fatalf("SearchTranscriptionCandidate() candidate = %q/%q, want %q/%q", candTitle, candAuthor, gatedCand.Title, gatedCand.Author)
	}

	applyErr := s.ApplyTranscriptionCandidate(ctx, bookID, candTitle, candAuthor)
	if applyErr == nil {
		t.Fatal("ApplyTranscriptionCandidate() = nil error, want non-nil error on cache-changed mismatch")
	}
	if len(*updateCalls) != 0 {
		t.Fatalf("UpdateBook was called %d times, want 0 — the mismatched (refreshed) candidate must never be applied", len(*updateCalls))
	}
}

// TestApplyTranscriptionCandidate_NoRegression_SameCandidateBothReads asserts
// that when the cache is unchanged between the gate and apply reads (the
// common, non-racy case), ApplyTranscriptionCandidate still succeeds and
// applies the candidate — the TOCTOU guard must not over-suppress the
// legitimate path.
func TestApplyTranscriptionCandidate_NoRegression_SameCandidateBothReads(t *testing.T) {
	bookID := "book-2"
	book := &database.Book{ID: bookID, Title: "Old Title"}

	cand := metafetch.MetadataCandidate{Title: "The Stable Book", Author: "Stable Author", Score: 0.9, Source: "test"}
	entry := mustCandidateCache(t, bookID, cand)

	store, updateCalls := newTOCTOUCacheStore(t, book, entry, entry)
	s := &Server{store: store, metadataFetchService: metafetch.NewService(store)}
	ctx := context.Background()

	candTitle, candAuthor, _, found, err := s.SearchTranscriptionCandidate(ctx, bookID, "irrelevant", "irrelevant")
	if err != nil || !found {
		t.Fatalf("SearchTranscriptionCandidate() = (found=%v, err=%v), want found=true, err=nil", found, err)
	}

	applyErr := s.ApplyTranscriptionCandidate(ctx, bookID, candTitle, candAuthor)
	if applyErr != nil {
		t.Fatalf("ApplyTranscriptionCandidate() = %v, want nil error on unchanged cache", applyErr)
	}
	if len(*updateCalls) != 1 {
		t.Fatalf("UpdateBook was called %d times, want 1 — the matching candidate must still be applied", len(*updateCalls))
	}
}

// TestApplyTranscriptionCandidateSourceHashDriftRefused proves the INIT-3-T5
// SourceHash layer catches a drift the slot-0 identity check cannot: the top
// candidate is unchanged between the gate and apply reads (so the slot-0 guard
// would pass), but the cache row's stored SourceHash no longer matches a hash
// recomputed over the book's current fields. The apply must fail closed and
// never reach the write path.
func TestApplyTranscriptionCandidateSourceHashDriftRefused(t *testing.T) {
	bookID := "book-3"
	book := &database.Book{ID: bookID, Title: "Current Title"}

	cand := metafetch.MetadataCandidate{Title: "The Stable Book", Author: "Stable Author", Score: 0.9, Source: "test"}
	entry := mustCandidateCache(t, bookID, cand)
	// Non-empty hash that cannot match a recompute over the book's real fields:
	// simulates a row whose search inputs drifted since the cache write.
	entry.SourceHash = "deadbeefdeadbeef"

	store, updateCalls := newTOCTOUCacheStore(t, book, entry, entry)
	s := &Server{store: store, metadataFetchService: metafetch.NewService(store)}
	ctx := context.Background()

	candTitle, candAuthor, _, found, err := s.SearchTranscriptionCandidate(ctx, bookID, "irrelevant", "irrelevant")
	if err != nil || !found {
		t.Fatalf("SearchTranscriptionCandidate() = (found=%v, err=%v), want found=true, err=nil", found, err)
	}

	applyErr := s.ApplyTranscriptionCandidate(ctx, bookID, candTitle, candAuthor)
	if applyErr == nil {
		t.Fatal("ApplyTranscriptionCandidate() = nil error, want non-nil on SourceHash drift")
	}
	if !errors.Is(applyErr, metafetch.ErrStaleMetadataCache) {
		t.Fatalf("ApplyTranscriptionCandidate() error = %v, want errors.Is ErrStaleMetadataCache", applyErr)
	}
	if len(*updateCalls) != 0 {
		t.Fatalf("UpdateBook was called %d times, want 0 — a drifted cache row must never be applied", len(*updateCalls))
	}
}

// TestApplyTranscriptionCandidateUnchangedStillApplies is the anti-over-
// suppression check for the INIT-3-T5 guard: a legacy cache row with an empty
// SourceHash (predating the field being load-bearing) must fail OPEN so an
// UNCHANGED, known-good book still applies with the new guard active. This is
// the mustCandidateCache shape (SourceHash == ""), so it exercises the
// fail-open branch of ValidateCachedIdentity end-to-end through the apply path.
func TestApplyTranscriptionCandidateUnchangedStillApplies(t *testing.T) {
	bookID := "book-4"
	book := &database.Book{ID: bookID, Title: "Old Title"}

	cand := metafetch.MetadataCandidate{Title: "The Stable Book", Author: "Stable Author", Score: 0.9, Source: "test"}
	entry := mustCandidateCache(t, bookID, cand) // SourceHash == "" → fail-open
	if entry.SourceHash != "" {
		t.Fatalf("precondition: expected empty SourceHash, got %q", entry.SourceHash)
	}

	store, updateCalls := newTOCTOUCacheStore(t, book, entry, entry)
	s := &Server{store: store, metadataFetchService: metafetch.NewService(store)}
	ctx := context.Background()

	candTitle, candAuthor, _, found, err := s.SearchTranscriptionCandidate(ctx, bookID, "irrelevant", "irrelevant")
	if err != nil || !found {
		t.Fatalf("SearchTranscriptionCandidate() = (found=%v, err=%v), want found=true, err=nil", found, err)
	}

	applyErr := s.ApplyTranscriptionCandidate(ctx, bookID, candTitle, candAuthor)
	if applyErr != nil {
		t.Fatalf("ApplyTranscriptionCandidate() = %v, want nil — an unchanged book must still apply with the new guard active", applyErr)
	}
	if len(*updateCalls) != 1 {
		t.Fatalf("UpdateBook was called %d times, want 1 — the legit apply must not be over-suppressed", len(*updateCalls))
	}
}
