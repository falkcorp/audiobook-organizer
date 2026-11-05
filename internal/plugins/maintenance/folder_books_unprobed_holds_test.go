// file: internal/plugins/maintenance/folder_books_unprobed_holds_test.go
// version: 1.3.0
// guid: 406b9bc1-da96-44bc-b198-fc0b48344643
// last-edited: 2026-11-05

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/mediainfo"
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

// actionFileIDs is the fileIds a hold's action sends to the backfill, read the
// way the backfill reads its params.
func actionFileIDs(t *testing.T, a *repairs.RowHoldAction) []string {
	t.Helper()
	require.NotNil(t, a)
	var p durationReextractParams
	require.NoError(t, json.Unmarshal(a.Params, &p))
	return p.FileIDs
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
			require.Equal(t, []string{rowID}, actionFileIDs(t, h.Action))
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
	require.Equal(t, durationBackfillOpID, a.OpID)
	wantJSON, err := json.Marshal(map[string]any{"mode": "unknownRows", "fileIds": want, "dryRun": false})
	require.NoError(t, err)
	require.JSONEq(t, string(wantJSON), string(a.Params))

	// The backfill accepts exactly this payload: strict decode, then its own
	// parameter validation, and it resolves to a real (not dry) unknownRows run.
	raw := []byte(a.Params)
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
	require.Equal(t, []string{once}, actionFileIDs(t, h.Action))
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
	require.Less(t, len(h.Why), 500)
}

// One unmeasured member holds a group whose known files alone already clear the
// chapter threshold (before this change the partial sum passed).
func TestFolderBooksFixer_OneUnknownMemberHoldsAGroupThatLooksLongEnough(t *testing.T) {
	f := newGlobalRootFragFixture(t)
	lead, paths := f.fbKnownSeed(t)
	extra := f.file(t, "Alfred Pennyworth/Hush/02.mp3", fbSeedFileSize)
	require.NotEqual(t, paths[0], extra)
	f.row(t, "flat-extra", f.ids["flat"], extra, "", fbSeedFileSize, 0, 7)

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
	require.NotContains(t, row.Current, "unmeasured_majority_shelf_files")

	f.fbProbeState(t, 0, 0, "", database.DurationUnprobed)
	f.fbProbeState(t, 1, 0, "", database.DurationUnprobed)
	row = fbFindRow(f.fbPlan(t, fbFixerID, "op-unknown"), lead)
	require.NotNil(t, row)
	require.Equal(t, "2", row.Current["unmeasured_majority_shelf_files"])
	require.Equal(t, "8.0", row.Current["hours"], "four measured works of two hours each; unknown adds nothing")
}

