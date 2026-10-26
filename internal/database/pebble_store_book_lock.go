// file: internal/database/pebble_store_book_lock.go
// version: 1.10.0
// guid: 3f8c2a91-6d4e-4b7a-9e15-c0d2a8b47f63
// last-edited: 2026-10-26

package database

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"reflect"
	"slices"
	"sync"
)

// bookLockStripes is the number of per-book write locks. A fixed array, not a
// map keyed by ID: the memory is bounded no matter how many books exist, and
// two books that hash to the same stripe merely queue behind each other.
const bookLockStripes = 256

// bookLocks serializes every read-modify-write of one book row.
//
// LOCK RULES (enforced by inspection; there is no re-entrancy):
//
//   - UpdateBook, ModifyBook and every in-store helper that reads a book and
//     writes it back take the stripe for that book's ID around BOTH the read
//     of the stored row and the batch commit.
//   - No code path holds two book stripes at once. That is what makes a stripe
//     collision between two different IDs safe: the second writer waits, it
//     never deadlocks. A helper that writes several books (MarkITunesSynced,
//     MoveBookFilesToBookBulk) takes and releases one stripe per book, in sequence.
//   - Nothing slow runs under a stripe: no network, no file I/O, no ffprobe.
//     Only Pebble reads and the one batch commit.
//   - A ModifyBook callback must not call any book write (UpdateBook,
//     ModifyBook, FillBookMediaInfo, or a helper built on them) -- the stripe
//     it would need may be the one already held, and sync.Mutex does not
//     re-enter. Callbacks mutate the struct they are handed and return.
type bookLocks [bookLockStripes]sync.Mutex

// stripeFor returns the stripe index for a book ID (FNV-1a).
func stripeFor(id string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	return int(h.Sum32() % bookLockStripes)
}

// lockBook takes the write stripe for id and returns its unlock func.
func (p *PebbleStore) lockBook(id string) func() {
	mu := &p.bookLocks[stripeFor(id)]
	mu.Lock()
	return mu.Unlock
}

// lockBookFile takes the write stripe for a book_file ID and returns its
// unlock func. UpdateBookFile, UpdateBookFileHashes, SetBookFileHash and
// PatchBookFileFields hold it across their read of the stored row and their
// commit, so none of them reverts a field another committed in between.
//
// It is a separate stripe set from bookLocks on purpose. UpdateBookFile's
// post-commit aggregate recompute writes the book under lockBook; every
// book_file writer releases its file stripe before that recompute, so a file
// stripe is never held while a book stripe is taken and the two sets cannot
// deadlock against each other. The multi-row writers take their rows' stripes
// through lockBookFiles (the retired-version writers and BatchUpsertBookFiles);
// the other batch paths (moves, deletes) do not take file stripes.
func (p *PebbleStore) lockBookFile(fileID string) func() {
	mu := &p.bookFileLocks[stripeFor(fileID)]
	mu.Lock()
	return mu.Unlock
}

// lockBookFiles takes the write stripes of every file ID at once, ascending and
// de-duplicated, and returns the unlock func. It is for the multi-row writers
// that read rows, stage a batch and commit it: the retired-version writers
// (RetireBookFiles, UnretireBookFiles, RehomeRetiredVersion) and
// BatchUpsertBookFiles. They must exclude every other book_file writer for the
// rows they rewrite: without it a fingerprint or transcript write, or a scan's
// upsert, committed between the read and the commit is silently undone by the
// batch, which writes the row as it was read.
//
// LOCK ORDER: version stripe (lockBookVersion), then the file stripes here, then
// the owner stripes (commitBookFileBatch), then, innermost, the single-owner
// index mutex (lockSingleOwnerIndex). Book stripes are taken only after the
// file stripes are released. Holding several file stripes is deadlock-free
// because every holder takes its stripes in this same ascending order (the
// single-row writers take exactly one and never wait for a second). The stripes
// are not re-entrant: nothing called under them may take a file stripe.
func (p *PebbleStore) lockBookFiles(ids []string) func() {
	stripes := make([]int, 0, len(ids))
	for _, id := range ids {
		if id != "" {
			stripes = append(stripes, stripeFor(id))
		}
	}
	slices.Sort(stripes)
	stripes = slices.Compact(stripes)
	for _, st := range stripes {
		p.bookFileLocks[st].Lock()
	}
	return func() {
		for _, st := range slices.Backward(stripes) {
			p.bookFileLocks[st].Unlock()
		}
	}
}

