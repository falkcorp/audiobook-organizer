// file: internal/reconcile/apply_file_rows_followup_test.go
// version: 1.0.0
// guid: 5b0e7c3a-91d4-4f62-a8c7-3e1d6b9f0a24
// last-edited: 2026-11-18

package reconcile

import (
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// The follow-up to the apply's first round of review (#4719): handing an earlier
// operation's rows over when a rerun finishes a stranded match, the book read
// back before a rollback, a trashed book refused inside the write, the twin's
// provenance filter, a multi-row book's row already at the found file, the holder
// check at the write, and the match type deciding which hashes survive.

// reverted is whether the operation's change row id is marked reverted.
func (f *applyFixture) reverted(operationID, rowID string) bool {
	f.t.Helper()
	all, err := f.ps.GetOperationChanges(operationID)
	if err != nil {
		f.t.Fatal(err)
	}
	for _, c := range all {
		if c.ID == rowID {
			return c.RevertedAt != nil
		}
	}
	f.t.Fatalf("operation %s has no row %s", operationID, rowID)
	return false
}

// requireLive fails unless exactly n of the operation's rows still stand: not
// voided and not marked reverted, so a revert of the operation would act on them.
func (f *applyFixture) requireLive(operationID string, n int) {
	f.t.Helper()
	all, err := f.ps.GetOperationChanges(operationID)
	if err != nil {
		f.t.Fatal(err)
	}
	live := 0
	for _, c := range all {
		if !c.Voided && c.RevertedAt == nil {
			live++
		}
	}
	if live != n {
		f.t.Errorf("operation %s has %d rows a revert would act on, want %d", operationID, live, n)
	}
}

// strand leaves the book's one row at the found file and the book at the old
// path, journaled live under operationID: the state a rollback that failed leaves.
func (f *applyFixture) strand(operationID string, item ReconcileApplyItem) {
	f.t.Helper()
	store := &failingStore{PebbleStore: f.ps, failBook: true, failRowAfter: 1}
	if _, err := f.apply(store, operationID, item); err == nil {
		f.t.Fatal("want the first apply to fail and strand the row")
	}
}

func (f *applyFixture) revertOp(operationID string) {
	f.t.Helper()
	// An operation whose every row is already marked reverted may answer with an
	// error: the callers check the state it leaves, not the answer.
	_, _ = audiobooks.NewRevertService(f.ps).RevertOperation(operationID)
}

// The sequence the review traced: op-1 is stranded, op-2 finishes the match, the
// user reverts op-2, op-3 applies the match again, and the user reverts op-1.
// Op-1's rows were left live (its book row never landed, its repoint row did), so
// that last revert found the book and the row at NewValue and undid op-3's work.
// Finishing the match hands op-1's rows to op-2: they are marked reverted with no
// write, and op-3's work survives a revert of op-1.
func TestExecuteReconcile_FinishingAStrandedMatchHandsOffTheEarlierOperationsRows(t *testing.T) {
	f := newApplyFixture(t)
	old := f.missingPath("Year One.m4b")
	id := f.book("Batman: Year One", old)
	row := f.row(id, old)
	item := ReconcileApplyItem{BookID: id, NewPath: f.newPath}
	f.strand("op-1", item)
	f.requireRows("op-1", 2, 2)
	op1 := f.journal("op-1")

	if res, err := f.apply(f.ps, "op-2", item); err != nil || res.Applied != 1 {
		t.Fatalf("op-2 finishing the match: %v, result %+v", err, res)
	}
	f.requireLive("op-1", 0)
	for _, c := range append(op1[undo.ChangeTypeBookPathUpdate], op1[database.ChangeTypeBookFileRepoint]...) {
		if !f.reverted("op-1", c.ID) {
			t.Errorf("op-1's %s row %s is still live after op-2 finished the match", c.ChangeType, c.ID)
		}
	}

	f.revertOp("op-2")
	if got := f.bookPath(id); got != old {
		t.Fatalf("after reverting op-2 the book is at %q, want %q", got, old)
	}
	if rows := f.rows(id); len(rows) != 1 || rows[0].FilePath != old || !rows[0].Missing {
		t.Fatalf("after reverting op-2 the row is %+v, want it back at %q", rows, old)
	}

	if res, err := f.apply(f.ps, "op-3", item); err != nil || res.Applied != 1 {
		t.Fatalf("op-3: %v, result %+v", err, res)
	}
	f.requireRepointedRow(id, row)

	f.revertOp("op-1")
	if got := f.bookPath(id); got != f.newPath {
		t.Errorf("reverting op-1 undid op-3: the book is at %q, want %q", got, f.newPath)
	}
	f.requireRepointedRow(id, row)
}

// Only the rows that describe this match go: another writer's rows (Source set),
// rows for another path and unrelated fields of op-1 stay live.
func TestExecuteReconcile_TheHandOffTouchesOnlyTheRowsOfThisMatch(t *testing.T) {
	f := newApplyFixture(t)
	old := f.missingPath("Year One.m4b")
	id := f.book("Batman: Year One", old)
	f.row(id, old)
	item := ReconcileApplyItem{BookID: id, NewPath: f.newPath}
	f.strand("op-1", item)

	// Rows beside the match, all live, all of op-1: an unrelated field, a path
	// change to another file, and a path change another writer made for this one.
	extras := []*database.OperationChange{
		{ID: "extra-title", OperationID: "op-1", BookID: id, ChangeType: "metadata_update", FieldName: "title", OldValue: "a", NewValue: "b"},
		{ID: "extra-elsewhere", OperationID: "op-1", BookID: id, ChangeType: undo.ChangeTypeBookPathUpdate, FieldName: "file_path", OldValue: old, NewValue: f.missingPath("elsewhere.m4b")},
		{ID: "extra-foreign", OperationID: "op-1", BookID: id, ChangeType: undo.ChangeTypeBookPathUpdate, FieldName: "file_path", OldValue: old, NewValue: f.newPath, Source: "some-fixer"},
	}
	for _, c := range extras {
		if err := f.ps.CreateOperationChange(c); err != nil {
			t.Fatal(err)
		}
	}

	if res, err := f.apply(f.ps, "op-2", item); err != nil || res.Applied != 1 {
		t.Fatalf("op-2: %v, result %+v", err, res)
	}
	f.requireLive("op-1", len(extras))
	for _, c := range extras {
		if f.reverted("op-1", c.ID) {
			t.Errorf("the hand-off marked %s reverted: it is not a row of this match", c.ID)
		}
	}
}

// The twin is a copy of the apply's OWN earlier row. A repoint row another
// writer journaled (Source set) is not copied, whatever its values.
func TestExecuteReconcile_AFinishingRerunDoesNotTwinAnotherWritersRow(t *testing.T) {
	f := newApplyFixture(t)
	old := f.missingPath("Year One.m4b")
	id := f.book("Batman: Year One", old)
	f.row(id, old)
	item := ReconcileApplyItem{BookID: id, NewPath: f.newPath}
	f.strand("op-1", item)
	for _, c := range f.journal("op-1")[database.ChangeTypeBookFileRepoint] {
		v := *c
		v.Source = "some-fixer"
		if err := f.ps.CreateOperationChange(&v); err != nil {
			t.Fatal(err)
		}
	}

	if res, err := f.apply(f.ps, "op-2", item); err != nil || res.Applied != 1 {
		t.Fatalf("op-2: %v, result %+v", err, res)
	}
	if n := len(f.journal("op-2")[database.ChangeTypeBookFileRepoint]); n != 0 {
		t.Errorf("the rerun journaled %d repoint rows copied from another writer's row, want none", n)
	}
	for _, c := range f.journal("op-1")[database.ChangeTypeBookFileRepoint] {
		if f.reverted("op-1", c.ID) {
			t.Errorf("the hand-off marked another writer's row %s reverted", c.ID)
		}
	}
}

// The hand-off is best effort, after the write: a journal that cannot mark the
// earlier operation's rows still leaves the match applied, and its rows live.
func TestExecuteReconcile_AFailedHandOffLeavesTheMatchApplied(t *testing.T) {
	f := newApplyFixture(t)
	old := f.missingPath("Year One.m4b")
	id := f.book("Batman: Year One", old)
	row := f.row(id, old)
	item := ReconcileApplyItem{BookID: id, NewPath: f.newPath}
	f.strand("op-1", item)

	store := &failingStore{PebbleStore: f.ps, failRowAfter: -1, failMark: true}
	res, err := f.apply(store, "op-2", item)
	if err != nil || res.Applied != 1 || res.Skipped != 0 {
		t.Fatalf("op-2: %v, result %+v, want applied despite the hand-off failing", err, res)
	}
	if got := f.bookPath(id); got != f.newPath {
		t.Errorf("book path = %q, want %q", got, f.newPath)
	}
	f.requireRepointedRow(id, row)
	f.requireLive("op-1", 2)
}

// A book with a hash, finished after a stranded match: op-1 journaled the hash
// clears too (live, never written), so they are handed off with the path rows,
// and reverting op-2 restores the hashes along with the path and the row.
func TestExecuteReconcile_TheHandOffCoversTheHashRowsToo(t *testing.T) {
	f := newApplyFixture(t)
	old := f.missingPath("Year One.m4b")
	id := f.book("Batman: Year One", old)
	row := f.row(id, old)
	setBookHashes(t, f, id, "book-hash", "organized-hash")
	item := ReconcileApplyItem{BookID: id, NewPath: f.newPath, MatchType: "filename"}
	f.strand("op-1", item)
	f.requireRows("op-1", 4, 4) // book path, two hash clears, the row repoint
	// A hash clear of op-1 that cleared a DIFFERENT value is not this match's.
	stale := &database.OperationChange{ID: "stale-hash", OperationID: "op-1", BookID: id, ChangeType: "metadata_update", FieldName: "file_hash", OldValue: "an-older-hash", NewValue: ""}
	if err := f.ps.CreateOperationChange(stale); err != nil {
		t.Fatal(err)
	}

	if res, err := f.apply(f.ps, "op-2", item); err != nil || res.Applied != 1 {
		t.Fatalf("op-2: %v, result %+v", err, res)
	}
	f.requireLive("op-1", 1)
	if f.reverted("op-1", stale.ID) {
		t.Error("the hand-off marked a hash clear of another value reverted")
	}
	f.requireLive("op-2", 4)

	f.revertOp("op-2")
	b, _ := f.ps.GetBookByID(id)
	if b.FilePath != old || str(b.FileHash) != "book-hash" || str(b.OrganizedFileHash) != "organized-hash" {
		t.Fatalf("after reverting op-2 the book is %q hash %q organized %q, want it all back", b.FilePath, str(b.FileHash), str(b.OrganizedFileHash))
	}

	if res, err := f.apply(f.ps, "op-3", item); err != nil || res.Applied != 1 {
		t.Fatalf("op-3: %v, result %+v", err, res)
	}
	f.requireRepointedRow(id, row)
	// The stale row legitimately stands (it is not this match's), and reverting it
	// would restore its own value; set it aside so the revert below tests the hand-off.
	if err := f.ps.MarkOperationChangesReverted("op-1", []string{stale.ID}); err != nil {
		t.Fatal(err)
	}
	f.revertOp("op-1")
	b, _ = f.ps.GetBookByID(id)
	if b.FilePath != f.newPath || b.FileHash != nil || b.OrganizedFileHash != nil {
		t.Errorf("reverting op-1 undid op-3: book %q hash %v organized %v", b.FilePath, b.FileHash, b.OrganizedFileHash)
	}
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func setBookHashes(t *testing.T, f *applyFixture, id, hash, organized string) {
	t.Helper()
	if _, err := f.ps.ModifyBook(id, func(b *database.Book) error {
		b.FileHash, b.OrganizedFileHash = &hash, &organized
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// A book write that reported a failure but landed (the book is at the matched
// path when read back) is applied: the row is NOT rolled back, so book and row
// agree, and the match counts as applied. Rolling the row back first, as the apply
// did, left the book at the matched file and the row at the old one, a state a
// rerun cannot finish.
func TestExecuteReconcile_ABookWriteThatLandedIsNotRolledBack(t *testing.T) {
	f := newApplyFixture(t)
	old := f.missingPath("Year One.m4b")
	id := f.book("Batman: Year One", old)
	row := f.row(id, old)
	store := &failingStore{PebbleStore: f.ps, bookLandsThenErr: true, failRowAfter: -1}

	res, err := f.apply(store, "op-1", ReconcileApplyItem{BookID: id, NewPath: f.newPath})
	if err != nil || res.Applied != 1 || res.Skipped != 0 {
		t.Fatalf("err = %v, result %+v, want the landed write counted as applied", err, res)
	}
	if got := f.bookPath(id); got != f.newPath {
		t.Errorf("book path = %q, want %q", got, f.newPath)
	}
	f.requireRepointedRow(id, row)
	f.requireRows("op-1", 2, 2)
	if store.rowCalls != 1 {
		t.Errorf("the row was written %d times, want once: no rollback", store.rowCalls)
	}
}

// When the book cannot be read back the apply cannot tell whether its write
// landed. It leaves the row at the matched file (a stranded match a rerun or a
// revert finishes) rather than rolling it back under a book that may be there.
func TestExecuteReconcile_ABookThatCannotBeReadBackKeepsItsRow(t *testing.T) {
	f := newApplyFixture(t)
	old := f.missingPath("Year One.m4b")
	id := f.book("Batman: Year One", old)
	row := f.row(id, old)
	store := &failingStore{PebbleStore: f.ps, failBook: true, failRowAfter: -1, failBookReadAfterWrite: true}

	res, err := f.apply(store, "op-1", ReconcileApplyItem{BookID: id, NewPath: f.newPath})
	if err == nil || res.Applied != 0 || res.Skipped != 1 || len(res.Errors) != 1 {
		t.Fatalf("err = %v, result %+v, want one failed match", err, res)
	}
	if !strings.Contains(res.Errors[0], "could not be read back") || !strings.Contains(res.Errors[0], row.ID) {
		t.Errorf("errors = %q, want the unread book and the row left at the matched file named", res.Errors)
	}
	f.requireRepointedRow(id, row)
	f.requireRows("op-1", 2, 2)
	if store.rowCalls != 1 {
		t.Errorf("the row was written %d times, want once: not rolled back blind", store.rowCalls)
	}
}

// A book trashed between the plan and the write is refused INSIDE the write, under
// the book's lock: nothing of it lands, the row is put back, and the rows are
// voided. The refusal has its own sentinel, so the message does not tell the
// user another writer changed the book's path.
func TestExecuteReconcile_ABookTrashedUnderTheApplyIsRefusedAtTheWrite(t *testing.T) {
	trash := func(f *applyFixture, id string) func() {
		return func() {
			yes := true
			if _, err := f.ps.ModifyBook(id, func(b *database.Book) error { b.MarkedForDeletion = &yes; return nil }); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Run("the row is put back", func(t *testing.T) {
		f := newApplyFixture(t)
		old := f.missingPath("Year One.m4b")
		id := f.book("Batman: Year One", old)
		f.row(id, old)
		before := f.rows(id)
		store := &failingStore{PebbleStore: f.ps, failRowAfter: -1}
		store.beforeBookCall = trash(f, id)

		res, err := f.apply(store, "op-1", ReconcileApplyItem{BookID: id, NewPath: f.newPath})
		if err == nil || res.Applied != 0 || res.Skipped != 1 || len(res.Errors) != 1 {
			t.Fatalf("err = %v, result %+v, want one refused match", err, res)
		}
		if !strings.Contains(res.Errors[0], "to the trash") || strings.Contains(res.Errors[0], "another writer") {
			t.Errorf("errors = %q, want the trash named and no other writer blamed", res.Errors)
		}
		f.requireUntouched(id, old, before)
		f.requireRows("op-1", 2, 0)
	})
	t.Run("a row that cannot be put back is not blamed on another writer", func(t *testing.T) {
		f := newApplyFixture(t)
		old := f.missingPath("Year One.m4b")
		id := f.book("Batman: Year One", old)
		row := f.row(id, old)
		store := &failingStore{PebbleStore: f.ps, failRowAfter: 1}
		store.beforeBookCall = trash(f, id)

		res, err := f.apply(store, "op-1", ReconcileApplyItem{BookID: id, NewPath: f.newPath})
		if err == nil || len(res.Errors) != 1 {
			t.Fatalf("err = %v, result %+v, want one failed match", err, res)
		}
		if strings.Contains(res.Errors[0], "another writer") || !strings.Contains(res.Errors[0], "in the trash: restore it") {
			t.Errorf("errors = %q, want the trash named with the restore advice, and no other writer blamed", res.Errors)
		}
		f.requireRepointedRow(id, row)
		f.requireRows("op-1", 2, 2) // the row landed and could not be put back: stays live
	})
}

// A trashed book already at the matched path needs no write, so the callback skips
// before it looks at the trash: refusing there would roll the row back under a
// book that is at the matched file.
func TestExecuteReconcile_ATrashedBookAlreadyAtTheFoundFileIsNotRolledBack(t *testing.T) {
	f := newApplyFixture(t)
	old := f.missingPath("Year One.m4b")
	id := f.book("Batman: Year One", old)
	row := f.row(id, old)
	store := &failingStore{PebbleStore: f.ps, failRowAfter: -1}
	store.beforeBookCall = func() {
		yes := true
		if _, err := f.ps.ModifyBook(id, func(b *database.Book) error { b.MarkedForDeletion = &yes; b.FilePath = f.newPath; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	res, err := f.apply(store, "op-1", ReconcileApplyItem{BookID: id, NewPath: f.newPath})
	if err != nil || res.Applied != 1 {
		t.Fatalf("err = %v, result %+v", err, res)
	}
	f.requireRepointedRow(id, row)
}

// A book with several rows and one of them at the found file was accepted as
// "the row is already there" with nothing said about its other rows, so a
// multi-file book's path was set to one chapter. It is accepted only when an
// earlier apply's live journal row says that row was repointed from this old path
// to the found file (a stranded match); otherwise the match is refused.
func TestExecuteReconcile_ARowAtTheFoundFileBesideOtherRowsNeedsAnEarlierApplyBehindIt(t *testing.T) {
	t.Run("no earlier apply: refused", func(t *testing.T) {
		f := newApplyFixture(t)
		old := f.missingPath("Year One")
		id := f.book("Batman: Year One", old)
		f.row(id, f.newPath)
		f.row(id, f.missingPath("chapter 2.m4b"))
		before := f.rows(id)

		res, err := f.apply(f.ps, "op-1", ReconcileApplyItem{BookID: id, NewPath: f.newPath})
		if err == nil || res.Applied != 0 || res.Skipped != 1 || len(res.Errors) != 1 || !strings.Contains(res.Errors[0], "no earlier apply") {
			t.Fatalf("err = %v, result %+v, want refused for the missing journal evidence", err, res)
		}
		f.requireUntouched(id, old, before)
		if all, _ := f.ps.GetOperationChanges("op-1"); len(all) != 0 {
			t.Errorf("%d change rows journaled for a refused match", len(all))
		}
	})
	t.Run("a stranded match of a multi-row book is finished", func(t *testing.T) {
		f := newApplyFixture(t)
		// The book's path is its first file; a second row is elsewhere.
		old := f.missingPath("Part 1.m4b")
		id := f.book("Batman: Year One", old)
		first := f.row(id, old)
		second := f.row(id, f.missingPath("Part 2.m4b"))
		item := ReconcileApplyItem{BookID: id, NewPath: f.newPath}
		f.strand("op-1", item)

		res, err := f.apply(f.ps, "op-2", item)
		if err != nil || res.Applied != 1 {
			t.Fatalf("finishing the stranded multi-row match: %v, result %+v", err, res)
		}
		if got := f.bookPath(id); got != f.newPath {
			t.Errorf("book path = %q, want %q", got, f.newPath)
		}
		rows := f.rows(id)
		if len(rows) != 2 {
			t.Fatalf("book has %d rows, want 2", len(rows))
		}
		for _, r := range rows {
			switch r.ID {
			case first.ID:
				if r.FilePath != f.newPath {
					t.Errorf("first row at %q, want %q", r.FilePath, f.newPath)
				}
			case second.ID:
				if r.FilePath != second.FilePath || !r.Missing {
					t.Errorf("second row changed: %+v", r)
				}
			}
		}
		f.requireLive("op-1", 0)
	})
}

// m3: a row another writer gives the matched file between the plan and the row
// write is seen at the write. Without the check the repoint takes the
// single-owner path key from the other book, and two books hold one file.
func TestExecuteReconcile_ARowThatTakesTheFoundFileBeforeTheWriteRefusesTheMatch(t *testing.T) {
	f := newApplyFixture(t)
	old := f.missingPath("Year One.m4b")
	id := f.book("Batman: Year One", old)
	f.row(id, old)
	before := f.rows(id)
	other := f.book("Batman: Knightfall", f.missingPath("kf.m4b"))
	var theirs database.BookFile
	store := &failingStore{PebbleStore: f.ps, failRowAfter: -1}
	store.beforeRowCall = func() {
		// A scan imports the still-untracked file as a new book's row.
		theirs = database.BookFile{BookID: other, FilePath: f.newPath}
		if err := f.ps.CreateBookFile(&theirs); err != nil {
			t.Fatal(err)
		}
	}

	res, err := f.apply(store, "op-1", ReconcileApplyItem{BookID: id, NewPath: f.newPath})
	if err == nil || res.Applied != 0 || res.Skipped != 1 || len(res.Errors) != 1 {
		t.Fatalf("err = %v, result %+v, want one refused match", err, res)
	}
	if !strings.Contains(res.Errors[0], "now held") || !strings.Contains(res.Errors[0], other) {
		t.Errorf("errors = %q, want the new holder named", res.Errors)
	}
	f.requireUntouched(id, old, before)
	f.requireRows("op-1", 2, 0)
	if got := f.atPath(f.newPath); len(got) != 1 || got[0] != other {
		t.Errorf("the found file is held by %v, want only book %s", got, other)
	}
}

// m3, journal half: the repoint row records the row that held the single-owner
// path key before the repoint, as the Missing files fixer's writer does, so a
// revert can hand the key back to exactly that row.
func TestExecuteReconcile_TheRepointRowRecordsTheRowThatHeldThePathKey(t *testing.T) {
	f := newApplyFixture(t)
	old := f.missingPath("Year One.m4b")
	id := f.book("Batman: Year One", old)
	f.row(id, old)
	ghost := &database.BookFile{ID: "ghost-row", BookID: "ghost-book", FilePath: f.newPath}
	store := &failingStore{PebbleStore: f.ps, failRowAfter: -1, keyOwner: ghost}

	// The lookup the holder check uses does not see the ghost, as when the key
	// is held by a row the path lookup no longer lists.
	res, err := f.apply(store, "op-1", ReconcileApplyItem{BookID: id, NewPath: f.newPath})
	if err != nil || res.Applied != 1 {
		t.Fatalf("apply: %v, result %+v", err, res)
	}
	rows := f.journal("op-1")[database.ChangeTypeBookFileRepoint]
	if len(rows) != 1 {
		t.Fatalf("%d repoint rows, want 1", len(rows))
	}
	to, err := database.DecodeBookFileLocation(rows[0].NewValue)
	if err != nil {
		t.Fatal(err)
	}
	if to.KeyOwnerBook != "ghost-book" || to.KeyOwnerRow != "ghost-row" {
		t.Errorf("journaled key owner = %q/%q, want ghost-book/ghost-row", to.KeyOwnerBook, to.KeyOwnerRow)
	}
}

// m4: a match proves the file's content only when it was a hash match. Any other
// match type clears the book's two current-file hashes along with the row's,
// journaled so a revert puts them back; a hash match keeps all three.
func TestExecuteReconcile_OnlyAHashMatchKeepsTheHashes(t *testing.T) {
	for _, matchType := range []string{"filename", "original_hash", ""} {
		t.Run("cleared for "+matchType, func(t *testing.T) {
			f := newApplyFixture(t)
			old := f.missingPath("Year One.m4b")
			id := f.book("Batman: Year One", old)
			row := f.row(id, old)
			setBookHashes(t, f, id, "book-hash", "organized-hash")
			orig := "original-hash"
			if _, err := f.ps.ModifyBook(id, func(b *database.Book) error { b.OriginalFileHash = &orig; return nil }); err != nil {
				t.Fatal(err)
			}

			res, err := f.apply(f.ps, "op-1", ReconcileApplyItem{BookID: id, NewPath: f.newPath, MatchType: matchType})
			if err != nil || res.Applied != 1 {
				t.Fatalf("apply: %v, result %+v", err, res)
			}
			b, _ := f.ps.GetBookByID(id)
			if b.FileHash != nil || b.OrganizedFileHash != nil {
				t.Errorf("book hashes = %v / %v, want both cleared", b.FileHash, b.OrganizedFileHash)
			}
			if str(b.OriginalFileHash) != orig {
				t.Errorf("OriginalFileHash = %q, want it kept (the import-time identity)", str(b.OriginalFileHash))
			}
			f.requireRepointedRow(id, row)
			hashRows := f.journal("op-1")["metadata_update"]
			if len(hashRows) != 2 {
				t.Fatalf("%d hash rows journaled, want 2", len(hashRows))
			}
			f.requireRows("op-1", 4, 4)

			f.revertOp("op-1")
			b, _ = f.ps.GetBookByID(id)
			if b.FilePath != old || str(b.FileHash) != "book-hash" || str(b.OrganizedFileHash) != "organized-hash" {
				t.Errorf("after revert: book %q hash %q organized %q, want all restored", b.FilePath, str(b.FileHash), str(b.OrganizedFileHash))
			}
			if rows := f.rows(id); len(rows) != 1 || rows[0].FilePath != old || rows[0].FileHash != applyOldHash {
				t.Errorf("after revert the row is %+v, want it back with its hash", rows)
			}
		})
	}
	t.Run("kept for hash", func(t *testing.T) {
		f := newApplyFixture(t)
		old := f.missingPath("Year One.m4b")
		id := f.book("Batman: Year One", old)
		row := f.row(id, old)
		setBookHashes(t, f, id, "book-hash", "organized-hash")

		res, err := f.apply(f.ps, "op-1", ReconcileApplyItem{BookID: id, NewPath: f.newPath, MatchType: "hash"})
		if err != nil || res.Applied != 1 {
			t.Fatalf("apply: %v, result %+v", err, res)
		}
		b, _ := f.ps.GetBookByID(id)
		if str(b.FileHash) != "book-hash" || str(b.OrganizedFileHash) != "organized-hash" {
			t.Errorf("book hashes = %q / %q, want both kept", str(b.FileHash), str(b.OrganizedFileHash))
		}
		rows := f.rows(id)
		if len(rows) != 1 || rows[0].ID != row.ID || rows[0].FilePath != f.newPath || rows[0].Missing || rows[0].FileHash != applyOldHash || rows[0].FileSize != f.newBytes {
			t.Errorf("row = %+v, want repointed with its hash kept and the found file's size", rows)
		}
		if n := len(f.journal("op-1")["metadata_update"]); n != 0 {
			t.Errorf("%d hash rows journaled for a hash match, want none", n)
		}
		f.requireRows("op-1", 2, 2)
	})
}

// A book with no file rows and no hashes journals no hash rows: only a hash that
// is there is cleared and journaled.
func TestExecuteReconcile_ABookWithNoHashesJournalsNoHashRows(t *testing.T) {
	f := newApplyFixture(t)
	id := f.book("Batman: Year One", f.missingPath("Year One.m4b"))
	if res, err := f.apply(f.ps, "op-1", ReconcileApplyItem{BookID: id, NewPath: f.newPath, MatchType: "filename"}); err != nil || res.Applied != 1 {
		t.Fatalf("apply: %v, result %+v", err, res)
	}
	f.requireRows("op-1", 1, 1)
}

// A book with no rows still has its hashes cleared by a non-hash match.
func TestExecuteReconcile_ABookWithNoRowsHasItsHashesClearedToo(t *testing.T) {
	f := newApplyFixture(t)
	id := f.book("Batman: Year One", f.missingPath("Year One.m4b"))
	setBookHashes(t, f, id, "book-hash", "organized-hash")
	if res, err := f.apply(f.ps, "op-1", ReconcileApplyItem{BookID: id, NewPath: f.newPath, MatchType: "filename"}); err != nil || res.Applied != 1 {
		t.Fatalf("apply: %v, result %+v", err, res)
	}
	b, _ := f.ps.GetBookByID(id)
	if b.FileHash != nil || b.OrganizedFileHash != nil {
		t.Errorf("book hashes = %v / %v, want both cleared", b.FileHash, b.OrganizedFileHash)
	}
	f.requireRows("op-1", 3, 3)
}

// Hashes another writer changed after the plan are not overwritten: the write
// refuses under the book's lock, the row is put back, and the rows are voided.
func TestExecuteReconcile_HashesChangedUnderTheApplyAreNotCleared(t *testing.T) {
	f := newApplyFixture(t)
	old := f.missingPath("Year One.m4b")
	id := f.book("Batman: Year One", old)
	f.row(id, old)
	setBookHashes(t, f, id, "book-hash", "organized-hash")
	before := f.rows(id)
	store := &failingStore{PebbleStore: f.ps, failRowAfter: -1}
	store.beforeBookCall = func() { setBookHashes(t, f, id, "newer-hash", "organized-hash") }

	res, err := f.apply(store, "op-1", ReconcileApplyItem{BookID: id, NewPath: f.newPath, MatchType: "filename"})
	if err == nil || res.Applied != 0 || res.Skipped != 1 || len(res.Errors) != 1 || !strings.Contains(res.Errors[0], "hashes changed") {
		t.Fatalf("err = %v, result %+v, want refused for the changed hashes", err, res)
	}
	b, _ := f.ps.GetBookByID(id)
	if b.FilePath != old || str(b.FileHash) != "newer-hash" {
		t.Errorf("book = %q hash %q, want it untouched with the other writer's hash", b.FilePath, str(b.FileHash))
	}
	f.requireUntouched(id, old, before)
	f.requireRows("op-1", 4, 0)
}
