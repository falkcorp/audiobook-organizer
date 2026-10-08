// file: internal/maintenance/jobs/membership_lock_writers_test.go
// version: 1.0.0
// guid: 8a3d5f92-1e6c-4b07-9d48-c2f7e0a1b635
// last-edited: 2026-10-02

package jobs

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// The maintenance-job membership writers (dedup phase 4's unlink, the dedup
// keeper fill, fix-version-groups' outlier unlink) each wait for a
// concurrent hand-off (versionprimary.LockGroup) on the groups the book
// leaves and joins; "" is the no-group sentinel.
func TestJobMembershipWriters_WaitForGroupHolder(t *testing.T) {
	cases := []struct {
		name string
		held string
		run  func(t *testing.T, f *vptest.Fixture) (write func() error, unchanged func() bool)
	}{
		{"dedup unlink leaves vg", "vg", ddUnlinkCase},
		{"dedup unlink joins no group", "", ddUnlinkCase},
		{"dedup keeper fill leaves no group", "", ddKeeperFillCase},
		{"dedup keeper fill joins dup group", "dg", ddKeeperFillCase},
		{"fix-version-groups outlier leaves g1", "g1", vgOutlierCase},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := vptest.New(t)
			write, unchanged := tc.run(t, f)
			vptest.RequireWaitsForHolder(t,
				func() func() { return versionprimary.LockGroup(tc.held) }, write, unchanged)
		})
	}
}

func ddUnlinkCase(t *testing.T, f *vptest.Fixture) (func() error, func() bool) {
	f.Book(t, vptest.Spec{ID: "keep", Group: "vg", Primary: "true"})
	d := f.Book(t, vptest.Spec{ID: "d", Group: "vg", Primary: "false"})
	return func() error { return ddUnlinkFromVersionGroup(f.S, "vg", d) },
		func() bool { return f.GroupOf(t, d) == "vg" }
}

func ddKeeperFillCase(t *testing.T, f *vptest.Fixture) (func() error, func() bool) {
	k := f.Book(t, vptest.Spec{ID: "k", Primary: "nil", NoFile: true})
	d := f.Book(t, vptest.Spec{ID: "dd", Group: "dg", Primary: "true", NoFile: true})
	write := func() error {
		keeper, err := f.S.GetBookByID(k)
		if err != nil {
			return err
		}
		dup, err := f.S.GetBookByID(d)
		if err != nil {
			return err
		}
		return ddMergeDuplicateBook(f.S, keeper, dup, false)
	}
	unchanged := func() bool {
		b, err := f.S.GetBookByID(d)
		return err == nil && b != nil && !b.IsSoftDeleted() && f.GroupOf(t, k) == ""
	}
	return write, unchanged
}

func vgOutlierCase(t *testing.T, f *vptest.Fixture) (func() error, func() bool) {
	f.Book(t, vptest.Spec{ID: "a", Group: "g1", Primary: "true"})
	o := f.Book(t, vptest.Spec{ID: "o", Group: "g1", Primary: "false"})
	write := func() error {
		ob, err := f.S.GetBookByID(o)
		if err != nil {
			return err
		}
		return vgUnlinkOutliers(context.Background(), f.S, "g1", []database.BookCore{ob.Core()}, f.Root)
	}
	return write, func() bool { return f.GroupOf(t, o) == "g1" }
}
