// file: internal/filehash/filehash.go
// version: 1.0.0
// guid: 6b4c9e21-7f0a-4d63-9c18-2a5e8b7d0f41
// last-edited: 2026-09-01

// Package filehash owns the ONE hash algorithm that identifies an audio file
// in this database.
//
// # Why this package exists
//
// `book_files.file_hash` (and the book-level `books.file_hash`) are an IDENTITY
// column: `internal/dedup/collectors_exact.go` emits a SigExactFile signal at
// Confidence 1.0 — certainty — whenever two books share a value in it. A
// confidence of 1.0 is only defensible if every row in the column was produced
// by the same function. Two byte-identical files hashed by two different
// algorithms yield two different strings, and the collector then never fires:
// the duplicate is silently NOT found, with no error anywhere.
//
// That had happened three times over. `internal/scanner` wrote the chunked
// digest below; `internal/versions` and `internal/plugins/maintenance` wrote a
// plain whole-file SHA-256; `internal/itunes/service` wrote a SHA-256 of only
// the first 1 MB. All four values landed in the same column.
//
// So the algorithm lives here, in a leaf package with no internal
// dependencies, and every writer of that column calls BookFileHash or
// BookFileHashFromFile. The package is deliberately NOT `internal/fileops`:
// fileops owns the whole-file digest (fileops.ComputeFileHashAndSize), and
// putting the two side by side is precisely the adjacency that let a writer
// reach for the wrong one.
//
// # Which hash to use
//
//   - Identity of a row in book_files / books  ->  filehash.BookFileHash
//   - Verifying bytes survived a mutation      ->  fileops.ComputeFileHashAndSize
//
// The two are equal for files at or below Threshold and differ above it. A
// test that only exercises a small file therefore cannot tell them apart, and
// cannot observe this class of bug at all.
package filehash

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
)

const (
	// Threshold is the file size above which BookFileHash switches from a
	// whole-file digest to the chunked one. Files at or below it are hashed in
	// full, so the two strategies agree there.
	Threshold = 100 * 1024 * 1024 // 100 MB

	// ChunkSize is how many bytes are read from each end of a file larger than
	// Threshold. It is a constant so that every make([]byte, ChunkSize) in this
	// package carries a compile-time allocation bound (CodeQL verifies this
	// statically; a runtime-configurable size would reopen that finding).
	ChunkSize = 10 * 1024 * 1024 // 10 MB
)

// BookFileHash returns the canonical identity digest for the file at path.
//
// For a file larger than Threshold the digest is
// SHA-256(first ChunkSize bytes ‖ last ChunkSize bytes ‖ decimal size); for
// anything smaller it is the SHA-256 of the whole file. Including the size is
// what keeps two files that share both end chunks but differ in the middle
// from colliding on length alone.
//
// This is the value that belongs in book_files.file_hash and books.file_hash.
// Nothing else does.
func BookFileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	// Stat the open descriptor rather than the path: the size then describes
	// the same bytes that are about to be hashed, even under a concurrent
	// rename or write.
	info, err := f.Stat()
	if err != nil {
		return "", err
	}

	return BookFileHashFromFile(f, info.Size())
}

// BookFileHashFromFile is BookFileHash over an already-open handle whose size
// the caller has determined, for callers that must not pay a second open.
//
// It seeks, so f must be seekable and the caller must not assume any
// particular offset afterwards. Hashing starts from f's CURRENT offset, so a
// caller that has already read from f must seek to the start first.
func BookFileHashFromFile(f *os.File, size int64) (string, error) {
	h := sha256.New()

	if size <= Threshold {
		if _, err := io.Copy(h, f); err != nil {
			return "", err
		}
		return hex.EncodeToString(h.Sum(nil)), nil
	}

	if err := writeChunk(h, f); err != nil {
		return "", err
	}
	if size > ChunkSize {
		// A discarded Seek error would hash the wrong window and poison dedup
		// (audit 2026-07-17 DL-4).
		if _, err := f.Seek(-ChunkSize, io.SeekEnd); err != nil {
			return "", err
		}
		if err := writeChunk(h, f); err != nil {
			return "", err
		}
	}
	// The size is part of the digest, not decoration: without it, two files
	// sharing both end chunks hash identically however much they differ in
	// between.
	h.Write([]byte(fmt.Sprintf("%d", size)))

	return hex.EncodeToString(h.Sum(nil)), nil
}

// writeChunk reads up to ChunkSize bytes from f and folds them into h.
// A short read is not an error — only however many bytes exist are hashed.
func writeChunk(h hash.Hash, f *os.File) error {
	buf := make([]byte, ChunkSize)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		return err
	}
	h.Write(buf[:n])
	return nil
}
