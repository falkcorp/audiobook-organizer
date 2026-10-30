// file: internal/database/pebble_store_author_books_index.go
// version: 1.1.0
// guid: 2f75dc58-b452-400c-9351-e2238d017bc9
// last-edited: 2026-10-30

package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"runtime/debug"
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
// Two key families, one per SOURCE of the link. A book is linked to an author
// when the author is in its book_authors:<bookID> credit row (any role or
// position) OR is its legacy Book.AuthorID. Those two are written under
// different locks: the credit writers hold the book_authors stripe and cannot
// take the book stripe (order book -> owner -> book_authors), while UpdateBook
// holds the book stripe and not the credit stripe. With ONE shared key a credit
// writer that read the old AuthorID could delete a key an UpdateBook had just
// set for the same pair, leaving a linked book with no key. Splitting by source
// gives each key exactly one writer family, so neither ever deletes the other's:
//
//	author_books:<authorID>:<bookID>         credit rows. Writers: SetBookAuthors /
//	                                         ModifyBookAuthors, ModifyBookCredits,
//	                                         DeleteBook, DeleteAuthor's sweep.
//	author_books_legacy:<authorID>:<bookID>  Book.AuthorID. Writers: CreateBook,
//	                                         UpdateBook (and its variants), DeleteBook.
//
// Both hold an empty value and are staged in the SAME batch as the row they
// derive from. Both cover trashed and non-primary books too (the reader
// filters), so they stay a pure function of the two sources. The author id is
// decimal, so <authorID>: terminates the id and author 1's range
// [author_books:1:, author_books:1;) cannot contain author 10's keys.
//
// READ CONTRACT. A missing key hides a linked book; an extra key is harmless.
// The reader therefore point-verifies every candidate (row exists and decodes,
// still links the author, then the same trash and primary-version filters the
// scan applied), so only a missing key can be wrong. Writers set their keys
// unconditionally on every write (a self-heal, like book:versiongroup:).
//
// TRUST. Until authorBooksIndexSentinel exists the reader keeps the two-scan
// path unchanged. ReconcileAuthorBooksIndex builds the index, then sets the
// sentinel only when every credit row decoded; it also runs on EVERY later boot
// (writing only keys that are absent) because a binary that predates this index
// writes none, so a rollback followed by a roll-forward would otherwise leave
// the sentinel set over a short index. See docs/decisions/2026-10-30-author-books-index.md.
//
// NOT USED by booksByAuthorIDForMutation / GetBooksByAuthorIDForRelinkCore:
// merges and deletes run exactly when memdb is incomplete and must see every
// link, so they stay on the authoritative scan rather than a derived index.

const (
	authorBooksPrefix       = "author_books:"
	authorBooksLegacyPrefix = "author_books_legacy:"

	// authorBooksIndexSentinel is set once a full build has written every key.
	// Bump the version to force readers back to the scan and a rebuild.
	authorBooksIndexSentinel = "system:backfill:author_books_index_v1_done"

	authorBooksBatchRows  = 512
	authorBooksProgressEv = 50_000
	authorBooksSampleCap  = 5
)

// authorBooksKey is the credit-row key for (authorID, bookID).
func authorBooksKey(authorID int, bookID string) []byte {
	return []byte(authorBooksPrefix + strconv.Itoa(authorID) + ":" + bookID)
}

