// file: internal/books/store/pebble_store_author_books_legacy_retire.go
// version: 0.1.0
// guid: 9a4f2c61-7e3b-4d58-b0a9-2c6e8f1d5b47
// last-edited: 2026-11-19

package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cockroachdb/pebble/v2"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// Retiring the author_books_legacy: rows (F5-18j).
//
// The family was the author -> books index's copy of Book.AuthorID. Since
// F5-18j no build stages or reads it (pebble_store_author_books_index.go), so
// the stored rows are inert. RetireAuthorBooksLegacy deletes them, behind one
// check: no book may carry an AuthorID its credit list does not hold. Such a
// book's legacy row is the last place an index ever named that author for it,
// and the owner rule is that the credit list is the author source; the retire
// refuses while any exists and lists them instead, so a person (or
// maintenance.credits-backfill) puts the author into the credit list first.
//
// The check covers every book row in every state (trashed and non-primary
// included), not only the listable ones the index trust check needs, and it
// reads the book rows, not the legacy keys: a book whose legacy key was never
// written still carries the AuthorID. A legacy key whose book is gone, or whose
// book's AuthorID has since moved on, is stale and never blocks.
//
// Idempotent and resumable: the delete is by prefix, so a run cut short leaves
// fewer rows and the next run deletes the rest; a run over a retired store
// counts zero and deletes nothing. Deleting changes nothing a current build
// reads; a rolled-back build rebuilds the family from Book.AuthorID in its boot
// reconcile, so the delete needs no undo journal.

// ErrAuthorBooksLegacyRetireRefused is returned by an applying
// RetireAuthorBooksLegacy that found books whose AuthorID their credit list
// does not hold. Nothing was deleted; the report lists the books.
var ErrAuthorBooksLegacyRetireRefused = errors.New("author_books_legacy retire refused: books carry an AuthorID their credit list does not hold")

// authorBooksLegacyRetireSampleCap is how many blocking books the report names.
// The full lists are maintenance.credit-census's author_id_join_empty and
// author_id_not_in_join classes.
const authorBooksLegacyRetireSampleCap = 50

// AuthorBooksLegacyRetireReport is the outcome of one RetireAuthorBooksLegacy or
// CheckAuthorBooksLegacyRetire.
type AuthorBooksLegacyRetireReport struct {
	// Apply is whether the run was allowed to delete.
	Apply bool `json:"apply"`
	// BookRows is every book row read; UnreadableBooks those that did not
	// decode (no path can list them, so they never block).
	BookRows        int `json:"book_rows"`
	UnreadableBooks int `json:"unreadable_books"`
	// WithAuthorID is the decoded rows carrying an AuthorID; Credited those whose
	// credit list holds it.
	WithAuthorID int `json:"with_author_id"`
	Credited     int `json:"credited"`
	// The blocking classes. NoCreditRow: AuthorID set, no credit row at all
	// (census author_id_join_empty). NotInCredits: the credit row lacks it
	// (census author_id_not_in_join). UnreadableCredits: the credit row does not
	// decode, so whether it holds the AuthorID is unknown.
	NoCreditRow       int `json:"no_credit_row"`
	NotInCredits      int `json:"not_in_credits"`
	UnreadableCredits int `json:"unreadable_credits"`
	// Blocking is the sum of the three. Of those, BlockingListable are live and
	// primary (what an author page would show) and BlockingDanglingAuthor name an
	// author id with no author row (maintenance.author-id-repair's ground, not
	// the backfill's).
	Blocking               int `json:"blocking"`
	BlockingListable       int `json:"blocking_listable"`
	BlockingDanglingAuthor int `json:"blocking_dangling_author"`
	// SampleBlocking names up to authorBooksLegacyRetireSampleCap blocking
	// books as "<class> book:<id> author:<id>".
	SampleBlocking []string `json:"sample_blocking,omitempty"`
	// LegacyRows is how many author_books_legacy: rows existed when the run
	// counted them; Deleted how many it deleted.
	LegacyRows int `json:"legacy_rows"`
	Deleted    int `json:"deleted"`
	// Refused is true when blocking books held the delete back (an applying run
	// returns ErrAuthorBooksLegacyRetireRefused; a preview says it would).
	Refused bool `json:"refused"`
}

// AuthorBooksLegacyRetireProgress is called after each counted or deleted chunk
// with a short phase name and the running count. The book-row phase calls it
// from the pool's producer goroutine, never from two goroutines at once.
type AuthorBooksLegacyRetireProgress func(phase string, n int)

