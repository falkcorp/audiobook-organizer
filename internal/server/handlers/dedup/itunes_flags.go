// file: internal/server/handlers/dedup/itunes_flags.go
// version: 1.0.0
// guid: 7c1e5a94-3b82-4d6f-a0e9-52f8d4c1b673
// last-edited: 2026-10-11

package deduphandler

import (
	"errors"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/itunesguard"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// bookWithITunes is the include_books payload for one side of a pair: the
// stored book exactly as before, plus is_itunes. The embedded pointer's fields
// are promoted into the JSON object (database.Book has no MarshalJSON), so a
// client that read book_a.title keeps reading it.
type bookWithITunes struct {
	*database.Book
	// IsITunes is itunesguard.OwnershipWhy != "": a book iTunes persistent id,
	// a row iTunes persistent id or path, a live itunes external id, a path
	// inside an iTunes Media folder, or a path under books/itunes/**. It is
	// what the Duplicates lane's read-only "iTunes" chip shows; it is a label
	// and decides nothing (the merge guard has its own, narrower path rule).
	IsITunes bool `json:"is_itunes"`
}

// iTunesFlagStore is the store surface iTunesFlags reads.
type iTunesFlagStore interface {
	GetBookFilesForIDsCore(bookIDs []string) (map[string][]database.BookFileCore, error)
	GetExternalIDsForBook(bookID string) ([]database.ExternalIDMapping, error)
}

// iTunesFlags reports, per book id, whether the book is an iTunes copy by the
// same predicate every iTunes-hands-off writer uses (itunesguard.OwnershipWhy),
// for the books of ONE listing page.
//
// The book_file rows of every book come from a single GetBookFilesForIDsCore
// call, not one read per book. The external-id read has no batch form in the
// store, so it is issued per book -- but only for a book the cheaper signals
// (book id, row ids and paths, path rules) did not already settle, and only
// once per distinct book on the page (the list can name one book on several
// pairs). OwnershipWhy is a plain OR over its signals, so evaluating it first
// without external ids and again with them only when that came back empty gives
// the same answer as evaluating it once with all of them.
//
// A read error is returned, never read as "not an iTunes book": a chip that
// silently went missing is exactly the failure this field exists to remove.
// A path whose location cannot be settled (OwnershipWhy's doubt) is reported
// as not-iTunes -- the chip claims only what was proven.
func iTunesFlags(store iTunesFlagStore, books []*database.Book) (map[string]bool, error) {
	ids := make([]string, 0, len(books))
	byID := make(map[string]*database.Book, len(books))
	for _, b := range books {
		if b == nil || byID[b.ID] != nil {
			continue
		}
		byID[b.ID] = b
		ids = append(ids, b.ID)
	}
	flags := make(map[string]bool, len(ids))
	if len(ids) == 0 {
		return flags, nil
	}
	filesByID, err := store.GetBookFilesForIDsCore(ids)
	if err != nil {
		return nil, fmt.Errorf("read book files for the iTunes flag: %w", err)
	}
	res := repairs.NewPathResolver()
	var errs []error
	for _, id := range ids {
		b := byID[id]
		files := filesByID[id]
		paths := make([]string, 0, len(files)+1)
		paths = append(paths, b.FilePath)
		rows := make([]itunesguard.Row, 0, len(files))
		for _, f := range files {
			paths = append(paths, f.FilePath)
			rows = append(rows, itunesguard.Row{ID: f.ID, ITunesPID: f.ITunesPersistentID, ITunesPath: f.ITunesPath})
		}
		pid := ""
		if b.ITunesPersistentID != nil {
			pid = *b.ITunesPersistentID
		}
		why, _ := itunesguard.OwnershipWhy(res, id, pid, paths, rows, nil, true)
		if why == "" {
			exts, xerr := store.GetExternalIDsForBook(id)
			if xerr != nil {
				errs = append(errs, fmt.Errorf("external ids of %s: %w", id, xerr))
				continue
			}
			why, _ = itunesguard.OwnershipWhy(res, id, pid, paths, rows, exts, true)
		}
		flags[id] = why != ""
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return flags, nil
}
