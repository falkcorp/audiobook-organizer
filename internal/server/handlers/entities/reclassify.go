// file: internal/server/handlers/entities/reclassify.go
// version: 1.6.0
// guid: 8a3f6d12-4c97-4e50-b2a8-1d70e5c93b46
// last-edited: 2026-10-30

package entities

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	ulid "github.com/oklog/ulid/v2"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// ReclassifyStore is what moving an author's credits over to a narrator reads and
// writes. *database.PebbleStore, the handler's EntitiesStore and package server's
// ops store all satisfy it.
type ReclassifyStore interface {
	reclassifyAuthorStore
	reclassifyNarratorStore
	// The books the author credits, the trash included (the delete sweeps it).
	database.AuthorRelinkLister
	GetBookAuthors(bookID string) ([]database.BookAuthor, error)
	SetBookAuthors(bookID string, authors []database.BookAuthor) error
	bookModifier
}

type reclassifyAuthorStore interface {
	unknownAuthorStore
	GetAuthorByID(id int) (*database.Author, error)
	DeleteAuthor(id int) error
}

type reclassifyNarratorStore interface {
	GetNarratorByName(name string) (*database.Narrator, error)
	CreateNarrator(name string) (*database.Narrator, error)
	GetBookNarrators(bookID string) ([]database.BookNarrator, error)
	SetBookNarrators(bookID string, narrators []database.BookNarrator) error
}

// unknownAuthorStore: unknownAuthor.
type unknownAuthorStore interface {
	GetAuthorByName(name string) (*database.Author, error)
	CreateAuthor(name string) (*database.Author, error)
}

// bookModifier: repointPrimaryAuthor, the column-scoped write of a book's primary author.
type bookModifier interface {
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
}

// ErrAuthorNotFound: ReclassifyAuthorAsNarrator was asked for an author that does
// not exist.
var ErrAuthorNotFound = errors.New("author not found")

// ReclassifyError is a failure with the public message the HTTP handler answers
// 500 with; Err is the cause, for the log.
type ReclassifyError struct {
	Msg string
	Err error
}

func (e *ReclassifyError) Error() string { return e.Msg + ": " + e.Err.Error() }
func (e *ReclassifyError) Unwrap() error { return e.Err }

// ReclassifyJournalStore is what journaling a reclassify for undo needs.
type ReclassifyJournalStore interface {
	CreateOperationChange(change *database.OperationChange) error
	GetAuthorAliases(authorID int) ([]database.AuthorAlias, error)
}

// ReclassifyJournal makes a reclassify undoable: each write is journaled under
// OperationID BEFORE it is made (undo/author_merge.go), and voided when it is
// refused. A book whose row cannot be journaled is not moved. Both callers pass
// one: entities.author-reclassify (the Authors view's button) and
// ai.author-scan-apply each journal under their own operation id.
type ReclassifyJournal struct {
	OperationID string
	Store       ReclassifyJournalStore
}

// step stamps and stores a row and returns the function that voids it.
func (j *ReclassifyJournal) step(row *database.OperationChange) (void func() error, err error) {
	row.ID = ulid.Make().String()
	row.OperationID = j.OperationID
	if err := j.Store.CreateOperationChange(row); err != nil {
		return nil, err
	}
	return func() error {
		voided := *row
		now := time.Now()
		// RevertedAt is the wall time; the store stamps RevertedSeq, the order the
		// undo decisions use, as it writes a row that is born reverted.
		voided.RevertedAt, voided.Voided = &now, true
		return j.Store.CreateOperationChange(&voided)
	}, nil
}

// ReclassifyOptions are the knobs a caller other than the HTTP handler turns.
type ReclassifyOptions struct {
	// Journal, when set, records the reclassify for undo.
	Journal *ReclassifyJournal
	// SkipBook, when set, is asked about each book before its author credit is
	// removed; a true answer leaves that book credited to the author (and the
	// reason is reported in ReclassifyOutcome.Skipped). An error leaves the book
	// alone too. The AI scan apply passes the author_name lock check;
	// entities.author-reclassify, a person's explicit action, passes none.
	SkipBook func(bookID string) (skip bool, reason string, err error)
	// Lease, when set, is called before each book's writes, again after the loop
	// before the author's links are re-checked, and once more right before the
	// delete; each call must return true to proceed: it renews the scan
	// stand-down the caller holds. False means the lease is gone and the scanner
	// may be running, so the call stops writing and returns
	// ErrReclassifyLeaseLost WITHOUT deleting the author; what moved before stays
	// moved and journaled. The post-loop checks fence the verify-to-delete window
	// and cover an author with no books, which the loop never checks.
	Lease func() bool
}

