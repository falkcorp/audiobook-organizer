// file: core/storage/restorejournal/slot_test.go
// version: 1.0.0
// guid: 2e7b9c05-6d1a-4f83-b4c2-8a5e1f0d7b39
// last-edited: 2026-11-08

package restorejournal

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"
)

// TestReadSlot: an empty slot is not busy; a staged intent is
// restore_pending, an in-flight one restore_inflight, each naming its kind;
// an undecodable pending.json still occupies the slot (fail closed).
func TestReadSlot(t *testing.T) {
	fs := vfs.NewMem()
	state := "/gotham/state"
	st, err := ReadSlot(fs, state)
	if err != nil || st.Busy() || st.Err() != nil {
		t.Fatalf("empty slot = %+v, %v", st, err)
	}
	if err := CreateIntentFS(fs, state, Intent{ID: "alfred", Kind: KindReset, Reset: ResetFactory, DBPath: "/batcave/main.pebble"}); err != nil {
		t.Fatal(err)
	}
	st, err = ReadSlot(fs, state)
	if err != nil || !st.Busy() || st.Pending == nil || st.Pending.Kind != KindReset {
		t.Fatalf("staged slot = %+v, %v", st, err)
	}
	if err := st.Err(); !errors.Is(err, ErrSwapPending) || !strings.Contains(err.Error(), TokenRestorePending) || !strings.Contains(err.Error(), "reset (factory)") {
		t.Fatalf("pending err = %v", err)
	}
	if err := fs.MkdirAll(JournalDir(state, "alfred"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := fs.Rename(PendingPath(state), filepath.Join(JournalDir(state, "alfred"), inflightName)); err != nil {
		t.Fatal(err)
	}
	st, err = ReadSlot(fs, state)
	if err != nil || st.Pending != nil || len(st.InFlight) != 1 || st.InFlight[0].ID != "alfred" {
		t.Fatalf("in-flight slot = %+v, %v", st, err)
	}
	if err := st.Err(); !errors.Is(err, ErrSwapPending) || !strings.Contains(err.Error(), TokenRestoreInflight) {
		t.Fatalf("in-flight err = %v", err)
	}
	f, err := fs.Create(PendingPath(state), vfs.WriteCategoryUnspecified)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("{joker")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if st, err := ReadSlot(fs, state); err != nil || st.Pending == nil {
		t.Fatalf("an undecodable pending.json reads as free: %+v, %v", st, err)
	}
}
