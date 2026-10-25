// file: internal/database/review_apply_notices.go
// version: 1.0.0
// guid: 3b7f4e21-8c06-4d95-a1e3-5f90c2d7b814
// last-edited: 2026-10-25

package database

import (
	"context"
	"sync"
)

// ApplyNotices collects what a review-apply function wants the reviewer told about
// an apply that SUCCEEDED but left something undone: a combine that retired a book
// without attaching one of its files, for example. An ApplyFunc returns only an
// error, and an error fails (or holds) the item, which is the wrong answer for a
// merge that committed. The review handler hands the function a context made by
// WithApplyNotices, the function adds its lines with AddApplyNotice, and the handler
// puts them in the response's note after the item is recorded as applied.
//
// It lives here, beside NothingWritten, because both the review handler and the
// apply functions (which must not import the server handlers) need it.
type ApplyNotices struct {
	mu    sync.Mutex
	lines []string
}

type applyNoticesKey struct{}

// WithApplyNotices returns a context that carries a new, empty collector, and the
// collector.
func WithApplyNotices(ctx context.Context) (context.Context, *ApplyNotices) {
	n := &ApplyNotices{}
	return context.WithValue(ctx, applyNoticesKey{}, n), n
}

// AddApplyNotice records line on the collector ctx carries. A context without one
// (a test, or a caller outside the review handler) drops it; the apply function
// logs the same text, so nothing depends on a collector being there.
func AddApplyNotice(ctx context.Context, line string) {
	n, ok := ctx.Value(applyNoticesKey{}).(*ApplyNotices)
	if !ok || n == nil || line == "" {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.lines = append(n.lines, line)
}

// Lines returns the recorded lines in the order they were added.
func (n *ApplyNotices) Lines() []string {
	if n == nil {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.lines...)
}
