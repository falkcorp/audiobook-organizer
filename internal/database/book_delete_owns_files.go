// file: internal/database/book_delete_owns_files.go
// version: 1.9.0
// guid: 8ffda8a0-e303-4a65-9f2f-71ab98e1b796
// last-edited: 2026-10-26

package database

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// ErrBookOwnsFiles is returned by DeleteBook when the book still owns
// book_file rows.
//
// DeleteBook tears down the book row and every index and sidecar keyed by the
// book's ID, but it does NOT delete book_file rows, and it must not: a
// book_file row is the only record tying an audio file on disk to the library,
// and deleting one as a side effect of a book delete is data loss (standing
// rule: never delete book_file rows as a repair — repoint them). Before this
// guard, every hard delete of a book that still owned files left those rows
// naming a book with no row: the soft-delete purge (a dedup-merge loser keeps
// its own files, so every purged loser orphaned them), the archive sweep,
// reconcile's version-group cleanup, batch hard-delete and the user-facing
// hard delete all did it.
//
// So a book that owns file rows cannot be hard-deleted. A caller that means to
// remove such a book must first move its rows to the book that should own them
// (MoveBookFilesToBook / MoveBookFilesToBookBulk) and then delete the empty
// shell. The check is in the primitive, not the callers, so it covers every
// caller by construction — including the next one somebody adds — which is the
// same reasoning DeleteBook's own dedup-candidate teardown gives.
var ErrBookOwnsFiles = errors.New("book still owns book_file rows")

// countBookFileRows counts the committed book_file:<bookID>: rows in Pebble.
//
// It reads Pebble, never memdb: this count authorizes (or refuses) a hard
// delete, and memdb is a derived projection that can be short. It counts keys
// only — no value is decoded — so it is O(rows this book owns).
func countBookFileRows(db *GatedDB, bookID string) (int, error) {
	prefix := []byte("book_file:" + bookID + ":")
	iter, err := db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixEnd(prefix),
	})
	if err != nil {
		return 0, err
	}
	defer iter.Close()
	n := 0
	for iter.First(); iter.Valid(); iter.Next() {
		n++
	}
	if err := iter.Error(); err != nil {
		return 0, err
	}
	return n, nil
}

// refuseDeleteIfBookOwnsFiles returns an ErrBookOwnsFiles-wrapping error when
// bookID still owns book_file rows, and fails closed (returns the read error)
// when the count cannot be taken.
func (p *PebbleStore) refuseDeleteIfBookOwnsFiles(bookID string) error {
	n, err := countBookFileRows(p.db, bookID)
	if err != nil {
		return fmt.Errorf("delete book %s: cannot verify it owns no book_file rows: %w", bookID, err)
	}
	if n > 0 {
		return fmt.Errorf("delete book %s: %w (%d row(s)); move them to the owning book first", bookID, ErrBookOwnsFiles, n)
	}
	return nil
}

