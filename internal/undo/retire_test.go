// file: internal/undo/retire_test.go
// version: 1.1.0
// guid: 54f67c90-9235-45fd-b826-66a6f3be19d9
// last-edited: 2026-10-24

package undo

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func retireRow(id, op, file, shell, holder, version string) *database.OperationChange {
	return &database.OperationChange{
		ID: id, OperationID: op, BookID: holder, ChangeType: ChangeTypeBookFileRetire,
		FieldName: "book_file:" + file,
		OldValue:  RetirePlace{BookID: shell}.Encode(),
		NewValue:  RetirePlace{BookID: holder, VersionID: version}.Encode(),
	}
}

func TestNotRestorableLabel_RetireRows(t *testing.T) {
	good := retireRow("c1", "op", "f1", "shell", "holder", "v1")
	if got := NotRestorableLabel(good); got != "" {
		t.Fatalf("a well-formed retire row is restorable, got %q", got)
	}
	noID := *good
	noID.FieldName = ""
	badVal := *good
	badVal.NewValue = "{"
	noVersion := *good
	noVersion.NewValue = RetirePlace{BookID: "holder"}.Encode()
	for name, c := range map[string]*database.OperationChange{"no id": &noID, "bad value": &badVal, "no version": &noVersion} {
		if got := NotRestorableLabel(c); got == "" || !strings.HasPrefix(got, ChangeTypeBookFileRetire) {
			t.Errorf("%s: label %q, want a %s label", name, got, ChangeTypeBookFileRetire)
		}
	}
	for _, c := range []*database.OperationChange{
		{ChangeType: ChangeTypeBookVersionCreate, FieldName: BookVersionField("v1"), NewValue: `{"id":"v1","book_id":"holder","status":"retired","retired_from_book_id":"shell"}`, BookID: "holder"},
		{ChangeType: ChangeTypeRetiredVersionRehome, FieldName: BookVersionField("v1"), OldValue: "a", NewValue: "b", BookID: "b"},
	} {
		if got := NotRestorableLabel(c); got != "" {
			t.Errorf("%s: label %q, want restorable", c.ChangeType, got)
		}
	}
	for _, c := range []*database.OperationChange{
		{ChangeType: ChangeTypeBookVersionCreate, FieldName: "", NewValue: "{}"},
		{ChangeType: ChangeTypeRetiredVersionRehome, FieldName: BookVersionField("v1")},
	} {
		if NotRestorableLabel(c) == "" {
			t.Errorf("%s with a missing field is not restorable", c.ChangeType)
		}
	}
}

func TestCheckRetireCurrent(t *testing.T) {
	r := BookFileRetire{FileID: "f1", Shell: "shell", Holder: "holder", VersionID: "v1"}
	retired := &database.BookFile{ID: "f1", BookID: "holder", Retired: true, VersionID: "v1"}
	cases := []struct {
		name      string
		onHolder  *database.BookFile
		onShell   *database.BookFile
		wantNil   bool
		wantAlrdy bool
	}{
		{"retired where the retire put it", retired, nil, true, false},
		{"back on the shell", nil, &database.BookFile{ID: "f1", BookID: "shell"}, false, true},
		{"on the holder, flag lost", &database.BookFile{ID: "f1", BookID: "holder", VersionID: "v1"}, nil, false, false},
		{"on the holder, another version", &database.BookFile{ID: "f1", BookID: "holder", Retired: true, VersionID: "v2"}, nil, false, false},
		{"on the shell but still retired", nil, &database.BookFile{ID: "f1", BookID: "shell", Retired: true}, false, false},
		{"nowhere", nil, nil, false, false},
	}
	for _, tc := range cases {
		err := CheckRetireCurrent(tc.onHolder, tc.onShell, r)
		switch {
		case tc.wantNil && err != nil:
			t.Errorf("%s: %v", tc.name, err)
		case tc.wantAlrdy && !errors.Is(err, ErrAlreadyRestored):
			t.Errorf("%s: %v, want ErrAlreadyRestored", tc.name, err)
		case !tc.wantNil && !tc.wantAlrdy && RefusalReason(err) != ReasonChangedSince:
			t.Errorf("%s: %v, want a changed_since refusal", tc.name, err)
		}
	}
}

