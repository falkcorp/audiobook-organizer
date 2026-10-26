// file: internal/audiobooks/purge_retired_path_test.go
// version: 1.0.0
// guid: 5c1a8e36-b2f9-4d70-8a14-9e63d7b0c2f5
// last-edited: 2026-10-26

package audiobooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// A purge removes a trashed book's file from disk only when no book_file row
// still names it. A retired version's row is such a row (BookFilesAtPath keeps
// retired rows by design: a retired row still owns its path), so the file of a
// retired version a live book keeps is never deleted by purging the shell it was
// retired out of. The control is the same path once the row is gone.
func TestPurgePathStillReferenced_RetiredRowKeepsItsFile(t *testing.T) {
	svc, store, _ := setupPurgeBoundary(t)
	path := filepath.Join(config.AppConfig.RootDir, "shell", "01.mp3")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}

	holder, err := store.CreateBook(&database.Book{Title: "Year One", FilePath: filepath.Join(config.AppConfig.RootDir, "holder")})
	if err != nil {
		t.Fatal(err)
	}
	shell, err := store.CreateBook(&database.Book{Title: "The Long Halloween", FilePath: filepath.Dir(path)})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	v, err := store.CreateBookVersion(&database.BookVersion{
		BookID: holder.ID, Status: database.BookVersionStatusRetired, Format: "mp3", Source: "retired_book",
		RetiredFromBookID: shell.ID, RetireOpID: "op-1", RetiredAt: &now,
	})
	if err != nil {
		t.Fatal(err)
	}
	row := &database.BookFile{BookID: shell.ID, FilePath: path}
	if err := store.CreateBookFile(row); err != nil {
		t.Fatal(err)
	}
	if err := store.RetireBookFiles([]database.BookFileMove{{FileIDs: []string{row.ID}, SourceBookID: shell.ID}}, holder.ID, v.ID); err != nil {
		t.Fatal(err)
	}

	refErr := svc.purgePathStillReferenced(path)
	if refErr == nil {
		t.Fatal("a path a retired row names was reported unreferenced: the purge would delete a retired version's file")
	}
	if !strings.Contains(refErr.Error(), row.ID) || !strings.Contains(refErr.Error(), holder.ID) {
		t.Errorf("the refusal must name the retired row %s and its holder %s: %v", row.ID, holder.ID, refErr)
	}

	// Control: a path no row names is free.
	if err := svc.purgePathStillReferenced(filepath.Join(config.AppConfig.RootDir, "shell", "unreferenced.mp3")); err != nil {
		t.Errorf("control: an unreferenced path was refused: %v", err)
	}
}
