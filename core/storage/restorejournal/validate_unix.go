// file: core/storage/restorejournal/validate_unix.go
// version: 1.0.0
// guid: e32129a8-a89c-4dd4-8937-7bd241c0b129
// last-edited: 2026-11-07

//go:build unix

package restorejournal

import (
	"fmt"
	"os"
	"syscall"
)

// OSProbe is the real filesystem's Probe: st_dev from lstat and stat, and
// access(2) with W_OK|X_OK for writability.
func OSProbe() Probe { return osProbe{} }

type osProbe struct{}

func (osProbe) ItemDev(path string) (uint64, error) { return devOf(os.Lstat(path)) }
func (osProbe) DirDev(path string) (uint64, error)  { return devOf(os.Stat(path)) }

func (osProbe) Writable(dir string) error {
	if err := syscall.Access(dir, 0x2|0x1); err != nil { // W_OK|X_OK
		return &os.PathError{Op: "access", Path: dir, Err: err}
	}
	return nil
}

func devOf(fi os.FileInfo, err error) (uint64, error) {
	if err != nil {
		return 0, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("restorejournal: no device for %s", fi.Name())
	}
	return uint64(st.Dev), nil //nolint:unconvert // Dev is int32 on darwin
}