// ── Serializing the check against book_file writers ─────────────────────────
//
// DeleteBook's "owns no rows" check is only true at the instant it is read. A
// book_file writer that commits a row naming the book between that read and
// DeleteBook's commit would leave an orphan all the same. So both sides take
// the book's OWNER stripe (bookOwnerLocks):
//
//   - DeleteBook holds it across the count and its commit.
//   - A writer that CREATES or MOVES a row under a book (CreateBookFile,
//     BatchCreateBookFiles, batch upserts, MoveBookFilesToBookBulk) holds the
//     stripes of every book its batch names across one existence Get per
//     distinct owner and the apply. A row whose owner is gone is refused ON
//     ITS OWN: the batch writers re-stage without it and commit everything
//     else (partitionedBookFileWrite, *BookFileRowsRefusedError).
//   - Every row a writer rewrites or moves away was read before the stripes
//     were taken, so it is re-checked under them (rewrites): the PID
//     transfer's prior owner, batch-upsert matches, move sources, and the
//     single-row rewriters (UpdateBookFile and friends, PatchBookFileFields,
//     the scan-cache stamp). The re-check is a COMPARE-AND-SWAP, not an
//     existence test: the stored bytes must equal the bytes the writer read
//     (bookFileRewrite.Expected). A key that vanished OR changed means "re-read
//     and re-stage" (errBookFileRowVanished), never "write it back": the
//     staged value was built from a row that no longer exists, so committing
//     it would revert whatever the concurrent writer changed (path, hash,
//     duration, tags, iTunes ID, fingerprint) and leave its secondary index
//     keys behind. Existence alone was the whole check until 2026-10-25.
//   - DeleteBookFile commits under the row's stripe after re-checking its key
//     too, and chases a row a concurrent move took elsewhere.
//
// Whichever commits first wins, and the other sees it: a delete that commits
// first makes the writer's existence check fail; a writer that commits first
// makes the delete's count non-zero.
//
// LOCK ORDER: owner stripes are the INNERMOST STRIPE. They may be taken
// while a book stripe (DeleteBook) or a book_file stripe (UpdateBookFile) is
// held, but nothing takes a book or book_file stripe while holding one. The one
// lock that may be taken under them is the single-owner index mutex
// (lockSingleOwnerIndex, pebble_store_book_lock.go), a leaf that holds only the
// batch apply (and the retired writers' index re-check) and takes nothing. A
// writer holding several takes them in ascending stripe order, de-duplicated,
// so two multi-book writers cannot deadlock and DeleteBook, which holds one,
// cannot either.
//
// WHAT RUNS UNDER THEM: one point Get per distinct owner book and per
// rewritten key, then the batch's apply WITHOUT an fsync (pebble.NoSync). The
// WAL fsync that makes the write durable runs AFTER the stripes are released
// (syncBookFileWAL). That is still correct: the apply makes the rows visible
// to DeleteBook's count the moment it returns, and the WAL is sequential, so
// any later commit that is durable — a DeleteBook that commits after us, say —
// is only durable once our earlier record is too. A 500-row batch naming 300
// books therefore holds those stripes for its reads and a memtable apply, not
// for a disk flush; a single-book writer behind it waits microseconds, not an
// fsync.

// ErrBookFileOwnerMissing is returned by a book_file writer asked to create or
// move a row under a book that has no row: committing it would create an
// orphan.
var ErrBookFileOwnerMissing = errors.New("book_file owner book does not exist")

// lockBookOwners takes the owner stripes of every id (de-duplicated, ascending)
// and returns the unlock func. Empty ids are ignored.
func (p *PebbleStore) lockBookOwners(ids ...string) func() {
	stripes := make([]int, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		stripes = append(stripes, stripeFor(id))
	}
	slices.Sort(stripes)
	stripes = slices.Compact(stripes)
	for _, st := range stripes {
		p.bookOwnerLocks[st].Lock()
	}
	return func() {
		for _, stripe := range slices.Backward(stripes) {
			p.bookOwnerLocks[stripe].Unlock()
		}
	}
}

// requireBookRowsExist returns ErrBookFileOwnerMissing unless every id has a
// committed book row (live or soft-deleted). Called with the ids' owner
// stripes held. An empty id is refused too: a row naming no book is an orphan.
func (p *PebbleStore) requireBookRowsExist(ids ...string) error {
	for _, id := range ids {
		if id == "" {
			return fmt.Errorf("%w: empty book id", ErrBookFileOwnerMissing)
		}
		_, closer, err := p.db.Get([]byte("book:" + id))
		if errors.Is(err, pebble.ErrNotFound) {
			return fmt.Errorf("%w: %s", ErrBookFileOwnerMissing, id)
		}
		if err != nil {
			return fmt.Errorf("check book %s exists: %w", id, err)
		}
		_ = closer.Close()
	}
	return nil
}

// errBookFileRowVanished marks a staged batch that rewrote (or moved) a row
// which was deleted after it was read. The batch was not applied; re-reading
// and re-staging gives the right answer (the row is simply gone now).
var errBookFileRowVanished = errors.New("book_file row changed while being written")

