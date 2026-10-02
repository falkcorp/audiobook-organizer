// file: internal/scanner/version_link_lock_test.go
// version: 1.1.0
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

// The raced-row carry (joinRacedRow) also moves an ungrouped row into a
// group, possibly as primary, so it waits for a holder of either the group
// or the no-group sentinel.
func TestJoinRacedRow_WaitsForGroupHolder(t *testing.T) {
	for _, held := range []string{"g", ""} {
		t.Run("holding "+held, func(t *testing.T) {
			f := vptest.New(t)
			prev := getStore()
			SetStore(f.S)
			t.Cleanup(func() { SetStore(prev) })
			f.Book(t, vptest.Spec{ID: "partner", Group: "g", Primary: "false"})
			r := f.Book(t, vptest.Spec{ID: "raced", Primary: "nil"})
			yes := true
			var joined bool
			vptest.RequireWaitsForHolder(t,
				func() func() { return versionprimary.LockGroup(held) },
				func() error {
					var err error
					joined, _, err = joinRacedRow(r, "g", &yes)
					return err
				},
				func() bool { return f.GroupOf(t, r) == "" })
			if !joined || f.GroupOf(t, r) != "g" || f.Flag(t, r) != "true" {
				t.Fatalf("raced row: joined=%v group=%q flag=%s, want joined into g as primary", joined, f.GroupOf(t, r), f.Flag(t, r))
			}
		})
	}
}