// ErrReclassifyLeaseLost: ReclassifyOptions.Lease reported the scan stand-down
// lost part way through, so the reclassify stopped before the author was deleted.
var ErrReclassifyLeaseLost = errors.New("scan stand-down lease lost; reclassify stopped before the author was deleted")

// ReclassifyOutcome is what a reclassify did. Skipped lists the books SkipBook
// left alone; it is filled in on the error return too.
type ReclassifyOutcome struct {
	NarratorID   int
	BooksUpdated int
	Skipped      []string
}

// ReclassifyAuthorAsNarrator turns an author into a narrator: every book credited
// to the author (the trash included) loses that credit and gains a narrator credit
// to a narrator of the same name, and the author row is deleted. It is the body of
// POST /authors/:id/reclassify-as-narrator (through the entities.author-reclassify
// op it queues), and the AI scan apply runs the same code for a narrator the
// suggestion flagged, so the two cannot diverge.
//
// A book the loop could not move is skipped; the delete is then refused with
// database.ErrAuthorStillLinked rather than erasing the only credit it has.
// Cache invalidation is the caller's.
func ReclassifyAuthorAsNarrator(store ReclassifyStore, authorID int, opts ReclassifyOptions) (ReclassifyOutcome, error) {
	author, err := store.GetAuthorByID(authorID)
	if err != nil || author == nil || author.ID != authorID {
		// GetAuthorByID follows the author tombstone: a merged-away id answers with
		// the author it became, whose ID differs. That author is gone.
		return ReclassifyOutcome{}, ErrAuthorNotFound
	}

	// Create or find narrator with same name
	narrator, err := store.GetNarratorByName(author.Name)
	if err != nil || narrator == nil {
		narrator, err = store.CreateNarrator(author.Name)
		if err != nil {
			return ReclassifyOutcome{}, &ReclassifyError{Msg: "failed to create narrator", Err: err}
		}
	}

	// Get all books linked to this author, trash included: the DeleteAuthor
	// below sweeps trashed books' junction rows too.
	books, err := store.GetBooksByAuthorIDForRelinkCore(authorID)
	if err != nil {
		return ReclassifyOutcome{}, &ReclassifyError{Msg: "failed to get author books", Err: err}
	}

	booksUpdated := 0
	var skipped []string
	var unknown *database.Author // resolved at most once, on first need
	for _, book := range books {
		if opts.Lease != nil && !opts.Lease() {
			return ReclassifyOutcome{BooksUpdated: booksUpdated, Skipped: skipped}, ErrReclassifyLeaseLost
		}
		if opts.SkipBook != nil {
			skip, why, serr := opts.SkipBook(book.ID)
			if serr != nil {
				skipped = append(skipped, fmt.Sprintf("book %s: %v", book.ID, serr))
				continue
			}
			if skip {
				skipped = append(skipped, fmt.Sprintf("book %s: %s", book.ID, why))
				continue
			}
		}
		// Everything the move reads is read BEFORE anything is written: a read that
		// fails after the credit was removed left a book with neither credit.
		bookAuthors, err := store.GetBookAuthors(book.ID)
		if err != nil {
			continue
		}
		bookNarrators, err := store.GetBookNarrators(book.ID)
		if err != nil {
			continue
		}
		var newAuthors []database.BookAuthor
		for _, ba := range bookAuthors {
			if ba.AuthorID != authorID {
				newAuthors = append(newAuthors, ba)
			}
		}

		// The reclassified person is a NARRATOR now, so unlike the split there
		// is no designated successor: promote whatever author the junction
		// still lists first, and clear when none is left. Either way
		// book.AuthorID must stop pointing at the row DeleteAuthor is about to
		// remove below.
		var successor *database.Author
		if len(newAuthors) > 0 {
			if a, err := store.GetAuthorByID(newAuthors[0].AuthorID); err == nil && a != nil {
				successor = a
			}
		}
		if successor == nil {
			// No surviving author (or its lookup failed): fall back to the
			// Unknown Author placeholder the name index resolves. Clearing is
			// not allowed -- it strands the book authorless -- and leaving the
			// scalar would dangle once DeleteAuthor below runs.
			if unknown == nil {
				u, uErr := unknownAuthor(store)
				if uErr != nil {
					return ReclassifyOutcome{}, &ReclassifyError{Msg: "failed to resolve the Unknown Author placeholder", Err: uErr}
				}
				unknown = u
			}
			successor = unknown
		}
		if len(newAuthors) == 0 {
			// The reclassified author was the book's only author. The credit
			// list is the ordered source of truth and the primary is only its
			// first entry, so the Unknown Author fallback goes into the list
			// too: a book is never left with an empty credit list.
			newAuthors = []database.BookAuthor{{BookID: book.ID, AuthorID: successor.ID, Role: "author", Position: 0}}
		}
		hasNarrator := false
		for _, bn := range bookNarrators {
			if bn.NarratorID == narrator.ID {
				hasNarrator = true
				break
			}
		}

		var void func() error
		var journaled *database.OperationChange
		var journaledMove undo.AuthorCreditsMove
		if opts.Journal != nil {
			primaryChanged := book.AuthorID != nil && *book.AuthorID == authorID
			after := append([]database.BookAuthor{}, newAuthors...)
			move := undo.AuthorCreditsMove{
				FromAuthorID: authorID, CreditsAfter: &after, PrimaryChanged: primaryChanged,
				NarratorID: narrator.ID, NarratorAdded: !hasNarrator,
			}
			if primaryChanged {
				sid := successor.ID
				move.PrimaryAfter = &sid
			}
			var primaryBefore *int
			if book.AuthorID != nil {
				v := *book.AuthorID
				primaryBefore = &v
			}
			row, rerr := undo.NewAuthorCreditsRow(undo.ChangeTypeAuthorReclassifyCredits, book.ID,
				undo.TitleRelinkCreditsSnapshot{AuthorID: primaryBefore, Credits: append([]database.BookAuthor{}, bookAuthors...)}, move)
			if rerr == nil {
				void, rerr = opts.Journal.step(row)
				journaled, journaledMove = row, move
			}
			if rerr != nil {
				// An unjournaled reclassify cannot be undone, so it does not happen.
				skipped = append(skipped, fmt.Sprintf("book %s: undo row not recorded: %v", book.ID, rerr))
				continue
			}
		}

		if err := store.SetBookAuthors(book.ID, newAuthors); err != nil {
			if void != nil {
				_ = void()
			}
			continue
		}
		if !repointPrimaryAuthor(store, &book, authorID, successor) && journaled != nil && journaledMove.PrimaryChanged {
			// The credit moved but the primary did not (the write failed, or another
			// writer repointed it meanwhile). A row that still claimed the repoint
			// made the revert read the untouched primary as "already back", then
			// refuse the junction as changed since: the credit could never go back.
			journaledMove.PrimaryChanged, journaledMove.PrimaryAfter = false, nil
			if nv, merr := json.Marshal(journaledMove); merr == nil {
				journaled.NewValue = string(nv)
				if uerr := opts.Journal.Store.CreateOperationChange(journaled); uerr != nil {
					skipped = append(skipped, fmt.Sprintf("book %s: undo row not updated after its primary author was not repointed: %v", book.ID, uerr))
				}
			}
		}

		// Add narrator link if not already present
		if !hasNarrator {
			bookNarrators = append(bookNarrators, database.BookNarrator{
				BookID:     book.ID,
				NarratorID: narrator.ID,
				Role:       "narrator",
				Position:   len(bookNarrators),
			})
			if err := store.SetBookNarrators(book.ID, bookNarrators); err != nil {
				continue
			}
		}
		booksUpdated++
	}

	// Delete the author record, unless a book the loop above skipped still
	// credits it. Re-take the lease first: the loop's per-book check does not run
	// for an author with no books, and a scanner that resumed after the last book
	// could re-link the author between the verify and the delete.
	if opts.Lease != nil && !opts.Lease() {
		return ReclassifyOutcome{BooksUpdated: booksUpdated, Skipped: skipped}, ErrReclassifyLeaseLost
	}
	if err := database.VerifyAuthorUnlinked(store, authorID); err != nil {
		if errors.Is(err, database.ErrAuthorStillLinked) {
			return ReclassifyOutcome{BooksUpdated: booksUpdated, Skipped: skipped}, err
		}
		return ReclassifyOutcome{}, &ReclassifyError{Msg: "failed to re-check author links before delete", Err: err}
	}
	var voidDelete func() error
	if opts.Journal != nil {
		aliases, aerr := opts.Journal.Store.GetAuthorAliases(authorID)
		if aerr != nil {
			return ReclassifyOutcome{BooksUpdated: booksUpdated, Skipped: skipped},
				&ReclassifyError{Msg: "author not deleted: its aliases could not be read for the undo row", Err: aerr}
		}
		snap := undo.AuthorSnapshot{ID: authorID, Name: author.Name}
		for _, al := range aliases {
			snap.Aliases = append(snap.Aliases, undo.AuthorAliasSnapshot{ID: al.ID, Name: al.AliasName, Type: al.AliasType})
		}
		// Whether the author holds its own name entry (a case or whitespace
		// variant may hold it instead): DeleteAuthor leaves an entry it does not
		// hold, and the restore must neither take it nor read it as "taken since".
		holder, herr := store.GetAuthorByName(author.Name)
		if herr != nil {
			return ReclassifyOutcome{BooksUpdated: booksUpdated, Skipped: skipped},
				&ReclassifyError{Msg: "author not deleted: its name entry could not be read for the undo row", Err: herr}
		}
		snap.SharedNameIndex = holder != nil && holder.ID != authorID
		row, rerr := undo.NewAuthorDeleteRow(undo.ChangeTypeAuthorReclassifyDelete, snap,
			undo.AuthorReclassifyDelete{NarratorID: narrator.ID, NarratorName: narrator.Name})
		if rerr == nil {
			voidDelete, rerr = opts.Journal.step(row)
		}
		if rerr != nil {
			return ReclassifyOutcome{BooksUpdated: booksUpdated, Skipped: skipped},
				&ReclassifyError{Msg: "author not deleted: undo row not recorded", Err: rerr}
		}
	}
	// Last lease check before the destructive step. The undo row recorded above
	// is voided: nothing was deleted, so nothing should be restorable.
	if opts.Lease != nil && !opts.Lease() {
		if voidDelete != nil {
			_ = voidDelete()
		}
		return ReclassifyOutcome{BooksUpdated: booksUpdated, Skipped: skipped}, ErrReclassifyLeaseLost
	}
	if err := store.DeleteAuthor(authorID); err != nil {
		if voidDelete != nil {
			_ = voidDelete()
		}
		return ReclassifyOutcome{}, &ReclassifyError{Msg: "failed to delete author", Err: err}
	}
	return ReclassifyOutcome{NarratorID: narrator.ID, BooksUpdated: booksUpdated, Skipped: skipped}, nil
}