// Measuring a row's unknown files never moves it between verdicts. classify's
// deep tier only ever adds to an applicable (shelf or author-copy) verdict, so
// the 80-hour threshold a freshly measured file can push the hours over changes
// the evidence readout and nothing else: the row keeps its class, stays
// applicable, and keeps the fingerprint apply compares. A hold keyed on
// "unmeasured files + hours below the threshold" would have broken this (it
// would have held a row that is applied today, and released it only to a deep
// verdict that does not exist).
func TestFolderBooksFixer_MeasuringUnknownFilesKeepsAnApplicableRowApplicable(t *testing.T) {
	f := newGlobalRootFragFixture(t)
	f.seedWolfe(t, fbITunes, "citadel")
	fb, fb2 := f.ids["fb"], f.ids["fb2"]
	// The Claw paths are held by a proper book, so they are no orphan group's
	// members; both folder-books carry their rows (a sibling at the same path
	// and size would otherwise lend its duration to the other).
	claw := []int{3, 4, 5}
	for _, role := range []string{"fb", "fb2"} {
		for _, i := range claw {
			f.fbSetRow(t, role, i, func(bf *database.BookFile) { bf.ResetDuration() })
		}
	}

	before := f.fbSingleRow(t, "op-unknown")
	require.Equal(t, fbRowID([]string{min(fb, fb2), max(fb, fb2)}), before.RowID)
	require.Equal(t, fbTierShelf, before.Class)
	require.True(t, before.Applicable(), "skipped=%q holds=%+v", before.Skipped, before.Holds)
	require.Equal(t, "3", before.Current["unmeasured_majority_shelf_files"])
	require.Less(t, mustParseHours(t, before.Current["hours"]), float64(fbDeepHours),
		"the row starts under the deep threshold, or measuring its files proves nothing")
	for _, k := range []string{fbSkipUnprobed, fbSkipUnreadableAudio} {
		require.False(t, fbHasHold(&before, k), "%s: %+v", k, before.Holds)
	}

	// Thirty hours each: the row's measured hours now pass fbDeepHours.
	const thirtyHours = 30 * 3600
	for _, role := range []string{"fb", "fb2"} {
		for _, i := range claw {
			f.fbSetRow(t, role, i, func(bf *database.BookFile) { require.NoError(t, bf.SetMeasuredDuration(thirtyHours)) })
		}
	}
	after := f.fbSingleRow(t, "op-measured")
	require.GreaterOrEqual(t, mustParseHours(t, after.Current["hours"]), float64(fbDeepHours),
		"the measurement must carry the row over the deep threshold or this test proves nothing")
	require.NotContains(t, after.Current, "unmeasured_majority_shelf_files")
	require.Equal(t, before.RowID, after.RowID)
	require.Equal(t, before.Class, after.Class, "deep only adds to an applicable verdict")
	require.True(t, after.Applicable(), "skipped=%q holds=%+v", after.Skipped, after.Holds)
	require.Equal(t, before.Fingerprint, after.Fingerprint)
	require.Equal(t, before.Holds, after.Holds)
}

func mustParseHours(t *testing.T, s string) float64 {
	t.Helper()
	var h float64
	_, err := fmt.Sscanf(s, "%f", &h)
	require.NoError(t, err)
	return h
}

// stubUnknownRowsProbe replaces the unknownRows backfill's header probe with one that
// reports every file as a two-hour work and counts its calls. It restores the
// real probe when the test ends. The probe is a package variable, so a test
// that uses it must not run in parallel.
func stubUnknownRowsProbe(t *testing.T) *atomic.Int64 {
	t.Helper()
	var calls atomic.Int64
	prev := unknownRowsProbe
	unknownRowsProbe = func(context.Context, string) (*mediainfo.MediaInfo, error) {
		calls.Add(1)
		return &mediainfo.MediaInfo{Duration: fbUHFine, Codec: "mp3", Format: "mp3"}, nil
	}
	t.Cleanup(func() { unknownRowsProbe = prev })
	return &calls
}

// runHoldAction runs a hold's action through the real Backfill book durations op
// (the params exactly as the Repairs page would send them) and returns it, so a
// test proves the offer does what it says rather than only that it is shaped
// right.
func (f *fragFixture) runHoldAction(t *testing.T, a *repairs.RowHoldAction) {
	t.Helper()
	require.NotNil(t, a)
	require.Equal(t, durationBackfillOpID, a.OpID)
	require.NoError(t, f.p.runDurationBackfill(context.Background(), a.Params, &urReporter{}))
}

// The offer closes the loop: Probe these files, run as sent, measures the rows,
// and the next Plan holds nothing for them. (A hold that names files the
// backfill declines would be shown forever.)
func TestFolderBooksFixer_ProbeActionClearsTheHold(t *testing.T) {
	calls := stubUnknownRowsProbe(t)
	f := newGlobalRootFragFixture(t)
	lead, _ := f.fbKnownSeed(t)
	f.fbProbeState(t, 1, 0, "", database.DurationUnprobed)
	f.fbProbeState(t, 3, 1, "ffprobe: Invalid data found", database.DurationProbeFailed)

	row := fbFindRow(f.fbPlan(t, fbFixerID, "op-before"), lead)
	require.NotNil(t, row)
	unprobed, bad := fbHold(row, fbSkipUnprobed), fbHold(row, fbSkipUnreadableAudio)
	require.NotNil(t, unprobed)
	require.NotNil(t, bad)

	f.runHoldAction(t, unprobed.Action)
	f.runHoldAction(t, bad.Action)
	require.EqualValues(t, 2, calls.Load(), "exactly the two named files are probed")

	after := fbFindRow(f.fbPlan(t, fbFixerID, "op-after"), lead)
	require.NotNil(t, after)
	for _, k := range []string{fbSkipUnprobed, fbSkipUnreadableAudio, fbSkipFragmentary} {
		require.False(t, fbHasHold(after, k), "%s still held after the probe: %+v", k, after.Holds)
	}
	require.NotContains(t, after.Current, "unmeasured_majority_shelf_files")
}

