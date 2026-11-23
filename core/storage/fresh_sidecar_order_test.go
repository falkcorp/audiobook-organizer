// file: core/storage/fresh_sidecar_order_test.go
// version: 0.1.0
// guid: be0a30cb-c649-409e-983c-8f6ad32f8403
// last-edited: 2026-11-22

package storage

import (
	"context"
	"errors"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/internal/bootseam"
	ipebble "github.com/falkcorp/mediacore/internal/pebble"
)

// sidecarAtLabel records the level sidecar's content at the moment a labelled
// write is entered, which is just before that write happens.
type sidecarAtLabel struct {
	fs    vfs.FS
	path  string
	label string
	found bool
	have  bool
	got   map[string]int
}

func (s *sidecarAtLabel) Enter(label string) func() {
	if label == s.label {
		s.found = true
		s.got, s.have, _ = ipebble.ReadLevels(s.fs, s.path)
	}
	return func() {}
}

// TestFreshInstallWritesTheSidecarsBeforeTheStampBatch (05-storage section
// 5S.3.16 machine 2; the crash kit's I3): a fresh install of a build whose
// level is above zero commits the stamps in one batch and writes the level
// sidecar beside the store. The sidecar goes first, so a kill between the two
// never leaves a committed stamp above its sidecar, which an older build would
// read as a store it can open. Written second (the order before this test), a
// kill at the batch's commit left stamps with no sidecar, which the boot crash
// matrix reports at storage.fresh.sidecars's kill points for any spec whose
// level is raised.
func TestFreshInstallWritesTheSidecarsBeforeTheStampBatch(t *testing.T) {
	fs := vfs.NewMem()
	path := "/batcave/storage/main.pebble"
	probe := &sidecarAtLabel{fs: fs, path: path, label: labelFreshBatch}
	ctx := bootseam.With(context.Background(), &bootseam.Seams{FS: fs, Labels: probe})
	op, err := Open(ctx, OpenOptions{Path: path, Build: "batcomputer-1", Owners: []OwnerSpec{
		{Owner: "wayne", Enabled: true, Spec: Spec{Keyspace: true, Level: 1, Migrations: []Migration{LevelStep("wayne", 1, 1)}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	db, err := op.Prepare(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(db.Migrate(ctx), db.Close()); err != nil {
		t.Fatal(err)
	}
	if !probe.found {
		t.Fatalf("the fresh install never entered %s", labelFreshBatch)
	}
	if !probe.have || probe.got["wayne"] != 1 {
		t.Fatalf("level sidecar when the stamp batch was entered = %v (present %t), want wayne at 1", probe.got, probe.have)
	}
}
