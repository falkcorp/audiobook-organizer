// file: internal/reconcile/apply_file_rows_test.go
// version: 1.0.0
// guid: c2e84b17-5d93-4a06-b8f1-7e3a90d4c625
// last-edited: 2026-11-18

package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// ExecuteReconcile used to repoint the book row alone: the book's book_file
// row kept the old path (still flagged Missing) and the matched file stayed
// unowned in the path index. These tests run the apply on a REAL PebbleStore,
// because what they pin is the store's: the row compare-and-set, the
// single-owner path index (BookFilesAtPath), the journal rows the revert reads.

const applyOldHash = "hash-of-the-old-file"

// applyFixture is one Gotham library: a root with the matched file on disk, and
// a store holding books whose rows sit at paths that no longer exist.
type applyFixture struct {
	t        *testing.T
	ps       *database.PebbleStore
	dir      string
	newPath  string
	newBytes int64
}

func newApplyFixture(t *testing.T) *applyFixture {
	t.Helper()
	ps, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("new pebble store: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	ps.WaitForWarmup()
	dir := t.TempDir()
	f := &applyFixture{t: t, ps: ps, dir: dir}
	f.newPath = f.file("found/Batman Year One.m4b", "the found audio, a few bytes more than the old one")
	st, err := os.Stat(f.newPath)
	if err != nil {
		t.Fatal(err)
	}
	f.newBytes = st.Size()
	return f
}

func (f *applyFixture) file(rel, content string) string {
	f.t.Helper()
	p := filepath.Join(f.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
	return p
}

// missingPath is a path that is not on disk.
func (f *applyFixture) missingPath(name string) string {
	return filepath.Join(f.dir, "gone", name)
}

// book creates a book pointing at path, and returns its id.
func (f *applyFixture) book(title, path string) string {
	f.t.Helper()
	b, err := f.ps.CreateBook(&database.Book{Title: title, FilePath: path})
	if err != nil {
		f.t.Fatal(err)
	}
	return b.ID
}

// row creates a file row of bookID at path, flagged missing, with the old
// file's hash and a size that is not the found file's.
func (f *applyFixture) row(bookID, path string) database.BookFile {
	f.t.Helper()
	r := database.BookFile{BookID: bookID, FilePath: path, Missing: true, FileHash: applyOldHash, FileSize: 7}
	if err := f.ps.CreateBookFile(&r); err != nil {
		f.t.Fatal(err)
	}
	return r
}

func (f *applyFixture) rows(bookID string) []database.BookFile {
	f.t.Helper()
	rows, err := f.ps.GetBookFiles(bookID)
	if err != nil {
		f.t.Fatal(err)
	}
	return rows
}

func (f *applyFixture) bookPath(bookID string) string {
	f.t.Helper()
	b, err := f.ps.GetBookByID(bookID)
	if err != nil || b == nil {
		f.t.Fatalf("book %s: %v", bookID, err)
	}
	return b.FilePath
}

// apply runs ExecuteReconcile on store for matches under operationID and
// returns its error and the result it saved.
func (f *applyFixture) apply(store Store, operationID string, matches ...ReconcileApplyItem) (error, ReconcileApplyResult) {
	f.t.Helper()
	var got ReconcileApplyResult
	save := func(payload any) error {
		raw, _ := payload.(json.RawMessage)
		return json.Unmarshal(raw, &got)
	}
	err := ExecuteReconcile(context.Background(), store, operationID, save, matches, logger.New("reconcile-test"))
	return err, got
}

// journal is the operation's change rows by type.
func (f *applyFixture) journal(operationID string) map[string][]*database.OperationChange {
	f.t.Helper()
	changes, err := f.ps.GetOperationChanges(operationID)
	if err != nil {
		f.t.Fatal(err)
	}
	out := map[string][]*database.OperationChange{}
	for _, c := range changes {
		out[c.ChangeType] = append(out[c.ChangeType], c)
	}
	return out
}

// requireUntouched fails unless the book and its rows are exactly as they were
// created: the match was refused before anything was written.
func (f *applyFixture) requireUntouched(bookID, oldPath string, rowsBefore []database.BookFile) {
	f.t.Helper()
	if got := f.bookPath(bookID); got != oldPath {
		f.t.Errorf("the book moved to %q, want it left at %q", got, oldPath)
	}
	after := f.rows(bookID)
	if len(after) != len(rowsBefore) {
		f.t.Fatalf("the book has %d file rows, want %d", len(after), len(rowsBefore))
	}
	for i := range after {
		if after[i].ID != rowsBefore[i].ID || after[i].FilePath != rowsBefore[i].FilePath ||
			after[i].Missing != rowsBefore[i].Missing || after[i].FileHash != rowsBefore[i].FileHash ||
			after[i].FileSize != rowsBefore[i].FileSize {
			f.t.Errorf("file row %s changed: %+v, want %+v", rowsBefore[i].ID, after[i], rowsBefore[i])
		}
	}
}

// requireRepointedRow fails unless the book's one row is the original row (same
// id), now at the found file, no longer missing, sized as the file, hash cleared.
func (f *applyFixture) requireRepointedRow(bookID string, original database.BookFile) {
	f.t.Helper()
	rows := f.rows(bookID)
	if len(rows) != 1 {
		f.t.Fatalf("the book has %d file rows, want its one row repointed, not recreated or deleted", len(rows))
	}
	r := rows[0]
	if r.ID != original.ID {
		t := f.t
		t.Errorf("the row is %s, want the original %s (repointed, never replaced)", r.ID, original.ID)
	}
	if r.FilePath != f.newPath || r.Missing || r.FileSize != f.newBytes || r.FileHash != "" {
		f.t.Errorf("row = path %q missing=%t size=%d hash=%q, want path %q missing=false size=%d hash cleared",
			r.FilePath, r.Missing, r.FileSize, r.FileHash, f.newPath, f.newBytes)
	}
}

// atPath is the ids of the books holding a row at path, as the path index
// answers.
func (f *applyFixture) atPath(path string) []string {
	f.t.Helper()
	rows, err := f.ps.BookFilesAtPath(path)
	if err != nil {
		f.t.Fatal(err)
	}
	var ids []string
	for _, r := range rows {
		ids = append(ids, r.BookID)
	}
	return ids
}

func TestExecuteReconcile_RepointsTheBookFileRowWithTheBook(t *testing.T) {
	f := newApplyFixture(t)
	old := f.missingPath("Year One.m4b")
	id := f.book("Batman: Year One", old)
	row := f.row(id, old)

	err, res := f.apply(f.ps, "op-repoint", ReconcileApplyItem{BookID: id, NewPath: f.newPath})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.Applied != 1 || res.Skipped != 0 {
		t.Fatalf("result = %+v, want 1 applied", res)
	}
	if got := f.bookPath(id); got != f.newPath {
		t.Errorf("book path = %q, want %q", got, f.newPath)
	}
	f.requireRepointedRow(id, row)
	// The path index follows the row: the found file is the book's, the old
	// path is nobody's.
	if got := f.atPath(f.newPath); len(got) != 1 || got[0] != id {
		t.Errorf("the found file is held by %v, want [%s]", got, id)
	}
	if got := f.atPath(old); len(got) != 0 {
		t.Errorf("the old path is still held by %v", got)
	}

	// The journal: the book's path change and the row's repoint, under the
	// operation, so reverting the operation restores both.
	j := f.journal("op-repoint")
	if len(j[undo.ChangeTypeBookPathUpdate]) != 1 || len(j[database.ChangeTypeBookFileRepoint]) != 1 {
		t.Fatalf("journal = %d book_path_update, %d repoint rows, want 1 and 1", len(j[undo.ChangeTypeBookPathUpdate]), len(j[database.ChangeTypeBookFileRepoint]))
	}
	rc := j[database.ChangeTypeBookFileRepoint][0]
	if rc.BookID != id || rc.FieldName != "book_file:"+row.ID {
		t.Errorf("repoint row names book %q field %q, want %q and %q", rc.BookID, rc.FieldName, id, "book_file:"+row.ID)
	}
	was, derr := database.DecodeBookFileLocation(rc.OldValue)
	to, derr2 := database.DecodeBookFileLocation(rc.NewValue)
	if derr != nil || derr2 != nil {
		t.Fatalf("the repoint row does not decode: %v %v", derr, derr2)
	}
	if was.Path != old || !was.Missing || was.Hash != applyOldHash || to.Path != f.newPath || to.Missing {
		t.Errorf("repoint row was=%+v to=%+v, want the old location (missing, old hash) and the found file", was, to)
	}
	// Reverting the operation restores the book AND its row together: both
	// journal rows are ones the revert engine can reverse (a book_path_update
	// and a book_file_repoint_location).
	all, err := f.ps.GetOperationChanges("op-repoint")
	if err != nil {
		t.Fatal(err)
	}
	report, err := undo.PreflightUndoConflictsFromChanges(f.ps, all)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if report.Safe != 2 || report.NotRestorable != 0 || len(report.CheckFailed) != 0 || len(report.ContentChanged) != 0 {
		t.Errorf("preflight of the revert: %+v, want both rows safe and nothing refused or not restorable", report)
	}
	if _, err := audiobooks.NewRevertService(f.ps).RevertOperation("op-repoint"); err != nil {
		t.Fatalf("revert: %v", err)
	}
	if got := f.bookPath(id); got != old {
		t.Errorf("after the revert the book is at %q, want %q", got, old)
	}
	reverted := f.rows(id)
	if len(reverted) != 1 || reverted[0].ID != row.ID || reverted[0].FilePath != old || !reverted[0].Missing || reverted[0].FileHash != applyOldHash {
		t.Errorf("after the revert the row is %+v, want the original %+v back", reverted, row)
	}
}

func TestExecuteReconcile_ABookWithNoFileRowsMovesAlone(t *testing.T) {
	f := newApplyFixture(t)
	id := f.book("Batman: The Long Halloween", f.missingPath("Long Halloween.m4b"))

	err, res := f.apply(f.ps, "op-norows", ReconcileApplyItem{BookID: id, NewPath: f.newPath})
	if err != nil || res.Applied != 1 {
		t.Fatalf("apply: %v, result %+v", err, res)
	}
	if got := f.bookPath(id); got != f.newPath {
		t.Errorf("book path = %q, want %q", got, f.newPath)
	}
	if rows := f.rows(id); len(rows) != 0 {
		t.Errorf("the book gained file rows: %+v", rows)
	}
	if n := len(f.journal("op-norows")[database.ChangeTypeBookFileRepoint]); n != 0 {
		t.Errorf("%d repoint rows journaled for a book with no rows", n)
	}
}

// A row already at the found file (an earlier apply that stopped, or the
// organizer, wrote the row and not the book) is not written again: the book
// moves to it, and the journal holds the book's change only.
func TestExecuteReconcile_ARowAlreadyAtTheFoundFileIsLeftAndTheBookMoves(t *testing.T) {
	f := newApplyFixture(t)
	id := f.book("Batman: Dark Victory", f.missingPath("Dark Victory.m4b"))
	row := f.row(id, f.newPath)

	err, res := f.apply(f.ps, "op-rowfirst", ReconcileApplyItem{BookID: id, NewPath: f.newPath})
	if err != nil || res.Applied != 1 {
		t.Fatalf("apply: %v, result %+v", err, res)
	}
	if got := f.bookPath(id); got != f.newPath {
		t.Errorf("book path = %q, want %q", got, f.newPath)
	}
	rows := f.rows(id)
	if len(rows) != 1 || rows[0].ID != row.ID || rows[0].FilePath != f.newPath || rows[0].FileHash != applyOldHash || !rows[0].Missing {
		t.Errorf("the row was rewritten: %+v, want it untouched %+v", rows, row)
	}
	if n := len(f.journal("op-rowfirst")[database.ChangeTypeBookFileRepoint]); n != 0 {
		t.Errorf("%d repoint rows journaled for a row that did not move", n)
	}
}

// Every refusal is decided before the first write: nothing is journaled, the
// book and its rows are exactly as they were, the match counts as skipped and
// the op reports the error.
func TestExecuteReconcile_RefusesAMatchItCannotRepointCleanly(t *testing.T) {
	type setup func(f *applyFixture, old string) (bookID string)
	cases := []struct {
		name string
		set  setup
		want string
	}{
		{"two rows at the old path", func(f *applyFixture, old string) string {
			id := f.book("Batman: Hush", old)
			f.row(id, old)
			f.row(id, old)
			return id
		}, "not exactly one row to repoint"},
		{"rows that are neither at the old path nor at the found file", func(f *applyFixture, old string) string {
			id := f.book("Batman: Hush", old)
			f.row(id, f.missingPath("elsewhere.m4b"))
			return id
		}, "not exactly one row to repoint"},
		{"a row at the old path and another at the found file", func(f *applyFixture, old string) string {
			id := f.book("Batman: Hush", old)
			f.row(id, old)
			f.row(id, f.newPath)
			return id
		}, "not exactly one row to repoint"},
		{"another book's row holds the found file", func(f *applyFixture, old string) string {
			id := f.book("Batman: Hush", old)
			f.row(id, old)
			other := f.book("Batman: Knightfall", f.newPath)
			f.row(other, f.newPath)
			return id
		}, "is held by a file row of book"},
		{"a retired row claims the found file", func(f *applyFixture, old string) string {
			id := f.book("Batman: Hush", old)
			f.row(id, old)
			holder := f.book("Batman: Knightfall", f.missingPath("kf.m4b"))
			shell := f.book("Batman: No Mans Land", f.missingPath("nml.m4b"))
			now := time.Now()
			v, err := f.ps.CreateBookVersion(&database.BookVersion{
				BookID: holder, Status: database.BookVersionStatusRetired, Format: "m4b", Source: "retired_book",
				RetiredFromBookID: shell, RetireOpID: "op-invented", RetiredAt: &now,
			})
			if err != nil {
				f.t.Fatal(err)
			}
			rr := database.BookFile{BookID: shell, FilePath: f.newPath}
			if err := f.ps.CreateBookFile(&rr); err != nil {
				f.t.Fatal(err)
			}
			if err := f.ps.RetireBookFiles([]database.BookFileMove{{FileIDs: []string{rr.ID}, SourceBookID: shell}}, holder, v.ID); err != nil {
				f.t.Fatal(err)
			}
			return id
		}, "is held by a file row of book"},
		{"the book itself holds a retired row on the found file", func(f *applyFixture, old string) string {
			// The retired row is re-keyed onto this very book, so its owner
			// check passes and only the retired-claim check can refuse.
			id := f.book("Batman: Hush", old)
			f.row(id, old)
			shell := f.book("Batman: No Mans Land", f.missingPath("nml.m4b"))
			now := time.Now()
			v, err := f.ps.CreateBookVersion(&database.BookVersion{
				BookID: id, Status: database.BookVersionStatusRetired, Format: "m4b", Source: "retired_book",
				RetiredFromBookID: shell, RetireOpID: "op-invented", RetiredAt: &now,
			})
			if err != nil {
				f.t.Fatal(err)
			}
			rr := database.BookFile{BookID: shell, FilePath: f.newPath}
			if err := f.ps.CreateBookFile(&rr); err != nil {
				f.t.Fatal(err)
			}
			if err := f.ps.RetireBookFiles([]database.BookFileMove{{FileIDs: []string{rr.ID}, SourceBookID: shell}}, id, v.ID); err != nil {
				f.t.Fatal(err)
			}
			return id
		}, "is claimed by a retired file row"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newApplyFixture(t)
			old := f.missingPath("Hush.m4b")
			id := tc.set(f, old)
			before := f.rows(id)

			err, res := f.apply(f.ps, "op-refused", ReconcileApplyItem{BookID: id, NewPath: f.newPath})
			if err == nil || res.Applied != 0 || res.Skipped != 1 {
				t.Fatalf("err = %v, result %+v, want an error and 0 applied, 1 skipped", err, res)
			}
			if len(res.Errors) != 1 || !strings.Contains(res.Errors[0], tc.want) || !strings.Contains(res.Errors[0], "nothing written") {
				t.Errorf("errors = %q, want one naming %q and that nothing was written", res.Errors, tc.want)
			}
			f.requireUntouched(id, old, before)
			if all, _ := f.ps.GetOperationChanges("op-refused"); len(all) != 0 {
				t.Errorf("%d change rows journaled for a refused match", len(all))
			}
		})
	}
}

// A store that cannot rewrite file rows refuses a book that has some, instead
// of moving the book alone and leaving its row at the old path.
func TestExecuteReconcile_StoreWithoutFileRowWritesRefusesABookWithRows(t *testing.T) {
	f := newApplyFixture(t)
	old := f.missingPath("Year Two.m4b")
	id := f.book("Batman: Year Two", old)
	f.row(id, old)
	before := f.rows(id)

	// Only Store's methods are promoted: no ModifyBookFile, no BookFilesAtPath.
	narrow := struct{ Store }{f.ps}
	err, res := f.apply(narrow, "op-narrow", ReconcileApplyItem{BookID: id, NewPath: f.newPath})
	if err == nil || res.Applied != 0 || res.Skipped != 1 {
		t.Fatalf("err = %v, result %+v, want refused", err, res)
	}
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0], "cannot look up or rewrite file rows") {
		t.Errorf("errors = %q, want the missing capability named", res.Errors)
	}
	f.requireUntouched(id, old, before)
}