// A row the backfill declines is never offered to it: it stays in file_ids, is
// left out of the action, and the Why names what settles it. The row that can be
// probed beside it still is, and after that run the hold names only the one the
// backfill cannot touch, with no action.
func TestFolderBooksFixer_HoldOnlyOffersWhatTheBackfillActsOn(t *testing.T) {
	cases := []struct {
		name   string
		spoil  func(bf *database.BookFile)
		remedy string
	}{
		{"flagged missing although on disk", func(bf *database.BookFile) { bf.Missing = true },
			"flagged missing although the file is on disk: a library scan refreshes the row"},
		{"excluded from scanning", func(bf *database.BookFile) { bf.SkipScan = true },
			"excluded from scanning"},
		{"size changed since the last scan", func(bf *database.BookFile) { bf.FileSize = fbSeedFileSize + 5 },
			"the file is 10 bytes on disk but the row records 15: a library scan refreshes the row"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			calls := stubUnknownRowsProbe(t)
			f := newGlobalRootFragFixture(t)
			lead, _ := f.fbKnownSeed(t)
			idle := f.fbProbeState(t, 0, 0, "", database.DurationUnprobed)
			f.fbSetRow(t, "flat", 0, c.spoil)
			live := f.fbProbeState(t, 1, 0, "", database.DurationUnprobed)

			row := fbFindRow(f.fbPlan(t, fbFixerID, "op-before"), lead)
			require.NotNil(t, row)
			h := fbHold(row, fbSkipUnprobed)
			require.NotNil(t, h, "skipped=%q holds=%+v", row.Skipped, row.Holds)
			want := []string{idle, live}
			slices.Sort(want)
			require.Equal(t, want, h.FileIDs, "every unmeasured file stays named")
			require.Contains(t, h.Why, c.remedy)
			require.Contains(t, h.Why, "the backfill leaves 1 of them alone")
			require.NotNil(t, h.Action)
			require.Equal(t, []string{live}, actionFileIDs(t, h.Action), "the action reaches only the file the backfill acts on")

			f.runHoldAction(t, h.Action)
			require.EqualValues(t, 1, calls.Load())

			after := fbFindRow(f.fbPlan(t, fbFixerID, "op-after"), lead)
			require.NotNil(t, after)
			h = fbHold(after, fbSkipUnprobed)
			require.NotNil(t, h)
			require.Equal(t, []string{idle}, h.FileIDs)
			require.Nil(t, h.Action, "nothing left that the backfill would act on: no offer to loop on")
			require.NotContains(t, h.Why, "run Backfill book durations")
			require.Contains(t, h.Why, c.remedy)
		})
	}
}

