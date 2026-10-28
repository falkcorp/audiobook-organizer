// file: internal/undo/credits_backfill.go
// version: 1.1.2
// guid: 28847eaf-80d0-49cd-95a2-6de0cb8f0381
// last-edited: 2026-10-28

package undo

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// ChangeTypeCreditsBackfill: maintenance.credits-backfill rewrote one book's
// credit lists, and possibly its flat author fields, so the lists agree with
// the flat fields (PR 3 of
// docs/plans/2026-10-04-author-narrator-credit-lists-audit.md). OldValue is a
// CreditsBackfillState: the book's author and narrator rows exactly as stored
// and its four flat fields (AuthorID, the Author snapshot, Narrator,
// NarratorsJSON), read immediately before the write. NewValue is a
// CreditsBackfillWrite, whose After is the EXACT state the write left (the
// lists as the store normalised them).
//
// Its own type rather than junk_author_credits: a backfill FILLS an empty
// join (there is no "from" author, which DecodeJunkAuthorCredits refuses),
// writes narrator rows, and may set flat fields; that type carries none of
// these.
//
// The revert is compare-and-set on the WHOLE state: it restores the pre-image
// only while the book still holds exactly After, counts a book already back at
// the pre-image as restored, and refuses anything else (a credit or flat field
// changed since is a later edit the revert must not overwrite).
//
// Positions: every credit write normalises its lists (PR 1:
// NormalizeBookAuthors / NormalizeBookNarrators). The backfill's own write
// breaks position ties with Book.AuthorID (the plan's tie-breaker), but the
// revert must NOT: the organizer files a book under the first STORED row at
// the lowest position (organizer.PrimaryJoinAuthor), so a pre-image of
// [Selina@0, Bruce@0] with AuthorID Bruce, written back through the store's
// AuthorID tie-breaker, would come back [Bruce, Selina] and the book would be
// filed under another author than before the backfill. The revert therefore
// restores the pre-image through Restored: ties resolved by STORED order,
// positions renumbered 0..n-1 before the store sees them, so its AuthorID
// tie-breaker has nothing left to break. A pre-image whose positions were not
// 0..n-1 comes back as the same rows in the order the organizer read them in,
// not as the duplicate positions themselves. Everything else, the flat fields
// included, comes back exactly.
const ChangeTypeCreditsBackfill = "credits_backfill"

// ChangeTypeNarratorCreateIntent: maintenance.credits-backfill is about to create
// a narrator record named NewValue. It is written BEFORE the create, so a crash
// between the store's commit and the credits_backfill row that would name the
// record leaves a trace: a later attempt of the same operation finds the record
// by this name and deletes it if nothing links it (it was made by the operation
// and nobody credits it). A ledger record only: the revert has no case for it, so
// POST /operations/:id/revert reports it under not_restorable_types, like the
// narrator_delete rows the same op writes when it removes such a record.
const ChangeTypeNarratorCreateIntent = "narrator_create_intent"

// CreditsBackfillState is one book's credit state as a credits_backfill row
// records it.
type CreditsBackfillState struct {
	Authors       []database.BookAuthor   `json:"authors"`
	Narrators     []database.BookNarrator `json:"narrators"`
	AuthorID      *int                    `json:"author_id"`
	Author        *database.Author        `json:"author"`
	Narrator      *string                 `json:"narrator"`
	NarratorsJSON *string                 `json:"narrators_json"`
}

// CreditsBackfillWrite is NewValue of a credits_backfill row.
type CreditsBackfillWrite struct {
	// After is the state the write left, exactly as stored.
	After CreditsBackfillState `json:"after"`
	// Classes are the backfill classes the write applied (never empty).
	Classes []string `json:"classes"`
	// CreatedNarrators names the narrator records the backfill created for
	// this book. Informational: the revert leaves them in place (another
	// book may credit them since) and maintenance.purge-empty-narrators
	// removes the ones nothing credits.
	CreatedNarrators []database.Narrator `json:"created_narrators,omitempty"`
}

// CreditsBackfillStateOf builds the state of book with the given rows. Every
// slice and pointer is copied, so the state does not alias the book.
func CreditsBackfillStateOf(book *database.Book, authors []database.BookAuthor, narrators []database.BookNarrator) CreditsBackfillState {
	s := CreditsBackfillState{
		Authors:   slices.Clone(authors),
		Narrators: slices.Clone(narrators),
	}
	if book == nil {
		return s
	}
	s.AuthorID = cloneIntPtr(book.AuthorID)
	if book.Author != nil {
		a := *book.Author
		s.Author = &a
	}
	s.Narrator = cloneStringPtr(book.Narrator)
	s.NarratorsJSON = cloneStringPtr(book.NarratorsJSON)
	return s
}

// ApplyFlatFields writes the state's four flat fields onto book (copies).
func (s CreditsBackfillState) ApplyFlatFields(book *database.Book) {
	book.AuthorID = cloneIntPtr(s.AuthorID)
	book.Author = nil
	if s.Author != nil {
		a := *s.Author
		book.Author = &a
	}
	book.Narrator = cloneStringPtr(s.Narrator)
	book.NarratorsJSON = cloneStringPtr(s.NarratorsJSON)
}

// Canonical returns s with both lists in the form a credit write stores them
// (NormalizeBookAuthors with s.AuthorID as the tie-breaker,
// NormalizeBookNarrators). Book ids on the rows are left as they are.
func (s CreditsBackfillState) Canonical() CreditsBackfillState {
	out := s
	out.Authors = database.NormalizeBookAuthors(s.Authors, s.AuthorID)
	out.Narrators = database.NormalizeBookNarrators(s.Narrators)
	return out
}