// CheckAuthorBooksLegacyRetire is the preview of RetireAuthorBooksLegacy: the
// same check and counts, nothing written. Refused in the report says whether an
// apply would refuse; the error is only for a failed read.
func (p *PebbleStore) CheckAuthorBooksLegacyRetire(ctx context.Context, progress AuthorBooksLegacyRetireProgress) (AuthorBooksLegacyRetireReport, error) {
	return p.retireAuthorBooksLegacy(ctx, false, progress)
}

// RetireAuthorBooksLegacy checks every book row's AuthorID against its credit
// list, counts the author_books_legacy: rows, and, when no book blocks, deletes
// every one of them. When any book blocks it deletes nothing and returns
// ErrAuthorBooksLegacyRetireRefused with the report naming the books: the
// refusal lives here, in the one call that deletes, so no caller can delete
// without the check.
//
// The book-row check runs on runAuthorBooksPass's bounded worker pool
// (runtime.NumCPU() workers; each row costs a decode and one point read). The
// delete is sequential: one iterator, a Sync commit every authorBooksBatchRows
// keys, ctx checked between commits.
func (p *PebbleStore) RetireAuthorBooksLegacy(ctx context.Context, progress AuthorBooksLegacyRetireProgress) (AuthorBooksLegacyRetireReport, error) {
	return p.retireAuthorBooksLegacy(ctx, true, progress)
}

