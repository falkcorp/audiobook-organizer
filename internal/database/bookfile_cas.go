// file: internal/database/bookfile_cas.go
// version: 1.3.0
// guid: 6b991179-c549-4086-9050-022c32b0cfce
// last-edited: 2026-10-26

package database

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// ErrBookFileKeyMismatch is returned when a book_file row is stored under a key
// that names a different book than the row's own BookID says. No amount of
// retrying finds such a row under the key its fields spell, so it is not a busy
// row (ErrBookFileConflict) and the writers that follow a row by that key stop
// with this error instead of looking for it until their budget runs out. The row
// is damaged and wants repair; nothing here touches it.
var ErrBookFileKeyMismatch = errors.New("book_file row is stored under a key that names a different book")

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

// BookFileSweepConflictError is returned by a library sweep that finished but
// had to leave Rows rows as stored because other writers kept changing them.
// errors.Is(err, ErrBookFileConflict) matches it. The rest of the sweep was
// applied, and re-running it finishes the remainder.
type BookFileSweepConflictError struct {
	Op   string
	Rows int
}

func (e *BookFileSweepConflictError) Error() string {
	return fmt.Sprintf("%s: %d row(s) kept changing under the sweep and were left as stored; run it again: %v",
		e.Op, e.Rows, ErrBookFileConflict)
}

// Is makes errors.Is(err, ErrBookFileConflict) true.
func (e *BookFileSweepConflictError) Is(target error) bool { return target == ErrBookFileConflict }

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

// restageBookFileBatch runs a whole-batch book_file writer (a move, retire,
// unretire, rehome or delete: all rows or none) to completion. attempt reads
// the rows and stages and commits them; it is re-run when the commit was
// refused because a row it rewrites vanished or changed since it was read
// (errBookFileRowVanished).
//
// Between rounds it waits a short, jittered, doubling interval
// (restageBackoff). The writer that wins the compare-and-swap is usually one
// that walks the same books (a hash backfill, the scanner on a forced pass),
// and re-running at once re-reads rows it is still in the middle of; waiting
// lets it move on, and the jitter keeps two refused batches from retrying in
// step.
//
// attempt is handed a row cache. A round that was refused names exactly the
// rows that changed (the commit's compare covers every row the attempt read
// through the cache, and the rows it did NOT name were verified unchanged under
// the stripes, which is fresher than a new read), so only those are dropped from
// the cache and the next round reads just them from the store. Encoding and
// staging are still redone each round.
//
// The budget is maxBatchStageRounds, not maxStageRetries, because a long batch
// loses a round to ANY writer that changes ANY of its rows, and a batch of a few
// thousand rows has a window of seconds, so five rounds are not much against a
// writer that walks the same books. After the budget it gives up with an error
// wrapping ErrBookFileConflict and writes nothing; the caller retries the whole
// batch later (a merge's combine answers retry-later, see merge/service.go).
//
// Why the rows' file stripes are not taken for the staging window instead:
//   - The writers that win these races take no file stripe: a create claiming
//     an iTunes ID, the bulk moves, the deletes, the fingerprint sweeps and the
//     scan-cache stamp would still get in, so the stripes would not remove the
//     loss, only the losses to the single-row writers (and, for the rows a
//     batch upsert rewrites, the batch upsert) that already take them.
//   - A regroup moves a thousand or two rows, which hash to nearly every one of
//     the 256 file stripes. Holding them across the staging window (seconds)
//     would be a library-wide lock on every single-row book_file write.
//
// (The post-commit aggregate recompute, which takes a book stripe and which
// the lock rules on lockBookFile forbid under a file stripe, could be moved
// after the unlock; it is not the reason.)
func restageBookFileBatch(who string, attempt func(rows *bookFileRowCache) error) error {
	rows := newBookFileRowCache()
	var err error
	for round := range maxBatchStageRounds {
		if round > 0 {
			bookFileRestageSleep(restageBackoff(round))
		}
		err = attempt(rows)
		if !errors.Is(err, errBookFileRowVanished) || errors.Is(err, ErrBookFileOwnerMissing) {
			return err
		}
		rows.dropStale(err)
	}
	return fmt.Errorf("%s: gave up after %d staging rounds: %w: %w", who, maxBatchStageRounds, ErrBookFileConflict, err)
}

