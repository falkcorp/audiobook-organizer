// file: internal/plugins/maintenance/folder_books_unprobed_holds_test.go
// version: 1.1.0
// guid: 406b9bc1-da96-44bc-b198-fc0b48344643
// last-edited: 2026-11-05

package maintenance

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// The Alfred Pennyworth shelf (fbBorrowSeed) has six one-file works: Hush, The
// Long Halloween, Year One, Knightfall, Court of Owls, Zero Year, in row order.
// These tests start from all six measured at two hours (none fragmentary), then
// spoil one row at a time.
const (
	fbUHFine  = 7200 // a measured two-hour work: not fragmentary
	fbUHShort = 60   // a measured one-minute work: fragmentary
)

// fbKnownSeed is fbBorrowSeed with every row measured at fbUHFine.
func (f *fragFixture) fbKnownSeed(t *testing.T) (lead string, paths []string) {
	t.Helper()
	lead, paths = f.fbBorrowSeed(t)
	for i := range paths {
		f.fbMeasure(t, i, fbUHFine)
	}
	return lead, paths
}

// fbMeasure stores a measured duration on the lead's row idx.
func (f *fragFixture) fbMeasure(t *testing.T, idx, sec int) {
	t.Helper()
	f.fbSetRow(t, "flat", idx, func(bf *database.BookFile) { require.NoError(t, bf.SetMeasuredDuration(sec)) })
}

// fbProbeState leaves the lead's row idx unmeasured with attempts failed probes
// (0 = never probed) and reads it back, so a test that asserts a hold proves
// the store kept the state the hold is a verdict on. It returns the row id.
func (f *fragFixture) fbProbeState(t *testing.T, idx, attempts int, lastErr string, want database.BookFileDurationState) string {
	t.Helper()
	f.fbSetRow(t, "flat", idx, func(bf *database.BookFile) {
		bf.ResetDuration()
		for range attempts {
			bf.RecordDurationProbeFailure(lastErr)
		}
	})
	id := f.rowIDs[fmt.Sprintf("flat-%d", idx)]
	got, err := f.s.GetBookFileByID(f.ids["flat"], id)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, want, got.DurationState(), "the store did not keep the probe state: %+v", got.Scan.DurationOK)
	if attempts > 0 {
		require.Equal(t, lastErr, got.Scan.DurationOK.LastError)
	}
	return id
}

func fbHold(row *repairs.Row, kind string) *repairs.RowHold {
	for i := range row.Holds {
		if row.Holds[i].Kind == kind {
			return &row.Holds[i]
		}
	}
	return nil
}