// staleStageError is commitBookFileBatch's refusal: the batch was staged from
// reads that no longer hold, so it was closed without applying. It names the
// owner books that are gone (rows for them must be refused), the rewritten
// keys that vanished and the rewritten keys whose stored bytes are no longer
// the bytes the writer read (the writer should re-read and re-stage).
type staleStageError struct {
	missingOwners []string
	vanishedKeys  [][]byte
	changedKeys   [][]byte
}

func (e *staleStageError) Error() string {
	return fmt.Sprintf("book_file batch refused: %d owner book(s) missing %v, %d rewritten row(s) vanished, %d changed",
		len(e.missingOwners), e.missingOwners, len(e.vanishedKeys), len(e.changedKeys))
}

// staleKeys is every rewritten key that no longer holds what the writer read:
// vanished and changed alike. Both mean "re-read and re-stage".
func (e *staleStageError) staleKeys() [][]byte {
	return append(append([][]byte(nil), e.vanishedKeys...), e.changedKeys...)
}

// Unwrap lets a single-owner caller test errors.Is(err, ErrBookFileOwnerMissing)
// and a retry loop test errors.Is(err, errBookFileRowVanished) (a rewritten
// row that vanished or changed since it was read).
func (e *staleStageError) Unwrap() error {
	if len(e.missingOwners) > 0 {
		return ErrBookFileOwnerMissing
	}
	return errBookFileRowVanished
}

// BookFileRowsRefusedError is returned by the batch book_file writers
// (BatchCreateBookFiles, BatchUpsertBookFiles, BatchUpsertScannedBookFiles)
// when some rows were refused because their owner book no longer exists.
// EVERY OTHER ROW WAS COMMITTED: one deleted book must not cost a batch its
// 499 unrelated rows. Callers must surface RefusedFileIDs (count and ids), not
// treat the batch as wholly failed, and never retry the committed rows as if
// they were lost.
type BookFileRowsRefusedError struct {
	Committed      int
	RefusedFileIDs []string
	MissingBookIDs []string
	// Reasons says why each refused row (by file ID) was refused.
	Reasons map[string]string
}

func (e *BookFileRowsRefusedError) Error() string {
	return fmt.Sprintf("%d book_file row(s) refused (missing owner book(s) %v; reasons by row %v); %d committed",
		len(e.RefusedFileIDs), e.MissingBookIDs, e.Reasons, e.Committed)
}

// Unwrap names what caused the refusals: ErrBookFileOwnerMissing when an owner
// book was gone, errBookFileRowVanished when a row kept changing.
func (e *BookFileRowsRefusedError) Unwrap() []error {
	var errs []error
	if len(e.MissingBookIDs) > 0 {
		errs = append(errs, ErrBookFileOwnerMissing)
	}
	if len(e.RefusedFileIDs) > 0 && len(e.MissingBookIDs) == 0 || e.churned() {
		errs = append(errs, errBookFileRowVanished)
	}
	if e.churned() {
		errs = append(errs, ErrBookFileConflict)
	}
	return errs
}

func (e *BookFileRowsRefusedError) churned() bool {
	for _, r := range e.Reasons {
		if strings.HasPrefix(r, "the row it rewrites kept changing") {
			return true
		}
	}
	return false
}

// maxBatchStageRounds is how many times a whole-batch book_file writer
// re-stages its batch because rows it rewrites vanished or changed before it
// could commit (2*maxStageRetries). partitionedBookFileWrite refuses, on its
// own, a row that lost twice once maxStageRetries rounds have passed without a
// refusal, refuses every row that is stale in a round once this budget is spent
// (the rest of the batch commits), and abandons the batch at twice this budget;
// restageBookFileBatch (moves, retire, unretire, rehome, deletes) gives up at it.
const maxBatchStageRounds = 2 * maxStageRetries

