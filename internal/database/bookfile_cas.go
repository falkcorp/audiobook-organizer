// file: internal/database/bookfile_cas.go
// version: 1.0.0
// guid: 6b991179-c549-4086-9050-022c32b0cfce
// last-edited: 2026-10-26

package database

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/cockroachdb/pebble/v2"
)

// ErrBookFileConflict is returned (inside a *BookFileConflictError) by a
// book_file read-modify-write that lost its compare-and-swap on every attempt:
// another writer changed the row between each read and its commit, so the
// store gave up rather than write a value built from a row that no longer
// exists. Nothing was written. A caller that counts failures should count this
// one separately — the row is not bad, it is busy — and may simply try again
// later.
//
// It is deliberately not ErrBookFileChangedSince, which means "the precondition
// YOU supplied no longer holds" (a different thing to act on), and it is not
// errBookFileRowVanished, which the store's own retry loops consume.
var ErrBookFileConflict = errors.New("book_file row kept changing under the write")

// BookFileConflictError is the typed form of ErrBookFileConflict:
// errors.Is(err, ErrBookFileConflict) matches it and errors.AsType extracts
// it for the row it names.
type BookFileConflictError struct {
	BookID, FileID string
	// Attempts is how many times the write was staged and refused.
	Attempts int
}

func (e *BookFileConflictError) Error() string {
	return fmt.Sprintf("book_file %s (book %s) changed under the write on each of %d attempts; nothing was written: %v",
		e.FileID, e.BookID, e.Attempts, ErrBookFileConflict)
}

// Is makes errors.Is(err, ErrBookFileConflict) true.
func (e *BookFileConflictError) Is(target error) bool { return target == ErrBookFileConflict }

// rmwBookFile runs one read-modify-write of a single book_file row to
// completion. attempt reads the row afresh, applies the caller's change to
// that read and stages the commit; when the commit is refused because the row
// vanished or changed since the read (errBookFileRowVanished — see
// commitBookFileBatch), attempt runs again from a new read, so the caller's
// change is re-applied on top of the concurrent writer's, not over it. After
// maxStageRetries refusals the write is abandoned with a *BookFileConflictError.
//
// attempt must start from a pristine copy of everything it mutates: a retry
// that began from a half-merged struct would carry the last attempt's
// restored-from-stale fields into the next one.
//
// bookID is a pointer because a caller that knows only the file ID learns the
// book from attempt's read; it names the row in a *BookFileConflictError.
func rmwBookFile[T any](bookID *string, fileID string, attempt func() (T, error)) (T, error) {
	for range maxStageRetries {
		v, err := attempt()
		if !errors.Is(err, errBookFileRowVanished) || errors.Is(err, ErrBookFileOwnerMissing) {
			return v, err
		}
	}
	var zero T
	return zero, &BookFileConflictError{BookID: *bookID, FileID: fileID, Attempts: maxStageRetries}
}

// readBookFileRow reads a book_file row and the exact stored bytes it was
// decoded from. The bytes are copied (a Pebble value is only valid until its
// closer is closed) and are what a writer hands commitBookFileBatch as the
// expected value (bookFileUnchanged). (nil, nil, nil) when the row is absent.
func (s *PebbleStore) readBookFileRow(bookID, fileID string) (*BookFile, []byte, error) {
	return s.readBookFileRowAt(bookFileKey(bookID, fileID))
}

// readBookFileRowAt is readBookFileRow for a primary key already in hand.
func (s *PebbleStore) readBookFileRowAt(key []byte) (*BookFile, []byte, error) {
	value, closer, err := s.db.Get(key)
	if errors.Is(err, pebble.ErrNotFound) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	defer closer.Close()
	raw := append([]byte(nil), value...)
	var f BookFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, nil, err
	}
	return &f, raw, nil
}

// readBookFileRowByIDIndex resolves a row through the book_file_id index (the
// path SetBookFileHash and UpdateBookFileHashes use, which know only the file
// ID) and returns it with its stored bytes. who prefixes errors, so each
// caller keeps its own message. A missing index entry or row is an error, as it
// always was for these two callers.
func (s *PebbleStore) readBookFileRowByIDIndex(id, who string) (*BookFile, []byte, error) {
	val, closer, err := s.db.Get([]byte("book_file_id:" + id))
	if err != nil {
		return nil, nil, fmt.Errorf("%s: lookup id index: %w", who, err)
	}
	key := append([]byte(nil), val...)
	closer.Close()

	row, raw, err := s.readBookFileRowAt(key)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: get file: %w", who, err)
	}
	if row == nil {
		return nil, nil, fmt.Errorf("%s: get file: %w", who, pebble.ErrNotFound)
	}
	return row, raw, nil
}

