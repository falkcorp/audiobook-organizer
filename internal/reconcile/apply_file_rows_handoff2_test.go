// file: internal/reconcile/apply_file_rows_handoff2_test.go
// version: 1.2.0
// guid: c41f8a27-6e3b-4d95-b1a0-7f2d9e5c8b36
// last-edited: 2026-11-19

package reconcile

import (
	"hash/fnv"
	"strings"
	"testing"

	"github.com/oklog/ulid/v2"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// The second follow-up to the apply's review (#4736 round 1): a match whose book
// write does not stage this operation's path or hash change must not leave live
// rows for the change it never made, and the hand-off must key on the book's own
// hash values and on this operation having written.

// anotherWriterRepoints is the writer that sets the book's path (and optionally
// its hashes) to the found file between the apply's plan and its book write.
func anotherWriterRepoints(t *testing.T, f *applyFixture, id string, hash, organized *string) func() {
	return func() {
		if _, err := f.ps.ModifyBook(id, func(b *database.Book) error {
			b.FilePath = f.newPath
			if hash != nil {
				b.FileHash = hash
			}
			if organized != nil {
				b.OrganizedFileHash = organized
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// m1: the book already holds the matched path when the apply writes it (another
// writer got there after the plan), and its hashes are still the ones the plan
// read. The apply's contract for a non-hash match is that the hashes go, so they
// are cleared under the same check as in the write branch; skipping them left the
// hash rows live for a clear that never ran.
func TestExecuteReconcile_ABookAlreadyAtTheFoundFileStillHasItsHashesCleared(t *testing.T) {
	f := newApplyFixture(t)
	old := f.missingPath("Year One.m4b")
	id := f.book("Batman: Year One", old)
	row := f.row(id, old)
	setBookHashes(t, f, id, "book-hash", "organized-hash")
	store := &failingStore{PebbleStore: f.ps, failRowAfter: -1}
	store.beforeBookCall = anotherWriterRepoints(t, f, id, nil, nil)

	res, err := f.apply(store, "op-1", ReconcileApplyItem{BookID: id, NewPath: f.newPath, MatchType: "filename"})
	if err != nil || res.Applied != 1 {
		t.Fatalf("apply: %v, result %+v", err, res)
	}
	b, _ := f.ps.GetBookByID(id)
	if b.FilePath != f.newPath || b.FileHash != nil || b.OrganizedFileHash != nil {
		t.Errorf("book = %q hash %v organized %v, want it at the found file with both hashes cleared", b.FilePath, b.FileHash, b.OrganizedFileHash)
	}
	f.requireRepointedRow(id, row)
	f.requireRows("op-1", 4, 4)

	f.revertOp("op-1")
	b, _ = f.ps.GetBookByID(id)
	if b.FilePath != old || str(b.FileHash) != "book-hash" || str(b.OrganizedFileHash) != "organized-hash" {
		t.Errorf("after revert: book %q hash %q organized %q, want it all back", b.FilePath, str(b.FileHash), str(b.OrganizedFileHash))
	}
}

// m1: the same, but the other writer also changed the hashes. They are not this
// apply's to clear, so the apply clears nothing and takes its two hash rows back;
// the path and the row's repoint stay live (the row did move, and a revert must
// put the book and the row back together).
func TestExecuteReconcile_AHashRowForAClearThatNeverRanIsVoided(t *testing.T) {
	f := newApplyFixture(t)
	old := f.missingPath("Year One.m4b")
	id := f.book("Batman: Year One", old)
	row := f.row(id, old)
	setBookHashes(t, f, id, "book-hash", "organized-hash")
	store := &failingStore{PebbleStore: f.ps, failRowAfter: -1}
	store.beforeBookCall = anotherWriterRepoints(t, f, id, new("newer-hash"), nil)

	res, err := f.apply(store, "op-1", ReconcileApplyItem{BookID: id, NewPath: f.newPath, MatchType: "filename"})
	if err != nil || res.Applied != 1 {
		t.Fatalf("apply: %v, result %+v", err, res)
	}
	b, _ := f.ps.GetBookByID(id)
	if str(b.FileHash) != "newer-hash" || str(b.OrganizedFileHash) != "organized-hash" {
		t.Errorf("book hashes = %q / %q, want the other writer's values untouched", str(b.FileHash), str(b.OrganizedFileHash))
	}
	f.requireRepointedRow(id, row)
	f.requireRows("op-1", 4, 2)

	// Reverting the operation must not restore a hash it never cleared.
	f.revertOp("op-1")
	b, _ = f.ps.GetBookByID(id)
	if str(b.FileHash) != "newer-hash" {
		t.Errorf("reverting the operation set the hash to %q: it undid a clear that never ran", str(b.FileHash))
	}
}

// m1: a book with no file rows whose path another writer set. This operation
// wrote nothing, so no row of it may stay live: a live path row would let a
// revert move the book off the other writer's path.
func TestExecuteReconcile_ABookWithNoRowsRepointedByAnotherWriterLeavesNoLiveRows(t *testing.T) {
	f := newApplyFixture(t)
	id := f.book("Batman: Year One", f.missingPath("Year One.m4b"))
	setBookHashes(t, f, id, "book-hash", "organized-hash")
	store := &failingStore{PebbleStore: f.ps, failRowAfter: -1}
	store.beforeBookCall = anotherWriterRepoints(t, f, id, new("newer-hash"), nil)

	res, err := f.apply(store, "op-1", ReconcileApplyItem{BookID: id, NewPath: f.newPath, MatchType: "filename"})
	if err != nil || res.Applied != 1 {
		t.Fatalf("apply: %v, result %+v", err, res)
	}
	f.requireRows("op-1", 3, 0)
	f.revertOp("op-1")
	if got := f.bookPath(id); got != f.newPath {
		t.Errorf("reverting the operation moved the book to %q: it undid another writer's path", got)
	}
}

// m1: the hand-off marks an earlier operation's rows reverted so THIS operation
// owns the match. When this operation wrote nothing (another writer set the path,
// so the callback skipped) it owns nothing, and the earlier operation's rows stay
// as they were.
func TestExecuteReconcile_ThePathAnotherWriterSetIsNotHandedOffByAnApplyThatWroteNothing(t *testing.T) {
	f := newApplyFixture(t)
	old := f.missingPath("Year One.m4b")
	id := f.book("Batman: Year One", old)
	f.row(id, old)
	item := ReconcileApplyItem{BookID: id, NewPath: f.newPath}
	f.strand("op-1", item)
	f.requireLive("op-1", 2)

	store := &failingStore{PebbleStore: f.ps, failRowAfter: -1}
	store.beforeBookCall = anotherWriterRepoints(t, f, id, nil, nil)
	res, err := f.apply(store, "op-2", item)
	if err != nil || res.Applied != 1 {
		t.Fatalf("op-2: %v, result %+v", err, res)
	}
	f.requireLive("op-1", 2)
	// Its twin of op-1's repoint describes a write op-2 did not make: voided.
	f.requireRows("op-2", 2, 0)
}

// m1: a book write that reported a failure, with the book at the matched path
// and its hashes still the plan's: the write did not land (a landed write cleared
// them), so another writer set the path, and the hash rows are taken back.
func TestExecuteReconcile_AReadBackBookTheApplyDidNotWriteVoidsItsHashRows(t *testing.T) {
	f := newApplyFixture(t)
	old := f.missingPath("Year One.m4b")
	id := f.book("Batman: Year One", old)
	row := f.row(id, old)
	setBookHashes(t, f, id, "book-hash", "organized-hash")
	store := &failingStore{PebbleStore: f.ps, failBook: true, failRowAfter: -1}
	store.beforeBookCall = anotherWriterRepoints(t, f, id, nil, nil)

	res, err := f.apply(store, "op-1", ReconcileApplyItem{BookID: id, NewPath: f.newPath, MatchType: "filename"})
	if err != nil || res.Applied != 1 {
		t.Fatalf("apply: %v, result %+v", err, res)
	}
	b, _ := f.ps.GetBookByID(id)
	if str(b.FileHash) != "book-hash" {
		t.Errorf("book hash = %q, want it untouched: the write did not land", str(b.FileHash))
	}
	f.requireRepointedRow(id, row)
	f.requireRows("op-1", 4, 2)
}

// m1: a book already at the matched file when the plan reads it (a match
// resubmitted after it applied) has nothing to apply: nothing is journaled,
// nothing is written, and the hashes the book holds now are not cleared.
func TestExecuteReconcile_ABookAlreadyAtTheFoundFileAtThePlanWritesNothing(t *testing.T) {
	f := newApplyFixture(t)
	id := f.book("Batman: Year One", f.newPath)
	row := database.BookFile{BookID: id, FilePath: f.newPath, FileSize: f.newBytes}
	if err := f.ps.CreateBookFile(&row); err != nil {
		t.Fatal(err)
	}
	setBookHashes(t, f, id, "book-hash", "organized-hash")

	res, err := f.apply(f.ps, "op-1", ReconcileApplyItem{BookID: id, NewPath: f.newPath, MatchType: "filename"})
	if err != nil || res.Applied != 1 {
		t.Fatalf("apply: %v, result %+v", err, res)
	}
	b, _ := f.ps.GetBookByID(id)
	if str(b.FileHash) != "book-hash" || str(b.OrganizedFileHash) != "organized-hash" {
		t.Errorf("book hashes = %q / %q, want them kept: nothing was applied", str(b.FileHash), str(b.OrganizedFileHash))
	}
	f.requireRows("op-1", 0, 0)
}

// m1: a write that landed but reported a failure, proven by the book's hashes
// being cleared, is this operation's write: the hand-off still runs.
func TestExecuteReconcile_ALandedWriteProvenByClearedHashesStillHandsOff(t *testing.T) {
	f := newApplyFixture(t)
	old := f.missingPath("Year One.m4b")
	id := f.book("Batman: Year One", old)
	f.row(id, old)
	setBookHashes(t, f, id, "book-hash", "organized-hash")
	item := ReconcileApplyItem{BookID: id, NewPath: f.newPath, MatchType: "filename"}
	f.strand("op-1", item)

	store := &failingStore{PebbleStore: f.ps, bookLandsThenErr: true, failRowAfter: -1}
	res, err := f.apply(store, "op-2", item)
	if err != nil || res.Applied != 1 {
		t.Fatalf("op-2: %v, result %+v", err, res)
	}
	f.requireLive("op-1", 0)
}

// m2: op-1 strands a FILENAME apply (its hash clears are live, never written);
// op-2 finishes the match as a HASH match, which keeps the hashes. The hand-off
// selected hash rows by what op-2 cleared (nothing), so op-1's hash rows stayed
// live. They are selected by the book's own hash values: a live clear of a value
// the book still holds never landed.
func TestExecuteReconcile_AHashMatchFinishHandsOffTheEarlierHashClears(t *testing.T) {
	f := newApplyFixture(t)
	old := f.missingPath("Year One.m4b")
	id := f.book("Batman: Year One", old)
	row := f.row(id, old)
	setBookHashes(t, f, id, "book-hash", "organized-hash")
	filename := ReconcileApplyItem{BookID: id, NewPath: f.newPath, MatchType: "filename"}
	f.strand("op-1", filename)
	f.requireRows("op-1", 4, 4)

	if res, err := f.apply(f.ps, "op-2", ReconcileApplyItem{BookID: id, NewPath: f.newPath, MatchType: "hash"}); err != nil || res.Applied != 1 {
		t.Fatalf("op-2: %v, result %+v", err, res)
	}
	f.requireLive("op-1", 0)

	// The review's sequence: revert op-2, apply op-3 as a filename match, revert
	// op-1. Op-1's hash rows used to restore the hashes op-3 had cleared.
	f.revertOp("op-2")
	if res, err := f.apply(f.ps, "op-3", filename); err != nil || res.Applied != 1 {
		t.Fatalf("op-3: %v, result %+v", err, res)
	}
	f.requireRepointedRow(id, row)
	f.revertOp("op-1")
	b, _ := f.ps.GetBookByID(id)
	if b.FilePath != f.newPath || b.FileHash != nil || b.OrganizedFileHash != nil {
		t.Errorf("reverting op-1 undid op-3: book %q hash %v organized %v", b.FilePath, b.FileHash, b.OrganizedFileHash)
	}
}

// m2: the hash a hash-match finish sees is the one in the book under its lock.
// A hash another writer set after the plan is not op-1's cleared value, so op-1's
// row for the OLD value is not handed off by a stale plan-time read.
func TestExecuteReconcile_AHashMatchFinishKeysTheHandOffOnTheHashUnderTheLock(t *testing.T) {
	f := newApplyFixture(t)
	old := f.missingPath("Year One.m4b")
	id := f.book("Batman: Year One", old)
	f.row(id, old)
	setBookHashes(t, f, id, "book-hash", "organized-hash")
	f.strand("op-1", ReconcileApplyItem{BookID: id, NewPath: f.newPath, MatchType: "filename"})
	f.requireRows("op-1", 4, 4)

	store := &failingStore{PebbleStore: f.ps, failRowAfter: -1}
	store.beforeBookCall = func() { setBookHashes(t, f, id, "newer-hash", "organized-hash") }
	if res, err := f.apply(store, "op-2", ReconcileApplyItem{BookID: id, NewPath: f.newPath, MatchType: "hash"}); err != nil || res.Applied != 1 {
		t.Fatalf("op-2: %v, result %+v", err, res)
	}
	// Only the organized-hash clear (the value still held) and the path and
	// repoint rows are op-1's to hand over; its file_hash clear names a value the
	// book no longer holds and stays as it was.
	f.requireLive("op-1", 1)
}

// n2: every row the apply journals names itself, because the twin and the
// hand-off select rows by that Source (the journal outlives its operation row, so
// the op row cannot say who wrote a row).
func TestExecuteReconcile_EveryJournalRowCarriesTheApplySource(t *testing.T) {
	f := newApplyFixture(t)
	old := f.missingPath("Year One.m4b")
	id := f.book("Batman: Year One", old)
	f.row(id, old)
	setBookHashes(t, f, id, "book-hash", "organized-hash")
	if res, err := f.apply(f.ps, "op-1", ReconcileApplyItem{BookID: id, NewPath: f.newPath, MatchType: "filename"}); err != nil || res.Applied != 1 {
		t.Fatalf("apply: %v, result %+v", err, res)
	}
	all, err := f.ps.GetOperationChanges("op-1")
	if err != nil || len(all) != 4 {
		t.Fatalf("%d rows, %v, want 4", len(all), err)
	}
	for _, c := range all {
		if c.Source != applySource {
			t.Errorf("%s row %s has Source %q, want %q", c.ChangeType, c.ID, c.Source, applySource)
		}
	}
}

// n2: a row with NO Source is not the apply's: fs_regroup_xml journals
// book_path_update with none. A Source-less repoint and path change for the exact
// move of a stranded match are neither twinned nor handed off.
func TestExecuteReconcile_ASourcelessRowForTheSameMoveIsNotTheApplys(t *testing.T) {
	f := newApplyFixture(t)
	old := f.missingPath("Year One.m4b")
	id := f.book("Batman: Year One", old)
	f.row(id, old)
	item := ReconcileApplyItem{BookID: id, NewPath: f.newPath}
	f.strand("op-1", item)
	for _, ch := range f.journal("op-1") {
		for _, c := range ch {
			v := *c
			v.Source = ""
			if err := f.ps.CreateOperationChange(&v); err != nil {
				t.Fatal(err)
			}
		}
	}

	if res, err := f.apply(f.ps, "op-2", item); err != nil || res.Applied != 1 {
		t.Fatalf("op-2: %v, result %+v", err, res)
	}
	if n := len(f.journal("op-2")[database.ChangeTypeBookFileRepoint]); n != 0 {
		t.Errorf("the rerun twinned %d Source-less repoint rows, want none", n)
	}
	f.requireLive("op-1", 2)
}

// unreadableJournalStore is a store whose journal cannot be read: GetBookChanges fails.
type unreadableJournalStore struct{ *failingStore }

func (unreadableJournalStore) GetBookChanges(string) ([]*database.OperationChange, error) {
	return nil, errInjected
}

// n5: a stranded match whose journal cannot be read is refused at the plan, with
// nothing written: finishing it without the twin and the hand-off would leave a
// revert of the finish putting the book back and the row at the matched file.
func TestExecuteReconcile_AStrandedMatchWithAnUnreadableJournalIsRefusedWithNothingWritten(t *testing.T) {
	f := newApplyFixture(t)
	old := f.missingPath("Year One.m4b")
	id := f.book("Batman: Year One", old)
	row := f.row(id, old)
	item := ReconcileApplyItem{BookID: id, NewPath: f.newPath}
	f.strand("op-1", item)

	store := unreadableJournalStore{&failingStore{PebbleStore: f.ps, failRowAfter: -1}}
	res, err := f.apply(store, "op-2", item)
	if err == nil || res.Applied != 0 || res.Skipped != 1 || len(res.Errors) != 1 {
		t.Fatalf("op-2: %v, result %+v, want one refused match", err, res)
	}
	if e := res.Errors[0]; !strings.Contains(e, "nothing written") || !strings.Contains(e, "journal cannot be read") {
		t.Errorf("errors = %q, want the unreadable journal named and nothing written", res.Errors)
	}
	if got := f.bookPath(id); got != old {
		t.Errorf("the book moved to %q, want it left at %q", got, old)
	}
	f.requireRows("op-2", 0, 0)
	f.requireLive("op-1", 2)
	if rows := f.rows(id); len(rows) != 1 || rows[0].ID != row.ID || rows[0].FilePath != f.newPath {
		t.Errorf("the row is %+v, want it left where op-1 stranded it", rows)
	}
}

// n5: a store that cannot read a journal at all is refused the same way.
func TestExecuteReconcile_AStrandedMatchOnAStoreWithNoJournalReadIsRefused(t *testing.T) {
	f := newApplyFixture(t)
	old := f.missingPath("Year One.m4b")
	id := f.book("Batman: Year One", old)
	f.row(id, old)
	item := ReconcileApplyItem{BookID: id, NewPath: f.newPath}
	f.strand("op-1", item)

	// Only Store's methods are promoted: no GetBookChanges.
	res, err := f.apply(struct{ Store }{f.ps}, "op-2", item)
	if err == nil || res.Applied != 0 || res.Skipped != 1 || len(res.Errors) != 1 || !strings.Contains(res.Errors[0], "cannot read") {
		t.Fatalf("op-2: %v, result %+v, want the store refused for the journal it cannot read", err, res)
	}
	f.requireRows("op-2", 0, 0)
}

// n4: the match type decides whether the book keeps its hashes, so a value the
// scan never produces is refused instead of applied as an unproven match.
func TestValidateApplyItems(t *testing.T) {
	for _, ok := range []string{"", "hash", "original_hash", "filename"} {
		if err := ValidateApplyItems([]ReconcileApplyItem{{BookID: "b", MatchType: ok}}); err != nil {
			t.Errorf("match_type %q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"Hash", "hash ", "sha256", "filename,hash"} {
		err := ValidateApplyItems([]ReconcileApplyItem{{BookID: "b1", MatchType: "hash"}, {BookID: "b2", MatchType: bad}})
		if err == nil || !strings.Contains(err.Error(), "match 1 (book b2)") || !strings.Contains(err.Error(), "unknown match_type") {
			t.Errorf("match_type %q: err = %v, want the second match named as unknown", bad, err)
		}
	}
}

// n4: a queued set that never passed the HTTP check still cannot clear a hash
// match's hashes by a typo: the apply refuses the match with nothing written.
func TestExecuteReconcile_AnUnknownMatchTypeIsRefusedWithNothingWritten(t *testing.T) {
	f := newApplyFixture(t)
	old := f.missingPath("Year One.m4b")
	id := f.book("Batman: Year One", old)
	f.row(id, old)
	setBookHashes(t, f, id, "book-hash", "organized-hash")
	before := f.rows(id)

	res, err := f.apply(f.ps, "op-1", ReconcileApplyItem{BookID: id, NewPath: f.newPath, MatchType: "Hash"})
	if err == nil || res.Applied != 0 || res.Skipped != 1 || len(res.Errors) != 1 || !strings.Contains(res.Errors[0], "unknown match_type") {
		t.Fatalf("err = %v, result %+v, want the match refused for its type", err, res)
	}
	f.requireUntouched(id, old, before)
	f.requireRows("op-1", 0, 0)
	if b, _ := f.ps.GetBookByID(id); strOf(b.FileHash) != "book-hash" || strOf(b.OrganizedFileHash) != "organized-hash" {
		t.Errorf("hashes = %q/%q, want them kept", strOf(b.FileHash), strOf(b.OrganizedFileHash))
	}
}

// intrudingStore creates another book's row at path from INSIDE the row write's
// callback, after the callback's own checks passed and before the commit: a window
// no read in the callback can see. Both row writes intrude, so a repoint that does
// not use the store's atomic variant is exposed instead of skipped.
type intrudingStore struct {
	*failingStore
	t        *testing.T
	ps       *database.PebbleStore
	bookID   string
	path     string
	intruded database.BookFile
	fired    bool
}

func (s *intrudingStore) wrap(fn func(*database.BookFile) error) func(*database.BookFile) error {
	return func(f *database.BookFile) error {
		if err := fn(f); err != nil {
			return err
		}
		if !s.fired {
			s.fired = true
			s.intruded = database.BookFile{ID: otherStripeULID(f.ID), BookID: s.bookID, FilePath: s.path, Format: "m4b"}
			if err := s.ps.CreateBookFile(&s.intruded); err != nil {
				s.t.Fatal(err)
			}
		}
		return nil
	}
}

func (s *intrudingStore) ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error) {
	return s.failingStore.ModifyBookFile(bookID, fileID, s.wrap(fn))
}

func (s *intrudingStore) ModifyBookFileUnlessPathHeld(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error) {
	return s.failingStore.ModifyBookFileUnlessPathHeld(bookID, fileID, s.wrap(fn))
}

// otherStripeULID is a fresh id on a different book_file lock stripe than id: the
// intruder is created while the row's stripe is held, and the same stripe would
// deadlock the test. The store has 256 stripes keyed by FNV-1a (a different count
// would only change how rarely a retry is needed).
func otherStripeULID(id string) string {
	stripe := func(s string) uint32 { h := fnv.New32a(); _, _ = h.Write([]byte(s)); return h.Sum32() % 256 }
	for {
		c := ulid.Make().String()
		if stripe(c) != stripe(id) {
			return c
		}
	}
}

// m3: the repoint refuses atomically, at the commit, when a row takes the matched
// file after the callback's own holder check passed. The check inside the callback
// cannot see that row (it is created after it); only the store's commit-time
// re-check under the single-owner index mutex does. The match is refused with its
// rows voided, the row stays at the old path, and the intruder keeps the file.
func TestExecuteReconcile_ARowThatTakesTheFoundFileInsideTheRowWriteRefusesTheMatch(t *testing.T) {
	f := newApplyFixture(t)
	old := f.missingPath("Year One.m4b")
	id := f.book("Batman: Year One", old)
	f.row(id, old)
	before := f.rows(id)
	other := f.book("Batman: Knightfall", f.missingPath("kf.m4b"))
	store := &intrudingStore{failingStore: &failingStore{PebbleStore: f.ps, failRowAfter: -1}, t: t, ps: f.ps, bookID: other, path: f.newPath}

	res, err := f.apply(store, "op-1", ReconcileApplyItem{BookID: id, NewPath: f.newPath})
	if err == nil || res.Applied != 0 || res.Skipped != 1 || len(res.Errors) != 1 {
		t.Fatalf("err = %v, result %+v, want one refused match", err, res)
	}
	if !strings.Contains(res.Errors[0], "now held") {
		t.Errorf("errors = %q, want the refusal to say the file is now held", res.Errors)
	}
	f.requireUntouched(id, old, before)
	f.requireRows("op-1", 2, 0)
	if got := f.atPath(f.newPath); len(got) != 1 || got[0] != other {
		t.Errorf("the found file is held by %v, want only book %s", got, other)
	}
}
