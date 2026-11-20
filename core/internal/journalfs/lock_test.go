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
