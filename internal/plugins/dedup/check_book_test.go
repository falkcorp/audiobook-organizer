// file: internal/plugins/dedup/check_book_test.go
// version: 1.0.1
// guid: 9a8b7c6d-5e4f-3210-fedc-ba9876543210
// last-edited: 2026-11-09

// Tests for the dedup.check-book op (M4).
//
// Test matrix:
//  1. Two book subjects → CheckBook called for each; no error.
//  2. Context cancelled before first book → returns ctx.Err(), zero CheckBook calls.
//  3. Context cancelled mid-batch (after first book) → returns ctx.Err(), one CheckBook call.
//  4. Non-book subject is skipped; book subject is processed.
//  5. CheckBook returns an error → error is logged (warning), iteration continues.
//  6. Empty subjects list → returns nil, zero CheckBook calls.
//  7. Nil engine → runCheckBook returns error (nil-guard).

package dedup

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeBookChecker is a test double for bookChecker.
// It records every (bookID, ctx) pair passed to CheckBook and can be
// configured to cancel a context after N calls.
type fakeBookChecker struct {
	called    []string // bookIDs in call order
	returnErr error    // if non-nil, returned for every call
	returnDup bool     // isDup result returned for every call
}

func (f *fakeBookChecker) CheckBook(_ context.Context, bookID string) (bool, error) {
	f.called = append(f.called, bookID)
	return f.returnDup, f.returnErr
}

// buildSubjectsParams builds the JSON params accepted by runCheckBookWith.
func buildSubjectsParams(t *testing.T, subs []database.OpSubject) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(checkBookParams{Subjects: subs})
	require.NoError(t, err)
	return raw
}

// ─── tests ────────────────────────────────────────────────────────────────────

func TestRunCheckBook_TwoBooks(t *testing.T) {
	t.Parallel()
	checker := &fakeBookChecker{}
	params := buildSubjectsParams(t, []database.OpSubject{
		{Type: "book", ID: "book-1"},
		{Type: "book", ID: "book-2"},
	})

	err := runCheckBookWith(context.Background(), checker, params, &mockReporter{})

	require.NoError(t, err)
	assert.Equal(t, []string{"book-1", "book-2"}, checker.called,
		"CheckBook must be called for each book subject in order")
}

func TestRunCheckBook_ContextCancelledBeforeStart(t *testing.T) {
	t.Parallel()
	checker := &fakeBookChecker{}
	params := buildSubjectsParams(t, []database.OpSubject{
		{Type: "book", ID: "book-1"},
		{Type: "book", ID: "book-2"},
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before the op starts

	err := runCheckBookWith(ctx, checker, params, &mockReporter{})

	assert.ErrorIs(t, err, context.Canceled, "should return ctx.Err() when cancelled")
	assert.Empty(t, checker.called, "no CheckBook calls when context is already cancelled")
}

func TestRunCheckBook_ContextCancelledMidBatch(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())

	// cancelAfterFirst cancels the context when the first book has been processed.
	cancelAfterFirst := &cancellingChecker{cancel: cancel}
	params := buildSubjectsParams(t, []database.OpSubject{
		{Type: "book", ID: "book-1"},
		{Type: "book", ID: "book-2"},
	})

	err := runCheckBookWith(ctx, cancelAfterFirst, params, &mockReporter{})

	assert.ErrorIs(t, err, context.Canceled, "should return ctx.Err() after cancellation")
	assert.Equal(t, []string{"book-1"}, cancelAfterFirst.called,
		"only the first book should be processed before cancel takes effect")
}

// cancellingChecker calls cancel after the first CheckBook invocation.
type cancellingChecker struct {
	called []string
	cancel context.CancelFunc
}

func (c *cancellingChecker) CheckBook(_ context.Context, bookID string) (bool, error) {
	c.called = append(c.called, bookID)
	c.cancel() // cancel after first call; next iteration will see ctx.Done()
	return false, nil
}

func TestRunCheckBook_NonBookSubjectSkipped(t *testing.T) {
	t.Parallel()
	checker := &fakeBookChecker{}
	params := buildSubjectsParams(t, []database.OpSubject{
		{Type: "file", ID: "file-99"}, // should be skipped
		{Type: "book", ID: "book-1"},  // should be processed
	})

	err := runCheckBookWith(context.Background(), checker, params, &mockReporter{})

	require.NoError(t, err)
	assert.Equal(t, []string{"book-1"}, checker.called,
		"non-book subjects must be skipped; only book subjects processed")
}

func TestRunCheckBook_CheckBookErrorContinues(t *testing.T) {
	t.Parallel()
	checker := &fakeBookChecker{
		returnErr: errors.New("transient store error"),
	}
	params := buildSubjectsParams(t, []database.OpSubject{
		{Type: "book", ID: "book-1"},
		{Type: "book", ID: "book-2"},
	})

	// Error from CheckBook must not abort the batch; op returns nil.
	err := runCheckBookWith(context.Background(), checker, params, &mockReporter{})

	require.NoError(t, err, "CheckBook errors must be logged, not propagated")
	assert.Equal(t, []string{"book-1", "book-2"}, checker.called,
		"both books processed despite error on each")
}

func TestRunCheckBook_EmptySubjects(t *testing.T) {
	t.Parallel()
	checker := &fakeBookChecker{}
	params := buildSubjectsParams(t, []database.OpSubject{})

	err := runCheckBookWith(context.Background(), checker, params, &mockReporter{})

	require.NoError(t, err)
	assert.Empty(t, checker.called, "no CheckBook calls for empty batch")
}

func TestRunCheckBook_NilEngine(t *testing.T) {
	t.Parallel()
	p := &Plugin{engine: nil}
	params := buildSubjectsParams(t, []database.OpSubject{
		{Type: "book", ID: "book-1"},
	})

	err := p.runCheckBook(context.Background(), params, &mockReporter{})

	require.Error(t, err, "nil engine should return an error")
	assert.Contains(t, err.Error(), "dedup engine not available")
}
