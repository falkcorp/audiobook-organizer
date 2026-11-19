//go:build unix

// file: core/library/reportfile_unix.go
// version: 1.0.0
// guid: d53ba4e1-f407-4ef6-84d3-1f92ebbe4a44
// last-edited: 2026-11-18

package library

import (
	"os"
	"syscall"
)

// linkCount is how many names the file has, from the stat structure; ok is false
// when it cannot be read.
func linkCount(info os.FileInfo) (n uint64, ok bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Nlink), true // the field is 16 bits on darwin, 64 on linux
}
