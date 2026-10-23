// file: internal/undo/journal_order.go
// version: 1.0.0
// guid: 3b8f1d56-9e24-4c70-a5d1-72e6c0f4b983
// last-edited: 2026-10-22

package undo

import "github.com/falkcorp/audiobook-organizer/internal/database"

// The order of the journal's rows, for the decisions that depend on which row
// came first (the membership history, the paired-row rule): the journal clock's
// sequence, never the wall time. CreatedAt and RevertedAt are wall times, and a
// wall clock can stand still or step back (an NTP step, a restored VM); the
// store stamps CreatedSeq and RevertedSeq from a clock that only moves forward
// (database pebble_store_journal_clock.go), so two rows are in the order they
// were written whatever the wall clock did in between. A row written before the
// sequences existed has none (0), and its wall time stands in: the sequence is
// in the same nanosecond domain as the wall time, so the two compare.

// CreatedKey is the position of c in the journal's order.
func CreatedKey(c *database.OperationChange) int64 {
	if c.CreatedSeq != 0 {
		return c.CreatedSeq
	}
	return c.CreatedAt.UnixNano()
}

// RevertedKey is the position in the journal's order of the moment c was
// stamped reverted: 0 while it is not.
func RevertedKey(c *database.OperationChange) int64 {
	switch {
	case c.RevertedAt == nil:
		return 0
	case c.RevertedSeq != 0:
		return c.RevertedSeq
	}
	return c.RevertedAt.UnixNano()
}