// A group is partitioned per member: one file at the attempt ceiling plus one
// never-probed file in the SAME group are held under both kinds, and the
// unprobed file is in the probe action (a group-wide verdict would have hidden
// it behind the unreadable hold, which probes nothing it does not name).
func TestFolderBooksFixer_MixedGroupIsHeldPerMember(t *testing.T) {
	f := newGlobalRootFragFixture(t)
	lead, _ := f.fbKnownSeed(t)
	extra := f.file(t, "Alfred Pennyworth/Hush/02.mp3", fbSeedFileSize)
	f.row(t, "flat-extra", f.ids["flat"], extra, "", fbSeedFileSize, 0, 7)
	extraID := f.rowIDs["flat-extra"]
	ceiling := f.fbProbeState(t, 0, database.DeepScanMaxAttempts, "ffprobe: moov atom not found", database.DurationUnreadable)

	row := fbFindRow(f.fbPlan(t, fbFixerID, "op-plan"), lead)
	require.NotNil(t, row)
	bad, unprobed := fbHold(row, fbSkipUnreadableAudio), fbHold(row, fbSkipUnprobed)
	require.NotNil(t, bad, "%+v", row.Holds)
	require.NotNil(t, unprobed, "%+v", row.Holds)
	require.Equal(t, []string{ceiling}, bad.FileIDs)
	require.Equal(t, []string{extraID}, unprobed.FileIDs)
	require.Contains(t, bad.Why, "1 file(s) in 1 orphan group(s) failed their duration probe")
	require.Contains(t, unprobed.Why, `"Hush" (2 file(s))`)
	require.Nil(t, bad.Action, "a file at the ceiling needs an explicit reset first")
	require.NotNil(t, unprobed.Action)
	require.Equal(t, []string{extraID}, actionFileIDs(t, unprobed.Action))

	// With a retryable failure instead, both actions exist, each over its own file.
	f.fbProbeState(t, 0, 1, "ffprobe: Invalid data found", database.DurationProbeFailed)
	row = fbFindRow(f.fbPlan(t, fbFixerID, "op-plan-2"), lead)
	require.NotNil(t, row)
	bad, unprobed = fbHold(row, fbSkipUnreadableAudio), fbHold(row, fbSkipUnprobed)
	require.NotNil(t, bad)
	require.NotNil(t, unprobed)
	require.NotNil(t, bad.Action)
	require.NotNil(t, unprobed.Action)
	require.Equal(t, []string{ceiling}, actionFileIDs(t, bad.Action))
	require.Equal(t, []string{extraID}, actionFileIDs(t, unprobed.Action))
}

// "Unknown is never a long file" in classify's flat-works test: flat files under
// distinct stems, none measured, are NOT long works, so the folder is not
// flagged; the same files measured at three hours each are. (Counting an unknown
// file as long would flag, and then apply, a folder on no evidence.) Six files:
// fbMinFiles is five.
func TestFolderBooksFixer_UnknownFilesAreNotLongFlatWorks(t *testing.T) {
	const threeHours = 3 * 3600
	seed := func(t *testing.T, secs int) (*fragFixture, string) {
		f := newGlobalRootFragFixture(t)
		_, err := f.s.CreateAuthor("Alfred Pennyworth")
		require.NoError(t, err)
		var paths []string
		for _, n := range []string{"Hush", "Year One", "Knightfall", "The Long Halloween", "Court of Owls", "Zero Year"} {
			paths = append(paths, f.file(t, fbITunes+"/Alfred Pennyworth/"+n+".mp3", fbSeedFileSize))
		}
		return f, f.fbBook(t, "flat", "Alfred Pennyworth", f.path(fbITunes+"/Alfred Pennyworth"), secs, paths...)
	}

	t.Run("measured at three hours each is flagged", func(t *testing.T) {
		f, id := seed(t, threeHours)
		row := fbFindRow(f.fbPlan(t, fbFixerID, "op-plan"), id)
		require.NotNil(t, row, "long flat works under distinct stems are a folder-book")
		require.Equal(t, fbTierShelf, row.Class)
		require.Contains(t, strings.Join(row.Evidence, "|"), "6 flat files of 3h or more under distinct names")
	})
	t.Run("unmeasured is not flagged", func(t *testing.T) {
		f, id := seed(t, threeHours)
		for i := range 6 {
			f.fbProbeState(t, i, 0, "", database.DurationUnprobed)
		}
		require.Nil(t, fbFindRow(f.fbPlan(t, fbFixerID, "op-plan"), id),
			"an unknown duration must not read as a long file")
	})
}