// ErrBookFileDurabilityUnknown means a book_file write was APPLIED — its rows
// are visible to every reader and were published to memdb — but the WAL fsync
// that makes it durable failed. It is not "nothing was written": a caller must
// not roll back or retry the write as if it were lost.
var ErrBookFileDurabilityUnknown = errors.New("book_file write applied, but its fsync failed: durability unknown")

// maxStageRetries bounds re-staging when rewritten rows keep vanishing or
// changing under a writer. Missing owners never trigger it: each such round
// refuses rows, so the set shrinks.
const maxStageRetries = 5

// bookFileRewrite names one committed book_file row a staged batch depends on.
//
// Expected is the exact stored value the writer read (copied: a Pebble value
// is only valid until its closer is closed), compared byte for byte against
// the stored value under the owner stripes. Never a re-marshalled struct: the
// comparison is stored bytes against stored bytes, so a row written by an
// older encoder cannot look "changed" to a writer that changed nothing.
//
// A nil Expected means the row is only REFERENCED (retire's live alternative
// owner: an index key is pointed at it, the row itself is not rewritten): the
// commit then requires only that the row still exists. Use bookFileMustExist
// for that; every row the batch rewrites or moves away uses bookFileUnchanged.
type bookFileRewrite struct {
	Key      []byte
	Expected []byte
}

// bookFileUnchanged is a rewrite guard: the row at key must still hold exactly
// the bytes raw, the value the writer read.
func bookFileUnchanged(key, raw []byte) bookFileRewrite {
	return bookFileRewrite{Key: key, Expected: raw}
}

// bookFileMustExist is a reference guard: the row at key must still be
// committed, whatever it holds.
func bookFileMustExist(key []byte) bookFileRewrite {
	return bookFileRewrite{Key: key}
}

// commitBookFileBatch applies a book_file writer's batch under the owner
// stripes of lockIDs, newOwners and the owners named by rewrites, after
// verifying — with the stripes held — that every book in newOwners has a row
// (rows are being created or moved under them) and that every rewrite still
// holds what the writer read: the row is still committed and, for
// bookFileUnchanged, its stored bytes are exactly the bytes read. Rows
// rewritten or moved away were read before the stripes were taken; if one
// vanished, a concurrent delete may have let its book go (writing it would
// recreate an orphan or put one row under two books), and if one changed, a
// writer that does not take the file stripe (a create claiming an iTunes ID,
// the bulk moves, the deletes, the fingerprint sweeps, the scan-cache stamp) got
// in between, and
// writing the staged value would silently revert its change and strand its
// index keys. On refusal the batch is closed, nothing is applied, and a
// *staleStageError says why. The fsync runs after the stripes are released
// (see WHAT RUNS UNDER THEM above).
func (p *PebbleStore) commitBookFileBatch(batch *GatedBatch, lockIDs, newOwners []string, rewrites []bookFileRewrite) error {
	return p.commitBookFileBatchVerified(batch, lockIDs, newOwners, rewrites, nil)
}

