// file: internal/books/store/pebble_store_author_books_index.go
// version: 1.1.1
// guid: 2f75dc58-b452-400c-9351-e2238d017bc9
// last-edited: 2026-11-09

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

var authorBooksIndexLog = logger.New("database.author-books-index")

// The author -> books index answers "which books does author A hold" with a
// prefix scan instead of a scan of every book and every credit row. It exists
// for GetBooksByAuthorIDCore's Pebble fallback, which memdb replaces ~130 s
// after every start; before this index that window paid two full-library
// unmarshal passes per request (TODO.md SQ-02).
//
// ONE key family, from ONE source: the book_authors:<bookID> credit row (any
// role or position). The credit list is the only author source the index
// holds.
//
//	author_books:<authorID>:<bookID>  credit rows. Writers: SetBookAuthors /
//	                                  ModifyBookAuthors, ModifyBookCredits,
//	                                  DeleteBook, DeleteAuthor's sweep.
//
// The key holds an empty value and is staged in the SAME batch as the credit row
// it derives from. It covers trashed and non-primary books too (the reader
// filters), so it stays a pure function of the credit rows. The author id is
// decimal, so <authorID>: terminates the id and author 1's range
// [author_books:1:, author_books:1;) cannot contain author 10's keys.
//
// RETIRED FAMILY (F5-18j, 2026-11-19). Until then a second family,
// author_books_legacy:<authorID>:<bookID>, carried the legacy Book.AuthorID,
// staged by CreateBook, UpdateBook and ModifyBook on every write that had an
// AuthorID. That made every book-row write an `authors` write, so every kind
// reaching ModifyBook had to declare `authors` (internal/maintenance/resources).
// Nothing stages or reads that family any more. The rows already stored are
// inert and maintenance.author-books-legacy-retire deletes them
// (pebble_store_author_books_legacy_retire.go).
//
// READ CONTRACT. A missing key hides a linked book; an extra key is harmless.
// The reader therefore point-verifies every candidate (row exists and decodes,
// still links the author, then the same trash and primary-version filters the
// scan applied), so only a missing key can be wrong. Writers set their keys
// unconditionally on every write (a self-heal, like book:versiongroup:).
//
// TRUST. The listing scan (getBooksByAuthorIDFull) still links a book through
// its Book.AuthorID as well as its credit row, and so do memdb and every merge
// and relink getter: retiring Book.AuthorID itself is the credit-list plan's PR
// 4 to 6 (docs/plans/2026-10-04-author-narrator-credit-lists-audit.md). The
// index cannot discover a book linked ONLY through Book.AuthorID, so it agrees
// with the scan only while every listable book's AuthorID is also in its credit
// row. The reader therefore uses the index only after THIS PROCESS's
// ReconcileAuthorBooksIndex has finished clean: every credit row decoded and
// every listable book's AuthorID is credited (authorBooksVerifiedThisBoot).
// A persistent sentinel cannot carry that: writers outside the store still set
// an AuthorID without a credit, so a check made by an earlier boot says nothing
// about the writes since. Cost: each boot scans until its reconcile finishes.
// The writes after the reconcile keep the trust in step with the rows, in memory only:
// a book-row write that gives a listable book an uncredited AuthorID (or makes
// such a book listable), and a credit write that drops an author, revoke it for
// the rest of the process, and a reconcile that a revoking write overlapped
// does not grant it (pebble_store_author_books_trust.go). A memdb warmup that
// ends unpublished also revokes it for good. See
// docs/decisions/2026-10-30-author-books-index.md and seams.d/F5-18j.md.
//
// NOT USED by booksByAuthorIDForMutation / GetBooksByAuthorIDForRelinkCore:
// merges and deletes run exactly when memdb is incomplete and must see every
// link, so they stay on the authoritative scan rather than a derived index.