// unknownAuthor returns the Unknown Author placeholder row the name index
// resolves, creating it when absent.
func unknownAuthor(store unknownAuthorStore) (*database.Author, error) {
	u, err := store.GetAuthorByName(database.UnknownAuthorName)
	if err != nil {
		return nil, err
	}
	if u == nil {
		if u, err = store.CreateAuthor(database.UnknownAuthorName); err != nil {
			return nil, err
		}
	}
	if u == nil || u.ID <= 0 {
		return nil, fmt.Errorf("%q author did not resolve", database.UnknownAuthorName)
	}
	return u, nil
}

// repointPrimaryAuthor moves book.AuthorID off an author that is about to be
// deleted, onto newPrimary, keeping the denormalized book.Author snapshot in
// agreement with it.
//
// Updating the book_authors junction is NOT sufficient before a DeleteAuthor.
// book.AuthorID is a denormalized pointer living on the BOOK row, and
// DeleteAuthor never touches it: it removes the author row, the name index and
// the aliases, then sweeps the junction (pebble_store_authors.go:157-220) and
// stops. A caller that rewrites the junction, deletes the author and returns
// leaves book.AuthorID dangling for every book it touched.
//
// A dangling AuthorID is not cosmetic, and it is not symmetric across read
// paths. The list path serves books out of memdb, where stripBookForMemdb
// clears book.Author (memdb_strip.go:53), so the name is re-hydrated via
// GetAuthorsByIDs -> GetAuthorByID. For a deleted author with no tombstone that
// returns nil, GetAuthorsByIDs omits the key entirely rather than recording a
// miss (pebble_store_authors.go:87), and service_query.go:658 then leaves
// AuthorName nil: the book renders with NO AUTHOR. Callers that read the full
// Pebble row instead still see the stale snapshot and show the OLD name, so the
// same book disagrees with itself between two screens.
//
// Both fields must be set together when there IS a successor. Writing AuthorID
// alone would leave UpdateBook's preserve-on-nil guard (pebble_store.go:2407)
// holding the previous Author object, which is precisely the stale-snapshot
// half of the divergence above.
//
// newPrimary must be non-nil. Until 2026-09-13 nil meant "clear AuthorID" (the
// reclassify case with no surviving author), which left an authorless book and a
// stale Author snapshot. Clearing is no longer allowed: a caller with no
// successor passes the Unknown Author placeholder (h.unknownAuthor), the same
// fallback DeleteAuthor applies at the store level. A nil here is logged and
// the write refused rather than silently clearing.
//
// Third copy of this logic, after internal/scheduler/extra_ops.go:382-410 and
// internal/plugins/maintenance/author.go:221-246. Those two agree; this handler
// was the one that had drifted, which is what the "keep in sync" comment on the
// scheduler copy was meant to prevent and did not.
//
// It reports whether it wrote the primary: false for a book whose primary is not
// the deleted author, a refused or failed write, and a row another writer
// repointed meanwhile.
func repointPrimaryAuthor(store bookModifier, book *database.BookCore, deletedAuthorID int, newPrimary *database.Author) (wrote bool) {
	if book == nil {
		return false
	}
	// Only the books whose PRIMARY author is the doomed one need repointing;
	// for the rest the junction rewrite was the whole job.
	if book.AuthorID == nil || *book.AuthorID != deletedAuthorID {
		return false
	}
	if newPrimary == nil {
		entitiesLog.Warn("author repoint: no successor given, refusing to clear AuthorID book=%s author=%d",
			book.ID, deletedAuthorID)
		return false
	}
	id := newPrimary.ID
	newID := &id

	// ModifyBook sets AuthorID and the denormalized Author on the stored row
	// under its lock. Until 2026-09-14 this hydrated the row with GetBookByID,
	// set the two fields and wrote the whole row back with UpdateBook -- and
	// when the hydrate failed, wrote book.ToBook(), a BookCore PROJECTION, as
	// the whole row (the store restores nine heavy columns on such a write,
	// but not the rest). Both were column writes in disguise: AuthorID and Author
	// are the only columns this function owns, the whole-row write reverted
	// anything another writer committed meanwhile, and the projection write
	// would have blanked every column BookCore does not carry. The store
	// reads the row itself now, so there is no hydrate step to fail and no
	// fallback. A row whose primary author is no longer the deleted one
	// (another writer repointed it meanwhile) is left as it is.
	updated, err := store.ModifyBook(book.ID, func(b *database.Book) error {
		if b.AuthorID == nil || *b.AuthorID != deletedAuthorID {
			wrote = false
			return database.ErrSkipBookWrite
		}
		b.AuthorID = newID
		b.Author = newPrimary
		wrote = true
		return nil
	})
	switch {
	case err != nil:
		entitiesLog.Warn("author repoint: failed to update book %s to author %d: %s",
			book.ID, id, err.Error())
		return false
	case updated == nil:
		entitiesLog.Warn("author repoint: book %s no longer exists, nothing to repoint from author %d",
			book.ID, deletedAuthorID)
		return false
	}
	return wrote
}
