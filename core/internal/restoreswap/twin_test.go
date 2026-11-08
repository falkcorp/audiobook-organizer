// file: core/internal/restoreswap/twin_test.go
// version: 1.0.0
// guid: add1ec36-b167-459f-a907-736b11d3481b
// last-edited: 2026-11-08

package restoreswap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// TestRealFSStrayIsNeverATwin (R1-21 review M3, probe 3): on the real
// filesystem rename(2) never leaves two names, so a rename that finds its
// source and its destination both present was given a foreign write, not a
// crash's second name. The case: a kill inside step c after the live store
// moved to previous/, then a store created at the empty live path by a
// binary with no P0a. The resume must not discard previous/ (the only copy
// of the original) to redo the rename: boot halts with ErrSwapAmbiguous and
// both stores stay where they are, on this boot and the next.
func TestRealFSStrayIsNeverATwin(t *testing.T) {
	h := newOSHost(t.TempDir())
	const id = "r-stray"
	makeStore(t, vfs.Default, h.db, 0, "live")
	staging := filepath.Join(restorejournal.StagingDir(h.db, id), "main.pebble")
	makeStore(t, vfs.Default, staging, 1, "staged")
	if err := os.MkdirAll(h.state, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s := h.swapper(t)
	in := restorejournal.Intent{ID: id, Kind: restorejournal.KindArchive, DBPath: h.db,
		Targets:       []restorejournal.Target{{From: staging, To: h.db}},
		StagedByBuild: testBuild, ExpiresAt: testNow().Add(time.Hour), RequestedBy: "alfred", RequestedAt: testNow()}
	if err := s.Stage(ctx, in); err != nil {
		t.Fatal(err)
	}
	original := treeHash(t, vfs.Default, h.db)
	// The state a kill inside step c leaves, after the first live rename.
	if err := s.appendRec(ctx, id, restorejournal.Record{Step: StepRechecked}); err != nil {
		t.Fatal(err)
	}
	if err := slotMove(vfs.Default, s.pendingPath(), s.inflightPath(id)); err != nil {
		t.Fatal(err)
	}
	aside := restorejournal.SetAsideDest(h.state, h.db, id, h.db)
	if err := mv(vfs.Default, h.db, aside, nil); err != nil {
		t.Fatal(err)
	}
	// The stray at the live path.
	makeStore(t, vfs.Default, h.db, 0, "stray")
	stray := treeHash(t, vfs.Default, h.db)
	for boot := 1; boot <= 2; boot++ {
		_, err := h.swapper(t).RunP0a(ctx)
		if !errors.Is(err, ErrSwapAmbiguous) {
			t.Fatalf("boot %d: P0a = %v, want ErrSwapAmbiguous", boot, err)
		}
		if got := treeHash(t, vfs.Default, aside); got != original {
			t.Fatalf("boot %d: the original store in previous/ changed or is gone", boot)
		}
		if got := treeHash(t, vfs.Default, h.db); got != stray {
			t.Fatalf("boot %d: the stray at the live path changed or is gone", boot)
		}
	}
}

// TestMvTwinProofByIdentity (R1-21 review M3): on the real filesystem mv's
// caller proof is ignored; two names of one file (a hard link) are twins
// and the rename is redone, two different items never are, whatever the
// caller claims. On an in-memory filesystem the caller's proof decides.
func TestMvTwinProofByIdentity(t *testing.T) {
	yes := func() (bool, error) { return true, nil }
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "robin"), filepath.Join(dir, "nightwing")
	if err := os.WriteFile(src, []byte("dick"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(src, dst); err != nil {
		t.Fatal(err)
	}
	if err := mv(vfs.Default, src, dst, nil); err != nil {
		t.Fatalf("a hard link's two names: %v, want the rename redone", err)
	}
	if _, err := os.Stat(src); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source after the redone rename: %v", err)
	}
	if err := os.WriteFile(src, []byte("jason"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := mv(vfs.Default, src, dst, yes); !errors.Is(err, ErrSwapAmbiguous) {
		t.Fatalf("two different files with a caller proof: %v, want ErrSwapAmbiguous", err)
	}
	for p, want := range map[string]string{src: "jason", dst: "dick"} {
		if b, err := os.ReadFile(p); err != nil || string(b) != want {
			t.Fatalf("%s = %q, %v; want %q kept", p, b, err, want)
		}
	}
	mem := vfs.NewMem()
	writeFile(t, mem, "/cave/a", "same")
	writeFile(t, mem, "/cave/b", "same")
	if err := mv(mem, "/cave/a", "/cave/b", nil); !errors.Is(err, ErrSwapAmbiguous) {
		t.Fatalf("in memory without a proof: %v, want ErrSwapAmbiguous", err)
	}
	if err := mv(mem, "/cave/a", "/cave/b", yes); err != nil {
		t.Fatalf("in memory with the caller's proof: %v", err)
	}
}
