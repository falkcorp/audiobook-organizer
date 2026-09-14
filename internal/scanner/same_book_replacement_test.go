// file: internal/scanner/same_book_replacement_test.go
// version: 1.0.0
// guid: 4f0b7c2e-81d3-4a96-b5e7-2c9d0a6f3e18
// last-edited: 2026-09-14

package scanner

import (
	"bytes"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/filehash"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// countingHashScanner counts ComputeFileHash calls and returns the real digest.
// Everything else comes from mockScannerImpl.
type countingHashScanner struct {
	mockScannerImpl
	hashes atomic.Int64
}

func (c *countingHashScanner) ComputeFileHash(filePath string) (string, error) {
	c.hashes.Add(1)
	return filehash.BookFileHash(filePath)
}

// installHashCounter routes ComputeFileHash through a counter for the rest of
// the test.
func installHashCounter(t *testing.T) *countingHashScanner {
	t.Helper()
	c := &countingHashScanner{}
	SetScanner(c)
	t.Cleanup(func() { SetScanner(nil) })
	return c
}

// seedSameBookRow imports path as a single-file book through the real scanner
// path, then gives its row the placement and audio-derived data a backfill
// would have added. The book keeps owning the row, so the next
// createBookFilesForBook call is a same-book rescan.
func seedSameBookRow(t *testing.T, store *database.PebbleStore, path string, content []byte) database.BookFile {
	t.Helper()
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	// Backdate the file so its mtime is unambiguously older than the row.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	if _, err := store.CreateBook(&database.Book{FilePath: path, Title: "Same Book"}); err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	createBookFilesForBook(path, []string{path}, logger.New("test"), keepFilePath)
	row, err := store.GetBookFileByPath(path)
	if err != nil || row == nil {
		t.Fatalf("GetBookFileByPath after first scan: row=%v err=%v", row, err)
	}
	intro, status := "This is the old recording.", "ok"
	row.Duration = 120
	row.Codec = "mp3"
	row.BitrateKbps = 64
	row.TrackNumber = 3
	row.DiscNumber = 2
	row.AcoustIDFingerprint = []byte{1, 2, 3, 4}
	row.IntroTranscription = &intro
	row.TranscribeStatus = &status
	if err := store.UpdateBookFile(row.ID, row); err != nil {
		t.Fatalf("UpdateBookFile: %v", err)
	}
	seeded, _ := store.GetBookFileByPath(path)
	return *seeded
}

// stampScanCache records the file's current mtime and size as the row's
// scan-cache stamp, as the single-file mirror of UpdateScanCache would.
func stampScanCache(t *testing.T, store *database.PebbleStore, row database.BookFile, path string) database.BookFile {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	mt, sz := fi.ModTime().Unix(), fi.Size()
	row.LastScanMtime, row.LastScanSize = &mt, &sz
	if err := store.UpdateBookFile(row.ID, &row); err != nil {
		t.Fatalf("stamp: %v", err)
	}
	stamped, _ := store.GetBookFileByPath(path)
	return *stamped
}

func rescanSameBook(t *testing.T, store *database.PebbleStore, path string) database.BookFile {
	t.Helper()
	createBookFilesForBook(path, []string{path}, logger.New("test"), keepFilePath)
	row, err := store.GetBookFileByPath(path)
	if err != nil || row == nil {
		t.Fatalf("GetBookFileByPath after rescan: row=%v err=%v", row, err)
	}
	return *row
}

func hasAudioDerived(bf database.BookFile) bool {
	return len(bf.AcoustIDFingerprint) != 0 || bf.IntroTranscription != nil || bf.TranscribeStatus != nil
}

// A file replaced in place under a book that already owns its row must be
// re-checked on rescan: the row takes the new bytes' hash and size, and the old
// recording's fingerprint, transcript and duration are dropped through the
// merge. On main the rescan returned early and none of this happened.
func TestCreateBookFilesForBook_SameBookReplacementRefreshesRow(t *testing.T) {
	cases := []struct {
		name        string
		replacement []byte
		stamp       bool
	}{
		{"different size", bytes.Repeat([]byte("new edition "), 5000), false},
		// Same size, so only the scan-cache mtime stamp flags it.
		{"same size, stamped row", bytes.Repeat([]byte("B"), 40000), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, cleanup := setupPebbleStore(t)
			defer cleanup()
			SetStore(store)
			defer SetStore(nil)

			path := filepath.Join(t.TempDir(), "book.mp3")
			first := seedSameBookRow(t, store, path, bytes.Repeat([]byte("A"), 40000))
			if !hasAudioDerived(first) {
				t.Fatal("fixture error: seeded row has no audio-derived data")
			}
			if tc.stamp {
				first = stampScanCache(t, store, first, path)
			}

			if err := os.WriteFile(path, tc.replacement, 0o644); err != nil {
				t.Fatalf("replace: %v", err)
			}
			future := time.Now().Add(time.Minute)
			if err := os.Chtimes(path, future, future); err != nil {
				t.Fatalf("chtimes: %v", err)
			}
			counter := installHashCounter(t)
			after := rescanSameBook(t, store, path)

			if after.ID != first.ID || after.BookID != first.BookID {
				t.Fatalf("row identity changed: %s/%s -> %s/%s", first.BookID, first.ID, after.BookID, after.ID)
			}
			newHash, _ := filehash.BookFileHash(path)
			if after.FileHash != newHash {
				t.Errorf("FileHash = %q, want the replacement's %q (stale hash kept)", after.FileHash, newHash)
			}
			if after.FileSize != int64(len(tc.replacement)) {
				t.Errorf("FileSize = %d, want %d", after.FileSize, len(tc.replacement))
			}
			if after.OriginalFileHash != first.OriginalFileHash {
				t.Errorf("OriginalFileHash = %q, want the first-seen %q", after.OriginalFileHash, first.OriginalFileHash)
			}
			if hasAudioDerived(after) {
				t.Errorf("old recording's fingerprint/transcript survived a replacement: fp=%v intro=%v status=%v",
					after.AcoustIDFingerprint, after.IntroTranscription, after.TranscribeStatus)
			}
			if after.Duration == 120 || after.BitrateKbps == 64 {
				t.Errorf("old stream info survived: duration=%d bitrate=%d", after.Duration, after.BitrateKbps)
			}
			if after.TrackNumber != 3 || after.DiscNumber != 2 {
				t.Errorf("placement wiped: track=%d disc=%d, want 3/2", after.TrackNumber, after.DiscNumber)
			}
			if n := counter.hashes.Load(); n != 1 {
				t.Errorf("ComputeFileHash calls = %d, want exactly 1 (the flagged file)", n)
			}
		})
	}
}