func TestRetiredRowRefusalIsChangedSince(t *testing.T) {
	err := RetiredRowRefusal(&database.BookFile{ID: "f1", BookID: "holder", VersionID: "v1", Retired: true})
	if RefusalReason(err) != ReasonChangedSince {
		t.Fatalf("reason = %q", RefusalReason(err))
	}
}

func TestCheckVersionCreateAndRehomeCurrent(t *testing.T) {
	c := &database.OperationChange{BookID: "holder"}
	if err := CheckVersionCreateCurrent(nil, c); !errors.Is(err, ErrAlreadyRestored) {
		t.Errorf("gone version: %v", err)
	}
	if err := CheckVersionCreateCurrent(&database.BookVersion{ID: "v1", BookID: "holder", Status: database.BookVersionStatusRetired}, c); err != nil {
		t.Errorf("still the retired version: %v", err)
	}
	for _, v := range []*database.BookVersion{
		{ID: "v1", BookID: "holder", Status: database.BookVersionStatusActive},
		{ID: "v1", BookID: "other", Status: database.BookVersionStatusRetired},
	} {
		if RefusalReason(CheckVersionCreateCurrent(v, c)) != ReasonChangedSince {
			t.Errorf("version %+v should be changed since", v)
		}
	}

	rh := &database.OperationChange{FieldName: BookVersionField("v1"), OldValue: "old", NewValue: "new"}
	if err := CheckRehomeCurrent(&database.BookVersion{ID: "v1", BookID: "new"}, rh); err != nil {
		t.Errorf("held by the new holder: %v", err)
	}
	if err := CheckRehomeCurrent(&database.BookVersion{ID: "v1", BookID: "old"}, rh); !errors.Is(err, ErrAlreadyRestored) {
		t.Errorf("back on the old holder: %v", err)
	}
	for _, v := range []*database.BookVersion{nil, {ID: "v1", BookID: "third"}} {
		if RefusalReason(CheckRehomeCurrent(v, rh)) != ReasonChangedSince {
			t.Errorf("version %+v should be changed since", v)
		}
	}
}

// recreatingShells is a PebbleStore that can recreate one purged shell.
type recreatingShells struct {
	*database.PebbleStore
	has map[string]bool
	err error
}

func (s recreatingShells) HasPurgeSnapshot(id string) (bool, error) { return s.has[id], s.err }
func (s recreatingShells) RecreatePurgedBook(id string) error {
	_, err := s.PebbleStore.CreateBook(&database.Book{ID: id, Title: "recreated", FilePath: "/lib/syn/" + id})
	return err
}

