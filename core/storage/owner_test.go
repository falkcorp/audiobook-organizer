// file: core/storage/owner_test.go
// version: 1.0.0
// guid: 011932a1-921b-4fd8-87f2-9db91ff7b5df
// last-edited: 2026-11-09

package storage

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/internal/bootseam"
)

// TestRefuseNotStateDirOwner: the owner passes, a stranger's effective uid
// (the Euid seam) is refused with the sudo -u line, an empty state
// directory has nothing to check, and a filesystem other than the OS's
// (the crash kit's MemFS) has no owners to compare.
func TestRefuseNotStateDirOwner(t *testing.T) {
	dir := t.TempDir()
	if _, ok, err := StateDirOwner(vfs.Default, dir); err != nil || !ok {
		t.Skipf("no file owners on this platform (%v)", err)
	}
	as := func(euid int) context.Context {
		return bootseam.With(t.Context(), &bootseam.Seams{Euid: func() int { return euid }})
	}
	if err := RefuseNotStateDirOwner(as(os.Geteuid()), dir, "a joker test"); err != nil {
		t.Errorf("the owner = %v, want no refusal", err)
	}
	err := RefuseNotStateDirOwner(as(os.Geteuid()+1), dir, "a joker test")
	if !errors.Is(err, ErrNotStateDirOwner) || !strings.Contains(err.Error(), "sudo -u ") || !strings.Contains(err.Error(), "a joker test") {
		t.Errorf("a stranger = %v, want ErrNotStateDirOwner naming the command and the sudo -u line", err)
	}
	if err := RefuseNotStateDirOwner(as(os.Geteuid()+1), "", "a joker test"); err != nil {
		t.Errorf("no state directory = %v, want nothing to check", err)
	}
	mem := bootseam.With(t.Context(), &bootseam.Seams{FS: vfs.NewMem(), Euid: func() int { return os.Geteuid() + 1 }})
	if err := RefuseNotStateDirOwner(mem, dir, "a joker test"); err != nil {
		t.Errorf("on MemFS = %v, want no owners to compare", err)
	}
}

// TestRefuseNotStateDirOwnerUsesTheProcessEuid (R1-25 follow-up N-1): with
// no seam, the effective uid is the process's own (os.Geteuid), so a
// process that is not root is refused on a directory root owns. Every
// other test drives the uid through the seam; this one pins the production
// fallback.
func TestRefuseNotStateDirOwnerUsesTheProcessEuid(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root owns the directory this test needs another user's")
	}
	const rootOwned = "/"
	owner, ok, err := StateDirOwner(vfs.Default, rootOwned)
	if err != nil || !ok || owner != 0 {
		t.Skipf("%s is not root's here (owner %d, %v, %v)", rootOwned, owner, ok, err)
	}
	if err := RefuseNotStateDirOwner(context.Background(), rootOwned, "a batcave test"); !errors.Is(err, ErrNotStateDirOwner) {
		t.Errorf("RefuseNotStateDirOwner as uid %d on root's %s = %v, want ErrNotStateDirOwner", os.Geteuid(), rootOwned, err)
	}
	if err := RefuseNotStateDirOwner(context.Background(), t.TempDir(), "a batcave test"); err != nil {
		t.Errorf("RefuseNotStateDirOwner on this process's own directory = %v, want none", err)
	}
}