// An unchanged file must stay a stat: zero hashing, whether or not the row
// carries a scan-cache stamp, and nothing about the row changes. An unstamped
// row whose file was only touched (same size, newer mtime) is also not hashed:
// size is the only signal there, so touched segments of multi-file books are
// not re-hashed on every scan.
func TestCreateBookFilesForBook_SameBookUnchangedDoesNotHash(t *testing.T) {
	cases := []struct {
		name    string
		stamped bool
		touch   bool
	}{
		{"no scan-cache stamp", false, false},
		{"scan-cache stamp matches", true, false},
		{"no scan-cache stamp, touched", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, cleanup := setupPebbleStore(t)
			defer cleanup()
			SetStore(store)
			defer SetStore(nil)

			path := filepath.Join(t.TempDir(), "book.mp3")
			first := seedSameBookRow(t, store, path, bytes.Repeat([]byte("A"), 40000))
			if tc.stamped {
				first = stampScanCache(t, store, first, path)
			}
			if tc.touch {
				future := time.Now().Add(time.Minute)
				if err := os.Chtimes(path, future, future); err != nil {
					t.Fatalf("chtimes: %v", err)
				}
			}
			counter := installHashCounter(t)
			after := rescanSameBook(t, store, path)

			if n := counter.hashes.Load(); n != 0 {
				t.Errorf("ComputeFileHash calls = %d on an unchanged file, want 0", n)
			}
			if after.FileHash != first.FileHash || !hasAudioDerived(after) || after.Duration != 120 {
				t.Errorf("unchanged file's row changed: hash %q->%q audio=%v duration=%d",
					first.FileHash, after.FileHash, hasAudioDerived(after), after.Duration)
			}
		})
	}
}