// Restored returns s with its lists in the form the revert stores them: rows
// in STORED order (a stable sort on Position, so a tie keeps the order the
// rows were stored in, which is the order the organizer picks its filing
// author in), positions renumbered 0..n-1, rows without a record and repeated
// (id, role) rows dropped, as a credit write would. Unlike Canonical, no
// AuthorID tie-breaker is applied: the positions it returns are distinct, so
// the store's own AuthorID tie-breaker cannot reorder them afterwards.
func (s CreditsBackfillState) Restored() CreditsBackfillState {
	out := s
	rows := make([]database.BookAuthor, 0, len(s.Authors))
	for _, r := range s.Authors {
		if r.AuthorID > 0 {
			rows = append(rows, r)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Position < rows[j].Position })
	for i := range rows {
		rows[i].Position = i
	}
	out.Authors = database.NormalizeBookAuthors(rows, nil)
	out.Narrators = database.NormalizeBookNarrators(s.Narrators)
	return out
}

// SameCreditsBackfillState reports whether two states are the same rows in
// the same order with the same roles and positions, and the same four flat
// fields. Row book ids are not compared: the key names the book, and the store
// stamps it on every read and write.
func SameCreditsBackfillState(a, b CreditsBackfillState) bool {
	if len(a.Authors) != len(b.Authors) || len(a.Narrators) != len(b.Narrators) {
		return false
	}
	for i := range a.Authors {
		x, y := a.Authors[i], b.Authors[i]
		if x.AuthorID != y.AuthorID || x.Role != y.Role || x.Position != y.Position {
			return false
		}
	}
	for i := range a.Narrators {
		x, y := a.Narrators[i], b.Narrators[i]
		if x.NarratorID != y.NarratorID || x.Role != y.Role || x.Position != y.Position {
			return false
		}
	}
	if !sameIntPointer(a.AuthorID, b.AuthorID) || !sameStringPointer(a.Narrator, b.Narrator) ||
		!sameStringPointer(a.NarratorsJSON, b.NarratorsJSON) {
		return false
	}
	switch {
	case a.Author == nil || b.Author == nil:
		return a.Author == nil && b.Author == nil
	default:
		return a.Author.ID == b.Author.ID && a.Author.Name == b.Author.Name
	}
}

// DecodeCreditsBackfill parses a credits_backfill row. A row without classes
// is refused: it is not something the backfill wrote.
func DecodeCreditsBackfill(c *database.OperationChange) (CreditsBackfillState, CreditsBackfillWrite, error) {
	var before CreditsBackfillState
	var write CreditsBackfillWrite
	if err := json.Unmarshal([]byte(c.OldValue), &before); err != nil {
		return before, write, &ReferentError{Reason: ReasonOldValueUnparsable, Detail: fmt.Sprintf("credits pre-image of change %s: %v", c.ID, err)}
	}
	if err := json.Unmarshal([]byte(c.NewValue), &write); err != nil || len(write.Classes) == 0 {
		return before, write, &ReferentError{Reason: ReasonOldValueUnparsable, Detail: fmt.Sprintf("credits write of change %s", c.ID)}
	}
	return before, write, nil
}

// CheckCreditsBackfillCurrent is the revert's compare-and-set. It returns nil
// when current is exactly the state the backfill wrote (after), so the
// pre-image may be restored; ErrAlreadyRestored when current already is the
// pre-image, either as the revert stores it (before.Restored()) or exactly as
// the backfill read it (before): a journal row whose write never landed (the
// run died between the journal and ModifyBookCredits) leaves the book at the
// raw pre-image, which differs from before.Restored() whenever the stored
// lists were not in canonical form (all-zero positions, say), and is already
// restored, not changed-since. A ReasonChangedSince refusal otherwise.
func CheckCreditsBackfillCurrent(bookID string, current, before, after CreditsBackfillState) error {
	switch {
	case SameCreditsBackfillState(current, after):
		return nil
	case SameCreditsBackfillState(current, before.Restored()), SameCreditsBackfillState(current, before):
		return ErrAlreadyRestored
	}
	return refuse(ReasonChangedSince, "book %s credits or author/narrator fields changed since the credits backfill (%s)",
		bookID, describeCreditsDrift(current, after))
}

// describeCreditsDrift names which parts of current differ from want, for the
// refusal text.
func describeCreditsDrift(current, want CreditsBackfillState) string {
	var parts []string
	a, b := current, want
	if !SameCreditsBackfillState(CreditsBackfillState{Authors: a.Authors}, CreditsBackfillState{Authors: b.Authors}) {
		parts = append(parts, "author credits")
	}
	if !SameCreditsBackfillState(CreditsBackfillState{Narrators: a.Narrators}, CreditsBackfillState{Narrators: b.Narrators}) {
		parts = append(parts, "narrator credits")
	}
	if !sameIntPointer(a.AuthorID, b.AuthorID) {
		parts = append(parts, "author_id")
	}
	if !SameCreditsBackfillState(CreditsBackfillState{Author: a.Author}, CreditsBackfillState{Author: b.Author}) {
		parts = append(parts, "author snapshot")
	}
	if !sameStringPointer(a.Narrator, b.Narrator) {
		parts = append(parts, "narrator")
	}
	if !sameStringPointer(a.NarratorsJSON, b.NarratorsJSON) {
		parts = append(parts, "narrators_json")
	}
	if len(parts) == 0 {
		return "unknown"
	}
	return strings.Join(parts, ", ")
}

func sameStringPointer(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func cloneIntPtr(p *int) *int {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func cloneStringPtr(p *string) *string {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
