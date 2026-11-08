// file: internal/database/standalone_fs_test.go
// version: 1.0.0
// guid: 7a1c03ac-f1b0-4da8-9764-f22f3e7165b1
// last-edited: 2026-11-08

package database

import (
	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/falkcorp/mediacore/storage"
)

// newPebbleStore is the standalone opener over fs, for this package's tests
// of a store on a filesystem they control (a fault-injecting or stalling
// wrapper, or a MemFS they reopen). nil is the OS's.
func newPebbleStore(path string, fs vfs.FS) (*PebbleStore, error) {
	o := StandaloneOptions{}
	if fs != nil {
		o.FS = storage.FSOf(fs)
	}
	return OpenStandalone(path, o)
}