func (p *PebbleStore) retireAuthorBooksLegacy(ctx context.Context, apply bool, progress AuthorBooksLegacyRetireProgress) (AuthorBooksLegacyRetireReport, error) {
	start := time.Now()
	rep := AuthorBooksLegacyRetireReport{Apply: apply}
	authorBooksIndexLog.Info("author-books-legacy-retire: starting: apply=%t", apply)
	if progress == nil {
		progress = func(string, int) {}
	}

	var (
		bookRows, unreadableBooks, withAuthorID, credited atomic.Int64
		noCreditRow, notInCredits, unreadableCredits      atomic.Int64
		listable, dangling                                atomic.Int64
		mu                                                sync.Mutex
		samples                                           []string
	)
	if err := p.runAuthorBooksPass(ctx, func(emit func(authorBooksRow) error) error {
		return forEachBookRow(p.db, func(id string, value []byte) error {
			if n := bookRows.Add(1); n%authorBooksProgressEv == 0 {
				progress("checking book rows", int(n))
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
		if row.AuthorID != nil {
			withAuthorID.Add(1)
		}
		var name string
		switch class {
		case coverageNoAuthorID:
			return nil
		case coverageCredited:
			credited.Add(1)
			return nil
		case coverageNoCreditRow:
			noCreditRow.Add(1)
			name = "no_credit_row"
		case coverageNotInCredits:
			notInCredits.Add(1)
			name = "not_in_credits"
		case coverageCreditUndecodable:
			unreadableCredits.Add(1)
			name = "unreadable_credits"
		}
		if row.listable() {
			listable.Add(1)
		}
		exists, err := p.authorRowExists(*row.AuthorID)
		if err != nil {
			return err
		}
		if !exists {
			dangling.Add(1)
		}
		mu.Lock()
		if len(samples) < authorBooksLegacyRetireSampleCap {
			samples = append(samples, fmt.Sprintf("%s book:%s author:%d", name, r.id, *row.AuthorID))
		}
		mu.Unlock()
		return nil
	}); err != nil {
		authorBooksIndexLog.Error("author-books-legacy-retire: book-row check failed: book_rows=%d err=%s", bookRows.Load(), logger.SanitizeLogValue(err.Error()))
		return rep, fmt.Errorf("author_books_legacy retire: check book rows: %w", err)
	}
	rep.BookRows, rep.UnreadableBooks = int(bookRows.Load()), int(unreadableBooks.Load())
	rep.WithAuthorID, rep.Credited = int(withAuthorID.Load()), int(credited.Load())
	rep.NoCreditRow, rep.NotInCredits, rep.UnreadableCredits = int(noCreditRow.Load()), int(notInCredits.Load()), int(unreadableCredits.Load())
	rep.Blocking = rep.NoCreditRow + rep.NotInCredits + rep.UnreadableCredits
	rep.BlockingListable, rep.BlockingDanglingAuthor = int(listable.Load()), int(dangling.Load())
	rep.SampleBlocking = samples
	rep.Refused = rep.Blocking > 0

	legacy := []byte(authorBooksLegacyPrefix)
	n := 0
	if err := forEachKeyInRange(p.db, legacy, prefixEnd(legacy), func(_, _ []byte) error {
		n++
		if n%authorBooksProgressEv == 0 {
			progress("counting legacy rows", n)
		}
		return ctx.Err()
	}); err != nil {
		return rep, fmt.Errorf("author_books_legacy retire: count rows: %w", err)
	}
	rep.LegacyRows = n

	switch {
	case !apply:
		authorBooksIndexLog.Info("author-books-legacy-retire: preview: %s duration=%s", rep.summary(), time.Since(start).Round(time.Millisecond))
		return rep, nil
	case rep.Refused:
		authorBooksIndexLog.Warn("author-books-legacy-retire: refused: %s duration=%s", rep.summary(), time.Since(start).Round(time.Millisecond))
		return rep, fmt.Errorf("%w: %d book(s) (no credit row %d, not in credits %d, unreadable credits %d; sample %v)",
			ErrAuthorBooksLegacyRetireRefused, rep.Blocking, rep.NoCreditRow, rep.NotInCredits, rep.UnreadableCredits, firstN(rep.SampleBlocking, authorBooksSampleCap))
	}

	deleted, err := p.deleteAuthorBooksLegacyRows(ctx, func(n int) { progress("deleting legacy rows", n) })
	rep.Deleted = deleted
	if err != nil {
		authorBooksIndexLog.Error("author-books-legacy-retire: delete stopped: deleted=%d err=%s", deleted, logger.SanitizeLogValue(err.Error()))
		return rep, fmt.Errorf("author_books_legacy retire: delete rows (deleted %d before stopping; rerun to finish): %w", deleted, err)
	}
	authorBooksIndexLog.Info("author-books-legacy-retire: complete: %s duration=%s", rep.summary(), time.Since(start).Round(time.Millisecond))
	return rep, nil
}

// deleteAuthorBooksLegacyRows deletes every author_books_legacy: row, committing
// (Sync) every authorBooksBatchRows keys and checking ctx between commits. It
// returns how many it deleted, including when it stops on an error.
func (p *PebbleStore) deleteAuthorBooksLegacyRows(ctx context.Context, progress func(n int)) (int, error) {
	legacy := []byte(authorBooksLegacyPrefix)
	deleted := 0
	batch := p.db.NewBatch()
	pending := 0
	commit := func() error {
		if pending == 0 {
			return nil
		}
		err := batch.Commit(pebble.Sync)
		batch.Close()
		batch = p.db.NewBatch()
		if err != nil {
			return err
		}
		deleted += pending
		pending = 0
		progress(deleted)
		return nil
	}
	err := forEachKeyInRange(p.db, legacy, prefixEnd(legacy), func(key, _ []byte) error {
		if err := batch.Delete(append([]byte(nil), key...), nil); err != nil {
			return err
		}
		pending++
		if pending >= authorBooksBatchRows {
			if err := commit(); err != nil {
				return err
			}
			return ctx.Err()
		}
		return nil
	})
	if err == nil {
		err = commit()
	}
	batch.Close()
	return deleted, err
}

// authorRowExists reports whether author:<id> is stored.
func (p *PebbleStore) authorRowExists(id int) (bool, error) {
	_, closer, err := p.db.Get([]byte("author:" + strconv.Itoa(id)))
	switch {
	case err == nil:
		closer.Close()
		return true, nil
	case errors.Is(err, pebble.ErrNotFound):
		return false, nil
	default:
		return false, err
	}
}

func (r AuthorBooksLegacyRetireReport) summary() string {
	return fmt.Sprintf("book_rows=%d unreadable_books=%d with_author_id=%d credited=%d no_credit_row=%d not_in_credits=%d unreadable_credits=%d blocking=%d blocking_listable=%d blocking_dangling_author=%d legacy_rows=%d deleted=%d refused=%t",
		r.BookRows, r.UnreadableBooks, r.WithAuthorID, r.Credited, r.NoCreditRow, r.NotInCredits, r.UnreadableCredits,
		r.Blocking, r.BlockingListable, r.BlockingDanglingAuthor, r.LegacyRows, r.Deleted, r.Refused)
}

func firstN(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
