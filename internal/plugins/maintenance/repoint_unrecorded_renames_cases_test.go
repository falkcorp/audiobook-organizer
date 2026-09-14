// file: internal/plugins/maintenance/repoint_unrecorded_renames_cases_test.go
// version: 1.2.0
// guid: 4c9a0e63-d8b2-47f1-9e05-b3a6f2d7c814
// last-edited: 2026-09-14

package maintenance

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
)

func clearITunes(t *testing.T) {
	t.Helper()
	orig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = orig })
	config.AppConfig.ITunes = config.ITunesConfig{}
}

func TestRepointUnrecordedRenames_Outcomes(t *testing.T) {
	clearITunes(t)

	dir := t.TempDir()
	newFile := filepath.Join(dir, "new", "01.mp3")
	touch(t, newFile)
	// A directory where a book_file record expects a regular file.
	dirAsFile := filepath.Join(dir, "new", "02.mp3")
	if err := os.MkdirAll(dirAsFile, 0o755); err != nil {
		t.Fatal(err)
	}
	third := filepath.Join(dir, "new", "03.mp3")
	touch(t, third)
	recorded := time.Now().UTC()

	fx := &repointFixture{
		prefs: map[string]string{},
		files: map[string]database.BookFile{
			// Already at new_path: a write that committed but reported failure.
			"f1": {ID: "f1", BookID: "b1", FilePath: newFile},
			// Wrong kind at new_path.
			"f2": {ID: "f2", BookID: "b1", FilePath: "/old/02.mp3"},
			// Holds old_path but an unrelated write (an iTunes-path backfill,
			// say) touched it after the record: old_path is gone and new_path
			// is present, so it is still repointed.
			"f3": {ID: "f3", BookID: "b1", FilePath: "/old/03.mp3", UpdatedAt: recorded.Add(time.Minute)},
		},
		book: database.Book{ID: "b1", FilePath: "/old/dir"},
	}
	stamp := recorded.Format(time.RFC3339Nano)
	k1 := fx.record(t, organizer.RenamePathWriteFailure{BookID: "b1", BookFileID: "f1", OldPath: "/old/01.mp3", NewPath: newFile, RecordedAt: stamp})
	k2 := fx.record(t, organizer.RenamePathWriteFailure{BookID: "b1", BookFileID: "f2", OldPath: "/old/02.mp3", NewPath: dirAsFile, RecordedAt: stamp})
	k3 := fx.record(t, organizer.RenamePathWriteFailure{BookID: "b1", BookFileID: "f3", OldPath: "/old/03.mp3", NewPath: third, RecordedAt: stamp})
	// A book-row record whose new path is a symlink: wrong kind (a book row
	// accepts a directory or a regular file, never a link).
	link := filepath.Join(dir, "new", "link")
	if err := os.Symlink(filepath.Join(dir, "new"), link); err != nil {
		t.Fatal(err)
	}
	kb := fx.record(t, organizer.RenamePathWriteFailure{BookID: "b1", OldPath: "/old/dir", NewPath: link, RecordedAt: stamp})

	p := fx.plugin()

	runRepoint(t, p, false)
	for _, k := range []string{k1, k2, k3, kb} {
		if fx.prefs[k] == "" {
			t.Fatalf("dry run cleared %s", k)
		}
	}

	runRepoint(t, p, true)
	if fx.prefs[k1] != "" {
		t.Error("already_at_new_path record not cleared on apply")
	}
	if fx.prefs[k2] == "" || fx.files["f2"].FilePath != "/old/02.mp3" {
		t.Error("wrong-kind new path: record must be kept and row untouched")
	}
	if fx.files["f3"].FilePath != third || fx.prefs[k3] != "" {
		t.Errorf("row touched after the record was not repointed: path=%s record=%q", fx.files["f3"].FilePath, fx.prefs[k3])
	}
	if fx.prefs[kb] == "" || fx.book.FilePath != "/old/dir" {
		t.Error("book row whose new path is a symlink: must be skipped and kept")
	}
	if fx.fileWrite != 1 || fx.bookWrite != 0 {
		t.Fatalf("rows written: files=%d books=%d, want 1 and 0", fx.fileWrite, fx.bookWrite)
	}
}

// The A->B->A guards: both paths on disk is ambiguous, and a new path another
// row already holds is taken. Both skip and keep the record, on apply too.
func TestRepointUnrecordedRenames_ContentPreconditions(t *testing.T) {
	clearITunes(t)

	dir := t.TempDir()
	// Both old and new exist (the files may be back at old after an undo).
	old5, new5 := filepath.Join(dir, "old", "05.mp3"), filepath.Join(dir, "new", "05.mp3")
	touch(t, old5)
	touch(t, new5)
	// new6 is held by another book_file row, f7.
	new6 := filepath.Join(dir, "new", "06.mp3")
	touch(t, new6)
	// The book's new directory is held by another book, b2.
	newDir := filepath.Join(dir, "newdir")
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		t.Fatal(err)
	}
	oldDir := filepath.Join(dir, "olddir") // not on disk

	fx := &repointFixture{
		prefs: map[string]string{},
		files: map[string]database.BookFile{
			"f5": {ID: "f5", BookID: "b1", FilePath: old5},
			"f6": {ID: "f6", BookID: "b1", FilePath: "/gone/06.mp3"},
			"f7": {ID: "f7", BookID: "b1", FilePath: new6},
		},
		book:   database.Book{ID: "b1", FilePath: oldDir},
		bookAt: map[string][]string{newDir: {"b2"}},
	}
	k5 := fx.record(t, organizer.RenamePathWriteFailure{BookID: "b1", BookFileID: "f5", OldPath: old5, NewPath: new5})
	k6 := fx.record(t, organizer.RenamePathWriteFailure{BookID: "b1", BookFileID: "f6", OldPath: "/gone/06.mp3", NewPath: new6})
	kb := fx.record(t, organizer.RenamePathWriteFailure{BookID: "b1", OldPath: oldDir, NewPath: newDir})

	runRepoint(t, fx.plugin(), true)

	if fx.prefs[k5] == "" || fx.files["f5"].FilePath != old5 {
		t.Error("both paths exist: row must stay at old_path and the record be kept")
	}
	if fx.prefs[k6] == "" || fx.files["f6"].FilePath != "/gone/06.mp3" || fx.files["f7"].FilePath != new6 {
		t.Error("new_path held by another book_file: must be skipped and kept")
	}
	if fx.prefs[kb] == "" || fx.book.FilePath != oldDir {
		t.Error("new_path held by another book: must be skipped and kept")
	}
	if fx.fileWrite != 0 || fx.bookWrite != 0 {
		t.Fatalf("rows written: files=%d books=%d", fx.fileWrite, fx.bookWrite)
	}
}