// lockBookVersion takes the write stripe for a book version ID. It is the
// outermost lock of the retired-version writers (before the file stripes,
// before the owner stripes): UpdateBookVersion and DeleteBookVersion hold it
// across their read and commit, and RetireBookFiles, UnretireBookFiles and
// RehomeRetiredVersion hold it across the whole attempt loop, so the set of
// rows that carry a retired version and the version's holder cannot change
// under any of them. Nothing takes a book, book_file or owner stripe and then a
// version stripe.
func (p *PebbleStore) lockBookVersion(versionID string) func() {
	mu := &p.bookVersionLocks[stripeFor(versionID)]
	mu.Lock()
	return mu.Unlock
}

// singleOwnerIndexContendedHook, when non-nil, runs when lockSingleOwnerIndex
// finds the mutex held, just before it waits. Test-only: it lets a test prove a
// writer was held out without sleeping.
var singleOwnerIndexContendedHook func()

// lockSingleOwnerIndex takes the single-owner index commit mutex and returns its
// unlock func.
//
// WHAT IT GUARDS. The single-owner index keys (book_file_path:, book_file_pid:,
// book_file_hash:, book_file_orig_hash:, book_file_acoustid:) name ONE row each,
// last writer wins, and a LIVE row's write of one is an unconditional Set. The
// retired-version writers (RetireBookFiles, UnretireBookFiles,
// RehomeRetiredVersion) decide each key from a read taken before the owner
// stripes and re-check it (indexCAS.verify) under them; the owner stripes
// exclude only writers that share one of the batch's owner books, so a live
// writer of the same key under other books' stripes could commit between that
// re-check and the batch's apply, and the retired row's reference would then
// overwrite the live row's key (a duplicate PID import, a second live row for a
// scanned path). Holding this mutex across [re-check, apply] in the retired
// writers, and across the apply of every other batch that writes or deletes
// one of these keys, makes the re-check and the commit one step: another writer
// lands before the re-check (and fails it, re-staging the batch) or after the
// apply (and takes the key from the retired row, as a live row must).
//
// WHO TAKES IT. commitBookFileBatchVerified (so CreateBookFile,
// BatchCreateBookFiles, UpdateBookFile and its hash writers, UpsertBookFile and
// BatchUpsertBookFiles, MoveBookFilesToBook(Bulk), and the retire, un-retire
// and rehome writers), ClaimBookFilePathKey and ClaimRetiredBookFilePathKey
// (claimPathKey), the three book_file delete commits
// (commitWithWindowCascadeIfPresent, commitWithWindowCascade) and the
// AcoustID-segment wipes. NOT setBookFileRowIfPresent or PatchBookFileFields:
// they write the primary row key only and never an index key (their field
// sets exclude every indexed field). A new writer of one of these keys must
// take it around its Commit; TestSingleOwnerIndexStagers_AreAllListed fails on
// an unlisted stager, and TestSingleOwnerIndexWriters_AreHeldOutOfARetiresVerifyCommitWindow
// proves the writers are held out of a retire's window.
//
// WHERE IT SITS. It is the LEAF of the book_file lock order:
//
//	version stripe -> file stripes -> window refs -> owner stripes -> THIS
//
// Everything under it is Pebble reads (the verify hook) and the batch apply;
// nothing under it takes a stripe, another mutex or the WAL fsync (which runs
// after every release), so it cannot take part in a cycle, and it inverts no
// existing order: no code holds it while taking a book, file, version, window
// or owner stripe. It is NOT re-entrant: a verify hook must not call a writer.
// Hold time is one apply (NoSync) or, for the Sync-committing wipes, one
// flush.
func (p *PebbleStore) lockSingleOwnerIndex() func() {
	if !p.singleOwnerIndexMu.TryLock() {
		if singleOwnerIndexContendedHook != nil {
			singleOwnerIndexContendedHook()
		}
		p.singleOwnerIndexMu.Lock()
	}
	return p.singleOwnerIndexMu.Unlock
}