// failingStore is the real store with faults on the writes of one apply.
type failingStore struct {
	*database.PebbleStore
	mu sync.Mutex
	// failBook fails every ModifyBook. failRowAfter lets that many
	// ModifyBookFile calls through and fails the rest (-1: none fail).
	failBook     bool
	failRowAfter int
	rowCalls     int
	// beforeRowCall, when set, runs before the first ModifyBookFile call:
	// another writer changing the row between the apply's read and its write.
	beforeRowCall func()
	// beforeBookCall, when set, runs before the first ModifyBook call: another
	// writer changing the book between the apply's read and its write.
	beforeBookCall func()
	bookCalls      int
}

var errInjected = errors.New("injected store failure")

func (s *failingStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	s.mu.Lock()
	fail := s.failBook
	call := s.bookCalls
	s.bookCalls++
	hook := s.beforeBookCall
	s.mu.Unlock()
	if call == 0 && hook != nil {
		hook()
	}
	if fail {
		return nil, errInjected
	}
	return s.PebbleStore.ModifyBook(id, fn)
}

func (s *failingStore) ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error) {
	s.mu.Lock()
	call := s.rowCalls
	s.rowCalls++
	hook := s.beforeRowCall
	after := s.failRowAfter
	s.mu.Unlock()
	if call == 0 && hook != nil {
		hook()
	}
	if after >= 0 && call >= after {
		return nil, errInjected
	}
	return s.PebbleStore.ModifyBookFile(bookID, fileID, fn)
}