func TestFolderBooksFixer_UnknownDurationIsHeldNotShort(t *testing.T) {
	const badErr = "ffprobe: Invalid data found when processing input"
	slow := database.DurationProbeTimeoutPrefix + "ffprobe did not answer in 30s"
	cases := []struct {
		name string
		// spoil spoils row 0 (Hush) and returns its row id ("" when it only
		// shortens it).
		spoil func(f *fragFixture, t *testing.T) string
		kind  string
		// why are the fragments the hold's Why carries.
		why []string
		// action: the hold offers to probe row 0.
		action bool
	}{
		{"all-known short group is fragmentary",
			func(f *fragFixture, t *testing.T) string { f.fbMeasure(t, 0, fbUHShort); return "" },
			fbSkipFragmentary, []string{`"Hush" (1 file(s), 60s)`}, false},
		{"unprobed is held unprobed, not fragmentary",
			func(f *fragFixture, t *testing.T) string {
				return f.fbProbeState(t, 0, 0, "", database.DurationUnprobed)
			},
			fbSkipUnprobed, []string{"1 file(s) have no measured duration; run Backfill book durations (unknown rows) for book(s) ", `"Hush" (1 file(s))`}, true},
		{"a failed probe is held unreadable",
			func(f *fragFixture, t *testing.T) string {
				return f.fbProbeState(t, 0, 1, badErr, database.DurationProbeFailed)
			},
			fbSkipUnreadableAudio, []string{"1 file(s) in 1 orphan group(s) failed their duration probe and may be bad audio", "Hush/01.mp3 (" + badErr + ")"}, true},
		{"a file at the attempt ceiling is held unreadable, with nothing to re-probe",
			func(f *fragFixture, t *testing.T) string {
				return f.fbProbeState(t, 0, database.DeepScanMaxAttempts, badErr, database.DurationUnreadable)
			},
			fbSkipUnreadableAudio, []string{"Hush/01.mp3 (" + badErr + ")"}, false},
		{"a timeout below the ceiling is unprobed, not unreadable",
			func(f *fragFixture, t *testing.T) string {
				return f.fbProbeState(t, 0, 1, slow, database.DurationProbeFailed)
			},
			fbSkipUnprobed, []string{"1 file(s) have no measured duration"}, true},
		{"a timeout-only unreadable row is unprobed, not unreadable",
			func(f *fragFixture, t *testing.T) string {
				return f.fbProbeState(t, 0, database.DeepScanMaxAttempts, slow, database.DurationUnreadable)
			},
			fbSkipUnprobed, []string{"1 file(s) have no measured duration"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newGlobalRootFragFixture(t)
			lead, _ := f.fbKnownSeed(t)
			rowID := c.spoil(f, t)

			res := f.fbPlan(t, fbFixerID, "op-plan")
			row := fbFindRow(res, lead)
			require.NotNil(t, row)

			h := fbHold(row, c.kind)
			require.NotNil(t, h, "want a %s hold: skipped=%q holds=%+v", c.kind, row.Skipped, row.Holds)
			require.False(t, h.OwnerOK, "an owner click cannot know the runtime of an unmeasured file")
			for _, w := range c.why {
				require.Contains(t, h.Why, w)
			}
			// The verdicts are exclusive for the one spoiled group, and hard:
			// no owner click resolves the row.
			for _, other := range []string{fbSkipFragmentary, fbSkipUnprobed, fbSkipUnreadableAudio} {
				require.Equal(t, other == c.kind, fbHasHold(row, other), "kind %s: %+v", other, row.Holds)
			}
			require.False(t, row.OwnerApplicable)
			require.Equal(t, c.kind, row.Skipped, "the new holds come before every later one")

			if rowID == "" {
				require.Empty(t, h.FileIDs)
				require.Nil(t, h.Action)
				return
			}
			require.Equal(t, []string{rowID}, h.FileIDs)
			if !c.action {
				require.Nil(t, h.Action, "the backfill leaves a row that failed the ceiling alone")
				return
			}
			require.NotNil(t, h.Action)
			require.Equal(t, []string{rowID}, h.Action.Params["fileIds"])
		})
	}
}

// The action starts the real duration backfill, in the mode and spelling it
// accepts, on exactly the unmeasured rows, sorted.
func TestFolderBooksFixer_UnprobedHoldActionPayload(t *testing.T) {
	f := newGlobalRootFragFixture(t)
	lead, _ := f.fbKnownSeed(t)
	want := []string{
		f.fbProbeState(t, 1, 0, "", database.DurationUnprobed),
		f.fbProbeState(t, 3, 0, "", database.DurationUnprobed),
		f.fbProbeState(t, 4, 0, "", database.DurationUnprobed),
	}
	// A short measured row and an unreadable row are not in it.
	f.fbMeasure(t, 0, fbUHShort)
	f.fbProbeState(t, 2, database.DeepScanMaxAttempts, "ffprobe: moov atom not found", database.DurationUnreadable)
	slices.Sort(want)

	row := fbFindRow(f.fbPlan(t, fbFixerID, "op-plan"), lead)
	require.NotNil(t, row)
	h := fbHold(row, fbSkipUnprobed)
	require.NotNil(t, h)
	require.Equal(t, want, h.FileIDs)
	require.Contains(t, h.Why, "3 file(s) have no measured duration")

	a := h.Action
	require.NotNil(t, a)
	require.Equal(t, "Probe these files", a.Label)
	require.Equal(t, f.p.durationBackfillDef().ID, a.OpID, "the action must name the real op")
	require.Equal(t, map[string]any{"mode": "unknownRows", "fileIds": want, "dryRun": false}, a.Params)

	// The backfill accepts exactly this payload: strict decode, then its own
	// parameter validation, and it resolves to a real (not dry) unknownRows run.
	raw, err := json.Marshal(a.Params)
	require.NoError(t, err)
	var p durationReextractParams
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	require.NoError(t, dec.Decode(&p))
	require.NoError(t, validateUnknownRowsParams(p, raw))
	require.Equal(t, durationModeUnknownRows, p.Mode)
	require.Equal(t, want, p.FileIDs)
	require.NotNil(t, p.DryRun)
	require.False(t, *p.DryRun)

	// The other two verdicts of the row stay distinct.
	require.True(t, fbHasHold(row, fbSkipFragmentary))
	bad := fbHold(row, fbSkipUnreadableAudio)
	require.NotNil(t, bad)
	require.Len(t, bad.FileIDs, 1)
	require.Nil(t, bad.Action, "a row at the ceiling is settled for the backfill")
}

