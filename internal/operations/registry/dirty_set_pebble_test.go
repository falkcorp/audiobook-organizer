// file: internal/operations/registry/dirty_set_pebble_test.go
// version: 1.0.0
// guid: 5d9a7c13-e2b4-4f08-a6d1-3c7e90b24f58
// last-edited: 2026-10-30

package registry

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// TestDirtySetRunsOnPebbleStore: the real store carries the compare-and-delete
// primitive, so a DirtySet op enqueued on the registry starts (it used to be
// refused with ErrDirtySetUnsupported) and clears exactly the entries it
// processed: a failed item and a subject re-dirtied mid-run stay dirty, and
// an entry under another prefix is never touched.
func TestDirtySetRunsOnPebbleStore(t *testing.T) {
	const prefix = "idx:test:dirty:"
	store := newPebbleV3(t)
	if _, ok := database.AsCapability[dirtySetStore](store); !ok {
		t.Fatal("PebbleStore does not satisfy dirtySetStore")
	}
	const n = 300 // more than one 256-entry page
	for i := range n {
		if _, err := store.MarkDirty(prefix, fmt.Sprintf("s%04d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.MarkDirty("idx:other:dirty:", "bystander"); err != nil {
		t.Fatal(err)
	}
	var ran atomic.Int64
	def := ops.Batch("test.dirty-pebble", ops.BatchSpec[ops.NoParams, string, ops.NoResult]{
		Title:      "dirty",
		Effects:    ops.ReadOnly(ops.ResBooks),
		Permission: auth.PermLibraryView,
		Source: func(*ops.RC, ops.NoParams) (ops.Source[string], error) {
			return ops.DirtySet(prefix, func(_ context.Context, ids []string) ([]string, error) { return ids, nil }), nil
		},
		Item: func(_ *ops.ItemRC, _ ops.NoParams, id string) error {
			ran.Add(1)
			switch id {
			case "s0007": // re-dirtied while it runs
				if _, err := store.MarkDirty(prefix, id); err != nil {
					return err
				}
			case "s0009":
				return errors.New("a failed item stays dirty")
			}
			return nil
		},
		Concurrency: ops.Network(4, "test"),
	})
	r := startV3Registry(t, store, Options{})
	if err := Register(r, "test", def); err != nil {
		t.Fatal(err)
	}
	id, err := r.EnqueueOp(context.Background(), "test.dirty-pebble", nil)
	if err != nil {
		t.Fatal(err)
	}
	row := awaitTerminal(t, store, id)
	if ran.Load() != n {
		t.Fatalf("ran %d items, want %d (status %s, error %v)", ran.Load(), n, row.Status, row.ErrorMessage)
	}
	kvs, _, err := store.ScanPrefixPage(prefix, "", n+1)
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, kv := range kvs {
		left = append(left, kv.Key)
	}
	if want := []string{prefix + "s0007", prefix + "s0009"}; !slices.Equal(left, want) {
		t.Fatalf("left dirty = %v, want %v (status %s)", left, want, row.Status)
	}
	if got, _ := store.GetRaw("idx:other:dirty:bystander"); got == nil {
		t.Fatal("the run cleared an entry under another prefix")
	}
}
