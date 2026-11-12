// file: core/internal/libfs/stamp_other.go
// version: 1.0.0
// guid: 1ca313ca-6f1e-42ed-81a1-9046d035cfd5
// last-edited: 2026-11-12

//go:build !unix

package libfs

import "os"

// FactsOf on a platform without inode numbers reports what the descriptor's stat
// has: size, modification time and whether it is a regular file.
func FactsOf(f *os.File) (FileFacts, error) {
	st, err := f.Stat()
	if err != nil {
		return FileFacts{}, err
	}
	return FileFacts{Size: st.Size(), MTime: st.ModTime().UnixNano(), Regular: st.Mode().IsRegular()}, nil
}
