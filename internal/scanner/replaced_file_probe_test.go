// file: internal/scanner/replaced_file_probe_test.go
// version: 1.0.0
// guid: 1d7c4a92-5e63-4b8f-b0a2-9c3e6f1d8b57
// last-edited: 2026-09-13

package scanner

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/filehash"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// writeSilentMP3 writes a real mp3 of the given length and bitrate.
func writeSilentMP3(t *testing.T, ffmpeg, path string, seconds int, bitrate string) {
	t.Helper()
	cmd := exec.Command(ffmpeg,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "anullsrc=r=44100:cl=mono",
		"-t", fmt.Sprintf("%d", seconds),
		"-c:a", "libmp3lame", "-b:a", bitrate,
		path,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v: %s", err, out)
	}
}

// seedOwnedRowWithAudioData runs the real scanner path over path for an OLD
// book, gives the row the expensive audio-derived data a backfill would have
// added, then moves the old book off the path and creates the NEW book the
// scanner will find there.
//
// That is the shape in which createBookFilesForBook reaches the merge: it
// returns early for a book that already has rows (scanner.go, "BookFiles
// already created"), so the upsert only meets a stored row when the file's
// path is owned by a row of another book, which it then merges with.
func seedOwnedRowWithAudioData(t *testing.T, store *database.PebbleStore, path string, duration int) database.BookFile {
	t.Helper()
	oldBook, err := store.CreateBook(&database.Book{FilePath: path, Title: "Replaced At Same Path"})
	if err != nil {
		t.Fatalf("CreateBook old: %v", err)
	}
	createBookFilesForBook(path, []string{path}, logger.New("test"), keepFilePath)
	row, err := store.GetBookFileByPath(path)
	if err != nil || row == nil {
		t.Fatalf("GetBookFileByPath after first scan: row=%v err=%v", row, err)
	}
	intro, status := "This is the old recording.", "ok"
	row.Duration = duration
	row.AcoustIDFingerprint = []byte{1, 2, 3, 4}
	row.IntroTranscription = &intro
	row.TranscribeStatus = &status
	if err := store.UpdateBookFile(row.ID, row); err != nil {
		t.Fatalf("UpdateBookFile: %v", err)
	}
	oldBook.FilePath = filepath.Join(filepath.Dir(path), "moved-away")
	if _, err := store.UpdateBook(oldBook.ID, oldBook); err != nil {
		t.Fatalf("UpdateBook old: %v", err)
	}
	if _, err := store.CreateBook(&database.Book{FilePath: path, Title: "Replaced At Same Path"}); err != nil {
		t.Fatalf("CreateBook new: %v", err)
	}
	seeded, _ := store.GetBookFileByPath(path)
	return *seeded
}

// Review round 3, item 1, through the real scanner path: a file replaced at the
// same path by a recording of a different length must lose the old recording's
// fingerprint, transcript and duration, and carry the new real duration. A
// re-encode of the same length (different bytes, same audio length) keeps them.
func TestCreateBookFilesForBook_SamePathReplacement(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not on PATH")
	}

	rescan := func(t *testing.T, store *database.PebbleStore, path string) database.BookFile {
		t.Helper()
		createBookFilesForBook(path, []string{path}, logger.New("test"), keepFilePath)
		row, err := store.GetBookFileByPath(path)
		if err != nil || row == nil {
			t.Fatalf("GetBookFileByPath after rescan: row=%v err=%v", row, err)
		}
		return *row
	}

	t.Run("different duration drops audio-derived data", func(t *testing.T) {
		store, cleanup := setupPebbleStore(t)
		defer cleanup()
		SetStore(store)
		defer SetStore(nil)

		path := filepath.Join(t.TempDir(), "book.mp3")
		writeSilentMP3(t, ffmpeg, path, 4, "64k")
		first := seedOwnedRowWithAudioData(t, store, path, 4)
		if first.OriginalFileHashKind != database.FileHashKindSampled {
			t.Fatalf("scanner row OriginalFileHashKind = %q, want %q", first.OriginalFileHashKind, database.FileHashKindSampled)
		}

		writeSilentMP3(t, ffmpeg, path, 9, "64k")
		after := rescan(t, store, path)

		if after.ID != first.ID {
			t.Fatalf("the rescan created a new row (%s) instead of merging with the stored one (%s)", after.ID, first.ID)
		}
		newHash, _ := filehash.BookFileHash(path)
		if after.FileHash != newHash {
			t.Errorf("FileHash = %q, want the replacement's %q", after.FileHash, newHash)
		}
		if after.OriginalFileHash != first.OriginalFileHash {
			t.Errorf("OriginalFileHash = %q, want the first-seen %q", after.OriginalFileHash, first.OriginalFileHash)
		}
		if len(after.AcoustIDFingerprint) != 0 || after.IntroTranscription != nil || after.TranscribeStatus != nil {
			t.Errorf("the old recording's fingerprint/transcript survived a replacement: fp=%v intro=%v status=%v",
				after.AcoustIDFingerprint, after.IntroTranscription, after.TranscribeStatus)
		}
		if after.Duration < 8 || after.Duration > 10 {
			t.Errorf("Duration = %d, want the replacement's probed ~9s", after.Duration)
		}
	})

	t.Run("same duration re-encode keeps audio-derived data", func(t *testing.T) {
		store, cleanup := setupPebbleStore(t)
		defer cleanup()
		SetStore(store)
		defer SetStore(nil)

		path := filepath.Join(t.TempDir(), "book.mp3")
		writeSilentMP3(t, ffmpeg, path, 4, "64k")
		first := seedOwnedRowWithAudioData(t, store, path, 4)

		writeSilentMP3(t, ffmpeg, path, 4, "128k")
		if h, _ := filehash.BookFileHash(path); h == first.FileHash {
			t.Fatal("fixture error: the re-encode did not change the bytes")
		}
		after := rescan(t, store, path)

		if after.ID != first.ID {
			t.Fatalf("the rescan created a new row (%s) instead of merging with the stored one (%s)", after.ID, first.ID)
		}
		if newHash, _ := filehash.BookFileHash(path); after.FileHash != newHash {
			t.Errorf("FileHash = %q, want the re-encode's %q (the merge did not run)", after.FileHash, newHash)
		}
		if len(after.AcoustIDFingerprint) == 0 || after.IntroTranscription == nil || after.TranscribeStatus == nil {
			t.Errorf("a same-length re-encode lost its fingerprint/transcript: fp=%v intro=%v status=%v",
				after.AcoustIDFingerprint, after.IntroTranscription, after.TranscribeStatus)
		}
	})
}