// TestFolderBooksFixer_BackfillDeclinedFilesAreNeverOffered seeds an extra unmeasured file beside a normal one and checks the
// same shape for each way the backfill declines a file the stat can show: the
// declined file stays named, its Why says what is wrong, it is never in the
// action; the other file is probed through the action; and the re-plan offers
// nothing for the declined file.
func TestFolderBooksFixer_BackfillDeclinedFilesAreNeverOffered(t *testing.T) {
	cases := []struct {
		name string
		// make creates the declined file under f's root and returns its path;
		// it may call t.Skip.
		make   func(t *testing.T, f *fragFixture, rel string) string
		rel    string
		remedy string
	}{
		{"an extension the probe does not read",
			func(t *testing.T, f *fragFixture, rel string) string { return f.file(t, rel, fbSeedFileSize) },
			"Alfred Pennyworth/Hush/02.txt", "is not one the duration probe reads"},
		{"a directory at the row's path",
			func(t *testing.T, f *fragFixture, rel string) string {
				p := f.path(rel)
				require.NoError(t, os.MkdirAll(p, 0o755))
				return p
			},
			"Alfred Pennyworth/Hush/02.mp3", "not a regular file"},
		{"a file the process cannot open",
			func(t *testing.T, f *fragFixture, rel string) string {
				if os.Geteuid() == 0 {
					t.Skip("root opens a mode-000 file")
				}
				p := f.file(t, rel, fbSeedFileSize)
				require.NoError(t, os.Chmod(p, 0))
				t.Cleanup(func() { _ = os.Chmod(p, 0o600) })
				if fh, err := os.Open(p); err == nil {
					_ = fh.Close()
					t.Skip("this filesystem ignores file modes")
				}
				return p
			},
			"Alfred Pennyworth/Hush/02.mp3", "cannot be opened"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			calls := stubUnknownRowsProbe(t)
			f := newGlobalRootFragFixture(t)
			lead, _ := f.fbKnownSeed(t)
			p := c.make(t, f, c.rel)
			f.row(t, "flat-declined", f.ids["flat"], p, "", fbSeedFileSize, 0, 7)
			declined := f.rowIDs["flat-declined"]
			live := f.fbProbeState(t, 1, 0, "", database.DurationUnprobed)

			row := fbFindRow(f.fbPlan(t, fbFixerID, "op-before"), lead)
			require.NotNil(t, row)
			h := fbHold(row, fbSkipUnprobed)
			require.NotNil(t, h, "skipped=%q holds=%+v", row.Skipped, row.Holds)
			want := []string{declined, live}
			slices.Sort(want)
			require.Equal(t, want, h.FileIDs, "both files stay named")
			require.Contains(t, h.Why, c.remedy)
			require.Equal(t, []string{live}, actionFileIDs(t, h.Action), "the declined file is not in the action")

			f.runHoldAction(t, h.Action)
			require.EqualValues(t, 1, calls.Load(), "the declined file is never probed")

			after := fbFindRow(f.fbPlan(t, fbFixerID, "op-after"), lead)
			require.NotNil(t, after)
			h = fbHold(after, fbSkipUnprobed)
			require.NotNil(t, h)
			require.Equal(t, []string{declined}, h.FileIDs)
			require.Nil(t, h.Action, "nothing left the backfill would act on: no offer to loop on")
			require.Contains(t, h.Why, c.remedy)
		})
	}
}

// A stat that fails with anything but "does not exist" leaves the file's size
// and type unknown: the hold must not offer it (fail closed), however the rest
// of the row looks.
func TestFolderBooksFixer_StatFailureIsNotOffered(t *testing.T) {
	f := newGlobalRootFragFixture(t)
	_, paths := f.fbKnownSeed(t)
	denied := paths[2]
	id := f.fbProbeState(t, 2, 0, "", database.DurationUnprobed)
	fx := newFolderBooksFixer(f.p)
	fx.stat = func(p string) (os.FileInfo, error) {
		if p == denied {
			return nil, &fs.PathError{Op: "stat", Path: p, Err: fs.ErrPermission}
		}
		return os.Stat(p)
	}
	rows, err := fx.Plan(context.Background(), nil, &repairsOpReporter{id: "op-plan"})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	h := fbHold(&rows[0], fbSkipUnprobed)
	require.NotNil(t, h, "%+v", rows[0].Holds)
	require.Equal(t, []string{id}, h.FileIDs)
	require.Nil(t, h.Action)
	require.Contains(t, h.Why, "could not be checked on disk")
	require.True(t, fbHasHold(&rows[0], fbSkipUnreadable), "the row is also held unreadable: %+v", rows[0].Holds)
}

