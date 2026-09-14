// file: internal/server/handlers/audiobooks/handler_crud_hash_test.go
// version: 1.0.0
// guid: 9c4e1a73-8d25-4f6b-b3e0-7a2d6f9c1e58
// last-edited: 2026-09-13

package audiobookshandler_test

import (
	"net/http"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/mock"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/filehash"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// Review round 4, item 1 (audit A3 item 9): the book PATCH write-back rewrote
// the file through metadata.WriteMetadataToFile and left the file's book_file
// row holding the old file_hash, so the next rescan treated the file as
// replaced. The handler's own store is the usual mock; the hash is recorded
// through the metadata package's store, a real PebbleStore holding the row.
func TestUpdateAudiobook_WriteBackRecordsNewFileHash(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	path := filepath.Join(t.TempDir(), "book.mp3")
	gen := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "anullsrc=r=44100:cl=mono", "-t", "3",
		"-c:a", "libmp3lame", "-b:a", "64k", path)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v: %s", err, out)
	}

	real, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer real.Close()
	book, err := real.CreateBook(&database.Book{Title: "Old", FilePath: path})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	before, err := filehash.BookFileHash(path)
	if err != nil {
		t.Fatalf("BookFileHash: %v", err)
	}
	if err := real.CreateBookFile(&database.BookFile{BookID: book.ID, FilePath: path, FileHash: before}); err != nil {
		t.Fatalf("CreateBookFile: %v", err)
	}
	metadata.SetBookFileHashStore(real)
	defer metadata.SetBookFileHashStore(nil)

	h, d := newHandler(t)
	d.store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1", Title: "Old"}, nil)
	d.updater.EXPECT().UpdateAudiobook(mock.Anything, "b1", mock.Anything).
		Return(&database.Book{ID: "b1", Title: "A New Title", FilePath: path}, nil)
	d.store.EXPECT().RecordMetadataChange(mock.Anything).Return(nil).Maybe()
	d.store.EXPECT().GetBookAuthors("b1").Return([]database.BookAuthor{}, nil).Maybe()
	d.store.EXPECT().GetBookNarrators("b1").Return([]database.BookNarrator{}, nil).Maybe()
	d.store.EXPECT().SetLastWrittenAt("b1", mock.Anything).Return(nil)
	d.svc.EXPECT().InvalidateBookCaches().Return()
	d.writeBack.EXPECT().Enqueue("b1").Return()
	c, w := newCtx("PUT", "/audiobooks/b1", map[string]any{"title": "A New Title"}, p("id", "b1"))
	h.UpdateAudiobook(c)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", w.Code, w.Body.String())
	}

	after, err := filehash.BookFileHash(path)
	if err != nil {
		t.Fatalf("BookFileHash after: %v", err)
	}
	if after == before {
		t.Fatal("fixture error: the write-back did not change the file's bytes")
	}
	row, err := real.GetBookFileByPath(path)
	if err != nil || row == nil {
		t.Fatalf("GetBookFileByPath: row=%v err=%v", row, err)
	}
	if row.FileHash != after {
		t.Errorf("FileHash = %q after the write-back, want the new bytes' %q (old was %q)", row.FileHash, after, before)
	}
	if row.OriginalFileHash != before {
		t.Errorf("OriginalFileHash = %q, want the pre-write %q", row.OriginalFileHash, before)
	}
}
