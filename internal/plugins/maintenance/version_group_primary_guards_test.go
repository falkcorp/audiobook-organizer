// file: internal/plugins/maintenance/version_group_primary_guards_test.go
// version: 1.0.1
// guid: 9e2c4a61-7b3d-4f58-a0c2-6d1e8b5f3a47
// last-edited: 2026-09-26

package maintenance

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
)

func vgBoolPtr(v bool) *bool { return &v }

func vgGroupReportOf(t *testing.T, rep *vgRepairReport, gid string) vgRepairGroupReport {
	t.Helper()
	for _, g := range rep.Groups {
		if g.GroupID == gid {
			return g
		}
	}
	t.Fatalf("group %s not in report", gid)
	return vgRepairGroupReport{}
}

// seedDouble adds a double-primary group that Elect would otherwise fix: X
// wins on chapters, Y would be demoted.
func (f *vgRepairFixture) seedDouble(t *testing.T, gid string, mut func(*database.Book)) {
	base := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	f.book(t, gid+"-X", gid, "organized", "true", 10, false, base, mut)
	f.book(t, gid+"-Y", gid, "organized", "true", 1, false, base.Add(time.Hour), nil)
}

func TestVGPrimaryRepair_DryRunFalseWritesAndShowsDemotedIDs(t *testing.T) {
	f := newVGRepairFixture(t)
	f.seed(t)
	prev, err := f.run(t, fakeDeps{store: f.s}, vgPrimaryRepairParams{DryRun: vgBoolPtr(true)})
	require.NoError(t, err)
	require.True(t, prev.DryRun)
	g := vgGroupReportOf(t, prev, "vg-double")
	require.Equal(t, []string{"B", "C"}, g.DemotedIDs, "the dry run lists who a write would demote")
	require.Equal(t, 1, prev.Planned)
	require.Equal(t, 1, prev.MultiPrimary)
	require.Equal(t, 1, prev.SkippedByReason[versionprimary.HoldBetterCopyNotInLibrary])
	require.Equal(t, "true", f.flag(t, "B"), "dry_run:true writes nothing")

	rep, err := f.run(t, fakeDeps{store: f.s}, vgPrimaryRepairParams{DryRun: vgBoolPtr(false), GroupIDs: []string{"vg-double"}})
	require.NoError(t, err)
	require.False(t, rep.DryRun)
	require.Equal(t, 1, rep.Applied)
	require.Equal(t, "true", f.flag(t, "A"))
	require.Equal(t, "false", f.flag(t, "B"))
	require.Equal(t, "false", f.flag(t, "C"))
}

func TestVGPrimaryRepair_DryRunFalseStillNeedsGroupIDs(t *testing.T) {
	f := newVGRepairFixture(t)
	f.seed(t)
	_, err := f.run(t, fakeDeps{store: f.s}, vgPrimaryRepairParams{DryRun: vgBoolPtr(false)})
	require.ErrorContains(t, err, "group_ids")
	require.Equal(t, "true", f.flag(t, "B"))
}

func TestVGPrimaryRepair_ApplyContradictingDryRunIsRefused(t *testing.T) {
	f := newVGRepairFixture(t)
	f.seed(t)
	_, err := f.run(t, fakeDeps{store: f.s}, vgPrimaryRepairParams{Apply: true, DryRun: vgBoolPtr(true), GroupIDs: []string{"vg-double"}})
	require.ErrorContains(t, err, "contradicts")
	require.Equal(t, "true", f.flag(t, "B"))
}

func TestVGPrimaryRepair_SkipsITunesGroup(t *testing.T) {
	f := newVGRepairFixture(t)
	f.seedDouble(t, "vg-it", nil)
	// Y also has a live file in the iTunes tree.
	it := filepath.Join(f.root, "books", "itunes", "Author", "Y.m4b")
	require.NoError(t, f.s.CreateBookFile(&database.BookFile{ID: "bf-it", BookID: "vg-it-Y", FilePath: it}))

	rep, err := f.run(t, fakeDeps{store: f.s}, vgPrimaryRepairParams{DryRun: vgBoolPtr(false), GroupIDs: []string{"vg-it"}})
	require.NoError(t, err)
	g := vgGroupReportOf(t, rep, "vg-it")
	require.Equal(t, vgDecisionSkipITunes, g.Kind)
	require.Contains(t, g.SkipReason, "itunes")
	require.Empty(t, g.DemotedIDs)
	require.Equal(t, 1, rep.SkippedByReason[vgDecisionSkipITunes])
	require.Zero(t, rep.Applied)
	require.Equal(t, "true", f.flag(t, "vg-it-X"))
	require.Equal(t, "true", f.flag(t, "vg-it-Y"))
}

func TestVGPrimaryRepair_SkipsOwnerManualBySeries(t *testing.T) {
	f := newVGRepairFixture(t)
	s, err := f.s.CreateSeries("Doctor Who: The Monthly Adventures", nil)
	require.NoError(t, err)
	f.seedDouble(t, "vg-dw", func(b *database.Book) { b.SeriesID = &s.ID })

	rep, err := f.run(t, fakeDeps{store: f.s}, vgPrimaryRepairParams{DryRun: vgBoolPtr(false), GroupIDs: []string{"vg-dw"}})
	require.NoError(t, err)
	g := vgGroupReportOf(t, rep, "vg-dw")
	require.Equal(t, vgDecisionSkipOwnerManual, g.Kind)
	require.Equal(t, 1, rep.SkippedByReason[vgDecisionSkipOwnerManual])
	require.Zero(t, rep.Applied)
	require.Equal(t, "true", f.flag(t, "vg-dw-X"))
	require.Equal(t, "true", f.flag(t, "vg-dw-Y"))
}