// bookFileRestageSleep waits between whole-batch staging rounds. A variable so
// a test can observe the intervals and skip the wait.
var bookFileRestageSleep = time.Sleep

// restageBackoff is the wait before staging round (1-based count of rounds
// already refused): 10ms doubling to a 100ms cap, with the actual wait drawn
// uniformly from the upper half of that interval.
func restageBackoff(refusedRounds int) time.Duration {
	d := restageBackoffBase << min(refusedRounds-1, 4)
	if d > restageBackoffMax {
		d = restageBackoffMax
	}
	return d/2 + rand.N(d/2+1)
}

const (
	restageBackoffBase = 10 * time.Millisecond
	restageBackoffMax  = 100 * time.Millisecond
)

// bookFileRowCache keeps the stored bytes of the book_file rows a whole-batch
// writer read, across its staging rounds. It holds only bytes whose primary
// key is in the commit's compare set, so a cached row that changed is caught at
// the commit (and dropped by dropStale) exactly like a freshly read one.
// Not safe for concurrent use: one restageBookFileBatch call owns it.
type bookFileRowCache struct {
	raw map[string][]byte
}

func newBookFileRowCache() *bookFileRowCache {
	return &bookFileRowCache{raw: map[string][]byte{}}
}

// bookFileRowCacheMissHook, when non-nil, runs for every row a cache had to
// read from the store. Test-only.
var bookFileRowCacheMissHook func(key string)

// read is readBookFileRowAt through the cache: a row read in an earlier round
// and not named stale since is decoded from its cached bytes. The returned row
// is a fresh decode the caller may mutate, and raw is the cached slice, which
// nothing may modify. (nil, nil, nil) when the row is absent; an absent row is
// not cached.
func (c *bookFileRowCache) read(s *PebbleStore, key []byte) (*BookFile, []byte, error) {
	if raw, ok := c.raw[string(key)]; ok {
		var f BookFile
		if err := json.Unmarshal(raw, &f); err != nil {
			return nil, nil, fmt.Errorf("decode book_file %s: %w", key, err)
		}
		return &f, raw, nil
	}
	if bookFileRowCacheMissHook != nil {
		bookFileRowCacheMissHook(string(key))
	}
	f, raw, err := s.readBookFileRowAt(key)
	if err != nil || f == nil {
		return f, raw, err
	}
	c.raw[string(key)] = raw
	return f, raw, nil
}

// dropStale forgets the rows a refused round named. An error that does not name
// them (a refusal from a path that reports no keys) forgets everything.
func (c *bookFileRowCache) dropStale(err error) {
	stale, ok := errors.AsType[*staleStageError](err)
	if !ok || len(stale.staleKeys()) == 0 {
		clear(c.raw)
		return
	}
	for _, k := range stale.staleKeys() {
		delete(c.raw, string(k))
	}
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
	return s.loadBookFileRowAt(key, true)
}

