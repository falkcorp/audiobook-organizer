// file: internal/plugins/maintenance/dryrun_spelling_test.go
// version: 1.0.0
// guid: 3f0d6b52-8c1e-4a97-b5d4-71e2a9c04f68
// last-edited: 2026-10-30

package maintenance

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// TestDryRunSpellings_ConflictRefusedByEveryOp pins that both JSON spellings of
// the mode flag (dry_run and dryRun) reach opmode.ResolveDryRun in the ops that
// used to decode only dry_run. A body that states them with different values is
// refused before the op does any work, so an operator who sent dryRun=false
// next to a stale dry_run=true gets an error naming both instead of a silent
// preview (or a silent apply).
//
// Before 2026-10-30 these ops had no field for dryRun at all: encoding/json
// dropped it without a word, so {"dryRun":false} previewed and the caller saw a
// completed run that had written nothing. The empty MockStore is deliberate: an
// op that got past the mode check would call a method the mock does not set,
// which this test reports as "not at the mode check".
func TestDryRunSpellings_ConflictRefusedByEveryOp(t *testing.T) {
	p := New(&fakeDeps{store: &database.MockStore{}})
	// auto-match-transcribed checks for the metadata fetch service before it
	// reads its params, so it needs the deps that supply one.
	pAutoMatch := newAutoMatchPlugin(nil, &autoMatchDeps{})
	ops := []struct {
		id  string
		run func(context.Context, json.RawMessage, sdk.Reporter) error
	}{
		{"maintenance.author-conjunction-repair", p.runAuthorConjunctionRepair},
		{"maintenance.author-duplicate-merge", p.runAuthorDuplicateMerge},
		{"maintenance.auto-match-transcribed", pAutoMatch.runAutoMatchTranscribed},
		{"maintenance.intro-migrate-single-file", p.runIntroMigrateSingleFile},
		{"maintenance.repair-library-state", p.repairLibraryStateDef().Run},
		{"maintenance.repair-transcribe-status", p.runRepairTranscribeStatus},
		{"maintenance.rewrite-path-prefix", p.runRewritePathPrefix},
		{"maintenance.series-phantom-repair", p.runSeriesPhantomRepair},
		{"maintenance.duration-reextract", p.runDurationBackfill},
	}
	for _, op := range ops {
		for _, body := range []string{`{"dry_run":false,"dryRun":true}`, `{"dry_run":true,"dryRun":false}`} {
			t.Run(op.id+" "+body, func(t *testing.T) {
				var err error
				func() {
					defer func() {
						if r := recover(); r != nil {
							t.Fatalf("%s ran past the mode check on a conflicting body (panic: %v)", op.id, r)
						}
					}()
					err = op.run(context.Background(), json.RawMessage(body), &fakeReporter{})
				}()
				require.Error(t, err, "%s accepted a body whose two spellings disagree", op.id)
				require.Contains(t, err.Error(), "disagree", "%s: %v", op.id, err)
				require.Contains(t, err.Error(), op.id, "the refusal must name the op")
			})
		}
	}
}

// TestVersionGroupPrimaryRepair_DryRunSpellings: resolveWrite reads both
// spellings, still refuses apply:true beside a stated true, and refuses a
// disagreement.
func TestVersionGroupPrimaryRepair_DryRunSpellings(t *testing.T) {
	tr, fa := true, false
	cases := []struct {
		name      string
		p         vgPrimaryRepairParams
		wantWrite bool
		wantErr   string
	}{
		{"omitted previews", vgPrimaryRepairParams{}, false, ""},
		{"dry_run=false writes", vgPrimaryRepairParams{DryRun: &fa}, true, ""},
		{"dryRun=false writes", vgPrimaryRepairParams{DryRunCamel: &fa}, true, ""},
		{"dryRun=true previews", vgPrimaryRepairParams{DryRunCamel: &tr}, false, ""},
		{"apply alone writes", vgPrimaryRepairParams{Apply: true}, true, ""},
		{"apply with dryRun=false writes", vgPrimaryRepairParams{Apply: true, DryRunCamel: &fa}, true, ""},
		{"apply beside dry_run=true refused", vgPrimaryRepairParams{Apply: true, DryRun: &tr}, false, "contradicts"},
		{"apply beside dryRun=true refused", vgPrimaryRepairParams{Apply: true, DryRunCamel: &tr}, false, "contradicts"},
		{"spellings disagree refused", vgPrimaryRepairParams{DryRun: &fa, DryRunCamel: &tr}, false, "disagree"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.p.resolveWrite()
			if tc.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.wantErr)
				require.False(t, got, "a refused body must not write")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.wantWrite, got)
		})
	}
}

// TestVersionGroupPrimaryRepair_ConflictingSpellingsWriteNothing runs the op
// body itself on a disagreeing body: it is refused with nothing demoted.
func TestVersionGroupPrimaryRepair_ConflictingSpellingsWriteNothing(t *testing.T) {
	f := newVGRepairFixture(t)
	f.seed(t)
	var params vgPrimaryRepairParams
	require.NoError(t, json.Unmarshal([]byte(`{"group_ids":["vg-double"],"dry_run":true,"dryRun":false}`), &params))
	_, err := f.run(t, fakeDeps{store: f.s}, params)
	require.Error(t, err)
	require.Contains(t, err.Error(), "disagree")
	require.Equal(t, "true", f.flag(t, "B"), "a refused body demoted B")
}

