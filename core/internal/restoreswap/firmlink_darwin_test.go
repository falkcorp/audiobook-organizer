// file: core/internal/restoreswap/firmlink_darwin_test.go
// version: 1.0.0
// guid: 879c7dd9-eb05-4617-8846-91c95041115d
// last-edited: 2026-11-08

//go:build darwin

package restoreswap

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"
)

// TestFirmlinkIsNeverATwin (R1-21 delta review): on macOS /Users and
// /System/Volumes/Data/Users are one directory joined by a firmlink. Lstat
// gives both names the same device and inode, under parents that are
// different directories, so the parent check does not stop it, and only
// twinProven's directory check keeps the two from being called twins (mv
// would then RemoveAll one name, which empties both). Read-only: twinProven
// only stats. Skipped where either path is missing or they are not one
// directory under two parents, since the test would then prove nothing.
func TestFirmlinkIsNeverATwin(t *testing.T) {
	const a, b = "/Users", "/System/Volumes/Data/Users"
	fa, err := os.Lstat(a)
	if err != nil {
		t.Skipf("%s: %v", a, err)
	}
	fb, err := os.Lstat(b)
	if err != nil {
		t.Skipf("%s: %v", b, err)
	}
	pa, errA := os.Stat(filepath.Dir(a))
	pb, errB := os.Stat(filepath.Dir(b))
	if !fa.IsDir() || !os.SameFile(fa, fb) || errA != nil || errB != nil || os.SameFile(pa, pb) {
		t.Skipf("%s and %s are not one directory under two parents here", a, b)
	}
	if ok, err := twinProven(vfs.Default, a, b, nil); ok || err != nil {
		t.Fatalf("twinProven(%s, %s) = %v, %v; want false, nil", a, b, ok, err)
	}
}
