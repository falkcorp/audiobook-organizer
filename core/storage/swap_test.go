// file: core/storage/swap_test.go
// version: 1.0.1
// guid: 197248f8-64e9-44a3-a076-184d522f03ae
// last-edited: 2026-11-08

package storage

import (
	"context"
	"errors"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/internal/bootseam"
	ipebble "github.com/falkcorp/mediacore/internal/pebble"
)

// TestRewriteSidecarsFromClosedStore: the swap's step e and step x rewrite
// the level sidecar and every legacy projection sidecar from a closed
// store's own stamps, whatever stale values sat beside it (05-storage
// §5S.3.16 machine 2, "restore step e").
func TestRewriteSidecarsFromClosedStore(t *testing.T) {
	fs := vfs.NewMem()
	g := projectionGuard()
	owners := []OwnerSpec{
		{Owner: "wayne", Enabled: true, Spec: Spec{Keyspace: true, Level: 2, Migrations: []Migration{raise(1, 2)}}},
		{Owner: "gotham", Enabled: true, Spec: Spec{Claims: projectionClaims(), LegacyGuard: g}},
	}
	e := newEnvFS(t, fs, owners...)
	if err := e.open().Close(); err != nil {
		t.Fatal(err)
	}
	// Another store's sidecars sit beside this one.
	if err := ipebble.WriteLevels(fs, e.path, map[string]int{"core": 0, "wayne": 7, "joker": 3}); err != nil {
		t.Fatal(err)
	}
	if err := ipebble.WriteInt(fs, e.path+g.SidecarSuffix, 99); err != nil {
		t.Fatal(err)
	}
	if err := RewriteSidecars(context.Background(), fs, e.path, owners); err != nil {
		t.Fatal(err)
	}
	side, ok, err := ipebble.ReadLevels(fs, e.path)
	if err != nil || !ok || side["wayne"] != 2 || side["core"] != 0 || len(side) != 3 {
		t.Fatalf("level sidecar = %v (%v, %v), want core 0, gotham 0, wayne 2", side, ok, err)
	}
	if v, ok, err := ipebble.ReadInt(fs, e.path+g.SidecarSuffix); err != nil || !ok || v != g.projection(0) {
		t.Fatalf("projection sidecar = %d (%v, %v), want %d", v, ok, err, g.projection(0))
	}
	if err := RewriteSidecars(context.Background(), fs, e.path+".absent", owners); err == nil {
		t.Fatal("a directory with no store was rewritten")
	}
}

type labelLog struct{ seen []string }

func (l *labelLog) Enter(label string) func() {
	l.seen = append(l.seen, label)
	return func() {}
}

// TestOpenAdoptsBootSeams: storage.Open takes the crash-matrix kit's
// filesystem and labeller from its context, and a fresh install's writers
// carry their writer-table labels.
// TestExplicitFSWinsOverTheSeam: a caller's OpenOptions.FS wins over the
// boot seam's filesystem, which fills in only when the caller set none:
// OpenOptions.FS, then the seam's filesystem, then vfs.Default (adoptSeams
// over R1-20's defaults(), 8741ec2bf).
func TestExplicitFSWinsOverTheSeam(t *testing.T) {
	explicit, seam := vfs.NewMem(), vfs.NewMem()
	ctx := bootseam.With(context.Background(), &bootseam.Seams{FS: seam})
	path := "/batcave/storage/main.pebble"
	op, err := Open(ctx, OpenOptions{Path: path, FS: FS{fs: explicit}, Build: "batcomputer-1", Owners: []OwnerSpec{{Owner: "wayne", Enabled: true, Spec: wayneSpec()}}})
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
	if _, err := explicit.Stat(path); err != nil {
		t.Fatalf("the store is not on the caller's filesystem: %v", err)
	}
	if _, err := seam.Stat(path); err == nil {
		t.Fatal("the store went to the seam's filesystem over the caller's")
	}
}

func TestOpenAdoptsBootSeams(t *testing.T) {
	fs := vfs.NewMem()
	labels := &labelLog{}
	ctx := bootseam.With(context.Background(), &bootseam.Seams{FS: fs, Labels: labels})
	path := "/batcave/storage/main.pebble"
	op, err := Open(ctx, OpenOptions{Path: path, Build: "batcomputer-1", Owners: []OwnerSpec{{Owner: "wayne", Enabled: true, Spec: wayneSpec()}}})
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
	if _, err := fs.Stat(path); err != nil {
		t.Fatalf("the store is not on the seam's filesystem: %v", err)
	}
	want := map[string]bool{labelFreshBatch: false, labelFreshSidecars: false, labelFinishBatch: false, labelFinishSidecars: false}
	for _, l := range labels.seen {
		if _, ok := want[l]; ok {
			want[l] = true
		}
	}
	for l, seen := range want {
		if !seen {
			t.Errorf("label %s never entered; saw %v", l, labels.seen)
		}
	}
}