const (
	authorBooksPrefix = "author_books:"
	// authorBooksLegacyPrefix is the retired Book.AuthorID family. Only the
	// retirement op names it: it counts and deletes the rows older builds left.
	authorBooksLegacyPrefix = "author_books_legacy:"

	// authorBooksIndexSentinelV1 is the sentinel builds before F5-18j set once
	// both families were complete, and trusted without a reconcile whenever it
	// carried their own build identity. Reconcile deletes it: after this build
	// has run, the legacy family is no longer maintained, and a rolled-back
	// binary that found its own sentinel would answer from a legacy family that
	// misses every book written since. Without it the old build scans until its
	// own boot reconcile has rewritten every absent key from Book.AuthorID.
	authorBooksIndexSentinelV1 = "system:backfill:author_books_index_v1_done"

	authorBooksBatchRows  = 512
	authorBooksProgressEv = 50_000
	authorBooksSampleCap  = 5
)

// authorBooksKey is the credit-row key for (authorID, bookID).
func authorBooksKey(authorID int, bookID string) []byte {
	return []byte(authorBooksPrefix + strconv.Itoa(authorID) + ":" + bookID)
}

// authorBooksBounds is the scan range holding exactly one author's keys of one
// family: [<prefix><id>:, <prefix><id>;). The trailing colon is what keeps
// author 1 out of author 10's range; prefixEnd turns it into ';'.
func authorBooksBounds(prefix string, authorID int) (lower, upper []byte) {
	lower = []byte(prefix + strconv.Itoa(authorID) + ":")
	return lower, prefixEnd(lower)
}

// decodeCreditAuthorIDs decodes the author ids of a book_authors row. ok is false
// when the row does not decode, in which case the ids are unknown (not empty).
func decodeCreditAuthorIDs(data []byte) (ids []int, ok bool) {
	var rows []struct {
		AuthorID int `json:"author_id"`
	}
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, false
	}
	ids = make([]int, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.AuthorID)
	}
	return ids, true
}

func bookAuthorIDsOf(rows []BookAuthor) []int {
	ids := make([]int, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.AuthorID)
	}
	return ids
}

// stageAuthorBooksCreditDiff stages the credit-family keys for a credit row
// moving from oldIDs to newIDs: a Set for every new id (unconditional, so a
// lost key heals) and a Delete for every old id that is gone. oldIDs == nil
// with oldKnown false means the old row was unreadable, so nothing is deleted.
func stageAuthorBooksCreditDiff(batch *GatedBatch, bookID string, oldIDs []int, oldKnown bool, newIDs []int) error {
	for _, id := range newIDs {
		if err := batch.Set(authorBooksKey(id, bookID), []byte{}, nil); err != nil {
			return err
		}
	}
	if !oldKnown {
		return nil
	}
	for _, id := range oldIDs {
		if slices.Contains(newIDs, id) {
			continue
		}
		if err := batch.Delete(authorBooksKey(id, bookID), nil); err != nil {
			return err
		}
	}
	return nil
}

// stageAuthorBooksDelete stages the removal of every key a book owns, given
// the author ids of the credit row it is leaving (as decodeCreditAuthorIDs).
func stageAuthorBooksDelete(batch *GatedBatch, bookID string, creditIDs []int) error {
	for _, id := range creditIDs {
		if err := batch.Delete(authorBooksKey(id, bookID), nil); err != nil {
			return err
		}
	}
	return nil
}

// readCreditAuthorIDs reads book bookID's stored credit row from r and decodes
// its author ids. found is false when there is no row; ok is false when the row
// exists but does not decode (the ids are then unknown).
func readCreditAuthorIDs(r gatedReader, bookID string) (ids []int, found, ok bool, err error) {
	val, closer, err := r.Get(bookAuthorsKey(bookID))
	if errors.Is(err, pebble.ErrNotFound) {
		return nil, false, true, nil
	}
	if err != nil {
		return nil, false, false, err
	}
	defer closer.Close()
	ids, ok = decodeCreditAuthorIDs(val)
	return ids, true, ok, nil
}