// A below-ceiling failure is re-probed; the at-ceiling row of the same hold is
// not in the action's ids.
func TestFolderBooksFixer_UnreadableHoldReprobesOnlyWhatTheBackfillActsOn(t *testing.T) {
	f := newGlobalRootFragFixture(t)
	lead, _ := f.fbKnownSeed(t)
	once := f.fbProbeState(t, 0, 1, "ffprobe: Invalid data found", database.DurationProbeFailed)
	ceiling := f.fbProbeState(t, 1, database.DeepScanMaxAttempts, "ffprobe: moov atom not found", database.DurationUnreadable)

	row := fbFindRow(f.fbPlan(t, fbFixerID, "op-plan"), lead)
	require.NotNil(t, row)
	h := fbHold(row, fbSkipUnreadableAudio)
	require.NotNil(t, h)
	want := []string{once, ceiling}
	slices.Sort(want)
	require.Equal(t, want, h.FileIDs)
	require.Contains(t, h.Why, "2 file(s) in 2 orphan group(s)")
	require.NotNil(t, h.Action)
	require.Equal(t, "Re-probe these files", h.Action.Label)
	require.Equal(t, []string{once}, h.Action.Params["fileIds"])
}

// A long probe error is clipped in the Why.
func TestFolderBooksFixer_UnreadableWhyClipsTheError(t *testing.T) {
	f := newGlobalRootFragFixture(t)
	lead, _ := f.fbKnownSeed(t)
	f.fbProbeState(t, 0, 1, strings.Repeat("moov ", 200), database.DurationProbeFailed)
	row := fbFindRow(f.fbPlan(t, fbFixerID, "op-plan"), lead)
	require.NotNil(t, row)
	h := fbHold(row, fbSkipUnreadableAudio)
	require.NotNil(t, h)
	require.Contains(t, h.Why, "...)")
	// The 1000-character error is cut to fbErrClip runes; the rest of the Why
	// (path, counts, the lower-bound sentence) stays well under the original.
	require.NotContains(t, h.Why, strings.Repeat("moov ", 200))
	require.Less(t, len(h.Why), 800)
}

// One unmeasured member holds a group whose known files alone already clear the
// chapter threshold (before this change the partial sum passed).
func TestFolderBooksFixer_OneUnknownMemberHoldsAGroupThatLooksLongEnough(t *testing.T) {
	f := newGlobalRootFragFixture(t)
	lead, paths := f.fbKnownSeed(t)
	extra := f.file(t, "Alfred Pennyworth/Hush/02.mp3", 10)
	require.NotEqual(t, paths[0], extra)
	f.row(t, "flat-extra", f.ids["flat"], extra, "", 1999, 0, 7)

	row := fbFindRow(f.fbPlan(t, fbFixerID, "op-plan"), lead)
	require.NotNil(t, row)
	h := fbHold(row, fbSkipUnprobed)
	require.NotNil(t, h, "skipped=%q holds=%+v", row.Skipped, row.Holds)
	require.Contains(t, h.Why, `"Hush" (2 file(s))`)
	require.Equal(t, []string{f.rowIDs["flat-extra"]}, h.FileIDs)
	require.False(t, fbHasHold(row, fbSkipFragmentary))
}

// A sibling's measurement clears the unprobed hold with nothing probed, and the
// row is not held again as short.
func TestFolderBooksFixer_SiblingBorrowClearsUnprobedHold(t *testing.T) {
	f := newGlobalRootFragFixture(t)
	lead, paths := f.fbKnownSeed(t)
	f.fbProbeState(t, 2, 0, "", database.DurationUnprobed)
	r, err := f.s.GetBookFileByID(lead, f.rowIDs["flat-2"])
	require.NoError(t, err)

	before := fbFindRow(f.fbPlan(t, fbFixerID, "op-before"), lead)
	require.NotNil(t, before)
	require.True(t, fbHasHold(before, fbSkipUnprobed), "%+v", before.Holds)

	donor, err := f.s.CreateBook(&database.Book{Title: "Batman: Year One", FilePath: f.path("Alfred Pennyworth/Year One")})
	require.NoError(t, err)
	// The unmeasured file's size, so a same-size sibling at its path lends.
	f.row(t, "donor-0", donor.ID, paths[2], "", r.FileSize, fbUHFine, 1)

	after := fbFindRow(f.fbPlan(t, fbFixerID, "op-after"), lead)
	require.NotNil(t, after)
	require.False(t, fbHasHold(after, fbSkipUnprobed), "%+v", after.Holds)
	require.False(t, fbHasHold(after, fbSkipUnreadableAudio), "%+v", after.Holds)
	require.False(t, fbHasHold(after, fbSkipFragmentary), "%+v", after.Holds)
}

