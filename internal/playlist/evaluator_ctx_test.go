// file: internal/playlist/evaluator_ctx_test.go
// version: 1.1.0
// guid: 4f9a1d36-8c52-4e70-b3a9-6d0e8c2f7b14
// last-edited: 2026-10-30

package playlist

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// N-12: the viewer-state scan stops for a request that went away, and the answer is
// the context's error. It is not a failure to read state: a caller that logged
// "state read failed" for a client that left would report a healthy collection as
// unavailable.
func TestLoadViewerStates_ACancelledRequestIsNotAStateReadFailure(t *testing.T) {
	store, _, _ := collectionFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	states, err := LoadViewerStates(ctx, &countingStore{real: store}, "alice")
	require.Error(t, err)
	assert.Nil(t, states)
	assert.ErrorIs(t, err, context.Canceled)
	assert.NotErrorIs(t, err, ErrViewerStateRead)

	// A real failure of the read stays a state-read failure.
	_, err = LoadViewerStates(context.Background(), &countingStore{real: store, listErr: errors.New("disk")}, "alice")
	assert.ErrorIs(t, err, ErrViewerStateRead)
}

// cancelOnFirstRead is a book source that cancels the request while the sort is
// reading books. The sort reads from several workers at once, so the count is atomic.
type cancelOnFirstRead struct {
	cancel context.CancelFunc
	reads  atomic.Int64
}

func (c *cancelOnFirstRead) GetBookByID(id string) (*database.Book, error) {
	c.reads.Add(1)
	c.cancel()
	return &database.Book{ID: id, Title: id}, nil
}

// N-12: the sort reads one book per id, up to a library's worth, in chunks that are a
// worker's share of the list. A request that goes away partway through a chunk stops
// it; the check is not only where a chunk starts. Each worker may read up to
// ctxCheckEvery books before it looks, so with the check inside the chunk the whole
// list is far from read, and without it every chunk that had started runs to its end.
func TestSortBookIDs_StopsPartwayThroughAChunkWhenTheRequestGoesAway(t *testing.T) {
	const n = 40000
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("s%05d", i)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src := &cancelOnFirstRead{cancel: cancel}
	out, err := sortBookIDs(ctx, src, ids, `[{"field":"title","direction":"asc"}]`)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, out)
	assert.Less(t, src.reads.Load(), int64(n/2), "the reads stopped long before the whole list was read")
}

// A sort that is not cancelled still reads every book, and an unreadable book is
// "missing", not an error.
func TestSortBookIDs_AnUncancelledSortReadsEveryBook(t *testing.T) {
	ids := []string{"b3", "b1", "b2"}
	out, err := sortBookIDs(context.Background(), noCancelBooks{}, ids, `[{"field":"title","direction":"asc"}]`)
	require.NoError(t, err)
	assert.Equal(t, []string{"b1", "b2", "b3"}, out)
}

type noCancelBooks struct{}

func (noCancelBooks) GetBookByID(id string) (*database.Book, error) {
	return &database.Book{ID: id, Title: id}, nil
}

// N-13: which failures are the playlist's own definition (shown as unavailable, with
// a way to fix it) and which are not (a server error, or a request that went away).
func TestIsDefinitionError_SeparatesAStoredDefinitionFromEverythingElse(t *testing.T) {
	store, idx, _ := collectionFixture(t)
	ctx := context.Background()

	for name, tc := range map[string]struct{ query, sort string }{
		"a query that does not parse":  {"title:(unclosed", ""},
		"an empty query":               {"", ""},
		"a per-user value":             {"read_status:completed", ""},
		"a per-user value under an OR": {"read_status:completed OR title:b1", ""},
		"a sort spec that is not JSON": {"title:b1 OR title:b2", "not json"},
	} {
		_, err := EvaluateSmartPlaylist(ctx, store, idx, tc.query, tc.sort, 0, "alice")
		require.Error(t, err, name)
		assert.True(t, IsDefinitionError(err), "%s: %v", name, err)
	}

	// Not the playlist's definition.
	_, err := EvaluateSmartPlaylist(ctx, &countingStore{real: store, listErr: errors.New("disk")}, idx, "read_status:finished", "", 0, "alice")
	require.Error(t, err)
	assert.False(t, IsDefinitionError(err), "a state read failure is the server's: %v", err)

	_, err = EvaluateSmartPlaylist(ctx, store, nil, "title:b1", "", 0, "alice")
	assert.ErrorIs(t, err, ErrSearchIndexUnavailable)
	assert.False(t, IsDefinitionError(err), "an index that is not ready is not the definition")

	cctx, cancel := context.WithCancel(ctx)
	cancel()
	_, err = EvaluateSmartPlaylist(cctx, store, idx, "title:b1", "", 0, "alice")
	require.Error(t, err)
	assert.False(t, IsDefinitionError(err), "a request that went away is not the definition: %v", err)
}

// The per-user refusal is not wrapped, so a caller's own lead-in is not doubled.
func TestEvaluate_APerUserRefusalIsNotWrapped(t *testing.T) {
	store, idx, _ := collectionFixture(t)
	_, err := EvaluateSmartPlaylist(context.Background(), store, idx, "read_status:completed", "", 0, "alice")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read_status must be one of")
	assert.NotContains(t, err.Error(), "query:")
}
