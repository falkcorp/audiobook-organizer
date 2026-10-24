// file: internal/database/retired_shell.go
// version: 1.0.0
// guid: 873c25c2-790a-4312-b6fa-37d376699776
// last-edited: 2026-10-24

package database

import (
	"fmt"
)

// A retired shell is a book whose live book_file rows the retired-versions
// repair re-keyed onto another book (the holder) as retired versions. It owns
// no live row, so every "does it own files" check passes it, yet it must be
// neither purged, nor hard-deleted, nor brought back from the trash:
//
//   - removing it deletes the only record of where its retired versions came
//     from, and the purge that journals a snapshot (so a per-version restore can
//     put the book back) is not built yet, so nothing could undo it;
//   - bringing it back puts an empty "imported" book next to the audio it lost,
//     which sits retired under the holder.
//
// ErrBookIsRetiredShell (retired_book_files.go) is the refusal. The helpers
// here answer "is this book a shell" from the retired side of the table.

// RetiredRowsHeldFor returns how many retired book_file rows, across every
// holder, belong to a retired version made from shellID. It fails closed: a
// read error is returned, never treated as zero, and so is a store that cannot
// read retired rows. Only a store that declares it never holds a retired row
// (NoRetiredBookFiles: MockStore, or a fake that says so) answers zero without
// reading; a store that merely lacks the reader (a decorator that was never
// taught to forward the capability) could be hiding rows, and "zero" from it
// would turn every shell guard off silently (ErrRetiredReaderUnavailable).
func RetiredRowsHeldFor(store any, shellID string) (int, error) {
	r, ok := AsCapability[RetiredBookFileReader](store)
	if !ok {
		if plainReadsAreComplete(store) {
			return 0, nil
		}
		return 0, fmt.Errorf("retired rows held for %s: %w", shellID, ErrRetiredReaderUnavailable)
	}
	return retiredRowsHeld(r, shellID)
}

// retiredShellReader is the two reads the count needs.
type retiredShellReader interface {
	GetRetiredVersionsFromBook(shellID string) ([]BookVersion, error)
	GetRetiredBookFiles(holderID string) ([]BookFile, error)
}

// retiredHoldCountAttempts bounds the recount when a retired version is moved
// to another holder (RehomeRetiredVersion) between the version list and the
// rows read, which would count its rows under the old holder as none.
const retiredHoldCountAttempts = 3

// retiredRowsHeld counts the retired rows held for shellID: one read of its
// versions (an idx:bv:from: prefix seek, empty for almost every book) and,
// only when some exist, one read of each holder's retired rows. A version that
// changed holder while the rows were read is counted again, because
// RehomeRetiredVersion does not take the shell's stripes; if it keeps moving
// the count is refused rather than guessed.
func retiredRowsHeld(r retiredShellReader, shellID string) (int, error) {
	for attempt := 0; attempt < retiredHoldCountAttempts; attempt++ {
		versions, err := r.GetRetiredVersionsFromBook(shellID)
		if err != nil {
			return 0, fmt.Errorf("read the retired versions of %s: %w", shellID, err)
		}
		if len(versions) == 0 {
			return 0, nil
		}
		byHolder := map[string]map[string]bool{}
		for i := range versions {
			if byHolder[versions[i].BookID] == nil {
				byHolder[versions[i].BookID] = map[string]bool{}
			}
			byHolder[versions[i].BookID][versions[i].ID] = true
		}
		held := 0
		for holder, ids := range byHolder {
			rows, err := r.GetRetiredBookFiles(holder)
			if err != nil {
				return 0, fmt.Errorf("read the retired rows held by %s: %w", holder, err)
			}
			for i := range rows {
				if ids[rows[i].VersionID] {
					held++
				}
			}
		}
		after, err := r.GetRetiredVersionsFromBook(shellID)
		if err != nil {
			return 0, fmt.Errorf("read the retired versions of %s: %w", shellID, err)
		}
		if sameVersionHolders(versions, after) {
			return held, nil
		}
	}
	return 0, fmt.Errorf("the retired versions of %s kept changing holder while they were counted; refusing rather than guessing", shellID)
}

// sameVersionHolders reports whether two reads of a shell's retired versions
// name the same versions under the same holders.
func sameVersionHolders(a, b []BookVersion) bool {
	if len(a) != len(b) {
		return false
	}
	holder := make(map[string]string, len(a))
	for i := range a {
		holder[a[i].ID] = a[i].BookID
	}
	for i := range b {
		if h, ok := holder[b[i].ID]; !ok || h != b[i].BookID {
			return false
		}
	}
	return true
}

// RefuseRetiredShell returns an error wrapping ErrBookIsRetiredShell, naming
// the row count and what to do instead, when shellID still has retired
// versions holding rows, and nil when it has none. A read error, or a store
// that cannot read retired rows, is returned as is (fail closed). action
// names what was refused ("purge", "restore", ...).
func RefuseRetiredShell(store any, shellID, action string) error {
	n, err := RetiredRowsHeldFor(store, shellID)
	if err != nil {
		return fmt.Errorf("%s %s refused: %w", action, shellID, err)
	}
	return retiredShellRefusal(action, shellID, n)
}

// retiredShellRefusal is the one wording of the refusal; nil when n is 0.
func retiredShellRefusal(action, shellID string, n int) error {
	if n == 0 {
		return nil
	}
	return fmt.Errorf("%s %s refused: %w (%d retired file record(s) still held); restore one of its retired versions as a separate book instead",
		action, shellID, ErrBookIsRetiredShell, n)
}

// deleteBookOpts are the variations of the one DeleteBook body. The zero value
// is DeleteBook.
type deleteBookOpts struct {
	// allowRetiredShell lets the delete proceed for a book that is a retired
	// shell. It is the seam for the journaled purge (retire plan PR 6), the
	// one caller that means to delete a shell: it snapshots the shell first, so
	// a per-version restore can put the book back, and then sets this. Nothing
	// sets it yet, on purpose: until that purge exists a delete of a shell has
	// no undo, so DeleteBook refuses it for every caller. It is deliberately
	// not reachable through DeleteBook(id) or any exported function; the purge
	// PR adds the exported entry point (its own method, so the refusal is never
	// bypassed by a flag a caller could pass by accident).
	allowRetiredShell bool
}

// refuseDeleteIfRetiredShell is DeleteBook's backstop for the shell guard the
// callers (purge, hard delete, restore) run before their own side effects
// (hash block, tombstone, os.Remove). Putting it in the primitive covers every
// caller by construction, the same reasoning ErrBookOwnsFiles gives
// (book_delete_owns_files.go), including one somebody adds later.
//
// Called with the book's write stripe and its OWNER stripe held, so the shell's
// retire (whose batch takes the shell's owner stripe through
// commitBookFileBatch) cannot commit in between. Every read is lock-free (a
// prefix seek of idx:bv:from:<id>:, then, only when versions exist, one read of
// each version and of its holder's retired rows), as the lock order requires:
// owner stripes are innermost.
func (p *PebbleStore) refuseDeleteIfRetiredShell(bookID string) error {
	n, err := retiredRowsHeld(p, bookID)
	if err != nil {
		return fmt.Errorf("delete book %s: cannot verify it is not a retired shell: %w", bookID, err)
	}
	if n == 0 {
		return nil
	}
	return retiredShellRefusal("delete book", bookID, n)
}
