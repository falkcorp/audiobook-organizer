// file: internal/database/retired_shell.go
// version: 1.1.0
// guid: 873c25c2-790a-4312-b6fa-37d376699776
// last-edited: 2026-10-29

package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cockroachdb/pebble/v2"
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
// read retired rows (a decorator that was never taught to forward the
// RetiredBookFileReader capability could be hiding rows, and "zero" from it
// would turn every shell guard off silently: ErrRetiredReaderUnavailable). The
// hand-written and generated MockStores implement the reader and hold none.
func RetiredRowsHeldFor(store any, shellID string) (int, error) {
	r, ok := AsCapability[RetiredBookFileReader](store)
	if !ok {
		return 0, fmt.Errorf("retired rows held for %s: %w", shellID, ErrRetiredReaderUnavailable)
	}
	return retiredRowsHeld(r, shellID)
}

// retiredShellReader is the two reads the generic count needs.
type retiredShellReader interface {
	GetRetiredVersionsFromBook(shellID string) ([]BookVersion, error)
	GetRetiredBookFiles(holderID string) ([]BookFile, error)
}

// retiredRowMarker is the part of a stored book_file row the held-row count
// reads. Decoding into it skips every other field, notably the raw AcoustID
// fingerprint (about 230 KB a row), which a full BookFile decode allocates.
type retiredRowMarker struct {
	VersionID string `json:"version_id"`
	Retired   bool   `json:"retired"`
}

// CountRetiredRowsHeldFor is retiredRowsHeld for the Pebble store: the same
// number, read from one snapshot. The versions retired out of shellID are listed
// once (an idx:bv:from: prefix seek, empty for almost every book, which then
// ends here) and each holder's rows are scanned once into a retiredRowMarker.
// A RehomeRetiredVersion commits the version and its rows in one batch, so a
// snapshot sees them under the old holder or the new one, never split: the
// recount retiredRowsHeld needs on a live read is not needed here.
func (p *PebbleStore) CountRetiredRowsHeldFor(shellID string) (int, error) {
	if shellID == "" {
		return 0, nil
	}
	snap := p.db.NewSnapshot()
	defer snap.Close()
	prefix := []byte(retiredVersionFromPrefix + shellID + ":")
	var versionIDs []string
	if err := forEachKeyInRange(snap, prefix, prefixEnd(prefix), func(key, _ []byte) error {
		versionIDs = append(versionIDs, strings.TrimPrefix(string(key), string(prefix)))
		return nil
	}); err != nil {
		return 0, fmt.Errorf("read the retired versions of %s: %w", shellID, err)
	}
	if len(versionIDs) == 0 {
		return 0, nil
	}
	byHolder := map[string]map[string]bool{}
	for _, id := range versionIDs {
		data, closer, err := snap.Get([]byte("bv:" + id))
		if errors.Is(err, pebble.ErrNotFound) {
			continue // GetRetiredVersionsFromBook skips a version row that is gone
		}
		if err != nil {
			return 0, fmt.Errorf("read retired version %s of %s: %w", id, shellID, err)
		}
		var v struct {
			BookID string `json:"book_id"`
		}
		err = json.Unmarshal(data, &v)
		_ = closer.Close()
		if err != nil {
			return 0, fmt.Errorf("decode retired version %s of %s: %w", id, shellID, err)
		}
		if byHolder[v.BookID] == nil {
			byHolder[v.BookID] = map[string]bool{}
		}
		byHolder[v.BookID][id] = true
	}
	held := 0
	for holder, ids := range byHolder {
		rowPrefix := []byte("book_file:" + holder + ":")
		if err := forEachKeyInRange(snap, rowPrefix, prefixEnd(rowPrefix), func(_, value []byte) error {
			var m retiredRowMarker
			if err := json.Unmarshal(value, &m); err != nil {
				return err
			}
			if m.Retired && ids[m.VersionID] {
				held++
			}
			return nil
		}); err != nil {
			return 0, fmt.Errorf("read the retired rows held by %s: %w", holder, err)
		}
	}
	return held, nil
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
	// Only a bare PebbleStore takes the narrow count, by concrete type and not
	// by an interface: a decorator that embeds the store would be handed the
	// method by promotion, and its own override of the two reads (a fault
	// injector, a filter) would be bypassed without anyone noticing, which is
	// how this check would fail open.
	if p, ok := r.(*PebbleStore); ok {
		return p.CountRetiredRowsHeldFor(shellID)
	}
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
// versions holding rows, and nil when it has none. It is the ONE check behind
// every refusal of a book the retired-versions repair emptied: the restore
// (merge.RestoreFromTrashLocked), the purge and its preview, the
// discard-progress purge, the hard delete, and the store's own backstop in
// DeleteBook and RevertBookToVersion.
//
// A read error, or a store that cannot read retired rows, is an error that
// does not wrap ErrBookIsRetiredShell (fail closed: neither is proof the book
// is a shell, neither is proof it is not). action names what was refused
// ("purge", "restore", ...).
func RefuseRetiredShell(store any, shellID, action string) error {
	n, err := RetiredRowsHeldFor(store, shellID)
	if err != nil {
		return fmt.Errorf("%s %s: cannot verify it is not the shell of a retired version: %w", action, shellID, err)
	}
	return retiredShellRefusal(action, shellID, n)
}

// retiredShellRefusal is the one wording of the refusal; nil when n is 0. It
// says what to do instead ("leave it in the trash") and that restoring a
// retired version as a separate book is not available yet, so it never sends
// the user to a control that does not work.
func retiredShellRefusal(action, shellID string, n int) error {
	if n == 0 {
		return nil
	}
	consequence := "removing this book would drop the record of where they came from"
	switch action {
	case "restore":
		consequence = "this book would come back with none"
	case "revert book":
		consequence = "a whole-row revert would bring back the trash mark and clear the link the retire wrote"
	}
	return fmt.Errorf("%s %s: %w (%d retired file record(s) still held under the books its versions were retired onto, so %s); leave it in the trash, its files are safe and listed as retired versions of those books, and restoring them as a separate book is not available yet",
		action, shellID, ErrBookIsRetiredShell, n, consequence)
}

// deleteBookOpts are the variations of the one DeleteBook body. The zero value
// is DeleteBook.
type deleteBookOpts struct {
	// allowRetiredShell lets the delete proceed for a book that is a retired
	// shell. It is the seam for the journaled purge (retire plan PR 6), the
	// one caller that means to delete a shell: it snapshots the shell first, so
	// a per-version restore can put the book back, and then sets this. Today
	// only DeleteRetiredShellForTest sets it (retired_shell_seed.go: it deletes
	// nothing outside a test binary, ErrShellDeleteOutsideTest), so no
	// production path deletes a shell and DeleteBook refuses it for every
	// caller. It is deliberately not reachable through DeleteBook(id) or any
	// exported function a caller could pass it to by accident; the purge PR adds
	// its own exported entry point, never a flag.
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
