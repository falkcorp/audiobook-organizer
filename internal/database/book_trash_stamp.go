// file: internal/database/book_trash_stamp.go
// version: 1.0.0
// guid: 7b0e5c3a-92d4-4f18-a6c1-3d8e0f7a5b24
// last-edited: 2026-10-24

package database

import "time"

// TrashedAt is the instant b went to the trash: MarkedForDeletionAt, which
// every trash writer sets (and, since enforceTrashStamp, every store write that
// trashes a row); for a row trashed by older code without one, the row's last
// write (UpdatedAt), which trashing was. nil when neither is known.
//
// It is the one answer to "how old is this trashed book" for the retention
// filters of ListSoftDeletedBooks and CountSoftDeletedBooks (both the Pebble
// and the memdb path), the purge's bound and the trash listing's "eligible for
// the nightly purge" flag, so they cannot disagree. UpdatedAt is only a
// fallback for a row with no stamp: it is never preferred over a stamp, because
// it moves on every write, and a retention window means "trashed that long ago",
// not "last touched that long ago".
func (b *Book) TrashedAt() *time.Time {
	if b == nil {
		return nil
	}
	if b.MarkedForDeletionAt != nil {
		return b.MarkedForDeletionAt
	}
	return b.UpdatedAt
}

// trashedAfter reports whether b must be left out of a listing limited to
// books trashed at or before cutoff. A row whose trash time is unknown is left
// out as well: it is not provably old enough, and a retention purge that took
// it would delete a book it cannot show was ever past its window. (Until
// 2026-10-24 the filter let such a row through, so with retention on the first
// nightly pass purged every row a writer had trashed without a stamp,
// whatever its age.)
func trashedAfter(b *Book, cutoff time.Time) bool {
	at := b.TrashedAt()
	return at == nil || at.After(cutoff)
}

// enforceTrashStamp makes a write that TRASHES a row stamp it. old is the
// stored row (nil on create); book is the row about to be written, before it is
// marshalled or handed to memdb. now is the write's own timestamp.
//
//   - A row that goes from live to trashed gets now when it carries no stamp, or
//     the very stamp the stored live row already had: a stale stamp left behind
//     by a restore that did not clear it (older code, or a writer that
//     forgot), which would otherwise make a book that was just trashed look as
//     old as its first trashing, past every retention window and every purge
//     bound. A stamp the writer minted for this write (merge and split-book
//     journals mint theirs before the write and compare it after) differs from
//     the stored one and is kept as given.
//   - A row that stays trashed with no stamp keeps the stored stamp, or, for a
//     row older code trashed without one, takes the stored row's last write:
//     the same instant TrashedAt falls back to, now written down.
//
// A row that is live after the write is left exactly as given: the restore
// paths clear the stamp themselves, and live rows' stamps are read by
// group_redirect, revert and undo, so the store does not clear it behind them.
func enforceTrashStamp(book, old *Book, now time.Time) {
	if !bookIsSoftDeleted(book) {
		return
	}
	if old == nil || !bookIsSoftDeleted(old) {
		if book.MarkedForDeletionAt == nil || (old != nil && old.MarkedForDeletionAt != nil && book.MarkedForDeletionAt.Equal(*old.MarkedForDeletionAt)) {
			at := now
			book.MarkedForDeletionAt = &at
		}
		return
	}
	if book.MarkedForDeletionAt != nil {
		return
	}
	switch {
	case old.MarkedForDeletionAt != nil:
		at := *old.MarkedForDeletionAt
		book.MarkedForDeletionAt = &at
	case old.UpdatedAt != nil:
		at := *old.UpdatedAt
		book.MarkedForDeletionAt = &at
	default:
		at := now
		book.MarkedForDeletionAt = &at
	}
}
