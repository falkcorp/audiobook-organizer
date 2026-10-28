// file: internal/audiobooks/revert_retire_guard_test.go
// version: 1.1.1
// guid: 9a2dc3c4-95fe-49aa-95aa-9d97cd9599db
// last-edited: 2026-10-28

package audiobooks

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// guardFx is one file row retired from a shell into a holder, with a real file
// on disk at the row's path, so a revert that did not refuse would act on it.
type guardFx struct {
	st            *database.PebbleStore
	holder, shell *database.Book
	file          *database.BookFile
	path          string
}

func newGuardFx(t *testing.T) *guardFx {
	t.Helper()
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	st.WaitForWarmup()
	dir := t.TempDir()
	holder, err := st.CreateBook(&database.Book{Title: "Synthetic Holder", FilePath: filepath.Join(dir, "holder")})
	require.NoError(t, err)
	shell, err := st.CreateBook(&database.Book{Title: "Synthetic Shell", FilePath: filepath.Join(dir, "shell")})
	require.NoError(t, err)
	path := filepath.Join(dir, "shell", "01.mp3")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("synthetic"), 0o644))
	f := &database.BookFile{BookID: shell.ID, FilePath: path, FileHash: "hash-guard", TrackNumber: 1}
	require.NoError(t, st.CreateBookFile(f))
	fx := &guardFx{st: st, holder: holder, shell: shell, file: f, path: path}
	trashBookRow(t, st, shell) // a retire acts on a trashed shell
	w := repairs.NewWriter(st, st, "fx", "bulk_update", "repairs-").WithJournal(st, st, "op-retire")
	require.NoError(t, w.RetireVersion(repairs.RetireSpec{Shell: shell.ID, Holder: holder.ID, VersionID: "ver-guard",
		FileIDs: []string{f.ID}, Format: "mp3", Source: "retired_book"}))
	return fx
}

func (fx *guardFx) row() database.BookFile {
	f, err := fx.st.GetBookFileByID(fx.holder.ID, fx.file.ID)
	if err != nil || f == nil {
		panic("the retired row is not under the holder")
	}
	c := *f
	c.UpdatedAt = c.UpdatedAt.Truncate(0)
	return c
}

// An earlier operation journaled a step on a row that has since been retired
// under the book the step names. Reverting it must refuse (changed since) and
// write nothing, in the preflight and in the revert, for every by-id type.
func TestRevertRetiredRowGuards_EarlierOperationsNeverOverwriteARetiredRow(t *testing.T) {
	cases := []struct {
		name  string
		build func(fx *guardFx) *database.OperationChange
	}{
		{"book_file_track", func(fx *guardFx) *database.OperationChange {
			return &database.OperationChange{BookID: fx.holder.ID, ChangeType: undo.ChangeTypeBookFileTrack,
				FieldName: "book_file:" + fx.file.ID, OldValue: "9", NewValue: "1"}
		}},
		{"book_file_repoint", func(fx *guardFx) *database.OperationChange {
			return &database.OperationChange{BookID: fx.holder.ID, ChangeType: database.ChangeTypeBookFileRepoint,
				FieldName: "book_file:" + fx.file.ID,
				OldValue:  database.BookFileLocation{Path: fx.path + ".old"}.Encode(),
				NewValue:  database.BookFileLocation{Path: fx.path}.Encode()}
		}},
		{"itunes_path_clear", func(fx *guardFx) *database.OperationChange {
			return &database.OperationChange{BookID: fx.holder.ID, ChangeType: undo.ChangeTypeITunesPathClear,
				FieldName: "book_file:" + fx.file.ID, OldValue: "/itunes/synthetic.mp3"}
		}},
		{"book_file_move", func(fx *guardFx) *database.OperationChange {
			return &database.OperationChange{BookID: fx.holder.ID, ChangeType: undo.ChangeTypeBookFileMove,
				FieldName: "book_file:" + fx.file.ID, OldValue: fx.path + ".old", NewValue: fx.path}
		}},
		// The reassign moved the row from the holder to the shell; the shell
		// was then retired into the holder, so the row sits retired on the
		// holder, the very book the reassign names as its source. Without the
		// guard that reads as "back on the source" and is marked restored.
		{"book_file_reassign", func(fx *guardFx) *database.OperationChange {
			return &database.OperationChange{BookID: fx.shell.ID, ChangeType: undo.ChangeTypeBookFileReassign,
				FieldName: "book_file:" + fx.file.ID, OldValue: fx.holder.ID, NewValue: fx.shell.ID}
		}},
		{"tag_write", func(fx *guardFx) *database.OperationChange {
			return &database.OperationChange{BookID: fx.holder.ID, ChangeType: "tag_write",
				FieldName: undo.TagWriteField("TITLE", fx.file.ID), OldValue: "Old", NewValue: "New"}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newGuardFx(t)
			c := tc.build(fx)
			c.ID, c.OperationID = "chg-"+tc.name, "op-"+tc.name
			require.NoError(t, fx.st.CreateOperationChange(c))
			before := fx.row()
			require.True(t, before.Retired)

			report, err := preflightUndoConflicts(fx.st, c.OperationID)
			require.NoError(t, err)
			require.Len(t, report.CheckFailed, 1, "the preflight refuses: %+v", report)
			assert.Equal(t, undo.ReasonChangedSince, report.CheckFailed[0].Reason)

			rs := NewRevertService(fx.st)
			wrote := 0
			rs.ReadTags = func(string) (map[string]string, error) { return map[string]string{"TITLE": "New"}, nil }
			rs.WriteTags = func(string, map[string]any) error { wrote++; return nil }
			res, err := rs.RevertOperation(c.OperationID)
			require.Error(t, err)
			require.NotNil(t, res)
			assert.Equal(t, 1, res.Failed)
			assert.Equal(t, 1, res.ChangedSince, "refused as changed since, not restored")
			assert.Zero(t, res.Restored)
			assert.Zero(t, wrote)
			assert.Equal(t, before, fx.row(), "the retired row is untouched")
			_, statErr := os.Stat(fx.path)
			assert.NoError(t, statErr, "the file did not move")
			changes, err := fx.st.GetOperationChanges(c.OperationID)
			require.NoError(t, err)
			assert.Nil(t, changes[0].RevertedAt, "the row is not marked reverted")
		})
	}
}