func (s *failingStore) heal() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failBook, s.failRowAfter, s.beforeRowCall = false, -1, nil
}

// The row is written first. If the book's write then fails, the row is put back
// whole (path, Missing, hash, size) so the match is all or nothing: the book is
// not applied, the error says the row was restored, and the match applies
// cleanly once the fault is gone.
func TestExecuteReconcile_BookWriteFailureRestoresTheRowAndARerunApplies(t *testing.T) {
	f := newApplyFixture(t)
	old := f.missingPath("Year One.m4b")
	id := f.book("Batman: Year One", old)
	row := f.row(id, old)
	before := f.rows(id)
	store := &failingStore{PebbleStore: f.ps, failBook: true, failRowAfter: -1}

	err, res := f.apply(store, "op-1", ReconcileApplyItem{BookID: id, NewPath: f.newPath})
	if err == nil || res.Applied != 0 || res.Skipped != 1 {
		t.Fatalf("err = %v, result %+v, want a failed match counted as skipped, never applied", err, res)
	}
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0], "put back to "+old) {
		t.Errorf("errors = %q, want the row reported as put back", res.Errors)
	}
	f.requireUntouched(id, old, before)
	if got := f.atPath(f.newPath); len(got) != 0 {
		t.Errorf("the found file is held by %v after the rollback, want nobody", got)
	}

	store.heal()
	err, res = f.apply(store, "op-2", ReconcileApplyItem{BookID: id, NewPath: f.newPath})
	if err != nil || res.Applied != 1 {
		t.Fatalf("rerun: %v, result %+v", err, res)
	}
	if got := f.bookPath(id); got != f.newPath {
		t.Errorf("book path = %q, want %q", got, f.newPath)
	}
	f.requireRepointedRow(id, row)
}