// ErrSkipBookWrite, returned by a ModifyBook callback, means "nothing to
// change": ModifyBook writes nothing (no UpdatedAt bump, no version snapshot)
// and returns the row as read, with a nil error.
var ErrSkipBookWrite = errors.New("skip book write")

// ModifyBook is the lost-update-safe way to change a book. Under the book's
// write stripe it reads the stored row, hands it to fn to mutate in place, and
// writes the result -- so no other writer's commit can land between the read
// and the write and be reverted by it, which is what a caller-side
// GetBookByID -> mutate -> UpdateBook cannot guarantee.
//
// It returns (nil, nil) when the book does not exist (fn is not called). An
// error from fn aborts without writing and is returned as-is, except
// ErrSkipBookWrite, which returns the unmodified row and a nil error.
//
// Unlike UpdateBook, a nil Author that fn leaves is written as nil: fn was
// handed the full row, so the nil is a deliberate clear, not a stripped
// projection to fill back in (bookWriteOpts.authorAsGiven).
//
// fn runs while the stripe is held: see the LOCK RULES on bookLocks.
func (p *PebbleStore) ModifyBook(id string, fn func(*Book) error) (*Book, error) {
	updated, before, err := p.modifyBookLocked(id, fn)
	if err == nil && updated != nil {
		p.syncNarratorJunctionAfterWrite(id, before, updated)
	}
	return updated, err
}

// modifyBookLocked is ModifyBook's body under the stripe. It also returns the
// Narrator the stored row held before fn ran, read under the same hold, so the
// junction sync compares against what was really overwritten.
func (p *PebbleStore) modifyBookLocked(id string, fn func(*Book) error) (*Book, string, error) {
	return p.modifyBookLockedMode(id, fn, bookWriteOpts{})
}

// modifyBookLockedMode is the read-fn-write body shared by ModifyBook and
// SeedLegacyBookRowForTest; opts selects the write variation.
func (p *PebbleStore) modifyBookLockedMode(id string, fn func(*Book) error, opts bookWriteOpts) (*Book, string, error) {
	// fn is handed the full stored row, so a nil Author it leaves is a
	// deliberate clear (see bookWriteOpts.authorAsGiven).
	opts.authorAsGiven = true
	unlock := p.lockBook(id)
	defer unlock()

	fresh, err := p.GetBookByID(id)
	if err != nil {
		return nil, "", err
	}
	if fresh == nil {
		return nil, "", nil
	}
	before := narratorOf(fresh)
	if err := fn(fresh); err != nil {
		if errors.Is(err, ErrSkipBookWrite) {
			return fresh, before, nil
		}
		return nil, "", err
	}
	updated, err := p.updateBookLockedMode(id, fresh, opts)
	return updated, before, err
}

// SnapshotBook returns a deep copy of b (a JSON round trip, so pointer fields
// are not shared with b). Pair it with MergeBookChanges: take the snapshot
// right after reading a book, mutate the original however the caller needs
// (including slow work in between), then merge inside ModifyBook.
func SnapshotBook(b *Book) (*Book, error) {
	if b == nil {
		return nil, nil
	}
	data, err := json.Marshal(b)
	if err != nil {
		return nil, fmt.Errorf("snapshot book %s: %w", b.ID, err)
	}
	var out Book
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("snapshot book %s: %w", b.ID, err)
	}
	return &out, nil
}

