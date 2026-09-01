// file: internal/fileops/hash.go
// version: 1.1.0
// guid: 0a1b2c3d-4e5f-6a7b-8c9d-0e1f2a3b4c5d
// last-edited: 2026-09-01

package fileops

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

// ComputeFileHashAndSize is the canonical whole-file digest for this codebase.
// The single-return ComputeFileHash wrapper that used to sit here is gone: it
// had zero non-test callers, and a function of that name next to
// filehash.BookFileHash is exactly how a writer of book_files.file_hash reaches
// for the wrong algorithm. Use this for verifying bytes survived a mutation;
// use filehash.BookFileHash for row identity.
//
// It returns the whole-file SHA-256 and the size, both read from a single open
// handle.
//
// Callers that want both must use this rather than hashing and then calling
// os.Stat. Two reasons, and the second is the one that matters:
//
//  1. It is one open instead of two path lookups.
//  2. The size and the hash then describe the SAME bytes. A separate stat can
//     land either side of a concurrent write, so the pair it produces may
//     describe a file that never existed — which is a poor foundation for a
//     provenance record whose whole job is to be true.
func ComputeFileHashAndSize(filePath string) (string, int64, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()

	// (*os.File).Stat operates on the open descriptor, not on the path, so it
	// cannot resolve to a different file than the one being hashed.
	info, err := file.Stat()
	if err != nil {
		return "", 0, err
	}

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", 0, err
	}

	return hex.EncodeToString(hasher.Sum(nil)), info.Size(), nil
}

// GetFileSize returns the size of a file in bytes
func GetFileSize(filePath string) (int64, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}