// AuthorBooksIndexTrusted reports whether readers may use the author -> books
// index right now: this process's ReconcileAuthorBooksIndex has finished clean
// (see TRUST above). The error is always nil; the signature is the server's
// authorBooksIndexer capability.
func (p *PebbleStore) AuthorBooksIndexTrusted() (bool, error) {
	return p.authorBooksVerifiedThisBoot.Load(), nil
}

// booksByAuthorIDFromIndex is getBooksByAuthorIDFull answered from the index.
// Same listing view as the scan: linked through a credit row or the legacy
// AuthorID, soft-deleted and non-primary versions excluded, ascending by book
// id. One snapshot serves the key scan and every point read, so the answer is
// as of a single instant. trusted is false until this boot's reconcile has
// verified the index (the caller then scans).
//
// Candidates come from the credit-row keys only. The AuthorID check in the
// point-verify is the scan's own rule and costs nothing; it never discovers a
// book, it only keeps one a stale credit key named that the scan would list too.
//
// An undecodable book row is skipped, as the scan skips it. An undecodable
// credit row of a candidate that is not linked through AuthorID is an error
// naming the row: the candidate claims the link, and the fail-closed rule of
// bookIDsInAuthorJunction applies unchanged.
func (p *PebbleStore) booksByAuthorIDFromIndex(authorID int) (books []Book, trusted bool, err error) {
	// The flag before the snapshot: it is set only after every key the
	// reconcile wrote was committed, so a snapshot taken after a true load sees
	// them. And again after it: a write that uncovers a book revokes before it
	// commits (pebble_store_author_books_trust.go, ORDERING), so a snapshot
	// holding that commit is followed by a false load and the caller scans.
	// The answer is the index's as of the snapshot, and the snapshot is one in
	// which the index was trusted.
	if !p.authorBooksVerifiedThisBoot.Load() {
		return nil, false, nil
	}
	snap := p.db.NewSnapshot()
	defer func() { _ = snap.Close() }()
	if !p.authorBooksVerifiedThisBoot.Load() {
		return nil, false, nil
	}

	var ids []string
	lower, upper := authorBooksBounds(authorBooksPrefix, authorID)
	iter, err := snap.NewIter(&pebble.IterOptions{LowerBound: lower, UpperBound: upper})
	if err != nil {
		return nil, true, err
	}
	for ok := iter.First(); ok; ok = iter.Next() {
		// The book id is everything after the author's prefix; it may itself
		// contain ':', so it is sliced off, never split out.
		if id := iter.Key()[len(lower):]; len(id) > 0 {
			ids = append(ids, string(id))
		}
	}
	iterErr := iter.Error()
	_ = iter.Close()
	if iterErr != nil {
		return nil, true, fmt.Errorf("books by author index scan truncated, refusing to answer from a partial list: %w", iterErr)
	}

	for _, id := range ids {
		val, closer, err := snap.Get([]byte(bookRowPrefix + id))
		if errors.Is(err, pebble.ErrNotFound) {
			continue // key outlived its book
		}
		if err != nil {
			return nil, true, err
		}
		var book Book
		uerr := json.Unmarshal(val, &book)
		closer.Close()
		if uerr != nil {
			continue
		}
		if bookIsSoftDeleted(&book) {
			continue
		}
		if book.IsPrimaryVersion != nil && !*book.IsPrimaryVersion {
			continue
		}
		linked := book.AuthorID != nil && *book.AuthorID == authorID
		if !linked {
			creditIDs, found, ok, err := readCreditAuthorIDs(snap, id)
			if err != nil {
				return nil, true, err
			}
			if found && !ok {
				return nil, true, fmt.Errorf("books by author index: undecodable book_authors row %q", "book_authors:"+id)
			}
			linked = slices.Contains(creditIDs, authorID)
		}
		if linked {
			books = append(books, book)
		}
	}
	return books, true, nil
}

// authorIDCoverage is how one book row's Book.AuthorID stands against its credit
// row: the question both the index trust check and the legacy-row retirement
// ask.
type authorIDCoverage int

