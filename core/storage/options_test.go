// file: core/storage/options_test.go
// version: 1.0.0
// guid: 18813d80-967b-46fb-85ac-7531fab8e39b
// last-edited: 2026-11-08

package storage

import (
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"
)

// TestPresetFilesystemWinsOverOptionsFS (coordinator, after R1-21's scratch
// merge): defaults keeps a filesystem already in OpenOptions.fs, which is
// where a context seam puts the crash-matrix kit's filesystem (R1-21's
// adoptSeams), instead of overwriting it with OpenOptions.FS. Before the fix
// every kit boot opened the real disk. FS applies when nothing is preset,
// and the OS's when neither is. The boot opens its store on the winner.
func TestPresetFilesystemWinsOverOptionsFS(t *testing.T) {
	kit, other := vfs.NewMem(), vfs.NewMem()
	e := newEnvFS(t, other, cpOwners()...)
	e.opts.fs = kit // as a context seam sets it before Open's defaults
	if err := e.open().Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := kit.Stat(e.path); err != nil {
		t.Fatalf("the store on the preset filesystem: %v", err)
	}
	if _, err := other.Stat(e.path); !oserrorNotExist(err) {
		t.Fatalf("the store on OpenOptions.FS: %v, want none there", err)
	}
	o := OpenOptions{FS: FS{fs: other}}
	if o.defaults(); o.fs != other {
		t.Fatalf("OpenOptions.FS alone resolved to %T, want it", o.fs)
	}
	o = OpenOptions{}
	if o.defaults(); o.fs != vfs.Default {
		t.Fatalf("no filesystem resolved to %T, want the OS's", o.fs)
	}
}