func newRetireStore(t *testing.T) *database.PebbleStore {
	t.Helper()
	st, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestShellCheckAndEnsureShellPresent(t *testing.T) {
	st := newRetireStore(t)
	live, err := st.CreateBook(&database.Book{Title: "Synthetic", FilePath: "/lib/syn/live"})
	if err != nil {
		t.Fatal(err)
	}
	if state, err := ShellCheck(st, live.ID); err != nil || state != ShellPresent {
		t.Fatalf("present shell: %v, %v", state, err)
	}
	if recreated, err := EnsureShellPresent(st, live.ID); err != nil || recreated {
		t.Fatalf("present shell needs no recreate: %v, %v", recreated, err)
	}
	// Purged, nothing can recreate it.
	if _, err := ShellCheck(st, "purged"); RefusalReason(err) != ReasonBookMissing {
		t.Fatalf("no recreator: %v", err)
	}
	// Purged, a recreator without a snapshot.
	rs := recreatingShells{PebbleStore: st, has: map[string]bool{}}
	if _, err := ShellCheck(rs, "purged"); RefusalReason(err) != ReasonBookMissing {
		t.Fatalf("no snapshot: %v", err)
	}
	// A read error is a lookup failure, not "missing".
	if _, err := ShellCheck(recreatingShells{PebbleStore: st, err: errors.New("boom")}, "purged"); RefusalReason(err) != ReasonBookLookupFailed {
		t.Fatalf("snapshot read error: %v", err)
	}
	// Purged with a snapshot: recreatable, and ensure recreates it with its id.
	rs.has["purged"] = true
	if state, err := ShellCheck(rs, "purged"); err != nil || state != ShellRecreatable {
		t.Fatalf("snapshot: %v, %v", state, err)
	}
	if recreated, err := EnsureShellPresent(rs, "purged"); err != nil || !recreated {
		t.Fatalf("ensure: %v, %v", recreated, err)
	}
	if b, err := st.GetBookByID("purged"); err != nil || b == nil {
		t.Fatalf("the shell is back: %v, %v", b, err)
	}
	if recreated, err := EnsureShellPresent(rs, "purged"); err != nil || recreated {
		t.Fatalf("second ensure is a no-op: %v, %v", recreated, err)
	}
}

// A retire row on a purged shell is book_missing without a snapshot and
// restorable (not in any conflict bucket) with one.
func TestPreflightUndoConflicts_RetireRowsOnPurgedShell(t *testing.T) {
	st := newRetireStore(t)
	holder, err := st.CreateBook(&database.Book{Title: "Synthetic Holder", FilePath: "/lib/syn/holder"})
	if err != nil {
		t.Fatal(err)
	}
	// The row sits retired on the holder, as a real retire left it, and the
	// shell it came from was purged.
	shell, err := st.CreateBook(&database.Book{Title: "Synthetic Shell", FilePath: "/lib/syn/shell"})
	if err != nil {
		t.Fatal(err)
	}
	f := &database.BookFile{BookID: shell.ID, FilePath: "/lib/syn/shell/01.mp3", FileHash: "h1", TrackNumber: 1}
	if err := st.CreateBookFile(f); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	if _, err := st.CreateBookVersion(&database.BookVersion{ID: "v1", BookID: holder.ID, Status: database.BookVersionStatusRetired, RetiredFromBookID: shell.ID, RetiredAt: &now}); err != nil {
		t.Fatal(err)
	}
	if err := st.RetireBookFiles([]database.BookFileMove{{FileIDs: []string{f.ID}, SourceBookID: shell.ID}}, holder.ID, "v1"); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteBook(shell.ID); err != nil {
		t.Fatal(err)
	}
	row := retireRow("c1", "op1", f.ID, shell.ID, holder.ID, "v1")
	if err := st.CreateOperationChange(row); err != nil {
		t.Fatal(err)
	}
	report, err := PreflightUndoConflicts(st, "op1")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.BookMissing) != 1 {
		t.Fatalf("without a snapshot: %+v", report)
	}
	report, err = PreflightUndoConflicts(recreatingShells{PebbleStore: st, has: map[string]bool{shell.ID: true}}, "op1")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.BookMissing) != 0 {
		t.Fatalf("a recreatable shell is not book_missing: %+v", report)
	}
}

