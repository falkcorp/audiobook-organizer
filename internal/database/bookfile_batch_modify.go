// file: internal/database/bookfile_batch_modify.go
// version: 1.1.0
// guid: 7b3d9e15-c4a2-4f68-91d0-5a8e2c6f1b47
// last-edited: 2026-10-25

package database

import (
	"context"
	"errors"
	"fmt"
)

// BookFileBatchModifier is ModifyBookFile for many rows of ONE book, with the
// book's aggregates recomputed once instead of once per row. It is a capability
// (resolved with AsCapability, never a bare assertion: the production store can
// be wrapped in a decorator that hides capabilities from one) rather than a
// method on Store, so the 398-method interface does not widen for one op
// (TestStoreFlattenedWidth: Store may only narrow). *PebbleStore implements it.
//
// Why not UpdateBookFiles, the other once-per-book writer: it takes caller-built
// full rows and writes them as given, so a row read before the write goes back
// over whatever another writer changed in between, and the caller has no hook to
// re-check the row it is about to replace. This one reads each row under its own
// lockBookFile stripe, hands it to fn, and writes fn's result in the same step,
// exactly as ModifyBookFile does, so a precondition fn checks (still at this
// path, still un-hashed) cannot be invalidated before the commit.
//
// fn runs once per id with a copy of the stored row; it must not change ID or
// BookID and must not write any book_file itself. fn returning
// ErrSkipBookFileWrite writes nothing for that row. An id with no stored row is
// ignored. Rows are processed in the given order and ctx is checked before each;
// on cancellation the rows already written still get their book recomputed.
//
// It returns the IDs whose rows were written (a durability-unknown write counts
// as written), in order. A row whose write failed is skipped and its error is
// joined into the returned error; the rest are still processed. A recompute
// failure is RETURNED (wrapped with ErrBookAggregatesRecompute), not only
// logged, as UpdateBookFiles does: the caller wrote these rows and must be able
// to tell that the book's totals did not follow.
type BookFileBatchModifier interface {
	ModifyBookFiles(ctx context.Context, bookID string, fileIDs []string, fn func(*BookFile) error) ([]string, error)
}

var (
	_ BookFileBatchModifier = (*PebbleStore)(nil)
	_ BookFileBatchModifier = (*MockStore)(nil)
)

// ModifyBookFiles implements BookFileBatchModifier.
func (s *PebbleStore) ModifyBookFiles(ctx context.Context, bookID string, fileIDs []string, fn func(*BookFile) error) ([]string, error) {
	var (
		changed   []string
		errs      []error
		recompute bool
	)
	for _, id := range fileIDs {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		_, applied, needRecompute, err := s.modifyBookFileNoNotify("ModifyBookFiles", bookID, id, fn)
		if applied {
			changed = append(changed, id)
			recompute = recompute || needRecompute
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("book file %s: %w", id, err))
		}
	}
	// After every stripe is released: the recompute writes the BOOK under
	// lockBook, and no book_file stripe may be held while a book stripe is taken.
	if recompute {
		if err := s.RecomputeBookAggregates(bookID); err != nil {
			errs = append(errs, fmt.Errorf("%w for book %s: %w", ErrBookAggregatesRecompute, bookID, err))
		}
	}
	return changed, errors.Join(errs...)
}

// modifyBookFileNoNotify is the one read-modify-write behind ModifyBookFile and
// ModifyBookFiles, so the two cannot drift. Under the row's lockBookFile stripe
// it reads the stored row, hands a copy to fn, and writes fn's result through
// updateBookFileLocked (shared merge rule, secondary-index rewrite on a path or
// PID change). It does NOT recompute the book's aggregates: it reports whether
// one is needed and leaves it to the caller, which must run it after this
// returns (the stripe is released by then) and may coalesce it per book.
//
// op names the public method in error text ("ModifyBookFile"). row is the
// written row; the stored row when fn returned ErrSkipBookFileWrite; nil when
// no such row exists (a nil error) or on error. applied is true for a write
// that landed, including one whose fsync failed (ErrBookFileDurabilityUnknown,
// returned as err): that row is visible, so its book's aggregates must follow
// it (bookFileApplied's contract) and recompute reflects that.
func (s *PebbleStore) modifyBookFileNoNotify(op, bookID, fileID string, fn func(*BookFile) error) (row *BookFile, applied, recompute bool, err error) {
	unlock := s.lockBookFile(fileID)
	defer unlock()

	stored, err := s.getBookFileByID(bookID, fileID)
	if err != nil {
		return nil, false, false, fmt.Errorf("%s: read %s: %w", op, fileID, err)
	}
	if stored == nil {
		return nil, false, false, nil
	}
	next := *stored
	if err := fn(&next); err != nil {
		if errors.Is(err, ErrSkipBookFileWrite) {
			return stored, false, false, nil
		}
		return nil, false, false, err
	}
	if next.ID != stored.ID || next.BookID != stored.BookID {
		return nil, false, false, fmt.Errorf("%s: callback changed the identity of %s (id %q→%q, book %q→%q)",
			op, fileID, stored.ID, next.ID, stored.BookID, next.BookID)
	}
	n, err := s.updateBookFileLocked(fileID, &next, true, true)
	applied = bookFileApplied(err)
	if err != nil {
		return nil, applied, n && applied, fmt.Errorf("%s: write %s: %w", op, fileID, err)
	}
	return &next, applied, n && applied, nil
}