const (
	// coverageNoAuthorID: the row has no AuthorID, so there is nothing to cover.
	coverageNoAuthorID authorIDCoverage = iota
	// coverageCredited: the AuthorID is one of the credit row's authors.
	coverageCredited
	// coverageNoCreditRow: the AuthorID is set and the book has no credit row.
	coverageNoCreditRow
	// coverageNotInCredits: the credit row exists and does not name the AuthorID.
	coverageNotInCredits
	// coverageCreditUndecodable: the credit row exists and does not decode, so
	// whether it names the AuthorID is unknown.
	coverageCreditUndecodable
)

// authorIDCoverageRow is the part of a book row the coverage check reads.
type authorIDCoverageRow struct {
	AuthorID          *int  `json:"author_id"`
	IsPrimaryVersion  *bool `json:"is_primary_version"`
	MarkedForDeletion *bool `json:"marked_for_deletion"`
}

// listable reports whether the listing getters (the index reader, the scan and
// memdb's listing view) can return the book: not soft-deleted, and primary or
// unversioned.
func (r authorIDCoverageRow) listable() bool {
	if markedForDeletionFlag(r.MarkedForDeletion) {
		return false
	}
	return r.IsPrimaryVersion == nil || *r.IsPrimaryVersion
}

// classifyAuthorIDCoverage reads book id's credit row from r and classifies
// the row's AuthorID against it. ok is false when value does not decode.
func classifyAuthorIDCoverage(r gatedReader, id string, value []byte) (row authorIDCoverageRow, class authorIDCoverage, ok bool, err error) {
	if uerr := json.Unmarshal(value, &row); uerr != nil {
		return row, coverageNoAuthorID, false, nil
	}
	if row.AuthorID == nil {
		return row, coverageNoAuthorID, true, nil
	}
	creditIDs, found, decoded, err := readCreditAuthorIDs(r, id)
	switch {
	case err != nil:
		return row, coverageNoAuthorID, true, err
	case !found:
		return row, coverageNoCreditRow, true, nil
	case !decoded:
		return row, coverageCreditUndecodable, true, nil
	case slices.Contains(creditIDs, *row.AuthorID):
		return row, coverageCredited, true, nil
	default:
		return row, coverageNotInCredits, true, nil
	}
}

// authorBooksRow is one scanned row handed to a worker (key and value copied).
type authorBooksRow struct {
	id    string
	value []byte
}

// runAuthorBooksPass drives one row scan through a bounded worker pool
// (runtime.NumCPU() workers): scan feeds rows in chunks of authorBooksBatchRows,
// each worker applies handle to every row of a chunk on a fresh batch and
// commits the batch (Sync) when handle staged anything. The scan is a single
// sequential iterator; the JSON decode and point reads are what the workers
// spread over the cores. ctx cancels the pass between rows.
func (p *PebbleStore) runAuthorBooksPass(ctx context.Context, scan func(emit func(authorBooksRow) error) error, handle func(batch *GatedBatch, r authorBooksRow) error) error {
	workers := runtime.NumCPU()
	g, gctx := errgroup.WithContext(ctx)
	rows := make(chan []authorBooksRow, workers)
	for range workers {
		g.Go(func() error {
			for chunk := range rows {
				batch := p.db.NewBatch()
				for _, r := range chunk {
					if err := gctx.Err(); err != nil {
						batch.Close()
						return err
					}
					if err := handle(batch, r); err != nil {
						batch.Close()
						return err
					}
				}
				var err error
				if !batch.Empty() {
					err = batch.Commit(pebble.Sync)
				}
				batch.Close()
				if err != nil {
					return fmt.Errorf("commit author-books batch: %w", err)
				}
			}
			return nil
		})
	}
	g.Go(func() error {
		defer close(rows)
		var chunk []authorBooksRow
		send := func() error {
			if len(chunk) == 0 {
				return nil
			}
			select {
			case rows <- chunk:
				chunk = nil
				return nil
			case <-gctx.Done():
				return gctx.Err()
			}
		}
		if err := scan(func(r authorBooksRow) error {
			chunk = append(chunk, r)
			if len(chunk) >= authorBooksBatchRows {
				return send()
			}
			return nil
		}); err != nil {
			return err
		}
		return send()
	})
	return g.Wait()
}

