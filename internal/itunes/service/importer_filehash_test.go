// file: internal/itunes/service/importer_filehash_test.go
// version: 1.0.0
// guid: 5e37b0c9-4a12-4d68-91f7-0b6c2ad83e14
// last-edited: 2026-09-01

package itunesservice

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/filehash"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
)

// writeSparseTrack creates a file above filehash.Threshold whose middle is a
// hole — no disk blocks, no minutes spent writing 100 MB.
//
// The size is the whole point. At or below the threshold the canonical digest
// and a whole-file SHA-256 are the same string, and the first 1 MB of a file
// under 1 MB is the whole file, so a small fixture cannot tell ANY of the three
// algorithms apart and would pass against every one of them.
func writeSparseTrack(t *testing.T, path string, size int64) {
	t.Helper()
	if size <= filehash.Threshold {
		t.Fatalf("fixture size %d is not above filehash.Threshold %d", size, int64(filehash.Threshold))
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString("HEAD-MARKER-track"); err != nil {
		t.Fatalf("write head: %v", err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := f.Seek(size-int64(len("TAIL-MARKER-track")), io.SeekStart); err != nil {
		t.Fatalf("seek: %v", err)
	}
	if _, err := f.WriteString("TAIL-MARKER-track"); err != nil {
		t.Fatalf("write tail: %v", err)
	}
}

// TestCanonicalTrackFileHash pins the algorithm the iTunes importer stores in
// book_files.file_hash, against BOTH wrong answers that column has held.
//
// The importer used to write scanner.ComputeSegmentFileHash — a SHA-256 of only
// the first 1 MB. That never equals the scanner's own value for a real
// audiobook, so dedup's exact-file collector (Confidence 1.0 on a match)
// silently found nothing; and two tracks sharing a 1 MB opening collide on it,
// which asserts a duplicate at certainty.
func TestCanonicalTrackFileHash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Track 01.m4b")
	writeSparseTrack(t, path, filehash.Threshold+(1<<20))

	got, err := canonicalTrackFileHash(path)
	if err != nil {
		t.Fatalf("canonicalTrackFileHash: %v", err)
	}

	want, err := filehash.BookFileHash(path)
	if err != nil {
		t.Fatalf("BookFileHash: %v", err)
	}
	if got != want {
		t.Errorf("hash = %q, want the canonical chunked digest %q", got, want)
	}

	segment, err := scanner.ComputeSegmentFileHash(path)
	if err != nil {
		t.Fatalf("ComputeSegmentFileHash: %v", err)
	}
	if got == segment {
		t.Errorf("hash is a first-1MB segment digest (%q); that value never matches a scanner-written row and collides across tracks with a shared opening", got)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if whole := hex.EncodeToString(h.Sum(nil)); got == whole {
		t.Errorf("hash is a whole-file SHA-256 (%q); above 100 MB that disagrees with every scanner-written row", got)
	}
}

// TestCanonicalTrackFileHash_MissingFile keeps the error path honest: the
// importer only stamps the field when this returns nil, so a swallowed error
// here would mean a silently unhashed row.
func TestCanonicalTrackFileHash_MissingFile(t *testing.T) {
	if _, err := canonicalTrackFileHash(filepath.Join(t.TempDir(), "gone.m4b")); err == nil {
		t.Error("expected an error for a missing file")
	}
}
