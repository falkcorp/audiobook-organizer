// file: core/storage/restorejournal/validate_other.go
// version: 1.0.0
// guid: 97a8ca0a-acb5-48b3-bce4-ff80db0ffb84
// last-edited: 2026-11-07

//go:build !unix

package restorejournal

import "os"

// OSProbe off unix has no device numbers: every existing path reports device
// 0, so only existence and writability are checked, and a rename that would
// cross filesystems fails loudly at P0a and swaps back.
func OSProbe() Probe { return osProbe{} }

type osProbe struct{}

func (osProbe) ItemDev(path string) (uint64, error) { _, err := os.Lstat(path); return 0, err }
func (osProbe) DirDev(path string) (uint64, error)  { _, err := os.Stat(path); return 0, err }

func (osProbe) Writable(dir string) error {
	f, err := os.CreateTemp(dir, ".mediacore-probe-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}