// A row with a fingerprint duration is promoted by the backfill without a look
// at the disk, so a size that no longer matches is no reason to decline it, and
// the action reaches it.
func TestFolderBooksFixer_FingerprintDurationRowIsOfferedDespiteASizeMismatch(t *testing.T) {
	stubUnknownRowsProbe(t)
	f := newGlobalRootFragFixture(t)
	lead, _ := f.fbKnownSeed(t)
	f.fbProbeState(t, 0, 0, "", database.DurationUnprobed)
	f.fbSetRow(t, "flat", 0, func(bf *database.BookFile) {
		bf.AcoustIDFingerprintDurationSec = fbUHFine
		bf.FileSize = fbSeedFileSize + 5
	})
	id := f.rowIDs["flat-0"]

	row := fbFindRow(f.fbPlan(t, fbFixerID, "op-before"), lead)
	require.NotNil(t, row)
	h := fbHold(row, fbSkipUnprobed)
	require.NotNil(t, h, "skipped=%q holds=%+v", row.Skipped, row.Holds)
	require.Equal(t, []string{id}, actionFileIDs(t, h.Action))
	require.NotContains(t, h.Why, "bytes on disk")

	f.runHoldAction(t, h.Action)
	after := fbFindRow(f.fbPlan(t, fbFixerID, "op-after"), lead)
	require.NotNil(t, after)
	require.False(t, fbHasHold(after, fbSkipUnprobed), "%+v", after.Holds)
}

// fbUnknownFileOf fails closed on what the fixer could not learn about a file,
// and the open it makes is the caller's, so these cases need no real file mode
// (a root run or a filesystem that ignores modes cannot weaken them).
func TestFbUnknownFileOf_FailsClosedOnStatAndOpen(t *testing.T) {
	row := func(mut func(*database.BookFileCore)) database.BookFileCore {
		r := database.BookFileCore{ID: "r", BookID: "b", FilePath: "/lib/Batman/Hush.mp3", FileSize: 10}
		if mut != nil {
			mut(&r)
		}
		return r
	}
	fingerprinted := func(r *database.BookFileCore) { r.AcoustIDFingerprintDurationSec = 30; r.FileSize = 480_000 }
	opens := func(string) error { return nil }
	refuses := func(string) error { return fs.ErrPermission }
	statted := fbFileLook{disk: &unknownRowDisk{Size: 10, Regular: true}}
	cases := []struct {
		name string
		r    database.BookFileCore
		look fbFileLook
		open func(string) error
		acts bool
		why  string
	}{
		{"openable and statted is offered", row(nil), statted, opens, true, ""},
		{"a failed stat is not offered", row(nil), fbFileLook{statErr: fs.ErrPermission}, opens, false, "could not be checked on disk"},
		{"a file that will not open is not offered", row(nil), statted, refuses, false, "cannot be opened"},
		{"a fingerprint duration needs neither the stat", row(fingerprinted), fbFileLook{statErr: fs.ErrPermission}, refuses, true, ""},
		{"nor the open", row(fingerprinted), statted, refuses, true, ""},
		{"a declined row keeps its own reason, not the open's", row(func(r *database.BookFileCore) { r.FilePath = "/lib/Batman/Hush.txt" }),
			statted, refuses, false, "is not one the duration probe reads"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := fbUnknownFileOf(c.r, c.look, c.open)
			require.Equal(t, c.acts, got.acts)
			if c.acts {
				require.Empty(t, got.why)
				return
			}
			require.Contains(t, got.why, c.why)
		})
	}
}
