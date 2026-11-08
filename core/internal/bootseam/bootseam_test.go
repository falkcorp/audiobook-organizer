// file: core/internal/bootseam/bootseam_test.go
// version: 1.0.0
// guid: 88c14678-7561-45af-821d-4628fced91d1
// last-edited: 2026-11-07

package bootseam

import (
	"context"
	"errors"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"
)

type recorder struct{ entered []string }

func (r *recorder) Enter(label string) func() {
	r.entered = append(r.entered, label)
	return func() { r.entered = append(r.entered, "/"+label) }
}

// TestSeamsTravelInContext: without Seams every accessor is production's;
// with them, FS, the labeller and Labelled's error pass through.
func TestSeamsTravelInContext(t *testing.T) {
	ctx := context.Background()
	if From(ctx) != nil || FS(ctx) != vfs.Default {
		t.Fatal("a context without Seams is not production's")
	}
	Region(ctx, "unseen")() // no labeller: a no-op
	mem := vfs.NewMem()
	r := &recorder{}
	ctx = With(ctx, &Seams{FS: mem, Labels: r})
	if FS(ctx) != mem {
		t.Fatal("FS is not the seam's")
	}
	want := errors.New("joker")
	if err := Labelled(From(ctx), "swap.slot.inflight", func() error { return want }); !errors.Is(err, want) {
		t.Fatalf("Labelled = %v", err)
	}
	if len(r.entered) != 2 || r.entered[0] != "swap.slot.inflight" || r.entered[1] != "/swap.slot.inflight" {
		t.Fatalf("regions = %v", r.entered)
	}
}