// The worst case: the book's write fails and the row cannot be put back. The
// row stays at the found file while the book is at the old path. That is
// reported by name and never counted as applied, and a rerun finishes it: the
// row is already where the book is going, so only the book is written, and no
// second row appears.
func TestExecuteReconcile_RollbackFailureIsReportedAndARerunFinishesTheMatch(t *testing.T) {
	f := newApplyFixture(t)
	old := f.missingPath("Year One.m4b")
	id := f.book("Batman: Year One", old)
	row := f.row(id, old)
	// The first ModifyBookFile (the forward repoint) lands, every later one fails.
	store := &failingStore{PebbleStore: f.ps, failBook: true, failRowAfter: 1}

	err, res := f.apply(store, "op-1", ReconcileApplyItem{BookID: id, NewPath: f.newPath})
	if err == nil || res.Applied != 0 || res.Skipped != 1 {
		t.Fatalf("err = %v, result %+v, want a failed match counted as skipped, never applied", err, res)
	}
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0], "could not be put back") || !strings.Contains(res.Errors[0], row.ID) {
		t.Errorf("errors = %q, want the stranded row %s named", res.Errors, row.ID)
	}
	if got := f.bookPath(id); got != old {
		t.Errorf("book path = %q, want it left at %q", got, old)
	}
	f.requireRepointedRow(id, row) // the stranded state: the row is at the found file

	store.heal()
	err, res = f.apply(store, "op-2", ReconcileApplyItem{BookID: id, NewPath: f.newPath})
	if err != nil || res.Applied != 1 {
		t.Fatalf("rerun: %v, result %+v", err, res)
	}
	if got := f.bookPath(id); got != f.newPath {
		t.Errorf("book path = %q, want %q", got, f.newPath)
	}
	f.requireRepointedRow(id, row)
	if n := len(f.journal("op-2")[database.ChangeTypeBookFileRepoint]); n != 0 {
		t.Errorf("the rerun journaled %d repoint rows for a row it did not move", n)
	}
}

