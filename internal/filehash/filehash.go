// file: internal/filehash/filehash.go
// version: 1.2.0
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
	//
	// This constant IS the control that closed SEC-AUDIT-7c (uncontrolled
	// allocation, PR #768). That control used to be spelled
	// scanner.MaxScanBufferBytes, defined next to the only buffer it bounded.
	// When the algorithm moved here the buffer came with it, so the name went
	// too rather than being left behind pointing at a make() that no longer
	// exists — a named bound with no allocation under it reads as a live
	// control while guarding nothing.
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
// It seeks f to the start itself and seeks while hashing, so f must be
// seekable and the caller must not assume any particular offset afterwards.
// It does NOT hash from f's current offset: an earlier draft did, which made a
// caller that had already read a tag header get a silent partial digest with no
// error. The offset is the caller's business right up until it decides the
// contents of an identity column.
//
// size is trusted for the digest but not for locating the tail window — the
// tail is read at size-ChunkSize from the START, not backwards from the current
// end. Those differ if the file is appended to between the caller's stat and
// this call, and taking the tail from SeekEnd would pair a window from the new
// file with the size of the old one.
func BookFileHashFromFile(f *os.File, size int64) (string, error) {
	h := sha256.New()

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}

	if size <= Threshold {
		if _, err := io.Copy(h, f); err != nil {
			return "", err
		}
		return hex.EncodeToString(h.Sum(nil)), nil
	}

	if err := writeChunk(h, f); err != nil {
		return "", err
	}
	{
		// A discarded Seek error would hash the wrong window and poison dedup
		// (audit 2026-07-17 DL-4).
		if _, err := f.Seek(size-ChunkSize, io.SeekStart); err != nil {
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

// writeChunk reads exactly ChunkSize bytes from f and folds them into h.
//
// A short read here is an ERROR, not a tolerance, and the reasoning matters
// because the obvious reading is the opposite. writeChunk is reached only from
// the size > Threshold branch, and Threshold (100 MB) is an order of magnitude
// larger than ChunkSize (10 MB) — so whenever it runs, both windows are fully
// backed by bytes on disk. A short read therefore never means "that is all the
// bytes there are". It means the read was truncated: a signal arrived
// mid-transfer, or the file lives on NFS/SMB/FUSE, which is what a NAS-backed
// library is.
//
// Folding a truncated window into the digest yields a well-formed 64-hex string
// that is both wrong and NOT REPRODUCIBLE — hash the same unchanged file twice
// and get two values, in the column internal/dedup/collectors_exact.go reads at
// Confidence 1.0. An earlier draft of this package tolerated the short read on
// the grounds of staying byte-compatible with hashes already in production.
// That reasoning was wrong twice over: a filling read agrees with a single
// Read on every file whose digest is reproducible at all, so nothing
// legitimate changes; and where they disagree, the stored value is an artifact
// of one run's read boundary, which is the defect rather than the baseline.
//
// The size-consistency argument settles it independently: if a short read here
// somehow DID mean the file ended early, then size — already baked into the
// digest by the caller — describes a file that no longer exists, and the digest
// is wrong no matter how many bytes were hashed. Refusing is the only answer
// that cannot be silently wrong.
func writeChunk(h hash.Hash, f *os.File) error {
	buf := make([]byte, ChunkSize)
	if _, err := io.ReadFull(f, buf); err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return fmt.Errorf("filehash: short read of a %d-byte window on a file "+
				"large enough to back it; the file shrank or the read was truncated: %w", ChunkSize, err)
		}
		return err
	}
	h.Write(buf)
	return nil
}
