// file: internal/organizer/organize_batch_lock.go
// version: 1.0.0
// guid: 9a4c2e71-3b8d-4f15-a6e0-c5d7b1f3e284
// last-edited: 2026-09-30

package organizer

import (
	"errors"
	"slices"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/scanlock"
)

// batchLockTries bounds re-resolution when the library copy changes while a
// batch organize takes a book's lock.
const batchLockTries = 3

// errBatchBookBusy: the book (or its library copy) is held, or the pair would
// not settle; the batch treats it like a book the scan is reading.
var errBatchBookBusy = errors.New("book busy")

// batchLockSet is the sorted rows a batch organize of book locks: the book
// and, for a protected original, the library copy ResolveLibraryCopy maps it
// to (see Service.ResolveLibraryCopy).
func (orgSvc *Service) batchLockSet(book *database.Book) []string {
	ids := []string{book.ID}
	if subject := ResolveOrganizeSubject(orgSvc.ResolveLibraryCopy, book); subject != nil && subject.ID != book.ID {
		ids = append(ids, subject.ID)
	}
	slices.Sort(ids)
	return ids
}

// lockBatchBook takes batchLockSet(book) with lock and re-resolves it from the
// stored row UNDER the lock: when the copy changed meanwhile it releases and
// retries, at most batchLockTries times, then releases everything and
// returns errBatchBookBusy. It never returns a hold that misses the copy the
// stored row resolves to. A book that cannot be re-read keeps the set it
// holds; organizeOne re-reads it and reports the error.
func (orgSvc *Service) lockBatchBook(book *database.Book, lock func([]string) (*scanlock.Hold, error)) (*scanlock.Hold, error) {
	for range batchLockTries {
		want := orgSvc.batchLockSet(book)
		hold, err := lock(want)
		if err != nil {
			return nil, err
		}
		if orgSvc.ResolveLibraryCopy == nil || orgSvc.db == nil {
			return hold, nil
		}
		fresh, ferr := orgSvc.db.GetBookByID(book.ID)
		if ferr != nil || fresh == nil {
			return hold, nil
		}
		if slices.Equal(orgSvc.batchLockSet(fresh), want) {
			return hold, nil
		}
		hold.Release()
	}
	return nil, errBatchBookBusy
}