// bookFileRowWrite is what setBookFileRowIfUnchanged did.
type bookFileRowWrite uint8

const (
	// bookFileRowWritten: the row held the expected bytes and was replaced.
	bookFileRowWritten bookFileRowWrite = iota
	// bookFileRowGone: the row is no longer committed; nothing was written.
	bookFileRowGone
	// bookFileRowChanged: the row holds something other than the bytes the
	// writer read; nothing was written. The writer should re-read and re-apply.
	bookFileRowChanged
)

// sweepChunkRows caps how many rows one sweep commit rewrites. A commit holds
// the owner stripe of every book it touches (commitBookFileBatch), so a chunk
// that spanned a whole 2,000-row batch would pin most stripes against every
// other book_file writer for the memtable apply.
const sweepChunkRows = 256

// sweepRowRef is a book_file row a library sweep read: its primary key and
// exactly the bytes it read.
type sweepRowRef struct {
	key, raw []byte
}

// sweepRowStager stages the rewrite of one row into b, judging it from ref.raw
// (the bytes the row held when it was read, or, on a retry, when it was
// re-read). wrote reports whether it staged a write of ref.key: only those rows
// are compared at commit. applied, if non-nil, runs once the chunk containing
// the row has committed (counters, memdb) — never for a row of an attempt that
// was refused.
type sweepRowStager func(b *GatedBatch, ref sweepRowRef) (applied func(), wrote bool, err error)

// commitBookFileSweepChunk stages and commits one chunk of a sweep's rewrites
// under the owner stripes, compared with the bytes each row was read as. The
// sweeps read the whole library up front and write minutes later, so a row's
// snapshot is routinely stale: a row that vanished is dropped, a row that
// changed is re-read and re-judged by stage (it may no longer need a rewrite),
// and the chunk is re-staged. After maxStageRetries refusals a still-changing
// row is given up on and counted in conflicts (it is left exactly as stored;
// the sweep is resumable); the rest of the chunk commits.
//
// The sweeps used to commit through a raw batch that took no stripe at all, so
// they could land between another writer's compare and its commit, and could
// put back a row as it was minutes ago.
func (s *PebbleStore) commitBookFileSweepChunk(chunk []sweepRowRef, stage sweepRowStager) (conflicts int, err error) {
	rows := append([]sweepRowRef(nil), chunk...)
	for round := 1; len(rows) > 0; round++ {
		batch := s.db.NewBatch()
		var (
			rewrites []bookFileRewrite
			applied  []func()
			written  []sweepRowRef
		)
		for _, r := range rows {
			fn, wrote, serr := stage(batch, r)
			if serr != nil {
				_ = batch.Close()
				return conflicts, serr
			}
			if fn != nil {
				applied = append(applied, fn)
			}
			if wrote {
				written = append(written, r)
				rewrites = append(rewrites, bookFileUnchanged(r.key, r.raw))
			}
		}
		if len(written) == 0 {
			_ = batch.Close()
			for _, fn := range applied {
				fn()
			}
			return conflicts, nil
		}
		cerr := s.commitBookFileBatch(batch, nil, nil, rewrites)
		if stale, ok := errors.AsType[*staleStageError](cerr); ok {
			isStale := map[string]bool{}
			for _, k := range stale.staleKeys() {
				isStale[string(k)] = true
			}
			var next []sweepRowRef
			for _, r := range rows {
				if !isStale[string(r.key)] {
					next = append(next, r)
					continue
				}
				if round >= maxStageRetries {
					conflicts++
					continue
				}
				fresh, raw, rerr := s.readBookFileRowAt(r.key)
				if rerr != nil {
					return conflicts, rerr
				}
				if fresh == nil {
					continue // deleted: nothing left to rewrite
				}
				next = append(next, sweepRowRef{key: r.key, raw: raw})
			}
			rows = next
			continue
		}
		if !bookFileApplied(cerr) {
			return conflicts, cerr
		}
		for _, fn := range applied {
			fn()
		}
		return conflicts, cerr
	}
	return conflicts, nil
}
