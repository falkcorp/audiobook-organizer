// file: internal/backup/device_unix.go
// version: 1.0.0
// guid: de7ed1b4-0ab9-48be-b810-ce6103f92420
// last-edited: 2026-09-22

//go:build !windows

package backup

import (
	"fmt"
	"syscall"
)

// deviceID returns the st_dev of path. ok is true when the platform can
// answer; a false ok means "unknown", never "different".
func deviceID(path string) (dev uint64, ok bool, err error) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return 0, false, fmt.Errorf("stat %s: %w", path, err)
	}
	return uint64(st.Dev), true, nil //nolint:unconvert // Dev is int32 on darwin, uint64 on linux
}
