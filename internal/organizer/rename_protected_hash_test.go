// file: internal/organizer/rename_protected_hash_test.go
// version: 1.1.0
// guid: 4b8e2d61-7c39-4a05-9f13-d6a2e8c7b590
// last-edited: 2026-09-13

package organizer

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/filehash"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// Review round 5, item 1: with a protected source, ApplyRename copies the file
// into the library and writes tags to the COPY while no row names the copy
// yet, so the write's by-path lookup found nothing and recorded nothing. The
// row was then repointed to the copy still holding the source's file_hash, and
// the next rescan treated the copy as a replaced file.
func TestApplyRename_ProtectedSourceRecordsCopyHash(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	protectedDir := t.TempDir()
	src := filepath.Join(protectedDir, "seeding.mp3")
	gen := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "anullsrc=r=44100:cl=mono", "-t", "3",
		"-c:a", "libmp3lame", "-b:a", "64k", src)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v: %s", err, out)
	}

	oldRoot := config.AppConfig.RootDir
	config.AppConfig.RootDir = t.TempDir()
	defer func() { config.AppConfig.RootDir = oldRoot }()

	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer store.Close()
	book, err := store.CreateBook(&database.Book{Title: "Protected Copy", FilePath: src})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	srcHash, err := filehash.BookFileHash(src)
	if err != nil {
		t.Fatalf("BookFileHash: %v", err)
	}
	if err := store.CreateBookFile(&database.BookFile{BookID: book.ID, FilePath: src, FileHash: srcHash}); err != nil {
		t.Fatalf("CreateBookFile: %v", err)
	}
	// Production wiring: package-level writes record through the server store.
	metadata.SetBookFileHashStore(store)
	defer metadata.SetBookFileHashStore(nil)

	rs := NewRenameService(store)
	rs.IsProtectedPath = func(p string) bool {
		return strings.HasPrefix(p, protectedDir+string(os.PathSeparator))
	}
	// Production wiring (internal/audiobooks/rename.go): with no tag reader
	// ApplyRename writes no tags, since the write could not be undone.
	rs.ReadCurrentTags = metadata.ReadTagProperties
	res, err := rs.ApplyRename(book.ID, "")
	if err != nil {
		t.Fatalf("ApplyRename: %v", err)
	}
	if res.NewPath == src || res.TagsWritten == 0 {
		t.Fatalf("fixture error: want a tagged library copy, got new path %q with %d tags written", res.NewPath, res.TagsWritten)
	}
	copyHash, err := filehash.BookFileHash(res.NewPath)
	if err != nil {
		t.Fatalf("BookFileHash copy: %v", err)
	}
	if copyHash == srcHash {
		t.Fatal("fixture error: the tag write did not change the copy's bytes")
	}
	if now, _ := filehash.BookFileHash(src); now != srcHash {
		t.Fatal("the protected source was modified")
	}

	files, err := store.GetBookFiles(book.ID)
	if err != nil || len(files) != 1 {
		t.Fatalf("GetBookFiles: %d rows, err=%v", len(files), err)
	}
	row := files[0]
	if row.FilePath != res.NewPath {
		t.Fatalf("row FilePath = %q, want the copy %q", row.FilePath, res.NewPath)
	}
	if row.FileHash != copyHash {
		t.Errorf("row FileHash = %q after the rename, want the copy's %q (the source's is %q)", row.FileHash, copyHash, srcHash)
	}
	if row.PostMetadataHash == "" {
		t.Error("row PostMetadataHash is empty; want the copy's whole-file SHA-256")
	}
}