// A file_move revert repoints every row of the book under the moved folder.
// A retired version's rows live under the holder and name the same folder, so
// the repoint must reach them or they keep pointing at a folder that moved.
func TestRepointBookFiles_ReachesRetiredRows(t *testing.T) {
	fx := newGuardFx(t)
	rs := NewRevertService(fx.st)
	newPath := fx.path + ".moved"
	require.NoError(t, rs.repointBookFiles(fx.holder.ID, fx.path, newPath))
	assert.Equal(t, newPath, fx.row().FilePath, "the retired row follows the file")
	assert.True(t, fx.row().Retired)
}

// siblingRowAt answers whether another row of the book holds a path. A path
// held only by a retired row is held.
func TestSiblingRowAt_CountsRetiredRows(t *testing.T) {
	fx := newGuardFx(t)
	rs := NewRevertService(fx.st)
	assert.True(t, rs.siblingRowAt(fx.holder.ID, "some-other-row", fx.path))
	assert.False(t, rs.siblingRowAt(fx.holder.ID, fx.file.ID, fx.path), "the row itself is excepted")
	assert.False(t, rs.siblingRowAt(fx.holder.ID, "some-other-row", fx.path+".nope"))
}

// A soft-delete revert reads the book's rows to decide who may claim their
// paths; a retired row under the book must be in that read.
func TestFilesIncludingRetired_SeesRowsGetBookFilesHides(t *testing.T) {
	fx := newGuardFx(t)
	rs := NewRevertService(fx.st)
	live, err := fx.st.GetBookFiles(fx.holder.ID)
	require.NoError(t, err)
	require.Empty(t, live, "the live listing does not show a retired version's row")
	all, err := rs.filesIncludingRetired(fx.holder.ID)
	require.NoError(t, err)
	require.Len(t, all, 1)
	assert.Equal(t, fx.file.ID, all[0].ID)
}

// A trashed holder whose only file rows are the retired version's: undoing the
// trash must read those rows to decide the library state. With the live-only
// listing it sees no file in the library folder and demotes the book to
// "imported", which takes it out of the ABS listing.
func TestRevertBookSoftDelete_RetiredRowsCountForTheRestoredLibraryState(t *testing.T) {
	fx := newGuardFx(t)
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = filepath.Dir(filepath.Dir(fx.path))
	t.Cleanup(func() { config.AppConfig.RootDir = prev })
	organized, yes := "organized", true
	_, err := fx.st.ModifyBook(fx.holder.ID, func(b *database.Book) error {
		b.LibraryState = &organized
		b.MarkedForDeletion = &yes
		return nil
	})
	require.NoError(t, err)
	require.NoError(t, fx.st.CreateOperationChange(&database.OperationChange{
		ID: "chg-sd", OperationID: "op-sd", BookID: fx.holder.ID, ChangeType: undo.ChangeTypeBookSoftDelete,
		FieldName: "marked_for_deletion", OldValue: "false", NewValue: "true",
	}))
	res, err := NewRevertService(fx.st).RevertOperation("op-sd")
	require.NoError(t, err, "%+v", res)
	got, err := fx.st.GetBookByID(fx.holder.ID)
	require.NoError(t, err)
	require.NotNil(t, got.LibraryState)
	assert.Equal(t, "organized", *got.LibraryState)
}

// repointPathIndexAwayFrom hands the path key of a hidden book's row to a live
// book's row at the same path. A retired row never holds the key, so a hidden
// holder of retired rows leaves the live row's key where it is.
func TestRepointPathIndexAwayFrom_RetiredRowsOfTheHiddenBookLeaveTheKeyAlone(t *testing.T) {
	fx := newGuardFx(t)
	other, err := fx.st.CreateBook(&database.Book{Title: "Synthetic Live", FilePath: filepath.Dir(fx.path)})
	require.NoError(t, err)
	live := &database.BookFile{BookID: other.ID, FilePath: fx.path, FileHash: "hash-live", TrackNumber: 1}
	require.NoError(t, fx.st.CreateBookFile(live))
	// The holder is trashed; the retired row there must not own the path key.
	yes := true
	_, err = fx.st.ModifyBook(fx.holder.ID, func(b *database.Book) error { b.MarkedForDeletion = &yes; return nil })
	require.NoError(t, err)
	rs := NewRevertService(fx.st)
	require.NoError(t, rs.repointPathIndexAwayFrom(fx.st, fx.holder.ID))
	owner, err := fx.st.GetBookFileByPath(fx.path)
	require.NoError(t, err)
	require.NotNil(t, owner)
	assert.Equal(t, live.ID, owner.ID, "the live row holds the path key")
}
