// file: internal/plugins/maintenance/membership_lock_writers_test.go
// version: 1.0.0
// guid: 4b9e1c73-8d2a-4f60-a5e1-3c7f9b2d0e84
// last-edited: 2026-10-02

package maintenance

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// ApplyVersionGroup waits for a concurrent hand-off on the reused group its
// members join and on the no-group sentinel an ungrouped member leaves.
func TestApplyVersionGroup_WaitsForGroupHolder(t *testing.T) {
	for _, held := range []string{"g", ""} {
		t.Run("holding "+held, func(t *testing.T) {
			f := vptest.New(t)
			withLibraryRoot(t, f.Root)
			f.Book(t, vptest.Spec{ID: "inc", Group: "g", Primary: "true"})
			a := f.Book(t, vptest.Spec{ID: "a", Group: "g", Primary: "false"})
			b := f.Book(t, vptest.Spec{ID: "b", Primary: "true"})
			item := versionGroupItem(t, "/lib/vg", []string{a, b})
			vptest.RequireWaitsForHolder(t,
				func() func() { return versionprimary.LockGroup(held) },
				func() error { return ApplyVersionGroup(f.S)(context.Background(), item) },
				func() bool { return f.GroupOf(t, b) == "" })
		})
	}
}