func TestRetireScopeRefusals(t *testing.T) {
	st := newRetireStore(t)
	holder, err := st.CreateBook(&database.Book{Title: "Synthetic Holder", FilePath: "/lib/syn/holder"})
	if err != nil {
		t.Fatal(err)
	}
	shell, err := st.CreateBook(&database.Book{Title: "Synthetic Shell", FilePath: "/lib/syn/shell"})
	if err != nil {
		t.Fatal(err)
	}
	f := &database.BookFile{BookID: shell.ID, FilePath: "/lib/syn/shell/01.mp3", FileHash: "h1", TrackNumber: 1}
	if err := st.CreateBookFile(f); err != nil {
		t.Fatal(err)
	}
	// A retire that stopped half way is enough here: the check reads the row's
	// place, which is "on the shell, not retired" (already restored).
	rows := []*database.OperationChange{retireRow("c1", "op1", f.ID, shell.ID, holder.ID, "v1")}
	reasons, err := RetireScopeRefusals(st, shell.ID, rows)
	if err != nil || len(reasons) != 0 {
		t.Fatalf("a row that is already restored does not refuse: %v, %v", reasons, err)
	}

	rows = append(rows, &database.OperationChange{ID: "c2", OperationID: "op1", BookID: shell.ID, ChangeType: "metadata_update", FieldName: "title", OldValue: "a", NewValue: "b"})
	reasons, err = RetireScopeRefusals(st, shell.ID, rows)
	if err != nil || len(reasons) != 1 || !strings.Contains(reasons[0], "c2") {
		t.Fatalf("a row the combine undo does not reverse refuses, named: %v, %v", reasons, err)
	}

	rows = []*database.OperationChange{{ID: "c3", OperationID: "op1", BookID: holder.ID, ChangeType: ChangeTypeRetiredVersionRehome, FieldName: BookVersionField("v1"), OldValue: "x", NewValue: holder.ID}}
	reasons, err = RetireScopeRefusals(st, shell.ID, rows)
	if err != nil || len(reasons) != 1 || !strings.Contains(reasons[0], "c3") {
		t.Fatalf("a rehome row refuses: %v, %v", reasons, err)
	}
}

// rehomeRow is a ChangeTypeRetiredVersionRehome row: version versionID moved
// from from to to.
func rehomeRow(id, op, versionID, from, to string) *database.OperationChange {
	return &database.OperationChange{
		ID: id, OperationID: op, BookID: to, ChangeType: ChangeTypeRetiredVersionRehome,
		FieldName: BookVersionField(versionID), OldValue: from, NewValue: to,
	}
}

