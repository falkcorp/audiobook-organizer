// file: pkg/ops/errors.go
// version: 1.1.0
// guid: fb6a1e5f-2ac9-42e5-83e4-03f66fc77b3c
// last-edited: 2026-10-13

package ops

import (
	"context"
	"errors"
	"fmt"
)

// The Writer's refusals. opswriter returns exactly these values, so
// errors.Is works across the SDK and the executor.
var (
	ErrFenced           = errors.New("ops: run is fenced (canceled, abandoned or lease lost); write refused")
	ErrPreview          = errors.New("ops: preview mode; write recorded, not performed")
	ErrChangedSincePlan = errors.New("ops: row changed since the plan; refused")
)

// The causes the runner cancels an item's context with (context.Cause).
// They are item-scoped: a write refused for one of them is the item's own
// business (it ran out of time, or it already returned), not a sign that the
// run is being stopped, so the Writer does not flag it as a fenced refusal.
var (
	// ErrItemTimedOut: the item's ItemTimeout ran out. It wraps
	// context.DeadlineExceeded.
	ErrItemTimedOut = fmt.Errorf("ops: item timed out: %w", context.DeadlineExceeded)
	// ErrItemReturned: the item returned; its context is closed, so a write
	// a goroutine it left behind makes is refused.
	ErrItemReturned = errors.New("ops: item returned; its context is closed")
)