// An mtime-only change (a touch, a copy that kept the bytes) is hashed once to
// decide, and with the same hash nothing is invalidated or rewritten.
func TestCreateBookFilesForBook_SameBookMtimeOnlyKeepsDerivedData(t *testing.T) {
	store, cleanup := setupPebbleStore(t)
	defer cleanup()
	SetStore(store)
	defer SetStore(nil)

	path := filepath.Join(t.TempDir(), "book.mp3")
	first := seedSameBookRow(t, store, path, bytes.Repeat([]byte("A"), 40000))
	first = stampScanCache(t, store, first, path)
	future := time.Now().Add(time.Minute)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	counter := installHashCounter(t)
	after := rescanSameBook(t, store, path)

	if n := counter.hashes.Load(); n != 1 {
		t.Errorf("ComputeFileHash calls = %d, want 1 (stat flagged the file, the hash cleared it)", n)
	}
	if after.FileHash != first.FileHash {
		t.Errorf("FileHash changed on an mtime-only change: %q -> %q", first.FileHash, after.FileHash)
	}
	if !hasAudioDerived(after) || after.Duration != 120 || after.Codec != "mp3" || after.BitrateKbps != 64 {
		t.Errorf("mtime-only change invalidated derived data: audio=%v duration=%d codec=%q bitrate=%d",
			hasAudioDerived(after), after.Duration, after.Codec, after.BitrateKbps)
	}
	if !after.UpdatedAt.Equal(first.UpdatedAt) {
		t.Errorf("row rewritten on an mtime-only change: UpdatedAt %v -> %v", first.UpdatedAt, after.UpdatedAt)
	}
}

// A hash the caller already computed (the dedup pass's SegmentHashes) is used
// instead of hashing again.
func TestCreateBookFilesForBook_SameBookReplacementUsesKnownHash(t *testing.T) {
	store, cleanup := setupPebbleStore(t)
	defer cleanup()
	SetStore(store)
	defer SetStore(nil)

	path := filepath.Join(t.TempDir(), "book.mp3")
	seedSameBookRow(t, store, path, bytes.Repeat([]byte("A"), 40000))
	replacement := bytes.Repeat([]byte("C"), 50000)
	if err := os.WriteFile(path, replacement, 0o644); err != nil {
		t.Fatal(err)
	}
	newHash, _ := filehash.BookFileHash(path)
	counter := installHashCounter(t)
	createBookFilesForBook(path, []string{path}, logger.New("test"), keepFilePath, map[string]string{path: newHash})
	after, _ := store.GetBookFileByPath(path)

	if n := counter.hashes.Load(); n != 0 {
		t.Errorf("ComputeFileHash calls = %d with the hash supplied, want 0", n)
	}
	if after == nil || after.FileHash != newHash || hasAudioDerived(*after) {
		t.Errorf("replacement not refreshed from the supplied hash: row=%+v", after)
	}
}

// gateStat is an os.FileInfo with a chosen size and mtime.
type gateStat struct {
	os.FileInfo
	size  int64
	mtime time.Time
}

func (f gateStat) Size() int64        { return f.size }
func (f gateStat) ModTime() time.Time { return f.mtime }

// looksChangedSinceStored is the gate that keeps unchanged files from being
// hashed. Pinned directly, because an integration test that counts zero hashes
// cannot tell a gate from a function that never looks.
func TestLooksChangedSinceStored(t *testing.T) {
	now := time.Now()
	stampM, stampS := now.Unix(), int64(1000)
	otherM := now.Add(time.Hour).Unix()
	otherS := int64(999)
	cases := []struct {
		name   string
		stored database.BookFile
		fi     gateStat
		want   bool
	}{
		{"unstamped, same size, older mtime", database.BookFile{FileSize: 1000, UpdatedAt: now}, gateStat{size: 1000, mtime: now.Add(-time.Hour)}, false},
		{"unstamped, same size, newer mtime", database.BookFile{FileSize: 1000, UpdatedAt: now}, gateStat{size: 1000, mtime: now.Add(time.Hour)}, false},
		{"unstamped, size differs", database.BookFile{FileSize: 1000}, gateStat{size: 1001, mtime: now}, true},
		{"unknown stored size, unstamped", database.BookFile{}, gateStat{size: 1001, mtime: now}, false},
		{"stamp matches", database.BookFile{FileSize: 1000, LastScanMtime: &stampM, LastScanSize: &stampS}, gateStat{size: 1000, mtime: time.Unix(stampM, 0)}, false},
		{"stamp mtime differs", database.BookFile{FileSize: 1000, LastScanMtime: &otherM, LastScanSize: &stampS}, gateStat{size: 1000, mtime: time.Unix(stampM, 0)}, true},
		{"stamp size differs", database.BookFile{LastScanMtime: &stampM, LastScanSize: &otherS}, gateStat{size: 1000, mtime: time.Unix(stampM, 0)}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := looksChangedSinceStored(&tc.stored, tc.fi); got != tc.want {
				t.Errorf("looksChangedSinceStored = %v, want %v", got, tc.want)
			}
		})
	}
}
