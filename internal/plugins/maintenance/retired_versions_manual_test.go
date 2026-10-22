// file: internal/plugins/maintenance/retired_versions_manual_test.go
// version: 1.0.0
// guid: 0b7e4d92-5a13-4f68-8c20-d94e1a6b3f57
// last-edited: 2026-10-22

package maintenance

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// setSeries names a fixture book's series.
func (rf *retiredFixture) setSeries(name, series string) {
	rf.t.Helper()
	s, err := rf.st.CreateSeries(series, nil)
	require.NoError(rf.t, err)
	b, err := rf.st.GetBookByID(rf.ids[name])
	require.NoError(rf.t, err)
	b.SeriesID = &s.ID
	_, err = rf.st.UpdateBook(b.ID, b)
	require.NoError(rf.t, err)
}

// runPlan runs the framework's RunPlan over the fixer with the production
// guard inputs (tags and series), as runRepairsPlan builds them.
func (rf *retiredFixture) runPlan() *repairs.PlanResult {
	rf.t.Helper()
	series, err := rf.st.GetAllSeries()
	require.NoError(rf.t, err)
	res, err := repairs.RunPlan(context.Background(), rf.f, nil, rf.f.p.repairsPlanDeps(rf.st, series), &fakeReporter{})
	require.NoError(rf.t, err)
	return res
}

// A Doctor Who / Big Finish / Torchwood book is the owner's to apply by hand.
// The fixer says so itself, on the shell and on the target, from each of the
// guard's sources tested separately (path, series, franchise tag); a row the
// framework guard would skip is never counted would_retire.
func TestRetiredVersions_OwnerManualOnly(t *testing.T) {
	rf := newRetiredFixture(t)
	// Controls: a neutral shell onto a neutral target.
	rf.live("plain-primary", "gp", rf.file("plain/primary.mp3"))
	rf.trashed("plain", "gp", 30, rf.file("plain/a.mp3"))

	// The shell is the Doctor Who book, by each source.
	rf.live("sp-primary", "g-sp", rf.file("sp/primary.mp3"))
	rf.trashed("shell-path", "g-sp", 30, rf.file("Doctor Who/Stones/a.mp3"))
	rf.live("ss-primary", "g-ss", rf.file("ss/primary.mp3"))
	rf.trashed("shell-series", "g-ss", 30, rf.file("ss/a.mp3"))
	rf.setSeries("shell-series", "Doctor Who: The Monthly Adventures")
	rf.live("st-primary", "g-st", rf.file("st/primary.mp3"))
	rf.trashed("shell-tag", "g-st", 30, rf.file("st/a.mp3"))
	require.NoError(t, rf.st.AddBookTag(rf.ids["shell-tag"], "franchise:doctor-who"))

	// The target is the Torchwood / Big Finish book, by each source.
	rf.live("tp-primary", "g-tp", rf.file("Torchwood/Outbreak/primary.mp3"))
	rf.trashed("target-path", "g-tp", 30, rf.file("tp/a.mp3"))
	rf.live("ts-primary", "g-ts", rf.file("ts/primary.mp3"))
	rf.setSeries("ts-primary", "Big Finish Productions")
	rf.trashed("target-series", "g-ts", 30, rf.file("ts/a.mp3"))
	rf.live("tt-primary", "g-tt", rf.file("tt/primary.mp3"))
	require.NoError(t, rf.st.AddBookTag(rf.ids["tt-primary"], "franchise:torchwood"))
	rf.trashed("target-tag", "g-tt", 30, rf.file("tt/a.mp3"))

	rows := rf.plan()
	require.Equal(t, retCatWouldRetire, rows[rf.ids["plain"]].Class, rows[rf.ids["plain"]].Reason)
	for _, c := range []struct{ name, side string }{
		{"shell-path", "shell"}, {"shell-series", "shell"}, {"shell-tag", "shell"},
		{"target-path", "target"}, {"target-series", "target"}, {"target-tag", "target"},
	} {
		r := rows[rf.ids[c.name]]
		require.Equal(t, retCatOwnerManual, r.Class, "%s: %s", c.name, r.Reason)
		require.Equal(t, retCatOwnerManual, r.Skipped, c.name)
		require.False(t, r.Applicable(), c.name)
		require.Equal(t, c.side, r.Current["owner_manual_side"], c.name)
		require.Equal(t, repairs.RiskReview, r.Risk, c.name)
		require.NotEmpty(t, r.Proposed, "%s: the row still names the target it would have retired onto", c.name)
	}

	// The framework's guard agrees: it skips nothing the fixer called
	// would_retire, so no applicable row carries a guard's skip kind.
	res := rf.runPlan()
	for kind, n := range res.SkippedByKind {
		require.NotContains(t, []string{repairs.SkipOwnerManual, repairs.SkipITunes, repairs.SkipGuardUnreadable}, kind,
			"%d row(s) skipped by the framework guard after the fixer called them retirable", n)
	}
	for _, r := range res.Rows {
		if r.Applicable() {
			require.Empty(t, r.Skipped, r.RowID)
		}
		require.NotEqual(t, repairs.SkipOwnerManual, r.Skipped, "%s: %s", r.RowID, r.Reason)
	}
	require.Equal(t, 1, res.Applicable, "only the neutral control is applicable")
	require.Equal(t, 6, res.ByClass[retCatOwnerManual])
}