// TestLibraryStateRepairAndRewritePathPrefix_DryRunCamel: dryRun is read like
// dry_run by the two ops that expose the mode as a params method.
func TestLibraryStateRepairAndRewritePathPrefix_DryRunCamel(t *testing.T) {
	fa, tr := false, true
	require.True(t, (libraryStateRepairParams{}).dryRun(), "omitted previews")
	require.False(t, (libraryStateRepairParams{DryRunCamel: &fa}).dryRun(), "dryRun=false writes")
	require.True(t, (libraryStateRepairParams{DryRunCamel: &tr}).dryRun())
	require.True(t, (libraryStateRepairParams{DryRun: &fa, DryRunCamel: &tr}).dryRun(), "a disagreement fails toward preview")

	require.True(t, (rewritePathPrefixParams{}).dryRun(), "omitted previews")
	require.False(t, (rewritePathPrefixParams{DryRunCamel: &fa}).dryRun(), "dryRun=false writes")
	require.True(t, (rewritePathPrefixParams{DryRunCamel: &tr}).dryRun())
	require.True(t, (rewritePathPrefixParams{DryRun: &fa, DryRunCamel: &tr}).dryRun(), "a disagreement fails toward preview")
}

// TestAuthorDuplicateMerge_DryRunCamelAppliesAndDefaultsToPreview: the op wrote
// nothing for {"dryRun":false} before it had a dryRun field. It now applies,
// and a dryRun=true body still previews.
func TestAuthorDuplicateMerge_DryRunCamelAppliesAndDefaultsToPreview(t *testing.T) {
	var live authorDupWrites
	runAuthorDup(t, `{"names":["Raymond L. Weil"],"dryRun":false}`, &live)
	if live.total() == 0 {
		t.Fatal(`{"dryRun":false} wrote nothing: the camelCase spelling is still being dropped`)
	}
	var dry authorDupWrites
	runAuthorDup(t, `{"names":["Raymond L. Weil"],"dryRun":true}`, &dry)
	if dry.total() != 0 {
		t.Fatalf(`{"dryRun":true} wrote: %+v`, dry)
	}
}

// TestAutoMatchTranscribed_DryRunCamelApplies is the same pin for the op that
// applies metadata candidates: dryRun=false applies, dryRun=true does not.
func TestAutoMatchTranscribed_DryRunCamelApplies(t *testing.T) {
	trans, author := "The Name of the Wind", "Patrick Rothfuss"
	for _, tc := range []struct {
		body      string
		wantApply int
	}{
		{`{"dryRun":false}`, 1},
		{`{"dryRun":true}`, 0},
		{`{}`, 0},
	} {
		t.Run(tc.body, func(t *testing.T) {
			books := []database.Book{{ID: "b1", Title: "Name Wind", TranscribedTitle: new(trans), TranscribedAuthor: new(author)}}
			deps := &autoMatchDeps{
				searchFn: func(_ context.Context, _, _, _ string) (string, string, float64, bool, error) {
					return trans, author, 1.8, true, nil
				},
			}
			p := newAutoMatchPlugin(books, deps)
			require.NoError(t, p.runAutoMatchTranscribed(context.Background(), json.RawMessage(tc.body), &fakeReporter{}))
			require.Equal(t, tc.wantApply, deps.applyCalled, "body %s", tc.body)
		})
	}
}

// TestSeriesPhantomRepair_ConflictRefusedEvenForReportMode: report mode ignores
// the flag, but a body whose spellings disagree is still malformed and is
// refused rather than silently read.
func TestSeriesPhantomRepair_ConflictRefusedEvenForReportMode(t *testing.T) {
	s := newSeriesPhantomStore(t)
	fx := seedSeriesPhantomFixture(t, s)
	p := &Plugin{deps: fakeDeps{store: s}}
	fa, tr := false, true
	for _, mode := range []string{"", seriesPhantomModeReport, seriesPhantomModeNull} {
		_, err := runSeriesPhantom(t, p, seriesPhantomRepairParams{Mode: mode, DryRun: &fa, DryRunCamel: &tr})
		require.Error(t, err, "mode %q", mode)
		require.True(t, strings.Contains(err.Error(), "disagree"), "mode %q: %v", mode, err)
	}
	requireFixtureUntouched(t, s, fx)
}

// TestSeriesPhantomRepair_DryRunCamelAppliesNullMode: dryRun=false is a live
// run of a repair mode, exactly as dry_run=false is.
func TestSeriesPhantomRepair_DryRunCamelAppliesNullMode(t *testing.T) {
	s := newSeriesPhantomStore(t)
	seedSeriesPhantomFixture(t, s)
	p := &Plugin{deps: fakeDeps{store: s}}
	fa := false
	res, err := runSeriesPhantom(t, p, seriesPhantomRepairParams{Mode: seriesPhantomModeNull, DryRunCamel: &fa})
	require.NoError(t, err)
	require.False(t, res.DryRun, "dryRun=false must be a live run")
	require.Equal(t, 4, res.Nulled)
}
