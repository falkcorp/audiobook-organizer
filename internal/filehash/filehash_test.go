// file: internal/filehash/filehash_test.go
// version: 1.0.0
// guid: 9d2e7f04-1a63-4b58-8e70-c3f5a916d84b
// last-edited: 2026-09-01

package filehash

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// writeSparse creates a file of exactly size bytes with distinct head and tail
// markers and a hole in between.
//
// The hole is the point: it costs no disk blocks on APFS or ext4, so a fixture
// above the 100 MB threshold is cheap. The markers are the other point — a
// digest that reads the wrong window, not merely the wrong number of bytes, is
// caught too.
func writeSparse(t *testing.T, path string, size int64, head, tail string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString(head); err != nil {
		t.Fatalf("write head: %v", err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := f.Seek(size-int64(len(tail)), io.SeekStart); err != nil {
		t.Fatalf("seek: %v", err)
	}
	if _, err := f.WriteString(tail); err != nil {
		t.Fatalf("write tail: %v", err)
	}
}

func wholeFileSHA256(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatalf("copy: %v", err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// TestBookFileHash_BelowThresholdIsWholeFile pins the small-file branch, and
// records why every other test here uses a LARGE fixture: below the threshold
// the canonical digest and a plain whole-file SHA-256 are the same string, so a
// small fixture cannot distinguish them and cannot observe an algorithm swap.
func TestBookFileHash_BelowThresholdIsWholeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "small.m4b")
	if err := os.WriteFile(path, []byte("a modest amount of audio"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := BookFileHash(path)
	if err != nil {
		t.Fatalf("BookFileHash: %v", err)
	}
	if want := wholeFileSHA256(t, path); got != want {
		t.Errorf("BookFileHash = %q, want whole-file SHA-256 %q", got, want)
	}
}

// TestBookFileHash_AboveThresholdIsChunked is the assertion the whole package
// exists for: above Threshold the digest is the chunked one and NOT a
// whole-file SHA-256. Any writer of book_files.file_hash that produces the
// latter is silently invisible to dedup's exact-file collector.
func TestBookFileHash_AboveThresholdIsChunked(t *testing.T) {
	const size = int64(Threshold) + (1 << 20)
	path := filepath.Join(t.TempDir(), "big.m4b")
	writeSparse(t, path, size, "HEAD-A", "TAIL-A")

	got, err := BookFileHash(path)
	if err != nil {
		t.Fatalf("BookFileHash: %v", err)
	}

	// Expected value derived independently of the implementation: SHA-256 over
	// the first ChunkSize bytes, the last ChunkSize bytes, and the decimal size.
	//
	// This derivation uses io.ReadFull where the implementation uses a single
	// Read. That asymmetry is deliberate and must NOT be "fixed" by changing the
	// implementation: a single Read is what the original scanner code did, and
	// every hash already stored in production was produced that way, so
	// switching to ReadFull would silently change values for any file where a
	// short read occurs. The fixture here is a plain regular file at a known
	// offset, where a short read cannot happen, so the two forms agree.
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	h := sha256.New()
	first := make([]byte, ChunkSize)
	if _, err := io.ReadFull(f, first); err != nil {
		t.Fatalf("read first: %v", err)
	}
	h.Write(first)
	if _, err := f.Seek(-int64(ChunkSize), io.SeekEnd); err != nil {
		t.Fatalf("seek: %v", err)
	}
	last := make([]byte, ChunkSize)
	if _, err := io.ReadFull(f, last); err != nil {
		t.Fatalf("read last: %v", err)
	}
	h.Write(last)
	h.Write([]byte(fmt.Sprintf("%d", size)))
	want := hex.EncodeToString(h.Sum(nil))

	if got != want {
		t.Errorf("BookFileHash = %q, want chunked digest %q", got, want)
	}
	if whole := wholeFileSHA256(t, path); got == whole {
		t.Errorf("BookFileHash returned a whole-file SHA-256 above Threshold; the chunked strategy is not being applied")
	}
}

// TestBookFileHash_SizeIsPartOfTheDigest guards the size suffix. Two files that
// share both end chunks but differ in length must not collide — without the
// suffix they would, and dedup would assert Confidence 1.0 on two different
// books.
func TestBookFileHash_SizeIsPartOfTheDigest(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.m4b")
	b := filepath.Join(dir, "b.m4b")
	writeSparse(t, a, int64(Threshold)+(1<<20), "HEAD-A", "TAIL-A")
	writeSparse(t, b, int64(Threshold)+(2<<20), "HEAD-A", "TAIL-A")

	ha, err := BookFileHash(a)
	if err != nil {
		t.Fatalf("BookFileHash(a): %v", err)
	}
	hb, err := BookFileHash(b)
	if err != nil {
		t.Fatalf("BookFileHash(b): %v", err)
	}
	if ha == hb {
		t.Error("files with identical end chunks but different sizes hashed the same; the size is not in the digest")
	}
}

// TestBookFileHash_DetectsHeadAndTailChanges proves the digest actually reads
// both windows rather than one of them.
func TestBookFileHash_DetectsHeadAndTailChanges(t *testing.T) {
	const size = int64(Threshold) + (1 << 20)
	dir := t.TempDir()

	base := filepath.Join(dir, "base.m4b")
	headDiff := filepath.Join(dir, "head.m4b")
	tailDiff := filepath.Join(dir, "tail.m4b")
	writeSparse(t, base, size, "HEAD-A", "TAIL-A")
	writeSparse(t, headDiff, size, "HEAD-B", "TAIL-A")
	writeSparse(t, tailDiff, size, "HEAD-A", "TAIL-B")

	hBase, err := BookFileHash(base)
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	hHead, err := BookFileHash(headDiff)
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	hTail, err := BookFileHash(tailDiff)
	if err != nil {
		t.Fatalf("tail: %v", err)
	}
	if hBase == hHead {
		t.Error("a changed first chunk did not change the digest")
	}
	if hBase == hTail {
		t.Error("a changed last chunk did not change the digest")
	}
}

// TestBookFileHashFromFile_MatchesPathVariant keeps the two entry points from
// drifting: internal/scanner's single-pass ProcessFile uses the handle variant
// and everything else uses the path variant, and rows from both land in the
// same column.
func TestBookFileHashFromFile_MatchesPathVariant(t *testing.T) {
	for _, size := range []int64{1 << 10, int64(Threshold) + (1 << 20)} {
		path := filepath.Join(t.TempDir(), "x.m4b")
		writeSparse(t, path, size, "HEAD-A", "TAIL-A")

		viaPath, err := BookFileHash(path)
		if err != nil {
			t.Fatalf("BookFileHash: %v", err)
		}

		f, err := os.Open(path)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		viaHandle, err := BookFileHashFromFile(f, size)
		f.Close()
		if err != nil {
			t.Fatalf("BookFileHashFromFile: %v", err)
		}
		if viaPath != viaHandle {
			t.Errorf("size %d: path variant = %q, handle variant = %q", size, viaPath, viaHandle)
		}
	}
}

func TestBookFileHash_NonexistentFile(t *testing.T) {
	if _, err := BookFileHash(filepath.Join(t.TempDir(), "nope.m4b")); err == nil {
		t.Error("expected an error for a missing file")
	}
}