func TestVGPrimaryRepair_SkipsOwnerManualByPath(t *testing.T) {
	f := newVGRepairFixture(t)
	f.seedDouble(t, "vg-bf", nil)
	p := filepath.Join(f.root, "Big Finish", "Torchwood 01.m4b")
	require.NoError(t, f.s.CreateBookFile(&database.BookFile{ID: "bf-bf", BookID: "vg-bf-X", FilePath: p}))

	rep, err := f.run(t, fakeDeps{store: f.s}, vgPrimaryRepairParams{})
	require.NoError(t, err)
	g := vgGroupReportOf(t, rep, "vg-bf")
	require.Equal(t, vgDecisionSkipOwnerManual, g.Kind)
	require.Zero(t, rep.Planned)
}

func TestVGPrimaryRepair_NoEligibleMemberIsReportedNotWritten(t *testing.T) {
	f := newVGRepairFixture(t)
	base := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	f.book(t, "N1", "vg-none", "imported", "nil", 0, false, base, nil)
	f.book(t, "N2", "vg-none", "organized", "nil", 0, true, base.Add(time.Hour), nil)

	rep, err := f.run(t, fakeDeps{store: f.s}, vgPrimaryRepairParams{DryRun: vgBoolPtr(false), GroupIDs: []string{"vg-none"}})
	require.NoError(t, err)
	g := vgGroupReportOf(t, rep, "vg-none")
	require.Equal(t, versionprimary.DecisionHeld, g.Kind)
	require.Equal(t, versionprimary.HoldNeedsOrganizeOrRestore, g.HoldReason)
	require.Equal(t, 1, rep.SkippedByReason[versionprimary.HoldNeedsOrganizeOrRestore])
	require.Equal(t, 1, rep.NoPrimary)
	require.Equal(t, "nil", f.flag(t, "N1"))
	require.Equal(t, "nil", f.flag(t, "N2"))
}

// The organized member keeps the flag and the imported one is demoted, never
// the reverse. When the imported copy is the better one the group is held
// and nothing is written.
func TestVGPrimaryRepair_NeverDemotesOrganizedForNonOrganized(t *testing.T) {
	f := newVGRepairFixture(t)
	base := time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
	f.book(t, "O1", "vg-mix", "organized", "true", 5, false, base.Add(time.Hour), nil)
	f.book(t, "I1", "vg-mix", "imported", "true", 5, false, base, nil)
	f.book(t, "O2", "vg-mix2", "organized", "true", 1, false, base.Add(time.Hour), nil)
	f.book(t, "I2", "vg-mix2", "imported", "nil", 20, false, base, nil)

	rep, err := f.run(t, fakeDeps{store: f.s}, vgPrimaryRepairParams{DryRun: vgBoolPtr(false), GroupIDs: []string{"vg-mix", "vg-mix2"}})
	require.NoError(t, err)
	require.Equal(t, "O1", vgGroupReportOf(t, rep, "vg-mix").WinnerID)
	require.Equal(t, "true", f.flag(t, "O1"))
	require.Equal(t, "false", f.flag(t, "I1"))
	require.Equal(t, versionprimary.DecisionHeld, vgGroupReportOf(t, rep, "vg-mix2").Kind)
	require.Equal(t, "true", f.flag(t, "O2"))
	require.Equal(t, "nil", f.flag(t, "I2"))
}

func TestVGPrimaryRepair_LimitCapsPlannedGroups(t *testing.T) {
	f := newVGRepairFixture(t)
	f.seed(t)
	rep, err := f.run(t, fakeDeps{store: f.s}, vgPrimaryRepairParams{Limit: 1})
	require.NoError(t, err)
	require.Equal(t, 2, rep.Candidates, "candidates is the whole-library count, not the limited one")
	require.Equal(t, 1, rep.MultiPrimary)
	require.Equal(t, 1, rep.LimitedOut)
	require.Len(t, rep.Groups, 1)
	require.Equal(t, "vg-double", rep.Groups[0].GroupID, "limit keeps the first groups by id")
}

func TestVGPrimaryRepair_SkipsITunesGroupEvenWhenRowIsMissing(t *testing.T) {
	f := newVGRepairFixture(t)
	f.seedDouble(t, "vg-itm", nil)
	it := filepath.Join(f.root, "books", "itunes", "Author", "Y.m4b")
	require.NoError(t, f.s.CreateBookFile(&database.BookFile{ID: "bf-itm", BookID: "vg-itm-Y", FilePath: it, Missing: true}))

	rep, err := f.run(t, fakeDeps{store: f.s}, vgPrimaryRepairParams{DryRun: vgBoolPtr(false), GroupIDs: []string{"vg-itm"}})
	require.NoError(t, err)
	require.Equal(t, vgDecisionSkipITunes, vgGroupReportOf(t, rep, "vg-itm").Kind)
	require.Equal(t, "true", f.flag(t, "vg-itm-Y"))
}