// AuthorBooksIndexReport is the outcome of one ReconcileAuthorBooksIndex run.
type AuthorBooksIndexReport struct {
	// CreditRows and BookRows are the rows read in each pass.
	CreditRows int `json:"credit_rows"`
	BookRows   int `json:"book_rows"`
	// Checked is the (author, book) credit pairs found; Written the keys that
	// were absent and are now written.
	Checked int `json:"checked"`
	Written int `json:"written"`
	// UnreadableCredits is how many book_authors rows did not decode; while any
	// exist the index is not trusted. UnreadableBooks is the same for book rows;
	// they cannot be listed by either path, so they do not hold trust.
	UnreadableCredits int      `json:"unreadable_credits"`
	UnreadableBooks   int      `json:"unreadable_books"`
	SampleUnreadable  []string `json:"sample_unreadable,omitempty"`
	// UncoveredBooks is how many listable books (live, primary or unversioned)
	// carry a Book.AuthorID their credit row does not name. The index cannot
	// find them under that author while the scan and memdb can, so while any
	// exist the index is not trusted. maintenance.credits-backfill writes the
	// missing credits; maintenance.credit-census lists them by class.
	UncoveredBooks  int      `json:"uncovered_books"`
	SampleUncovered []string `json:"sample_uncovered,omitempty"`
	// WasTrusted is whether this process trusted the index when the run began;
	// Trusted whether it does when the run ends.
	WasTrusted bool `json:"was_trusted"`
	Trusted    bool `json:"trusted"`
	// RevokedDuringRun is set when the scans found nothing wrong but trust was
	// still withheld: a write revoked it while they ran, or the memdb warmup
	// has ended unpublished (pebble_store_author_books_trust.go).
	RevokedDuringRun bool `json:"revoked_during_run,omitempty"`
}

// authorBooksBeforeTrustHook, when non-nil, runs in ReconcileAuthorBooksIndex
// after both scans and before the trust decision. Tests use it to land a write
// inside the run. Production never sets it.
var authorBooksBeforeTrustHook func()