// The eval-hours readout is a measured lower bound, and says how many files it
// leaves out; a row with every duration known carries no such key.
func TestFolderBooksFixer_RowSaysHowManyFilesTheHoursLeaveOut(t *testing.T) {
	f := newGlobalRootFragFixture(t)
	lead, _ := f.fbKnownSeed(t)
	row := fbFindRow(f.fbPlan(t, fbFixerID, "op-known"), lead)
	require.NotNil(t, row)
	require.NotContains(t, row.Current, "unmeasured_files")

	f.fbProbeState(t, 0, 0, "", database.DurationUnprobed)
	f.fbProbeState(t, 1, 0, "", database.DurationUnprobed)
	row = fbFindRow(f.fbPlan(t, fbFixerID, "op-unknown"), lead)
	require.NotNil(t, row)
	require.Equal(t, "2", row.Current["unmeasured_files"])
	require.Equal(t, "8.0", row.Current["hours"], "four measured works of two hours each; unknown adds nothing")
}

// fbHeldSeed is fbKnownSeed plus one proper two-file book per pair of path
// indexes in pairs. Those paths are held by a proper book (not orphans), and
// two such books make the lead a shelf folder-book. The donors' rows differ in
// size from the lead's, so they never lend a duration.
func (f *fragFixture) fbHeldSeed(t *testing.T, pairs ...[2]int) (lead string, paths []string) {
	t.Helper()
	lead, paths = f.fbKnownSeed(t)
	for n, pr := range pairs {
		b, err := f.s.CreateBook(&database.Book{Title: fmt.Sprintf("Batman: Volume %d", n+1), FilePath: f.path(fmt.Sprintf("Alfred Pennyworth/Volume %d", n+1))})
		require.NoError(t, err)
		for k, i := range pr {
			f.row(t, fmt.Sprintf("held-%d-%d", n, k), b.ID, paths[i], "", int64(1000+i)+7, fbUHFine, k+1)
		}
	}
	return lead, paths
}

func fbCountHolds(row *repairs.Row, kind string) (n int) {
	for _, h := range row.Holds {
		if h.Kind == kind {
			n++
		}
	}
	return n
}

