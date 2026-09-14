// file: internal/metafetch/cover_embed_hash_test.go
// version: 1.0.0
// guid: 4d8b2f60-1a97-4e3c-8c52-b6e9a0d7f314
// last-edited: 2026-09-13

package metafetch

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/filehash"
)

// Review round 4, item 1 (audit A3 item 9): embedding a cover rewrites the
// whole audio file, and EmbedCoverArtSafe was called with no book_file row, so
// the row kept the old file_hash and the next rescan treated the file as
// replaced. Real store, real mp3, real embed.
func TestEmbedCoverInBookFiles_RecordsNewFileHash(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "book.mp3")
	gen := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "anullsrc=r=44100:cl=mono", "-t", "3",
		"-c:a", "libmp3lame", "-b:a", "64k", path)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v: %s", err, out)
	}

	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for x := 0; x < 16; x++ {
		for y := 0; y < 16; y++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 16), G: uint8(y * 16), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png: %v", err)
	}
	coverPath := filepath.Join(dir, "cover.png")
	if err := os.WriteFile(coverPath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write cover: %v", err)
	}

	store, err := database.NewPebbleStore(filepath.Join(dir, "db"))
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer store.Close()
	book, err := store.CreateBook(&database.Book{Title: "Cover", FilePath: path})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	before, err := filehash.BookFileHash(path)
	if err != nil {
		t.Fatalf("BookFileHash: %v", err)
	}
	if err := store.CreateBookFile(&database.BookFile{BookID: book.ID, FilePath: path, FileHash: before}); err != nil {
		t.Fatalf("CreateBookFile: %v", err)
	}

	svc := NewService(store)
	svc.embedCoverInBookFiles(book, coverPath)

	after, err := filehash.BookFileHash(path)
	if err != nil {
		t.Fatalf("BookFileHash after: %v", err)
	}
	if after == before {
		t.Fatal("fixture error: the embed did not change the file's bytes")
	}
	row, err := store.GetBookFileByPath(path)
	if err != nil || row == nil {
		t.Fatalf("GetBookFileByPath: row=%v err=%v", row, err)
	}
	if row.FileHash != after {
		t.Errorf("FileHash = %q after the cover embed, want the new bytes' %q (old was %q)", row.FileHash, after, before)
	}
}