// loadBookFileRowAt reads and decodes the row at key. With copyRaw it also
// returns the stored bytes, copied, for a writer that compares them at commit.
// Without it the row is decoded straight from the Pebble buffer and raw is nil:
// a pure reader pays no copy of a row that can run to hundreds of kilobytes
// once its fingerprint is in it. (The decode copies every string and byte
// slice it keeps, so the row outlives the buffer.) (nil, nil, nil) when the row
// is absent.
func (s *PebbleStore) loadBookFileRowAt(key []byte, copyRaw bool) (*BookFile, []byte, error) {
	value, closer, err := s.db.Get(key)
	if errors.Is(err, pebble.ErrNotFound) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	defer closer.Close()
	var f BookFile
	if !copyRaw {
		if err := json.Unmarshal(value, &f); err != nil {
			return nil, nil, err
		}
		return &f, nil, nil
	}
	raw := append([]byte(nil), value...)
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
// the owner stripe of every book it touches (commitBookFileBatch). The owner
// stripes number 256, so a chunk of N rows spread over N books pins roughly
// 256*(1-exp(-N/256)) distinct stripes against every other book_file writer for
// the memtable apply: 256 rows would pin about 160 of them, close to a global
// book_file write lock. 64 pins about 56 (under a quarter).
const sweepChunkRows = 64

// sweepRowRef is a book_file row a library sweep read: its primary key and
// exactly the bytes it read. tries counts how many times a commit of the row
// was refused because it had changed; the retry budget is per row.
type sweepRowRef struct {
	key, raw []byte
	tries    int
}

// sweepRowStager stages the rewrite of one row into b, judging it from ref.raw
// (the bytes the row held when it was read, or, on a retry, when it was
// re-read). wrote reports whether it staged a write of ref.key: only those rows
// are compared at commit. applied, if non-nil, runs once the chunk containing
// the row has committed (counters, memdb) — never for a row of an attempt that
// was refused.
type sweepRowStager func(b *GatedBatch, ref sweepRowRef) (applied func(), wrote bool, err error)

// reloadSweepRow re-reads a row a sweep staged and found changed or missing at
// its key. ok is false only when the row is really gone: deleted, which leaves
// a tombstone, or absent from an authoritative scan of every book_file key.
//
// A row missing at its OLD key has not necessarily been deleted: a move, retire,
// unretire or rehome deletes the old key and writes the row, with its legacy
// segment columns and fingerprint, under a new owner. Treating that as a delete
// would leave the moved row unswept (and its fingerprint indexed or its legacy
// segments in place) while the sweep reports no trouble, so the row is looked up
// by its file ID, which is the last segment of the key.
func (s *PebbleStore) reloadSweepRow(key []byte) (ref sweepRowRef, ok bool, err error) {
	id := string(key[bytes.LastIndexByte(key, ':')+1:])
	for range maxStageRetries {
		row, raw, err := s.readBookFileRowAt(key)
		if err != nil {
			return sweepRowRef{}, false, err
		}
		if row != nil {
			return sweepRowRef{key: key, raw: raw}, true, nil
		}
		live, at, err := s.locateBookFileRow(id)
		if err != nil {
			return sweepRowRef{}, false, err
		}
		if live == nil {
			return sweepRowRef{}, false, nil
		}
		// Follow the row; if it moved again before it could be read, go round.
		key = at
	}
	// Still moving: ok with no bytes. The caller counts it as busy.
	return sweepRowRef{key: key}, true, nil
}

// locateBookFileRow finds the live row of a book_file id and the primary key it
// is stored at: through the id index, or, for a row written before the index
// existed, the authoritative scan of every book_file key. (nil, nil, nil) when
// the row is really gone: it carries a deletion tombstone, or no index entry, no
// tombstone and no scan hit. A row stored under a key that names another book
// than its own BookID is ErrBookFileKeyMismatch.
func (s *PebbleStore) locateBookFileRow(id string) (*BookFile, []byte, error) {
	live, _, at, gone, err := s.bookFileIDStateAt(id)
	if err != nil {
		return nil, nil, err
	}
	if gone {
		return nil, nil, nil
	}
	if live == nil {
		// No index entry and no tombstone: a row written before the id index,
		// or deleted without a tombstone.
		if live, at, err = s.scanForBookFileRowByID(id); err != nil {
			return nil, nil, err
		}
		if live == nil {
			return nil, nil, nil
		}
	}
	if want := bookFileKey(live.BookID, live.ID); !bytes.Equal(at, want) {
		return nil, nil, fmt.Errorf("%w: %s holds book_file %s that says it belongs to book %s", ErrBookFileKeyMismatch, at, id, live.BookID)
	}
	return live, at, nil
}

// commitBookFileSweepChunk stages and commits one chunk of a sweep's rewrites
// under the owner stripes, compared with the bytes each row was read as. The
// sweeps read the whole library up front and write minutes later, so a row's
// snapshot is routinely stale: a row that changed is re-read and re-judged by
// stage (it may no longer need a rewrite), a row that moved is followed to its
// new key (reloadSweepRow), a row that is really gone is dropped, and the chunk
// is re-staged. A row refused maxStageRetries times is given up on and returned
// in busy (left exactly as stored; the sweep is resumable); the rest of the
// chunk commits.
//
// The sweeps used to commit through a raw batch that took no stripe at all, so
// they could land between another writer's compare and its commit, and could
// put back a row as it was minutes ago.
func (s *PebbleStore) commitBookFileSweepChunk(chunk []sweepRowRef, stage sweepRowStager) (busy []sweepRowRef, err error) {
	rows := append([]sweepRowRef(nil), chunk...)
	// Staging a row is a pure function of its bytes (stagers have no stage-time
	// side effects; a row's counters run in applied, after the commit), so a row
	// whose bytes did not change is staged once, into a batch of its own, and each
	// round copies that into the round's batch (GatedBatch.Apply). Without this a
	// round refused over one row re-decoded, re-encoded and re-staged all 64: for
	// fingerprinted rows, 64 unmarshals and marshals of multi-KB values per round,
	// under sustained churn on a single row up to maxStageRetries rounds. A row
	// that changed (or moved) is re-read, and its cached staging dropped with it.
	type stagedRow struct {
		sub     *GatedBatch
		applied func()
		wrote   bool
	}
	staged := make(map[string]*stagedRow, len(rows))
	defer func() {
		for _, st := range staged {
			_ = st.sub.Close()
		}
	}()
	for len(rows) > 0 {
		batch := s.db.NewBatch()
		var (
			rewrites []bookFileRewrite
			applied  []func()
			written  int
		)
		for _, r := range rows {
			st := staged[string(r.key)]
			if st == nil {
				sub := s.db.NewBatch()
				fn, wrote, serr := stage(sub, r)
				if serr != nil {
					_ = sub.Close()
					_ = batch.Close()
					return busy, serr
				}
				st = &stagedRow{sub: sub, applied: fn, wrote: wrote}
				staged[string(r.key)] = st
			}
			if aerr := batch.Apply(st.sub, nil); aerr != nil {
				_ = batch.Close()
				return busy, aerr
			}
			if st.applied != nil {
				applied = append(applied, st.applied)
			}
			if st.wrote {
				written++
				rewrites = append(rewrites, bookFileUnchanged(r.key, r.raw))
			}
		}
		if written == 0 {
			_ = batch.Close()
			for _, fn := range applied {
				fn()
			}
			return busy, nil
		}
		cerr := s.commitBookFileBatch(batch, nil, nil, rewrites)
		stale, isStale := errors.AsType[*staleStageError](cerr)
		if !isStale {
			if !bookFileApplied(cerr) {
				return busy, cerr
			}
			for _, fn := range applied {
				fn()
			}
			return busy, cerr
		}
		staleKey := map[string]bool{}
		for _, k := range stale.staleKeys() {
			staleKey[string(k)] = true
		}
		var next []sweepRowRef
		for _, r := range rows {
			if !staleKey[string(r.key)] {
				next = append(next, r)
				continue
			}
			if st := staged[string(r.key)]; st != nil {
				_ = st.sub.Close()
				delete(staged, string(r.key))
			}
			fresh, ok, rerr := s.reloadSweepRow(r.key)
			if rerr != nil {
				return busy, rerr
			}
			if !ok {
				continue // really deleted: nothing left to rewrite
			}
			fresh.tries = r.tries + 1
			if fresh.raw == nil || fresh.tries >= maxStageRetries {
				busy = append(busy, fresh)
				continue
			}
			next = append(next, fresh)
		}
		rows = next
	}
	return busy, nil
}

// retrySweepRows gives rows a commit returned as busy one more full budget,
// after the rest of the sweep has finished (the writers that kept changing them
// have often moved on by then). It returns the rows that are still busy.
func (s *PebbleStore) retrySweepRows(busy []sweepRowRef, stage sweepRowStager) ([]sweepRowRef, error) {
	var still []sweepRowRef
	for start := 0; start < len(busy); start += sweepChunkRows {
		var chunk []sweepRowRef
		for _, r := range busy[start:min(start+sweepChunkRows, len(busy))] {
			fresh, ok, err := s.reloadSweepRow(r.key)
			if err != nil {
				return still, err
			}
			if ok && fresh.raw == nil {
				still = append(still, fresh)
			} else if ok {
				chunk = append(chunk, fresh)
			}
		}
		left, err := s.commitBookFileSweepChunk(chunk, stage)
		still = append(still, left...)
		if err != nil {
			return still, err
		}
	}
	return still, nil
}