// If the row's own write fails, the book is not touched.
func TestExecuteReconcile_RowWriteFailureLeavesTheBookAlone(t *testing.T) {
	f := newApplyFixture(t)
	old := f.missingPath("Year One.m4b")
	id := f.book("Batman: Year One", old)
	f.row(id, old)
	before := f.rows(id)
	store := &failingStore{PebbleStore: f.ps, failRowAfter: 0}

	err, res := f.apply(store, "op-1", ReconcileApplyItem{BookID: id, NewPath: f.newPath})
	if err == nil || res.Applied != 0 || res.Skipped != 1 {
		t.Fatalf("err = %v, result %+v, want a failed match counted as skipped, never applied", err, res)
	}
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0], "not repointed") {
		t.Errorf("errors = %q, want the row reported as not repointed", res.Errors)
	}
	f.requireUntouched(id, old, before)
}

// The row is repointed only while it still is where the apply read it. Another
// writer flagging it between the read and the write refuses the match, and the
// book stays where it was.
func TestExecuteReconcile_ARowChangedUnderTheApplyIsNotRepointed(t *testing.T) {
	f := newApplyFixture(t)
	old := f.missingPath("Year One.m4b")
	id := f.book("Batman: Year One", old)
	row := f.row(id, old)
	store := &failingStore{PebbleStore: f.ps, failRowAfter: -1}
	store.beforeRowCall = func() {
		// The file reappears at the old path and the row stops being missing.
		if _, err := f.ps.ModifyBookFile(id, row.ID, func(r *database.BookFile) error {
			r.Missing = false
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}

	err, res := f.apply(store, "op-1", ReconcileApplyItem{BookID: id, NewPath: f.newPath})
	if err == nil || res.Applied != 0 || res.Skipped != 1 {
		t.Fatalf("err = %v, result %+v, want refused", err, res)
	}
	if got := f.bookPath(id); got != old {
		t.Errorf("book path = %q, want it left at %q", got, old)
	}
	rows := f.rows(id)
	if len(rows) != 1 || rows[0].FilePath != old || rows[0].Missing {
		t.Errorf("rows = %+v, want the row left where the other writer put it", rows)
	}
}

// One refused match does not stop the rest: the good match is applied whole.
func TestExecuteReconcile_ARefusedMatchDoesNotStopTheNext(t *testing.T) {
	f := newApplyFixture(t)
	badOld := f.missingPath("Bad.m4b")
	bad := f.book("Batman: Hush", badOld)
	f.row(bad, badOld)
	f.row(bad, badOld) // two rows at the old path: refused
	goodOld := f.missingPath("Good.m4b")
	good := f.book("Batman: Year One", goodOld)
	row := f.row(good, goodOld)
	badTarget := f.file("found/Hush.m4b", "other audio")

	err, res := f.apply(f.ps, "op-mixed",
		ReconcileApplyItem{BookID: bad, NewPath: badTarget},
		ReconcileApplyItem{BookID: good, NewPath: f.newPath})
	if err == nil {
		t.Fatal("want the op to report the refused match")
	}
	if res.Applied != 1 || res.Skipped != 1 {
		t.Fatalf("result = %+v, want 1 applied and 1 skipped", res)
	}
	if got := f.bookPath(bad); got != badOld {
		t.Errorf("refused book moved to %q", got)
	}
	f.requireRepointedRow(good, row)
}

// The book's path is replaced only while it still is the path the plan, the row
// move and the journal were made from. Another writer repointing the book
// between the apply's read and its write refuses the book, and the row that
// moved is put back: no path of the other writer's is overwritten and no row is
// left pointing somewhere the book is not.
func TestExecuteReconcile_ABookRepointedUnderTheApplyIsNotOverwrittenAndItsRowIsRestored(t *testing.T) {
	f := newApplyFixture(t)
	old := f.missingPath("Year One.m4b")
	id := f.book("Batman: Year One", old)
	f.row(id, old)
	before := f.rows(id)
	theirs := f.missingPath("Year One (moved by someone else).m4b")
	store := &failingStore{PebbleStore: f.ps, failRowAfter: -1}
	store.beforeBookCall = func() {
		if _, err := f.ps.ModifyBook(id, func(b *database.Book) error { b.FilePath = theirs; return nil }); err != nil {
			t.Fatal(err)
		}
	}

	err, res := f.apply(store, "op-1", ReconcileApplyItem{BookID: id, NewPath: f.newPath})
	if err == nil || res.Applied != 0 || res.Skipped != 1 {
		t.Fatalf("err = %v, result %+v, want refused and never applied", err, res)
	}
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0], "now points at") || !strings.Contains(res.Errors[0], "put back to "+old) {
		t.Errorf("errors = %q, want the changed path and the restored row named", res.Errors)
	}
	f.requireUntouched(id, theirs, before)
}