// commitBookFileBatchVerified is commitBookFileBatch plus a verify hook that
// runs with the owner stripes held, after the owner and rewrite checks and
// immediately before the batch is applied. A non-nil error from verify closes
// the batch without applying it and is returned as is (a verify that wants a
// re-stage wraps errBookFileRowVanished, which every retry loop already tests).
//
// It exists for decisions the batch made from index state it read BEFORE the
// stripes were taken (the retired-version writers' single-owner index keys). The
// owner stripes serialize this commit only against writers that share one of ITS
// owner books, so verify and the apply also run under the single-owner index
// mutex (lockSingleOwnerIndex), which every other writer of those keys takes
// around its own apply: no writer of a key can commit between verify returning
// and the batch being applied.
func (p *PebbleStore) commitBookFileBatchVerified(batch *GatedBatch, lockIDs, newOwners []string, rewrites []bookFileRewrite, verify func() error) error {
	if bookFileBeforeCommitHook != nil {
		bookFileBeforeCommitHook()
	}
	owners := slices.Compact(slices.Sorted(slices.Values(newOwners)))
	all := append(append([]string(nil), lockIDs...), owners...)
	for _, rw := range rewrites {
		if b := bookIDOfBookFileKey(rw.Key); b != "" {
			all = append(all, b)
		}
	}
	unlock := p.lockBookOwners(all...)
	stale := &staleStageError{}
	for _, id := range owners {
		if err := p.requireBookRowsExist(id); err != nil {
			if !errors.Is(err, ErrBookFileOwnerMissing) {
				unlock()
				_ = batch.Close()
				return err
			}
			stale.missingOwners = append(stale.missingOwners, id)
		}
	}
	for _, rw := range rewrites {
		cur, closer, err := p.db.Get(rw.Key)
		if errors.Is(err, pebble.ErrNotFound) {
			stale.vanishedKeys = append(stale.vanishedKeys, rw.Key)
			continue
		}
		if err != nil {
			unlock()
			_ = batch.Close()
			return fmt.Errorf("re-check %s: %w", rw.Key, err)
		}
		// The compare-and-swap. Skipped only for a bookFileMustExist guard.
		changed := rw.Expected != nil && !bytes.Equal(cur, rw.Expected)
		_ = closer.Close()
		if changed {
			stale.changedKeys = append(stale.changedKeys, rw.Key)
		}
	}
	if len(stale.missingOwners) > 0 || len(stale.vanishedKeys) > 0 || len(stale.changedKeys) > 0 {
		unlock()
		_ = batch.Close()
		return stale
	}
	// The re-check and the apply are one step with respect to every other
	// writer of a single-owner index key (lockSingleOwnerIndex).
	unlockIndex := p.lockSingleOwnerIndex()
	if verify != nil {
		if err := verify(); err != nil {
			unlockIndex()
			unlock()
			_ = batch.Close()
			return err
		}
		if singleOwnerIndexAfterVerifyHook != nil {
			singleOwnerIndexAfterVerifyHook()
		}
	}
	err := batch.Commit(pebble.NoSync)
	unlockIndex()
	unlock()
	if err != nil {
		return err
	}
	return p.syncBookFileWAL()
}

// bookFileWALSyncHook, when non-nil, runs after a book_file writer released
// its owner stripes and before its WAL fsync. Test-only.
var bookFileWALSyncHook func() error

// singleOwnerIndexAfterVerifyHook, when non-nil, runs inside
// commitBookFileBatchVerified after a verify hook succeeded and before the apply,
// with the owner stripes and the single-owner index mutex held. Test-only.
var singleOwnerIndexAfterVerifyHook func()

// bookFileBeforeCommitHook, when non-nil, runs at the start of every
// commitBookFileBatch and setBookFileRowIfUnchanged, before any owner stripe is
// taken. Test-only.
var bookFileBeforeCommitHook func()

// syncBookFileWAL makes every record applied so far durable: an fsync'd
// empty WAL record, which the sequential WAL cannot persist before the
// NoSync records ahead of it.
func (p *PebbleStore) syncBookFileWAL() error {
	var err error
	if bookFileWALSyncHook != nil {
		err = bookFileWALSyncHook()
	}
	if err == nil {
		err = p.db.SyncWAL()
	}
	if err != nil {
		// Called only after an apply: the rows are already visible.
		return fmt.Errorf("%w: %w", ErrBookFileDurabilityUnknown, err)
	}
	return nil
}

// bookFileApplied reports whether a book_file write's error still means its
// batch was applied (nil, or only its fsync failed): the caller must then do
// its post-commit work (memdb, aggregates, copy-back) and still return err.
func bookFileApplied(err error) bool {
	return err == nil || errors.Is(err, ErrBookFileDurabilityUnknown)
}