// A folder-book whose measured hours are under the deep threshold but which has
// unmeasured files is held (they could be the hours that make it deep), even
// when every file is held by a proper book and no orphan group is involved.
func TestFolderBooksFixer_UnmeasuredFilesHoldARowWhoseHoursAreALowerBound(t *testing.T) {
	const bad = "ffprobe: moov atom not found"
	t.Run("a row with no unmeasured file is unchanged", func(t *testing.T) {
		f := newGlobalRootFragFixture(t)
		lead, _ := f.fbHeldSeed(t, [2]int{0, 1}, [2]int{2, 3}, [2]int{4, 5})
		row := fbFindRow(f.fbPlan(t, fbFixerID, "op-plan"), lead)
		require.NotNil(t, row)
		require.Empty(t, row.Skipped, "holds=%+v", row.Holds)
		require.Empty(t, row.Holds)
		require.NotContains(t, row.Current, "unmeasured_files")
	})
	t.Run("unprobed files hold the row, with the probe action", func(t *testing.T) {
		f := newGlobalRootFragFixture(t)
		lead, _ := f.fbHeldSeed(t, [2]int{0, 1}, [2]int{2, 3}, [2]int{4, 5})
		a := f.fbProbeState(t, 0, 0, "", database.DurationUnprobed)
		b := f.fbProbeState(t, 5, 0, "", database.DurationUnprobed)
		row := fbFindRow(f.fbPlan(t, fbFixerID, "op-plan"), lead)
		require.NotNil(t, row)
		h := fbHold(row, fbSkipUnprobed)
		require.NotNil(t, h, "skipped=%q holds=%+v", row.Skipped, row.Holds)
		require.False(t, h.OwnerOK)
		require.Equal(t, fbSkipUnprobed, row.Skipped)
		require.False(t, row.OwnerApplicable)
		want := []string{a, b}
		slices.Sort(want)
		require.Equal(t, want, h.FileIDs)
		require.Contains(t, h.Why, "2 file(s) have no measured duration; run Backfill book durations (unknown rows)")
		require.Contains(t, h.Why, "the row's 8.0 measured hours are a lower bound (2 file(s) unmeasured)")
		require.NotContains(t, h.Why, "orphan group(s) held", "no orphan group is involved")
		require.NotNil(t, h.Action)
		require.Equal(t, want, h.Action.Params["fileIds"])
		require.Equal(t, 1, fbCountHolds(row, fbSkipUnprobed))
	})
	t.Run("only files at the attempt ceiling: unreadable, no action", func(t *testing.T) {
		f := newGlobalRootFragFixture(t)
		lead, _ := f.fbHeldSeed(t, [2]int{0, 1}, [2]int{2, 3}, [2]int{4, 5})
		a := f.fbProbeState(t, 1, database.DeepScanMaxAttempts, bad, database.DurationUnreadable)
		row := fbFindRow(f.fbPlan(t, fbFixerID, "op-plan"), lead)
		require.NotNil(t, row)
		h := fbHold(row, fbSkipUnreadableAudio)
		require.NotNil(t, h, "skipped=%q holds=%+v", row.Skipped, row.Holds)
		require.False(t, h.OwnerOK)
		require.Equal(t, []string{a}, h.FileIDs)
		require.Nil(t, h.Action)
		require.Contains(t, h.Why, "lower bound")
		require.False(t, fbHasHold(row, fbSkipUnprobed))
		require.False(t, row.OwnerApplicable)
	})
	t.Run("a mix splits into one hold per kind", func(t *testing.T) {
		f := newGlobalRootFragFixture(t)
		lead, _ := f.fbHeldSeed(t, [2]int{0, 1}, [2]int{2, 3}, [2]int{4, 5})
		dead := f.fbProbeState(t, 0, database.DeepScanMaxAttempts, bad, database.DurationUnreadable)
		fresh := f.fbProbeState(t, 1, 0, "", database.DurationUnprobed)
		row := fbFindRow(f.fbPlan(t, fbFixerID, "op-plan"), lead)
		require.NotNil(t, row)
		require.Equal(t, []string{dead}, fbHold(row, fbSkipUnreadableAudio).FileIDs)
		require.Equal(t, []string{fresh}, fbHold(row, fbSkipUnprobed).FileIDs)
	})
	t.Run("measured hours that already reach deep add no hold", func(t *testing.T) {
		f := newGlobalRootFragFixture(t)
		lead, _ := f.fbHeldSeed(t, [2]int{0, 1}, [2]int{2, 3}, [2]int{4, 5})
		for i := range 5 {
			f.fbMeasure(t, i, 17*3600) // 85 h measured
		}
		f.fbProbeState(t, 5, 0, "", database.DurationUnprobed)
		row := fbFindRow(f.fbPlan(t, fbFixerID, "op-plan"), lead)
		require.NotNil(t, row)
		require.Equal(t, "85.0", row.Current["hours"])
		require.Equal(t, "1", row.Current["unmeasured_files"])
		require.False(t, fbHasHold(row, fbSkipUnprobed), "holds=%+v", row.Holds)
		require.False(t, fbHasHold(row, fbSkipUnreadableAudio), "holds=%+v", row.Holds)
	})
}

// A file that both an orphan group and the row's hours check find is named once,
// in one hold per kind, and the hold carries the union of the two sources.
func TestFolderBooksFixer_RowAndGroupHoldsShareOneHoldPerKind(t *testing.T) {
	f := newGlobalRootFragFixture(t)
	// Paths 0-3 are held by proper books; 4 and 5 are orphans (two groups).
	lead, _ := f.fbHeldSeed(t, [2]int{0, 1}, [2]int{2, 3})
	held := f.fbProbeState(t, 0, 0, "", database.DurationUnprobed)   // row check only
	orphan := f.fbProbeState(t, 4, 0, "", database.DurationUnprobed) // row check AND its group
	row := fbFindRow(f.fbPlan(t, fbFixerID, "op-plan"), lead)
	require.NotNil(t, row)
	require.Equal(t, 1, fbCountHolds(row, fbSkipUnprobed), "holds=%+v", row.Holds)
	h := fbHold(row, fbSkipUnprobed)
	want := []string{held, orphan}
	slices.Sort(want)
	require.Equal(t, want, h.FileIDs)
	require.Contains(t, h.Why, "2 file(s) have no measured duration")
	require.Contains(t, h.Why, `orphan group(s) held: "Court of Owls" (1 file(s))`)
	require.Contains(t, h.Why, "lower bound")
	require.Equal(t, want, h.Action.Params["fileIds"])
	require.Equal(t, 0, fbCountHolds(row, fbSkipUnreadableAudio))
}