// MergeBookChanges copies onto dst every top-level Book field whose value in
// after differs from before, and leaves every other field of dst alone. It is
// a three-way merge for a caller that read a book (before = SnapshotBook of
// it), changed some fields (after), and now writes into the freshly re-read
// row (dst) inside ModifyBook: a field the caller did not touch keeps whatever
// a concurrent writer committed in the meantime, instead of being reverted to
// the caller's stale read. A field both sides changed ends with the caller's
// value (last write wins, per field).
//
// Fields are compared by their JSON encoding, which is what the store persists,
// so a time.Time that lost its monotonic reading in the snapshot round trip
// still compares equal. ID, CreatedAt and UpdatedAt are never copied: the store
// owns them.
//
// The db:"-" fields Authors and MetadataProvenance are persisted in the JSON
// row like everything else, so they are merged like everything else, on
// purpose. A copied slice or map shares its backing storage with after; that is
// safe because after is the caller's working copy and is not used again once
// the merged row is written.
//
// Memdb-projection safety: dst must be a full Pebble row (ModifyBook passes
// one). If before and after came from a stripped projection, the stripped
// fields are nil on both sides, compare equal, and are not copied, so dst keeps
// the stored values.
//
// It returns the JSON names of the fields it copied, for logging and tests.
func MergeBookChanges(dst, before, after *Book) ([]string, error) {
	if dst == nil || before == nil || after == nil {
		return nil, fmt.Errorf("merge book changes: nil book")
	}
	dv := reflect.ValueOf(dst).Elem()
	bv := reflect.ValueOf(before).Elem()
	av := reflect.ValueOf(after).Elem()
	t := dv.Type()
	var changed []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		switch f.Name {
		case "ID", "CreatedAt", "UpdatedAt":
			continue
		}
		bj, err := json.Marshal(bv.Field(i).Interface())
		if err != nil {
			return nil, fmt.Errorf("merge book changes: encode %s: %w", f.Name, err)
		}
		aj, err := json.Marshal(av.Field(i).Interface())
		if err != nil {
			return nil, fmt.Errorf("merge book changes: encode %s: %w", f.Name, err)
		}
		if bytes.Equal(bj, aj) || emptyCollections(bv.Field(i), av.Field(i)) {
			continue
		}
		dv.Field(i).Set(av.Field(i))
		name := f.Tag.Get("json")
		if comma := bytes.IndexByte([]byte(name), ','); comma >= 0 {
			name = name[:comma]
		}
		if name == "" {
			name = f.Name
		}
		changed = append(changed, name)
	}
	return changed, nil
}

// emptyCollections reports whether a and b are both slices, or both maps, of
// length zero. MergeBookChanges treats nil and empty as the same value because
// SnapshotBook's JSON round trip cannot keep them apart: Authors and
// MetadataProvenance are omitempty, so a non-nil empty []/{} in the caller's
// read comes back nil in before. Without this, a working copy that still holds
// that empty []/{} reads as "changed" and wipes the authors or provenance a
// concurrent writer committed.
func emptyCollections(a, b reflect.Value) bool {
	switch a.Kind() {
	case reflect.Slice, reflect.Map:
		return a.Len() == 0 && b.Len() == 0
	}
	return false
}

// ClearBookSignature implements Store. Under the book's write lock it runs a
// normal book write (same book_ver: snapshot and indexes as UpdateBook) that
// deletes the book_sig: sidecar AND rewrites the row without any inline
// signature, all in one batch. Deleting only the sidecar is not enough: a row
// written before the sidecar migration still carries BookSigV1 inline, and
// hydrateBookSig falls back to it, so the signature would survive the clear.
// A book with no signature at all is left untouched (no write).
func (p *PebbleStore) ClearBookSignature(id string) error {
	unlock := p.lockBook(id)
	defer unlock()
	fresh, err := p.GetBookByID(id)
	if err != nil || fresh == nil {
		return err
	}
	if _, has := bookSigOf(fresh); !has {
		return nil
	}
	_, err = p.updateBookLockedMode(id, fresh, bookWriteOpts{clearSig: true})
	return err
}