// A rehome that took a version off the shell itself is part of restoring the
// shell: the scoped check judges it (the shell is where the version goes back)
// instead of refusing it, and one that took it off another holder still refuses.
func TestRetireScopeRefusals_RehomeFromTheShellIsJudged(t *testing.T) {
	st := newRetireStore(t)
	shell, err := st.CreateBook(&database.Book{Title: "Synthetic Shell", FilePath: "/lib/syn/shell"})
	if err != nil {
		t.Fatal(err)
	}
	newHolder, err := st.CreateBook(&database.Book{Title: "Synthetic New Holder", FilePath: "/lib/syn/new"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	if _, err := st.CreateBookVersion(&database.BookVersion{ID: "v1", BookID: newHolder.ID, Status: database.BookVersionStatusRetired, RetiredFromBookID: "someone", RetiredAt: &now}); err != nil {
		t.Fatal(err)
	}
	row := rehomeRow("c1", "op1", "v1", shell.ID, newHolder.ID)
	reasons, err := RetireScopeRefusals(st, shell.ID, []*database.OperationChange{row})
	if err != nil || len(reasons) != 0 {
		t.Fatalf("a rehome off the shell, version still on the new holder, is restorable: %v, %v", reasons, err)
	}
	// The same row judged for another shell is a move the combine undo does not own.
	reasons, err = RetireScopeRefusals(st, "other-shell", []*database.OperationChange{row})
	if err != nil || len(reasons) < 1 || !strings.Contains(reasons[0], "c1") {
		t.Fatalf("a rehome off some other holder refuses, named: %v, %v", reasons, err)
	}
	// The version has moved on since: changed since, named.
	third, err := st.CreateBook(&database.Book{Title: "Synthetic Third", FilePath: "/lib/syn/third"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RehomeRetiredVersion("v1", third.ID); err != nil {
		t.Fatal(err)
	}
	reasons, err = RetireScopeRefusals(st, shell.ID, []*database.OperationChange{row})
	if err != nil || len(reasons) != 1 || !strings.Contains(reasons[0], "c1") {
		t.Fatalf("a version that moved on refuses: %v, %v", reasons, err)
	}
}

// The chain case: the holder a rehome took the version from was trashed when
// the version left it. The preflight must accept the trashed holder (the revert
// does, RestoreRetiredVersionHolder), refuse a purged one with no snapshot, and
// accept a purged one a snapshot can recreate.
func TestPreflightUndoConflicts_RehomeToATrashedOrPurgedOldHolder(t *testing.T) {
	st := newRetireStore(t)
	oldHolder, err := st.CreateBook(&database.Book{Title: "Synthetic Old Holder", FilePath: "/lib/syn/old"})
	if err != nil {
		t.Fatal(err)
	}
	newHolder, err := st.CreateBook(&database.Book{Title: "Synthetic New Holder", FilePath: "/lib/syn/new"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	if _, err := st.CreateBookVersion(&database.BookVersion{ID: "v1", BookID: newHolder.ID, Status: database.BookVersionStatusRetired, RetiredFromBookID: "someone", RetiredAt: &now}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateOperationChange(rehomeRow("c1", "op1", "v1", oldHolder.ID, newHolder.ID)); err != nil {
		t.Fatal(err)
	}
	// Trashed, not purged: restorable.
	trashed := true
	oldHolder.MarkedForDeletion = &trashed
	if _, err := st.UpdateBook(oldHolder.ID, oldHolder); err != nil {
		t.Fatal(err)
	}
	report, err := PreflightUndoConflicts(st, "op1")
	if err != nil {
		t.Fatal(err)
	}
	if refusedRows(report) != 0 {
		t.Fatalf("a trashed old holder is restorable: %+v", report)
	}
	// Purged, nothing to recreate it from: refused.
	if err := st.DeleteBook(oldHolder.ID); err != nil {
		t.Fatal(err)
	}
	report, err = PreflightUndoConflicts(st, "op1")
	if err != nil {
		t.Fatal(err)
	}
	if refusedRows(report) == 0 {
		t.Fatalf("a purged old holder with no snapshot is refused: %+v", report)
	}
	// Purged, recreatable: restorable again.
	report, err = PreflightUndoConflicts(recreatingShells{PebbleStore: st, has: map[string]bool{oldHolder.ID: true}}, "op1")
	if err != nil {
		t.Fatal(err)
	}
	if refusedRows(report) != 0 {
		t.Fatalf("a recreatable old holder is restorable: %+v", report)
	}
}

// A retire row that does not parse refuses by name, never silently skipped.
func TestRetireScopeRefusals_UndecodableRowRefusesByName(t *testing.T) {
	st := newRetireStore(t)
	shell, err := st.CreateBook(&database.Book{Title: "Synthetic Shell", FilePath: "/lib/syn/shell"})
	if err != nil {
		t.Fatal(err)
	}
	bad := retireRow("cbad", "op1", "f1", shell.ID, "holder", "v1")
	bad.NewValue = "{"
	reasons, err := RetireScopeRefusals(st, shell.ID, []*database.OperationChange{bad})
	if err != nil || len(reasons) < 1 || !strings.Contains(reasons[0], "cbad") || !strings.Contains(reasons[0], "does not parse") {
		t.Fatalf("an undecodable retire row refuses by name: %v, %v", reasons, err)
	}
}

// The shell itself must be presentable, even when no row names a file on it.
func TestRetireScopeRefusals_ShellPurgedWithoutSnapshotRefuses(t *testing.T) {
	st := newRetireStore(t)
	reasons, err := RetireScopeRefusals(st, "purged-shell", nil)
	if err != nil || len(reasons) != 1 || !strings.Contains(reasons[0], "purged-shell") {
		t.Fatalf("a purged shell nothing can recreate refuses, named: %v, %v", reasons, err)
	}
	reasons, err = RetireScopeRefusals(recreatingShells{PebbleStore: st, has: map[string]bool{"purged-shell": true}}, "purged-shell", nil)
	if err != nil || len(reasons) != 0 {
		t.Fatalf("a recreatable shell does not refuse: %v, %v", reasons, err)
	}
}

// refusedRows counts the rows a preflight report files as not restorable.
func refusedRows(r *UndoConflictReport) int {
	return len(r.BookMissing) + len(r.CheckFailed) + len(r.SeriesDeleted) + len(r.SeriesRenamedSince) + len(r.SeriesNameTaken)
}
