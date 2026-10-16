// file: internal/dedup/enqueue.go
// version: 1.0.0
// guid: 6c1f0b9e-5a2d-4e7b-9c38-2f4d81a7e3b5
// last-edited: 2026-10-16

package dedup

import (
	"context"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// CheckBookOpID is the batchable per-book dedup op defined in
// internal/plugins/dedup/check_book.go.
const CheckBookOpID = "dedup.check-book"

// OpEnqueuer is the one registry method EnqueueCheckBook needs. Both
// *registry.Registry and sdk.Registry satisfy it.
type OpEnqueuer interface {
	EnqueueOp(ctx context.Context, defID string, params any, opts ...registry.EnqueueOption) (string, error)
}

// EnqueueCheckBook enqueues dedup.check-book for one book. The op is
// Batchable, so the registry coalesces bursts of these into one run whose
// params it builds itself; callers only ever send the single-book shape.
func EnqueueCheckBook(ctx context.Context, reg OpEnqueuer, bookID string) error {
	if _, err := reg.EnqueueOp(ctx, CheckBookOpID, map[string]any{"book_id": bookID}); err != nil {
		return fmt.Errorf("enqueue %s for %s: %w", CheckBookOpID, bookID, err)
	}
	return nil
}
