// file: internal/scanner/version_link_lock_test.go
// version: 1.0.0
// guid: 1f6a9d38-2c4e-4b75-a8d1-7e3b5c0f9a62
// last-edited: 2026-10-02

package scanner

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// The scanner's version link moves an ungrouped row into a group, so it waits
// for a holder of the no-group sentinel as well as of the group it joins.
func TestLinkVersionGroup_WaitsForGroupHolder(t *testing.T) {
	for _, held := range []string{"g", ""} {
		t.Run("holding "+held, func(t *testing.T) {
			f := vptest.New(t)
			prev := getStore()
			SetStore(f.S)
			t.Cleanup(func() { SetStore(prev) })
			f.Book(t, vptest.Spec{ID: "inc", Group: "g", Primary: "true"})
			n := f.Book(t, vptest.Spec{ID: "n", Primary: "nil"})
			vptest.RequireWaitsForHolder(t,
				func() func() { return versionprimary.LockGroup(held) },
				func() error {
					linkVersionGroup(n, "g", false)
					return nil
				},
				func() bool { return f.GroupOf(t, n) == "" })
			if got := f.GroupOf(t, n); got != "g" {
				t.Fatalf("after release the row is in %q, want g", got)
			}
		})
	}
}