// authorBooksLegacyKey is the Book.AuthorID key for (authorID, bookID).
func authorBooksLegacyKey(authorID int, bookID string) []byte {
	return []byte(authorBooksLegacyPrefix + strconv.Itoa(authorID) + ":" + bookID)
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

// stageAuthorBooksLegacy stages the Book.AuthorID key moving from old to new.
// The Set is unconditional (self-heal); the Delete only when the id changed.
func stageAuthorBooksLegacy(batch *GatedBatch, bookID string, old, cur *int) error {
	if old != nil && (cur == nil || *old != *cur) {
		if err := batch.Delete(authorBooksLegacyKey(*old, bookID), nil); err != nil {
			return err
		}
	}
	if cur != nil {
		return batch.Set(authorBooksLegacyKey(*cur, bookID), []byte{}, nil)
	}
	return nil
}

// stageAuthorBooksDelete stages the removal of every key a book owns, given
// the credit row it is leaving (ids, ok as decodeCreditAuthorIDs) and its AuthorID.
func stageAuthorBooksDelete(batch *GatedBatch, bookID string, creditIDs []int, authorID *int) error {
	for _, id := range creditIDs {
		if err := batch.Delete(authorBooksKey(id, bookID), nil); err != nil {
			return err
		}
	}
	if authorID != nil {
		return batch.Delete(authorBooksLegacyKey(*authorID, bookID), nil)
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

// authorBooksBuildIdentity names the running build. It is a variable so tests
// can stand in for a different build. The VCS revision (with a "+dirty" mark)
// when the binary carries one; empty under `go test` and for any build without
// VCS stamping, which counts as "equal to any recorded identity".
var authorBooksBuildIdentity = func() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	rev, dirty := "", ""
	for _, kv := range info.Settings {
		switch kv.Key {
		case "vcs.revision":
			rev = kv.Value
		case "vcs.modified":
			if kv.Value == "true" {
				dirty = "+dirty"
			}
		}
	}
	if rev == "" {
		return ""
	}
	return rev + dirty
}

// readAuthorBooksSentinel reads the sentinel from r: whether it exists and the
// build identity recorded in its value. A read error other than not-found is
// returned, never read as "absent".
func readAuthorBooksSentinel(r gatedReader) (present bool, recorded string, err error) {
	val, closer, err := r.Get([]byte(authorBooksIndexSentinel))
	switch {
	case err == nil:
		recorded = string(val)
		closer.Close()
		return true, recorded, nil
	case errors.Is(err, pebble.ErrNotFound):
		return false, "", nil
	default:
		return false, "", fmt.Errorf("read %s: %w", authorBooksIndexSentinel, err)
	}
}

// authorBooksTrustedAt reports whether readers may use the index as seen by r.
// Two conditions, both required:
//
//  1. the sentinel exists (a clean build or reconcile completed at some point);
//  2. the build that recorded it is THIS build, or this boot's reconcile has
//     finished (verifiedThisBoot). A sentinel written by another build says
//     nothing about writes made since: an intermediate binary may have run
//     without maintaining the index (a rollback), so until this boot has
//     written every absent key the reader scans.
//
// An empty running identity (tests, unstamped builds) counts as equal. A
// recorded value that is not an identity ("1", written when there was none) is
// compared like any other, so it differs from a non-empty running identity.
//
// verified is loaded BEFORE r is read: a true flag means the keys were durable
// before it was set, and the sentinel read is at least as new.
func (p *PebbleStore) authorBooksTrustedAt(r gatedReader) (bool, error) {
	verified := p.authorBooksVerifiedThisBoot.Load()
	present, recorded, err := readAuthorBooksSentinel(r)
	if err != nil || !present {
		return false, err
	}
	if verified {
		return true, nil
	}
	running := authorBooksBuildIdentity()
	return running == "" || recorded == running, nil
}

// AuthorBooksIndexTrusted reports whether readers may use the author -> books
// index right now (see authorBooksTrustedAt).
func (p *PebbleStore) AuthorBooksIndexTrusted() (bool, error) {
	return p.authorBooksTrustedAt(p.db)
}

// booksByAuthorIDFromIndex is getBooksByAuthorIDFull answered from the index.
// Same listing view as the scan: linked through a credit row or the legacy
// AuthorID, soft-deleted and non-primary versions excluded, ascending by book
// id. One snapshot serves the sentinel, both key scans and every point read, so
// the answer is as of a single instant. trusted is false when the sentinel is
// absent (the caller then scans).
//
// An undecodable book row is skipped, as the scan skips it. An undecodable
// credit row of a candidate that is not linked through AuthorID is an error
// naming the row: the candidate claims the link, and the fail-closed rule of
// bookIDsInAuthorJunction applies unchanged.
func (p *PebbleStore) booksByAuthorIDFromIndex(authorID int) (books []Book, trusted bool, err error) {
	snap := p.db.NewSnapshot()
	defer func() { _ = snap.Close() }()

	// Sentinel first: keys committed before it are visible to the scans below.
	if trusted, err = p.authorBooksTrustedAt(snap); err != nil || !trusted {
		return nil, false, err
	}

	var ids []string
	for _, prefix := range []string{authorBooksPrefix, authorBooksLegacyPrefix} {
		lower, upper := authorBooksBounds(prefix, authorID)
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
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)

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

// AuthorBooksIndexReport is the outcome of one ReconcileAuthorBooksIndex run.
type AuthorBooksIndexReport struct {
	// CreditRows and BookRows are the rows read in each pass.
	CreditRows int `json:"credit_rows"`
	BookRows   int `json:"book_rows"`
	// Checked is the (author, book) pairs found; Written the keys that were
	// absent and are now written.
	Checked int `json:"checked"`
	Written int `json:"written"`
	// UnreadableCredits is how many book_authors rows did not decode; while any
	// exist the sentinel is not set. UnreadableBooks is the same for book rows;
	// they cannot be listed by either path, so they do not hold the sentinel.
	UnreadableCredits int      `json:"unreadable_credits"`
	UnreadableBooks   int      `json:"unreadable_books"`
	SampleUnreadable  []string `json:"sample_unreadable,omitempty"`
	// WasTrusted is whether the sentinel existed when the run began; Trusted
	// whether it exists when the run ends.
	WasTrusted bool `json:"was_trusted"`
	Trusted    bool `json:"trusted"`
}

// authorBooksRow is one scanned row handed to a worker (key and value copied).
type authorBooksRow struct {
	id    string
	value []byte
}

// ReconcileAuthorBooksIndex makes sure every link the credit rows and book rows
// hold has its key, then sets the sentinel if every credit row decoded. It only
// ever WRITES MISSING KEYS, never a book or credit row and never a delete, so it
// is safe to run at any time, concurrently with live writers, and repeatedly:
// a key it writes from a row that changed under it is an extra key, which the
// reader's point-verify ignores. It is the first-build backfill when the
// sentinel is absent and the rollback self-heal on every other boot.
//
// The two row scans are sequential iterators feeding bounded worker pools
// (runtime.NumCPU()), so the JSON decode and the point reads are spread over
// the cores. A key is a pure function of (author, book), so workers can never
// write conflicting values. Every exit path logs.
func (p *PebbleStore) ReconcileAuthorBooksIndex(ctx context.Context) (AuthorBooksIndexReport, error) {
	start := time.Now()
	var rep AuthorBooksIndexReport
	present, recorded, err := readAuthorBooksSentinel(p.db)
	if err != nil {
		authorBooksIndexLog.Error("author-books-index: cannot read sentinel, aborting: err=%s", logger.SanitizeLogValue(err.Error()))
		return rep, err
	}
	trusted, err := p.AuthorBooksIndexTrusted()
	if err != nil {
		return rep, err
	}
	rep.WasTrusted = trusted
	authorBooksIndexLog.Info("author-books-index: starting: sentinel=%s present=%t was_trusted=%t",
		authorBooksIndexSentinel, present, trusted)

	var (
		checked, written, creditRows, bookRows atomic.Int64
		unreadableCredits, unreadableBooks     atomic.Int64
		mu                                     sync.Mutex
		samples                                []string
	)
	note := func(s string) {
		mu.Lock()
		if len(samples) < authorBooksSampleCap {
			samples = append(samples, s)
		}
		mu.Unlock()
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

	// runPass drives one scan: scan feeds rows into the channel, workers apply
	// handle to each and commit their batch every authorBooksBatchRows rows.
	runPass := func(name string, scan func(emit func(authorBooksRow) error) error, handle func(batch *GatedBatch, r authorBooksRow) error) error {
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
					err := batch.Commit(pebble.Sync)
					batch.Close()
					if err != nil {
						return fmt.Errorf("write author-books index keys: %w", err)
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
		if err := g.Wait(); err != nil {
			authorBooksIndexLog.Error("author-books-index: pass failed: pass=%q credit_rows=%d book_rows=%d err=%s",
				name, creditRows.Load(), bookRows.Load(), logger.SanitizeLogValue(err.Error()))
			return err
		}
		return nil
	}

	creditPrefix := []byte("book_authors:")
	if err := runPass("credit rows", func(emit func(authorBooksRow) error) error {
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
			note("book_authors:" + r.id)
			return nil
		}
		for _, id := range ids {
			if err := ensure(batch, authorBooksKey(id, r.id)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return rep, err
	}

	if err := runPass("book rows", func(emit func(authorBooksRow) error) error {
		return forEachBookRow(p.db, func(id string, value []byte) error {
			if n := bookRows.Add(1); n%authorBooksProgressEv == 0 {
				authorBooksIndexLog.Info("author-books-index: progress: pass=book-rows scanned=%d elapsed=%s",
					n, time.Since(start).Round(time.Second))
			}
			return emit(authorBooksRow{id: id, value: append([]byte(nil), value...)})
		})
	}, func(batch *GatedBatch, r authorBooksRow) error {
		var row struct {
			AuthorID *int `json:"author_id"`
		}
		if err := json.Unmarshal(r.value, &row); err != nil {
			unreadableBooks.Add(1)
			return nil
		}
		if row.AuthorID == nil {
			return nil
		}
		return ensure(batch, authorBooksLegacyKey(*row.AuthorID, r.id))
	}); err != nil {
		return rep, err
	}

	rep.CreditRows, rep.BookRows = int(creditRows.Load()), int(bookRows.Load())
	rep.Checked, rep.Written = int(checked.Load()), int(written.Load())
	rep.UnreadableCredits, rep.UnreadableBooks = int(unreadableCredits.Load()), int(unreadableBooks.Load())
	rep.SampleUnreadable = samples

	// Every key is durable (each chunk committed with Sync).
	if rep.UnreadableCredits > 0 {
		// An undecodable credit row hides its authors from the index, so the
		// index is not complete for this library. Fail closed: remove a
		// sentinel an earlier run left, so readers go back to the scan (which
		// fails closed naming the row) until a clean reconcile, rather than
		// keep trusting a sentinel this run just disproved.
		p.authorBooksVerifiedThisBoot.Store(false)
		if present {
			if err := p.db.Delete([]byte(authorBooksIndexSentinel), pebble.Sync); err != nil {
				authorBooksIndexLog.Error("author-books-index: cannot remove the sentinel: err=%s", logger.SanitizeLogValue(err.Error()))
				return rep, fmt.Errorf("delete %s: %w", authorBooksIndexSentinel, err)
			}
		}
		rep.Trusted = false
	} else {
		// Clean: record this build's identity (rewriting a sentinel another
		// build left) and only then open the gate for this boot.
		identity := authorBooksBuildIdentity()
		value := identity
		if value == "" {
			value = "1"
		}
		if !present || (identity != "" && recorded != identity) {
			if err := p.db.Set([]byte(authorBooksIndexSentinel), []byte(value), pebble.Sync); err != nil {
				authorBooksIndexLog.Error("author-books-index: sentinel commit failed: err=%s", logger.SanitizeLogValue(err.Error()))
				return rep, fmt.Errorf("set %s: %w", authorBooksIndexSentinel, err)
			}
		}
		p.authorBooksVerifiedThisBoot.Store(true)
		rep.Trusted = true
	}
	summary := fmt.Sprintf("author-books-index: complete: credit_rows=%d book_rows=%d checked=%d written=%d unreadable_credits=%d unreadable_books=%d sample_unreadable=%q was_trusted=%t trusted=%t duration=%s",
		rep.CreditRows, rep.BookRows, rep.Checked, rep.Written, rep.UnreadableCredits, rep.UnreadableBooks,
		logger.SanitizeLogValue(strings.Join(rep.SampleUnreadable, ",")), rep.WasTrusted, rep.Trusted,
		time.Since(start).Round(time.Millisecond))
	if rep.UnreadableCredits > 0 {
		authorBooksIndexLog.Error("%s", summary)
	} else {
		authorBooksIndexLog.Info("%s", summary)
	}
	if rep.UnreadableCredits > 0 {
		return rep, fmt.Errorf("author-books index not trusted: %d book_authors row(s) do not decode (sample %v)", rep.UnreadableCredits, rep.SampleUnreadable)
	}
	return rep, nil
}