// bookIDOfBookFileKey returns <bookID> from a book_file:<bookID>:<fileID> key.
func bookIDOfBookFileKey(k []byte) string {
	rest, ok := strings.CutPrefix(string(k), "book_file:")
	if !ok {
		return ""
	}
	if i := strings.LastIndex(rest, ":"); i > 0 {
		return rest[:i]
	}
	return ""
}

// setBookFileRowIfUnchanged writes data at a book_file row key only if that key
// is still committed AND still holds exactly the bytes expected (the value the
// writer read), under the owning book's owner stripe. It is the in-place
// rewrite form of commitBookFileBatch for the paths that write a single
// primary key and no index. Nothing is written for bookFileRowGone or
// bookFileRowChanged; the caller re-reads and re-applies its change on the
// latter (rmwBookFile), because data was built from a row that no longer
// exists.
func (p *PebbleStore) setBookFileRowIfUnchanged(bookID string, key, expected, data []byte) (bookFileRowWrite, error) {
	if bookFileBeforeCommitHook != nil {
		bookFileBeforeCommitHook()
	}
	unlock := p.lockBookOwners(bookID)
	cur, closer, err := p.db.Get(key)
	if errors.Is(err, pebble.ErrNotFound) {
		unlock()
		return bookFileRowGone, nil
	}
	if err != nil {
		unlock()
		return bookFileRowGone, err
	}
	same := bytes.Equal(cur, expected)
	_ = closer.Close()
	if !same {
		unlock()
		return bookFileRowChanged, nil
	}
	err = p.db.Set(key, data, pebble.NoSync)
	unlock()
	if err != nil {
		return bookFileRowGone, err
	}
	return bookFileRowWritten, p.syncBookFileWAL()
}

// bookFileKey is the primary key of a book_file row.
func bookFileKey(bookID, fileID string) []byte {
	return []byte("book_file:" + bookID + ":" + fileID)
}