// Owner-manual outranks the age gate but not the iTunes gate, and the owner's
// veto outranks both: the precedence the fixer documents.
func TestRetiredVersions_OwnerManualPrecedence(t *testing.T) {
	rf := newRetiredFixture(t)
	rf.live("primary", "g", rf.file("g/primary.mp3"))
	rf.trashed("recent-dw", "g", 2, rf.file("Doctor Who/x/a.mp3"))
	rf.trashed("itunes-dw", "g", 30, "/data/books/itunes/Doctor Who/a.m4b")
	rf.trashed("vetoed-dw", "g", 30, rf.file("Doctor Who/y/a.mp3"))
	rf.reject("vetoed-dw", "primary", "same_work")

	rows := rf.plan()
	require.Equal(t, retCatOwnerManual, rows[rf.ids["recent-dw"]].Class, "owner-manual is decided before the age gate")
	require.Equal(t, retCatITunes, rows[rf.ids["itunes-dw"]].Class)
	require.Equal(t, retCatVetoed, rows[rf.ids["vetoed-dw"]].Class)
}

// A path whose location cannot be settled (a folder that is a symlink to
// itself: lstat answers ELOOP, neither "missing" nor a location) is treated as
// possibly under the iTunes library, on the shell and on the target: the row
// is held for the owner's approval with the wording that says the check could
// not tell, never counted would_retire on a guess.
func TestRetiredVersions_UnresolvablePathIsITunesDoubt(t *testing.T) {
	rf := newRetiredFixture(t)
	loop := filepath.Join(rf.root, "loop")
	require.NoError(t, os.Symlink("loop", loop))
	stuck := filepath.Join(loop, "a.mp3")

	rf.live("primary", "g", rf.file("g/primary.mp3"))
	rf.trashed("shell-loop", "g", 30, stuck)
	rf.live("t-primary", "g2", stuck)
	rf.trashed("target-loop", "g2", 30, rf.file("g2/a.mp3"))
	rf.trashed("fine", "g", 30, rf.file("g/b.mp3"))

	rows := rf.plan()
	sl := rows[rf.ids["shell-loop"]]
	require.Equal(t, retCatITunes, sl.Class, sl.Reason)
	require.Equal(t, "shell", sl.Current["itunes_side"])
	require.Contains(t, sl.Reason, "could not tell whether a file is under the iTunes library")
	tl := rows[rf.ids["target-loop"]]
	require.Equal(t, retCatITunes, tl.Class, tl.Reason)
	require.Equal(t, "target", tl.Current["itunes_side"])
	require.Contains(t, tl.Reason, "could not tell whether a file is under the iTunes library")
	require.Equal(t, retCatWouldRetire, rows[rf.ids["fine"]].Class)
}
