// file: core/internal/journalfs/lock_test.go
// version: 1.0.0
// guid: 4d9a2e61-7b38-4c15-a0f2-8e3c5b1d9f67
// last-edited: 2026-11-19

package journalfs_test

import (
	"errors"
	"os"
	"testing"

	"github.com/falkcorp/mediacore/backup/journal"
)

// TestMemFS_RelockOfAHeldFileSucceeds: flock(2) on a descriptor that already
// holds the lock succeeds (the lock belongs to the open file description), and
// a second descriptor on the same path still gets ErrLocked while the first
// holds it, so a re-lock must neither fail nor release the path.
func TestMemFS_RelockOfAHeldFileSucceeds(t *testing.T) {
	e := newEnv(t, newStore(t), noOpts())
	path := stateDir + "/held"
	first, err := e.fsys.Open(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.fsys.Open(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Lock(); err != nil {
		t.Fatal(err)
	}
	if err := first.Lock(); err != nil {
		t.Fatalf("a second Lock on the descriptor that holds the lock: %v", err)
	}
	if err := second.Lock(); !errors.Is(err, journal.ErrLocked) {
		t.Fatalf("Lock on another descriptor while the first holds it: err = %v, want ErrLocked", err)
	}
	if err := errors.Join(first.Close(), second.Close()); err != nil {
		t.Fatal(err)
	}
}

// TestMemFS_LockFollowsARename: flock(2) belongs to the file, not to its name.
// The journal's trim holds the journal locked, locks a temporary file, renames
// it over the journal and closes the old descriptor; the file at the journal's
// name must stay locked throughout, by the new file, and be free once that one
// closes. A name a rename or a remove unlinked no longer excludes anyone.
func TestMemFS_LockFollowsARename(t *testing.T) {
	e := newEnv(t, newStore(t), noOpts())
	path, tmp := stateDir+"/robin", stateDir+"/robin.trimming"
	lock := func(p string, flag int) (journal.FSFile, error) {
		f, err := e.fsys.Open(p, flag, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Lock(); err != nil {
			return nil, errors.Join(err, f.Close())
		}
		return f, nil
	}
	old, err := lock(path, os.O_RDWR|os.O_CREATE)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := lock(tmp, os.O_RDWR|os.O_CREATE|os.O_EXCL)
	if err != nil {
		t.Fatalf("the temporary file's lock while the journal is held: %v", err)
	}
	if err := e.fsys.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
	if f, err := lock(path, os.O_RDWR); !errors.Is(err, journal.ErrLocked) {
		if f != nil {
			_ = f.Close()
		}
		t.Fatalf("a second opener of the renamed journal: err = %v, want ErrLocked", err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	if f, err := lock(path, os.O_RDWR); !errors.Is(err, journal.ErrLocked) {
		if f != nil {
			_ = f.Close()
		}
		t.Fatalf("after the old descriptor closed, a second opener: err = %v, want ErrLocked (the new file holds it)", err)
	}
	// The name the temporary file had is free: nothing is at it.
	if f, err := lock(tmp, os.O_RDWR|os.O_CREATE|os.O_EXCL); err != nil {
		t.Fatalf("the temporary file's old name after the rename: %v", err)
	} else if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := e.fsys.Remove(tmp); err != nil {
		t.Fatal(err)
	}
	if err := fresh.Close(); err != nil {
		t.Fatal(err)
	}
	g, err := lock(path, os.O_RDWR)
	if err != nil {
		t.Fatalf("after the new file closed, the journal's name is still locked: %v", err)
	}
	// A removed file's lock is gone from its name, as an unlinked inode's is.
	if err := e.fsys.Remove(path); err != nil {
		t.Fatal(err)
	}
	h, err := lock(path, os.O_RDWR|os.O_CREATE)
	if err != nil {
		t.Fatalf("a file created at a removed name: %v", err)
	}
	if err := errors.Join(g.Close(), h.Close()); err != nil {
		t.Fatal(err)
	}
}