// partitionedBookFileWrite runs a batch book_file writer so that rows whose
// owner book is missing are refused INDIVIDUALLY and every other row commits.
//
// attempt stages and commits one pass over the rows it is given and returns
// the rows it staged as NEW grouped by owner book. When the commit is refused
// (*staleStageError) nothing was applied; the rows of each missing owner are
// refused and the pass is re-run over the rest. Rows are re-staged from
// copies each pass, so a pass that read something now stale (a matched row
// that vanished, a retargeted BookID) leaves no trace on the caller's structs;
// after the pass that commits, each committed row's final state (assigned ID,
// timestamps, retargeted BookID) is copied back to the caller's struct.
//
// Returns nil when every row committed, *BookFileRowsRefusedError when some
// were refused (the rest are committed), or the attempt's own error (nothing
// from that pass committed).
func (p *PebbleStore) partitionedBookFileWrite(files []*BookFile, present []bool,
	attempt func([]*BookFile, []bool) (*stagedBookFileRows, error),
) error {
	refused := map[*BookFile]bool{}
	var missing []string
	var durErr error
	reasons := map[*BookFile]string{}
	vanishes := map[*BookFile]int{}
	idleRounds := 0
	total := 0
	for _, f := range files {
		if f != nil {
			total++
		}
	}
	for {
		var clones []*BookFile
		var pres []bool
		origOf := map[*BookFile]*BookFile{}
		for i, f := range files {
			if f == nil || refused[f] {
				continue
			}
			c := cloneBookFile(f) // deep: a pass must leave no trace on the caller's struct
			clones = append(clones, &c)
			origOf[&c] = f
			if present != nil {
				pres = append(pres, present[i])
			}
		}
		if len(clones) == 0 {
			break
		}
		staged, err := attempt(clones, pres)
		if stale, ok := errors.AsType[*staleStageError](err); ok {
			newlyRefused := 0
			refuse := func(c *BookFile, reason string) {
				if o := origOf[c]; o != nil && !refused[o] {
					refused[o] = true
					if o.ID == "" {
						// Give the refused row the ID it was staged
						// under, so the refusal names it.
						o.ID = c.ID
					}
					reasons[o] = reason
					newlyRefused++
				}
			}
			for _, b := range stale.missingOwners {
				missing = append(missing, b)
				if staged != nil {
					for _, c := range staged.byOwner[b] {
						refuse(c, "owner book "+b+" no longer exists")
					}
				}
			}
			// Count vanishes per ROW (by the caller's struct), not per round. A
			// row that CHANGED since it was read (the compare-and-swap in
			// commitBookFileBatch) is counted exactly like one that vanished:
			// both mean "re-read and re-stage", and a row that keeps doing it is
			// churn.
			staleKeys := stale.staleKeys()
			if staged != nil {
				for _, k := range staleKeys {
					if c := staged.byRewriteKey[string(k)]; c != nil {
						if o := origOf[c]; o != nil {
							vanishes[o]++
						}
					}
				}
			}
			if newlyRefused == 0 {
				idleRounds++
				if idleRounds >= maxStageRetries {
					// Sustained churn. From here a row whose re-checked key
					// vanished or changed in two or more rounds is refused on its
					// own, with a reason: it keeps changing. A row that vanished
					// once is re-staged like any other; one concurrent write is
					// not churn. Threshold 2 because a single vanish is exactly
					// what one retry is for.
					//
					// A writer sweeping the library changes a DIFFERENT row of
					// the batch each round, so no row reaches 2 vanishes, and
					// waiting for one to would re-stage the whole batch (reading
					// and re-encoding every row, fingerprinted rows included) once
					// per row. So once the budget (maxBatchStageRounds) is spent,
					// every row that is stale THIS round is refused, and the rest
					// commit in a later round: each such round refuses at least one
					// row, so the set shrinks, and the caller gets a
					// *BookFileRowsRefusedError naming the rows to try again
					// instead of losing the other rows of the batch.
					spent := idleRounds >= maxBatchStageRounds
					for _, k := range staleKeys {
						if c := staged.byRewriteKey[string(k)]; c != nil {
							if o := origOf[c]; o != nil && (spent || vanishes[o] >= 2) {
								refuse(c, "the row it rewrites kept changing under the write ("+string(k)+")")
							}
						}
					}
					// Hard backstop: a writer that keeps changing rows through
					// twice the budget, even as rows are trimmed, abandons the
					// batch with ErrBookFileConflict and nothing is written; the
					// caller tries the books again later. This is what ends a
					// batch no refusal can shrink fast enough (stale keys that map
					// to no row of the batch refuse nothing).
					if idleRounds >= 2*maxBatchStageRounds {
						return fmt.Errorf("book_file batch: rows kept changing under the write: %w: %w", ErrBookFileConflict, err)
					}
				}
			}
			continue
		}
		if !bookFileApplied(err) {
			return err
		}
		// Rows the pass declined outright are refused with its reason.
		if staged != nil {
			for c, reason := range staged.refusals {
				if o := origOf[c]; o != nil && !refused[o] {
					refused[o] = true
					reasons[o] = reason
					delete(origOf, c)
				}
			}
		}
		// Applied (possibly with an fsync failure): the rows are visible, so
		// the caller's structs get their final state either way.
		for c, o := range origOf {
			*o = *c
		}
		durErr = err
		break
	}
	if len(refused) == 0 {
		return durErr
	}
	e := &BookFileRowsRefusedError{
		Committed:      total - len(refused),
		MissingBookIDs: slices.Compact(slices.Sorted(slices.Values(missing))),
		Reasons:        map[string]string{},
	}
	for _, f := range files {
		if f != nil && refused[f] {
			e.RefusedFileIDs = append(e.RefusedFileIDs, f.ID)
			e.Reasons[f.ID] = reasons[f]
		}
	}
	if durErr != nil {
		return errors.Join(e, durErr)
	}
	return e
}

