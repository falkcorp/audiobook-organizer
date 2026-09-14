// file: internal/plugins/maintenance/repoint_unrecorded_renames_cases_test.go
// version: 1.1.0
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

func TestRepointUnrecordedRenames_Outcomes(t *testing.T) {
	orig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = orig })
	config.AppConfig.ITunes = config.ITunesConfig{}

	dir := t.TempDir()
	newFile := filepath.Join(dir, "new", "01.mp3")
	touch(t, newFile)
	// A directory where a book_file record expects a regular file.
	dirAsFile := filepath.Join(dir, "new", "02.mp3")
	if err := os.MkdirAll(dirAsFile, 0o755); err != nil {
		t.Fatal(err)
	}
	recorded := time.Now().UTC()

	fx := &repointFixture{
		prefs: map[string]string{},
		files: map[string]database.BookFile{
			// Already at new_path: a write that committed but reported failure.
			"f1": {ID: "f1", BookID: "b1", FilePath: newFile},
			// Wrong kind at new_path.
			"f2": {ID: "f2", BookID: "b1", FilePath: "/old/02.mp3"},
			// Holds old_path but was written after the record.
			"f3": {ID: "f3", BookID: "b1", FilePath: "/old/03.mp3", UpdatedAt: recorded.Add(time.Minute)},
		},
		book: database.Book{ID: "b1", FilePath: "/old/dir"},
	}
	stamp := recorded.Format(time.RFC3339Nano)
	k1 := fx.record(t, organizer.RenamePathWriteFailure{BookID: "b1", BookFileID: "f1", OldPath: "/old/01.mp3", NewPath: newFile, RecordedAt: stamp})
	k2 := fx.record(t, organizer.RenamePathWriteFailure{BookID: "b1", BookFileID: "f2", OldPath: "/old/02.mp3", NewPath: dirAsFile, RecordedAt: stamp})
	third := filepath.Join(dir, "new", "03.mp3")
	touch(t, third)
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
	if fx.prefs[k3] == "" || fx.files["f3"].FilePath != "/old/03.mp3" {
		t.Error("row modified since the record: must be skipped and kept")
	}
	if fx.prefs[kb] == "" || fx.book.FilePath != "/old/dir" {
		t.Error("book row whose new path is a symlink: must be skipped and kept")
	}
	if fx.fileWrite != 0 || fx.bookWrite != 0 {
		t.Fatalf("rows written: files=%d books=%d", fx.fileWrite, fx.bookWrite)
	}

	// The override repoints the modified-since row (it still holds old_path).
	runRepointParams(t, p, `{"apply":true,"allow_modified_since_record":true}`)
	if fx.files["f3"].FilePath != third || fx.prefs[k3] != "" {
		t.Errorf("override did not repoint f3: path=%s record=%q", fx.files["f3"].FilePath, fx.prefs[k3])
	}
}
