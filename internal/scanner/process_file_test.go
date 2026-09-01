// file: internal/scanner/process_file_test.go
// version: 1.1.0
// guid: b2c3d4e5-f6a7-8901-bcde-f12345678901
// last-edited: 2026-09-01

package scanner

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/filehash"
)

// testdataDir returns the absolute path to the project testdata/fixtures directory.
func testdataDir(t *testing.T) string {
	t.Helper()
	// The test binary runs with the package directory as cwd, but the testdata
	// dir is two levels up (internal/scanner → repo root).
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
	return filepath.Join(repoRoot, "testdata", "fixtures")
}

func TestProcessFile_EmptyPath(t *testing.T) {
	_, _, _, err := ProcessFile("")
	if err == nil {
		t.Fatal("expected error for empty path, got nil")
	}
}

func TestProcessFile_NonExistentFile(t *testing.T) {
	_, _, _, err := ProcessFile("/tmp/audiobook-organizer-nonexistent-file-xyz.mp3")
	if err == nil {
		t.Fatal("expected error for nonexistent file, got nil")
	}
}

func TestProcessFile_Directory(t *testing.T) {
	dir := t.TempDir()

	meta, mi, hash, err := ProcessFile(dir)
	if err != nil {
		t.Fatalf("expected no error for directory, got: %v", err)
	}
	if meta == nil {
		t.Fatal("expected non-nil metadata for directory, got nil")
	}
	if mi != nil {
		t.Fatalf("expected nil mediainfo for directory, got: %+v", mi)
	}
	if hash != "" {
		t.Fatalf("expected empty hash for directory, got: %q", hash)
	}
}

func TestProcessFile_MP3(t *testing.T) {
	fixtures := testdataDir(t)
	mp3Path := filepath.Join(fixtures, "test_sample.mp3")
	if _, err := os.Stat(mp3Path); os.IsNotExist(err) {
		t.Skipf("test fixture not found at %s, skipping", mp3Path)
	}

	meta, mi, hash, err := ProcessFile(mp3Path)
	if err != nil {
		t.Fatalf("ProcessFile(%q) returned error: %v", mp3Path, err)
	}
	if meta == nil {
		t.Fatal("expected non-nil metadata")
	}
	if mi == nil {
		t.Fatal("expected non-nil mediainfo for MP3")
	}
	if hash == "" {
		t.Fatal("expected non-empty hash for MP3")
	}
	if len(hash) != 64 {
		t.Fatalf("expected 64-char SHA-256 hex hash, got %d chars: %q", len(hash), hash)
	}
}

func TestProcessFile_M4B(t *testing.T) {
	fixtures := testdataDir(t)
	m4bPath := filepath.Join(fixtures, "test_sample.m4b")
	if _, err := os.Stat(m4bPath); os.IsNotExist(err) {
		t.Skipf("test fixture not found at %s, skipping", m4bPath)
	}

	meta, mi, hash, err := ProcessFile(m4bPath)
	if err != nil {
		t.Fatalf("ProcessFile(%q) returned error: %v", m4bPath, err)
	}
	if meta == nil {
		t.Fatal("expected non-nil metadata")
	}
	if mi == nil {
		t.Fatal("expected non-nil mediainfo for M4B")
	}
	if hash == "" {
		t.Fatal("expected non-empty hash for M4B")
	}
}

func TestProcessFile_FLAC(t *testing.T) {
	fixtures := testdataDir(t)
	flacPath := filepath.Join(fixtures, "test_sample.flac")
	if _, err := os.Stat(flacPath); os.IsNotExist(err) {
		t.Skipf("test fixture not found at %s, skipping", flacPath)
	}

	meta, mi, hash, err := ProcessFile(flacPath)
	if err != nil {
		t.Fatalf("ProcessFile(%q) returned error: %v", flacPath, err)
	}
	if meta == nil {
		t.Fatal("expected non-nil metadata")
	}
	if mi == nil {
		t.Fatal("expected non-nil mediainfo for FLAC")
	}
	if hash == "" {
		t.Fatal("expected non-empty hash for FLAC")
	}
}

// TestProcessFile_HashConsistency verifies that ProcessFile produces the same
// hash as ComputeFileHash for the same file.
func TestProcessFile_HashConsistency(t *testing.T) {
	fixtures := testdataDir(t)
	mp3Path := filepath.Join(fixtures, "test_sample.mp3")
	if _, err := os.Stat(mp3Path); os.IsNotExist(err) {
		t.Skipf("test fixture not found at %s, skipping", mp3Path)
	}

	_, _, hashFromProcessFile, err := ProcessFile(mp3Path)
	if err != nil {
		t.Fatalf("ProcessFile error: %v", err)
	}

	hashFromComputeFileHash, err := ComputeFileHash(mp3Path)
	if err != nil {
		t.Fatalf("ComputeFileHash error: %v", err)
	}

	if hashFromProcessFile != hashFromComputeFileHash {
		t.Fatalf("hash mismatch: ProcessFile=%q, ComputeFileHash=%q", hashFromProcessFile, hashFromComputeFileHash)
	}
}

// TestComputeHashFromReader_MatchesCanonicalAboveThreshold guards the scanner's
// single-pass reader path at a file size where it can actually be wrong.
//
// TestProcessFile_HashConsistency above cross-checks the two entry points on a
// small fixture — and below filehash.Threshold the chunked and whole-file
// strategies produce the SAME string, so that test passes against either
// algorithm and cannot observe a swap. Measured: replacing the reader path with
// a plain io.Copy into SHA-256 left the whole scanner package green.
//
// The fixture is sparse: a hole costs no disk blocks on APFS or ext4, so a
// >100 MB file is cheap to make and nothing like 100 MB is written.
func TestComputeHashFromReader_MatchesCanonicalAboveThreshold(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.m4b")
	const size = int64(filehash.Threshold) + (1 << 20)

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := f.WriteString("HEAD-MARKER-scan"); err != nil {
		t.Fatalf("write head: %v", err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := f.Seek(size-int64(len("TAIL-MARKER-scan")), io.SeekStart); err != nil {
		t.Fatalf("seek: %v", err)
	}
	if _, err := f.WriteString("TAIL-MARKER-scan"); err != nil {
		t.Fatalf("write tail: %v", err)
	}
	f.Close()

	rf, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer rf.Close()
	got, err := computeHashFromReader(rf, size)
	if err != nil {
		t.Fatalf("computeHashFromReader: %v", err)
	}

	want, err := ComputeFileHash(path)
	if err != nil {
		t.Fatalf("ComputeFileHash: %v", err)
	}
	if got != want {
		t.Errorf("reader path = %q, path entry point = %q; both write book_files.file_hash and must agree", got, want)
	}

	// And it must not be a whole-file digest, which is what a swapped
	// implementation would return.
	wf, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer wf.Close()
	h := sha256.New()
	if _, err := io.Copy(h, wf); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if whole := hex.EncodeToString(h.Sum(nil)); got == whole {
		t.Errorf("reader path returned a whole-file SHA-256 above filehash.Threshold")
	}
}