// stagedBookFileRows is what one staging pass of a batch writer reports back
// to partitionedBookFileWrite: its NEW rows grouped by owner book, and the row
// behind each key it rewrites, so a refusal can be pinned on exactly the rows
// it concerns.
type stagedBookFileRows struct {
	byOwner      map[string][]*BookFile
	byRewriteKey map[string]*BookFile
	// refusals are rows the pass declined to stage at all, with the reason
	// (an upsert naming a row ID that no longer exists, say). The rest of
	// the pass committed without them.
	refusals map[*BookFile]string
}

// ErrBookFileRowDeleted is returned by an upsert naming the ID of a book_file
// row that has been deleted: an upsert never recreates a deleted row.
var ErrBookFileRowDeleted = errors.New("book_file row was deleted")

// ErrBookFilePIDConflict is returned by an upsert whose row ID is live but
// whose iTunes PID is held by a different live row.
var ErrBookFilePIDConflict = errors.New("book_file iTunes PID is held by another row")

// bookFileGoneKey tombstones a deleted book_file ID. IDs are ULIDs and never
// reused, so the tombstone answers "was this row deleted?" with one point
// read (an upsert of a stale struct must not recreate it). One small key per
// deleted row, kept forever; wipes by prefix do not write them.
func bookFileGoneKey(id string) []byte { return []byte("book_file_gone:" + id) }

func stageBookFileTombstone(batch *GatedBatch, id string) error {
	if id == "" {
		return nil
	}
	return batch.Set(bookFileGoneKey(id), []byte(time.Now().UTC().Format(time.RFC3339)), nil)
}

// bookFileIDState resolves a book_file ID with point reads only (never a
// scan): the live row, or gone=true when the ID carries a deletion tombstone,
// or neither (an ID never written — a caller's fresh ULID — or a legacy row
// written before the id index, which the caller's path/PID matching finds).
func (p *PebbleStore) bookFileIDState(id string) (live *BookFile, gone bool, err error) {
	live, _, gone, err = p.bookFileIDStateRaw(id)
	return live, gone, err
}

// bookFileIDStateRaw is bookFileIDState plus the stored bytes of the live row
// (copied), for a writer that rewrites it and must compare them at commit.
func (p *PebbleStore) bookFileIDStateRaw(id string) (live *BookFile, raw []byte, gone bool, err error) {
	live, raw, _, gone, err = p.bookFileIDStateAt(id)
	return live, raw, gone, err
}

// bookFileIDStateAt is bookFileIDStateRaw plus the primary key the live row was
// read at (at), as the id index spells it. at is what the row's own BookID and
// ID should spell too; a caller that follows the row by building its key from
// those fields checks it (bookFileKeyMismatch).
func (p *PebbleStore) bookFileIDStateAt(id string) (live *BookFile, raw, at []byte, gone bool, err error) {
	idxVal, idxCloser, err := p.bookFileIDGet([]byte("book_file_id:" + id))
	switch {
	case err == nil:
		pk := append([]byte(nil), idxVal...)
		_ = idxCloser.Close()
		val, closer, gerr := p.db.Get(pk)
		if gerr == nil {
			defer closer.Close()
			raw = append([]byte(nil), val...)
			var f BookFile
			if uerr := json.Unmarshal(raw, &f); uerr != nil {
				return nil, nil, nil, false, fmt.Errorf("decode book_file %s: %w", pk, uerr)
			}
			return &f, raw, pk, false, nil
		}
		if !errors.Is(gerr, pebble.ErrNotFound) {
			return nil, nil, nil, false, fmt.Errorf("book_file row %s: %w", pk, gerr)
		}
	case !errors.Is(err, pebble.ErrNotFound):
		return nil, nil, nil, false, fmt.Errorf("book_file_id index for %s: %w", id, err)
	}
	_, closer, terr := p.db.Get(bookFileGoneKey(id))
	if terr == nil {
		_ = closer.Close()
		return nil, nil, nil, true, nil
	}
	if !errors.Is(terr, pebble.ErrNotFound) {
		return nil, nil, nil, false, fmt.Errorf("book_file tombstone for %s: %w", id, terr)
	}
	return nil, nil, nil, false, nil
}