// ReconcileAuthorBooksIndex makes sure every link the credit rows hold has its
// key, checks that every listable book's Book.AuthorID is credited, and opens
// the reader's gate for this process only when both hold (see TRUST). It only
// ever WRITES MISSING KEYS, never a book or credit row and never an index
// delete, so it is safe to run at any time, concurrently with live writers, and
// repeatedly: a key it writes from a row that changed under it is an extra key,
// which the reader's point-verify ignores. It is the first-build backfill and
// the per-boot verification at once.
//
// It also deletes the pre-F5-18j sentinel (authorBooksIndexSentinelV1), so a
// rolled-back binary scans until its own reconcile has rebuilt the legacy family.
//
// Both row scans are sequential iterators feeding bounded worker pools
// (runtime.NumCPU()), so the JSON decode and the point reads are spread over
// the cores. A key is a pure function of (author, book), so workers can never
// write conflicting values. Every exit path logs.
func (p *PebbleStore) ReconcileAuthorBooksIndex(ctx context.Context) (AuthorBooksIndexReport, error) {
	start := time.Now()
	var rep AuthorBooksIndexReport
	// Sampled before either scan opens its iterator: a revoking write whose
	// commit the scans miss bumps the generation after this read
	// (pebble_store_author_books_trust.go, ORDERING).
	gen := p.authorBooksTrustGen.Load()
	rep.WasTrusted = p.authorBooksVerifiedThisBoot.Load()
	authorBooksIndexLog.Info("author-books-index: starting: was_trusted=%t", rep.WasTrusted)

	if err := p.dropAuthorBooksSentinelV1(); err != nil {
		authorBooksIndexLog.Error("author-books-index: cannot remove the v1 sentinel, aborting: err=%s", logger.SanitizeLogValue(err.Error()))
		p.authorBooksVerifiedThisBoot.Store(false)
		return rep, err
	}

	var (
		checked, written, creditRows, bookRows atomic.Int64
		unreadableCredits, unreadableBooks     atomic.Int64
		uncovered                              atomic.Int64
		mu                                     sync.Mutex
		samples, uncoveredSamples              []string
	)
	note := func(dst *[]string, s string) {
		mu.Lock()
		if len(*dst) < authorBooksSampleCap {
			*dst = append(*dst, s)
		}
		mu.Unlock()
	}
	fail := func(pass string, err error) (AuthorBooksIndexReport, error) {
		p.authorBooksVerifiedThisBoot.Store(false)
		authorBooksIndexLog.Error("author-books-index: pass failed: pass=%q credit_rows=%d book_rows=%d err=%s",
			pass, creditRows.Load(), bookRows.Load(), logger.SanitizeLogValue(err.Error()))
		return rep, err
	}

	// ensure writes key unless it exists. The Get avoids rewriting (and
	// re-journaling) the whole index on every boot.
	ensure := func(batch *GatedBatch, key []byte) error {
		checked.Add(1)
		_, closer, err := p.db.Get(key)
		if err == nil {
			closer.Close()
			return nil
		}
		if !errors.Is(err, pebble.ErrNotFound) {
			return err
		}
		written.Add(1)
		return batch.Set(key, []byte{}, nil)
	}

	creditPrefix := []byte("book_authors:")
	if err := p.runAuthorBooksPass(ctx, func(emit func(authorBooksRow) error) error {
		return forEachKeyInRange(p.db, creditPrefix, prefixEnd(creditPrefix), func(key, value []byte) error {
			if n := creditRows.Add(1); n%authorBooksProgressEv == 0 {
				authorBooksIndexLog.Info("author-books-index: progress: pass=credit-rows scanned=%d elapsed=%s",
					n, time.Since(start).Round(time.Second))
			}
			return emit(authorBooksRow{id: string(key[len(creditPrefix):]), value: append([]byte(nil), value...)})
		})
	}, func(batch *GatedBatch, r authorBooksRow) error {
		ids, ok := decodeCreditAuthorIDs(r.value)
		if !ok {
			unreadableCredits.Add(1)
			note(&samples, "book_authors:"+r.id)
			return nil
		}
		for _, id := range ids {
			if err := ensure(batch, authorBooksKey(id, r.id)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return fail("credit rows", err)
	}

	// The coverage pass: a listable book whose AuthorID its credit row does not
	// name is one the index cannot find under that author. An undecodable
	// credit row is already counted by the credit pass.
	if err := p.runAuthorBooksPass(ctx, func(emit func(authorBooksRow) error) error {
		return forEachBookRow(p.db, func(id string, value []byte) error {
			if n := bookRows.Add(1); n%authorBooksProgressEv == 0 {
				authorBooksIndexLog.Info("author-books-index: progress: pass=book-rows scanned=%d elapsed=%s",
					n, time.Since(start).Round(time.Second))
			}
			return emit(authorBooksRow{id: id, value: append([]byte(nil), value...)})
		})
	}, func(_ *GatedBatch, r authorBooksRow) error {
		row, class, ok, err := classifyAuthorIDCoverage(p.db, r.id, r.value)
		if err != nil {
			return err
		}
		if !ok {
			unreadableBooks.Add(1)
			return nil
		}
		if (class == coverageNoCreditRow || class == coverageNotInCredits) && row.listable() {
			uncovered.Add(1)
			note(&uncoveredSamples, "book:"+r.id)
		}
		return nil
	}); err != nil {
		return fail("book rows", err)
	}

	rep.CreditRows, rep.BookRows = int(creditRows.Load()), int(bookRows.Load())
	rep.Checked, rep.Written = int(checked.Load()), int(written.Load())
	rep.UnreadableCredits, rep.UnreadableBooks = int(unreadableCredits.Load()), int(unreadableBooks.Load())
	rep.SampleUnreadable = samples
	rep.UncoveredBooks, rep.SampleUncovered = int(uncovered.Load()), uncoveredSamples

	// Every key is durable (each chunk committed with Sync), so the gate may
	// open now if nothing disproved the index: no unreadable credit, no
	// uncovered book, no revoking write while the scans ran, and a memdb warmup
	// that has not ended unpublished. The flag is stored first and the
	// generation and warmup state read after it, so a revocation racing the
	// Store either is seen here or clears the flag after it (ORDERING).
	if authorBooksBeforeTrustHook != nil {
		authorBooksBeforeTrustHook()
	}
	rep.Trusted = rep.UnreadableCredits == 0 && rep.UncoveredBooks == 0
	p.authorBooksVerifiedThisBoot.Store(rep.Trusted)
	if rep.Trusted && (p.authorBooksTrustGen.Load() != gen || p.memWarmupUnpublished.Load()) {
		p.authorBooksVerifiedThisBoot.Store(false)
		rep.Trusted = false
		rep.RevokedDuringRun = true
	}

	summary := fmt.Sprintf("author-books-index: complete: credit_rows=%d book_rows=%d checked=%d written=%d unreadable_credits=%d unreadable_books=%d sample_unreadable=%q uncovered_books=%d sample_uncovered=%q was_trusted=%t trusted=%t revoked_during_run=%t duration=%s",
		rep.CreditRows, rep.BookRows, rep.Checked, rep.Written, rep.UnreadableCredits, rep.UnreadableBooks,
		logger.SanitizeLogValue(strings.Join(rep.SampleUnreadable, ",")), rep.UncoveredBooks,
		logger.SanitizeLogValue(strings.Join(rep.SampleUncovered, ",")), rep.WasTrusted, rep.Trusted, rep.RevokedDuringRun,
		time.Since(start).Round(time.Millisecond))
	switch {
	case rep.UnreadableCredits > 0:
		authorBooksIndexLog.Error("%s", summary)
		return rep, fmt.Errorf("author-books index not trusted: %d book_authors row(s) do not decode (sample %v)", rep.UnreadableCredits, rep.SampleUnreadable)
	case rep.UncoveredBooks > 0:
		authorBooksIndexLog.Warn("%s", summary)
		return rep, fmt.Errorf("author-books index not trusted: %d listable book(s) carry an AuthorID their credit row does not name (sample %v); maintenance.credits-backfill writes the missing credits", rep.UncoveredBooks, rep.SampleUncovered)
	case rep.RevokedDuringRun:
		// Every key is written; only the trust is withheld. Not a failure of
		// the run, so no error: the listings scan, as they would after any
		// later revocation.
		authorBooksIndexLog.Warn("%s", summary)
		return rep, nil
	default:
		authorBooksIndexLog.Info("%s", summary)
		return rep, nil
	}
}

// dropAuthorBooksSentinelV1 deletes the pre-F5-18j sentinel if it exists (see
// authorBooksIndexSentinelV1). A read error other than not-found is returned.
func (p *PebbleStore) dropAuthorBooksSentinelV1() error {
	key := []byte(authorBooksIndexSentinelV1)
	_, closer, err := p.db.Get(key)
	switch {
	case errors.Is(err, pebble.ErrNotFound):
		return nil
	case err != nil:
		return fmt.Errorf("read %s: %w", authorBooksIndexSentinelV1, err)
	}
	closer.Close()
	if err := p.db.Delete(key, pebble.Sync); err != nil {
		return fmt.Errorf("delete %s: %w", authorBooksIndexSentinelV1, err)
	}
	authorBooksIndexLog.Info("author-books-index: removed the v1 sentinel %s (the legacy family is retired)", authorBooksIndexSentinelV1)
	return nil
}
