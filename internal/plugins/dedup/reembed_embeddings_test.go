// file: internal/plugins/dedup/reembed_embeddings_test.go
// version: 1.2.0
// guid: 3a2b1c0d-9e8f-7a6b-5c4d-3e2f1a0b9c8d
// last-edited: 2026-09-11

// Unit tests for the reembed-embeddings op's scan-partition helper and the
// Phase-1 candidate scan (scanReembedCandidates).

package dedup

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeEmbeddingGetter is a bookEmbeddingGetter backed by a map plus an
// optional per-call hook (used to install a concurrency barrier).
type fakeEmbeddingGetter struct {
	byID   map[string]*database.Embedding
	errID  map[string]error
	before func() error
	calls  atomic.Int32
}

func (f *fakeEmbeddingGetter) Get(entityType, entityID string) (*database.Embedding, error) {
	f.calls.Add(1)
	if f.before != nil {
		if err := f.before(); err != nil {
			return nil, err
		}
	}
	if err, ok := f.errID[entityID]; ok {
		return nil, err
	}
	return f.byID[entityID], nil
}

// TestScanReembedCandidates_ClassifiesInListingOrder pins the Phase-1
// partition: current / stale / missing-but-embeddable / missing-and-not-
// embeddable / lookup-error (treated as missing), with ToReembed in the order
// the books were listed.
func TestScanReembedCandidates_ClassifiesInListingOrder(t *testing.T) {
	primaryFalse := false
	books := []database.BookCore{
		{ID: "b-current", Title: "Already Current"},
		{ID: "b-stale", Title: "Stale Model"},
		{ID: "b-missing", Title: "Never Embedded"},
		{ID: "b-nonprimary", Title: "Non Primary", IsPrimaryVersion: &primaryFalse},
		{ID: "b-err", Title: "Lookup Errors"},
		{ID: "b-short", Title: "ab"},
	}
	getter := &fakeEmbeddingGetter{
		byID: map[string]*database.Embedding{
			"b-current": {EntityType: "book", EntityID: "b-current", Model: "bge-m3"},
			"b-stale":   {EntityType: "book", EntityID: "b-stale", Model: "text-embedding-3-large"},
		},
		errID: map[string]error{"b-err": errors.New("pebble: boom")},
	}

	res, err := scanReembedCandidates(context.Background(), &mockReporter{}, books, "bge-m3", getter)
	require.NoError(t, err)

	assert.Equal(t, int32(len(books)), getter.calls.Load(), "every book must be looked up exactly once")
	assert.Equal(t, len(books), res.Total)
	assert.Equal(t, 1, res.Current)
	assert.Equal(t, []string{"b-stale", "b-missing", "b-err"}, res.ToReembed,
		"stale, never-embedded, and lookup-error books are re-embedded, in listing order; non-primary and short titles are skipped")
}

// TestScanReembedCandidates_RunsConcurrently is the DA-03 regression test for
// the reembed op's Phase-1 scan: every embedding-store Get blocks until two
// are in flight at once. The pre-fix sequential loop trips the barrier
// timeout on its first call, and a timed-out Get reads as "no embedding", so
// an embeddable book lands in ToReembed instead of Current — which is what the
// assertions catch. The worker-pool scan releases the barrier and classifies
// every book as current.
func TestScanReembedCandidates_RunsConcurrently(t *testing.T) {
	requireMultiCore(t)

	const n = 32
	barrier := newConcurrencyBarrier(2, 2*time.Second)
	books := make([]database.BookCore, 0, n)
	byID := make(map[string]*database.Embedding, n)
	for i := range n {
		id := fmt.Sprintf("book-%02d", i)
		books = append(books, database.BookCore{ID: id, Title: "Book " + id})
		byID[id] = &database.Embedding{EntityType: "book", EntityID: id, Model: "bge-m3"}
	}
	getter := &fakeEmbeddingGetter{byID: byID, before: barrier.enter}

	res, err := scanReembedCandidates(context.Background(), &mockReporter{}, books, "bge-m3", getter)
	require.NoError(t, err)

	assert.Equal(t, int32(n), getter.calls.Load(), "every book must be looked up exactly once")
	assert.Equal(t, n, res.Total)
	assert.Equal(t, n, res.Current, "a book counted as needing re-embed here means the store Get ran sequentially and tripped the barrier")
	assert.Empty(t, res.ToReembed)
}

// TestScanReembedCandidates_HonorsCancellation asserts the parallel scan still
// surfaces the bare context sentinel the sequential loop returned.
func TestScanReembedCandidates_HonorsCancellation(t *testing.T) {
	books := []database.BookCore{{ID: "b-1", Title: "One Book"}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := scanReembedCandidates(ctx, &mockReporter{}, books, "bge-m3", &fakeEmbeddingGetter{})
	assert.Equal(t, context.Canceled, err)
}

func TestEmbeddableForReembed(t *testing.T) {
	primaryTrue := true
	primaryFalse := false

	cases := []struct {
		name string
		book database.BookCore
		want bool
	}{
		{"primary nil + good title", database.BookCore{Title: "A Real Book"}, true},
		{"primary true + good title", database.BookCore{Title: "Another Book", IsPrimaryVersion: &primaryTrue}, true},
		{"non-primary excluded", database.BookCore{Title: "A Real Book", IsPrimaryVersion: &primaryFalse}, false},
		{"empty title excluded", database.BookCore{Title: ""}, false},
		{"whitespace title excluded", database.BookCore{Title: "   "}, false},
		{"2-rune title excluded (matches hasUsableTitle >2)", database.BookCore{Title: "ab"}, false},
		{"3-rune title included", database.BookCore{Title: "abc"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := tc.book
			if got := embeddableForReembed(&b); got != tc.want {
				t.Errorf("embeddableForReembed(%+v) = %v, want %v", tc.book, got, tc.want)
			}
		})
	}
}
