// file: internal/plugins/maintenance/fragment_filesig_linux.go
// version: 1.0.0
// guid: 2c8e5a71-9d34-4b6f-a1e7-3f0b6d9c4e85
// last-edited: 2026-10-06

//go:build linux

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
	return uint64(st.Dev), uint64(st.Ino), st.Ctim.Sec*1e9 + st.Ctim.Nsec, true //nolint:unconvert // Dev's width varies by arch
}
