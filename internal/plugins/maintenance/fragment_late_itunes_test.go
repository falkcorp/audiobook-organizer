// file: internal/plugins/maintenance/fragment_late_itunes_test.go
// version: 1.0.0
// guid: 6d1f0a83-2c47-4e59-b8a6-91e3c5d7f204
// last-edited: 2026-10-06

package maintenance

import (
	"context"
	"fmt"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// lateITunesCase is one row shape for TestFragmentFixer_LateITunesRefusesWholeRow:
// a plan, the row to apply, and the books it retires or takes rows off
// (the late iTunes link lands on the one that sorts last).
type lateITunesCase struct {
	name  string
	setup func(t *testing.T) (f *fragFixture, plan *repairs.PlanResult, rowID string, targets []string)
}

// TestFragmentFixer_LateITunesRefusesWholeRow (review 2026-10-06, round 2):
// an iTunes id or path landing on a fragment's row after the locked re-plan
// refuses the WHOLE row before its first write, whichever book it lands on,
// in every Apply branch. Before the fix a no-parent member's row was moved
// onto the survivor and the member retired (an iTunes book written), and the
// other branches retired every book that sorted before the late one.
func TestFragmentFixer_LateITunesRefusesWholeRow(t *testing.T) {
	t.Parallel()
	cases := []lateITunesCase{
		{"copy into a plain parent", func(t *testing.T) (*fragFixture, *repairs.PlanResult, string, []string) {
			f := copyClaimantsFixture(t, true)
			return f, f.plan(t, "op-plan"), "copy:" + f.ids["parent"], []string{f.ids["libA"], f.ids["libB"]}
		}},
		{"copy into an iTunes-linked parent", func(t *testing.T) (*fragFixture, *repairs.PlanResult, string, []string) {
			f := copyClaimantsFixture(t, true)
			linkParentToITunes(t, f)
			return f, f.plan(t, "op-plan"), "copy:" + f.ids["parent"], []string{f.ids["libA"], f.ids["libB"]}
		}},
		{"moved", func(t *testing.T) (*fragFixture, *repairs.PlanResult, string, []string) {
			f := newFragFixture(t)
			p1 := f.file(t, "lib/Gone Twice/01.mp3", 3001)
			parent := f.book(t, "parent", "Gone Twice", f.path("lib/Gone Twice"), nil)
			f.row(t, "p01", parent, p1, "01.mp3", 3001, 600, 1)
			var frags []string
			for i, n := range []string{"02", "03"} {
				was := f.file(t, "lib/Gone Twice/"+n+".mp3", 3002+i)
				f.row(t, "p"+n, parent, was, n+".mp3", int64(3002+i), 600, 2+i)
				fr := f.book(t, "frag"+n, n, was, nil)
				f.row(t, "f"+n, fr, was, n+".mp3", int64(3002+i), 600, 0)
				f.organize(t, fr, was, f.path("lib/Gone Twice/"+n+"/"+n+"/"+n+".mp3"))
				frags = append(frags, fr)
			}
			return f, f.plan(t, "op-plan"), "moved:" + parent, frags
		}},
		{"join into an existing book", func(t *testing.T) (*fragFixture, *repairs.PlanResult, string, []string) {
			f := newFragFixture(t)
			const dir = "lib/Christopher Paolini/Book 2 - Eldest"
			f.existingBook(t, "eldest", "Eldest", "lib/Other Author/Eldest", 4, 900)
			frags := f.chapterFrags(t, dir, "Eldest", 6, 600)
			return f, f.plan(t, "op-plan"), existingRowID(f.path(dir), "eldest"), frags
		}},
		{"no-parent member", func(t *testing.T) (*fragFixture, *repairs.PlanResult, string, []string) {
			f := newFragFixture(t)
			ids := f.looseGroup(t, "lib/Late Loose", "Chap", 3, func(int) bool { return true })
			plan := f.plan(t, "op-plan")
			r := rowWithBooks(t, plan, ids)
			var members []string
			for _, id := range ids {
				if id != r.Proposed["survivor"] {
					members = append(members, id)
				}
			}
			return f, plan, r.RowID, members
		}},
		{"no-parent renamed copy", func(t *testing.T) (*fragFixture, *repairs.PlanResult, string, []string) {
			f := newFragFixture(t)
			dir := "lib/Clarke/02_light_of_other_days"
			_, copies := f.seedChapterCopies(t, dir, nil)
			return f, f.plan(t, "op-plan"), noParentRowID(f.path(dir), fragNumberedKey), copies
		}},
	}
	links := map[string]func(r *database.BookFile){
		"row iTunes path": func(r *database.BookFile) { r.ITunesPath = "file://localhost/Music/iTunes Media/late.mp3" },
		"row iTunes id":   func(r *database.BookFile) { r.ITunesPersistentID = "LATEROWPID" },
	}
	for _, tc := range cases {
		for linkName, link := range links {
			t.Run(tc.name+" / "+linkName, func(t *testing.T) {
				t.Parallel()
				f, plan, rowID, targets := tc.setup(t)
				r := findRow(t, plan, rowID)
				require.True(t, r.Applicable(), "%s: %s", r.Skipped, r.SkipReason)
				sorted := append([]string(nil), targets...)
				sort.Strings(sorted)
				last := sorted[len(sorted)-1]
				before := map[string][]database.BookFile{}
				for _, id := range r.BookIDs {
					rows, err := f.s.GetBookFiles(id)
					require.NoError(t, err)
					before[id] = rows
				}
				fx := newFragmentFixer(f.p)
				fx.afterLockedReplan = func() {
					rows, err := f.s.GetBookFiles(last)
					require.NoError(t, err)
					require.Len(t, rows, 1)
					f.updateRow(t, last, rows[0].ID, link)
					fresh, err := f.s.GetBookFiles(last)
					require.NoError(t, err)
					before[last] = fresh
				}
				err := applyRowInRun(t, f, fx, context.Background(), plan, rowID)
				require.ErrorIs(t, err, repairs.ErrChangedSincePlan)
				require.NotErrorIs(t, err, repairs.ErrPartiallyApplied)
				require.Contains(t, err.Error(), last)
				for _, id := range r.BookIDs {
					require.True(t, f.liveID(t, id), "book %s not retired", id)
					rows, err := f.s.GetBookFiles(id)
					require.NoError(t, err)
					require.Equal(t, before[id], rows, "book %s's rows untouched", id)
				}
				changes, err := f.s.GetOperationChanges("op-apply")
				require.NoError(t, err)
				require.Empty(t, changes, "nothing journaled: %s", fmt.Sprint(changes))
			})
		}
	}
}

// TestFragmentFixer_InterruptedITunesMemberOffersOnlyRevert (review
// 2026-10-06, round 2): an interrupted run whose re-plan is held because one
// of its books became an iTunes book offers only the revert (or, when no
// revert can be offered, asks the owner). Merging by hand would write that
// iTunes book.
func TestFragmentFixer_InterruptedITunesMemberOffersOnlyRevert(t *testing.T) {
	t.Parallel()
	for at := 1; at < 400; at++ {
		f, r, closeF := newCutFixture(t, "all", cutVGNone)
		cut, more := f.cutAt(t, r, at)
		if !more {
			closeF()
			t.Fatal("no cut point left a live book holding its own row")
		}
		if !cut || !f.cutRunRecorded(t, "op-cut") {
			closeF()
			continue
		}
		// A book of the run that is still live and holds its planned row.
		var live string
		var liveRow string
		for _, id := range r.BookIDs {
			if id == r.Proposed["survivor"] || !f.liveID(t, id) {
				continue
			}
			rows, err := f.s.GetBookFiles(id)
			require.NoError(t, err)
			if len(rows) == 1 {
				live, liveRow = id, rows[0].ID
				break
			}
		}
		if live == "" {
			closeF()
			continue
		}
		f.updateRow(t, live, liveRow, func(b *database.BookFile) { b.ITunesPath = "file://localhost/Music/iTunes Media/cut.mp3" })
		held := findRow(t, f.plan(t, "op-plan2"), r.RowID)
		require.False(t, held.Applicable(), "cut at %d", at)
		require.Contains(t, held.SkipReason, "is an iTunes book", "cut at %d: %s", at, held.SkipReason)
		require.NotContains(t, held.SkipReason, "by hand", "cut at %d: %s", at, held.SkipReason)
		closeF()
		return
	}
	t.Fatal("no cut point found")
}
