// file: internal/database/book_delete_owns_files.go
// version: 1.3.0
// guid: 8ffda8a0-e303-4a65-9f2f-71ab98e1b796
// last-edited: 2026-09-19

package database

import (
	"errors"
	"fmt"
	"slices"
	"strings"

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
func countBookFileRows(db *pebble.DB, bookID string) (int, error) {
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
//     were taken, so its key is re-checked under them (rewriteKeys): the PID
//     transfer's prior owner, batch-upsert matches, move sources, and the
//     single-row rewriters (UpdateBookFile and friends, PatchBookFileFields,
//     the scan-cache stamp). A key that vanished means "re-read and re-stage"
//     (errBookFileRowVanished), never "write it back".
//   - DeleteBookFile commits under the row's stripe after re-checking its key
//     too, and chases a row a concurrent move took elsewhere.
//
// Whichever commits first wins, and the other sees it: a delete that commits
// first makes the writer's existence check fail; a writer that commits first
// makes the delete's count non-zero.
//
// LOCK ORDER: owner stripes are always the INNERMOST lock. They may be taken
// while a book stripe (DeleteBook) or a book_file stripe (UpdateBookFile) is
// held, but nothing takes a book or book_file stripe while holding one. A
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
		for i := len(stripes) - 1; i >= 0; i-- {
			p.bookOwnerLocks[stripes[i]].Unlock()
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
// owner books that are gone (rows for them must be refused) and the rewritten
// keys that vanished (the writer should re-read and re-stage).
type staleStageError struct {
	missingOwners []string
	vanishedKeys  [][]byte
}

func (e *staleStageError) Error() string {
	return fmt.Sprintf("book_file batch refused: %d owner book(s) missing %v, %d rewritten row(s) vanished",
		len(e.missingOwners), e.missingOwners, len(e.vanishedKeys))
}

// Unwrap lets a single-owner caller test errors.Is(err, ErrBookFileOwnerMissing)
// and a retry loop test errors.Is(err, errBookFileRowVanished).
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

// ErrBookFileDurabilityUnknown means a book_file write was APPLIED — its rows
// are visible to every reader and were published to memdb — but the WAL fsync
// that makes it durable failed. It is not "nothing was written": a caller must
// not roll back or retry the write as if it were lost.
var ErrBookFileDurabilityUnknown = errors.New("book_file write applied, but its fsync failed: durability unknown")

// maxStageRetries bounds re-staging when rewritten rows keep vanishing under
// a writer. Missing owners never trigger it: each such round refuses rows, so
// the set shrinks.
const maxStageRetries = 5

// commitBookFileBatch applies a book_file writer's batch under the owner
// stripes of lockIDs, newOwners and the owners named by rewriteKeys, after
// verifying — with the stripes held — that every book in newOwners has a row
// (rows are being created or moved under them) and that every key in
// rewriteKeys is still committed (rows rewritten or moved away were read
// before the stripes were taken; if one vanished, a concurrent delete may
// have let its book go, and writing it would recreate an orphan or put one
// row under two books). On refusal the batch is closed, nothing is applied,
// and a *staleStageError says why. The fsync runs after the stripes are
// released (see WHAT RUNS UNDER THEM above).
func (p *PebbleStore) commitBookFileBatch(batch *pebble.Batch, lockIDs, newOwners []string, rewriteKeys [][]byte) error {
	if bookFileBeforeCommitHook != nil {
		bookFileBeforeCommitHook()
	}
	owners := slices.Compact(slices.Sorted(slices.Values(newOwners)))
	all := append(append([]string(nil), lockIDs...), owners...)
	for _, k := range rewriteKeys {
		if b := bookIDOfBookFileKey(k); b != "" {
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
	for _, k := range rewriteKeys {
		_, closer, err := p.db.Get(k)
		if errors.Is(err, pebble.ErrNotFound) {
			stale.vanishedKeys = append(stale.vanishedKeys, k)
			continue
		}
		if err != nil {
			unlock()
			_ = batch.Close()
			return fmt.Errorf("re-check %s: %w", k, err)
		}
		_ = closer.Close()
	}
	if len(stale.missingOwners) > 0 || len(stale.vanishedKeys) > 0 {
		unlock()
		_ = batch.Close()
		return stale
	}
	err := batch.Commit(pebble.NoSync)
	unlock()
	if err != nil {
		return err
	}
	return p.syncBookFileWAL()
}

// bookFileWALSyncHook, when non-nil, runs after a book_file writer released
// its owner stripes and before its WAL fsync. Test-only.
var bookFileWALSyncHook func() error

// bookFileBeforeCommitHook, when non-nil, runs at the start of every
// commitBookFileBatch, before any owner stripe is taken. Test-only.
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
		err = p.db.LogData(nil, pebble.Sync)
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

// setBookFileRowIfPresent writes data at a book_file row key only if that key
// is still committed, under the owning book's owner stripe. It is the in-place
// rewrite form of commitBookFileBatch for the paths that write a single
// primary key. Returns false (and writes nothing) when the row is gone.
func (p *PebbleStore) setBookFileRowIfPresent(bookID string, key, data []byte) (bool, error) {
	unlock := p.lockBookOwners(bookID)
	_, closer, err := p.db.Get(key)
	if errors.Is(err, pebble.ErrNotFound) {
		unlock()
		return false, nil
	}
	if err != nil {
		unlock()
		return false, err
	}
	_ = closer.Close()
	err = p.db.Set(key, data, pebble.NoSync)
	unlock()
	if err != nil {
		return false, err
	}
	return true, p.syncBookFileWAL()
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
			c := *f
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
		var stale *staleStageError
		if errors.As(err, &stale) {
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
			if newlyRefused == 0 {
				idleRounds++
				if idleRounds >= maxStageRetries {
					// Sustained churn: the rows whose re-checked keys keep
					// vanishing are refused on their own, with a reason, and
					// the rest of the batch goes ahead on the next pass.
					for _, k := range stale.vanishedKeys {
						if staged != nil && staged.byRewriteKey[string(k)] != nil {
							refuse(staged.byRewriteKey[string(k)], "the row it rewrites kept changing under the write ("+string(k)+")")
						}
					}
					if newlyRefused == 0 {
						return fmt.Errorf("book_file batch: rows kept changing under the write: %w", err)
					}
					idleRounds = 0
				}
			}
			continue
		}
		if !bookFileApplied(err) {
			return err
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
}
