// file: internal/plugins/maintenance/fragment_filesig_darwin.go
// version: 1.0.0
// guid: 7f1d3b96-4e2a-4c58-b0d9-8a6e2c5f1b37
// last-edited: 2026-10-06

//go:build darwin

package maintenance

import (
	"os"
	"syscall"
)

// fileIdentity is the device, inode and status-change time of fi (a stat of
// the file), ok false when the platform's stat data is unavailable.
func fileIdentity(fi os.FileInfo) (dev, ino uint64, ctimeNS int64, ok bool) {
	st, isStat := fi.Sys().(*syscall.Stat_t)
	if !isStat || st == nil {
		return 0, 0, 0, false
	}
	return uint64(st.Dev), st.Ino, st.Ctimespec.Sec*1e9 + st.Ctimespec.Nsec, true
}
