// file: internal/metafetch/partial_runtime_scoring_test.go
// version: 1.2.3
// guid: 3d0c8a52-6b1e-4f87-9a4d-5e2f7c1b8d93
// last-edited: 2026-10-20

package metafetch

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// Synthetic fixtures only: sizes and durations are made up.

// ownerCaseFiles: two files of ~4 MB, only the first measured (8 min). The
// size-derived estimate is ~16 min; the bound is the physical floor, 8 MB at
// 1000 B/s = 8000 s.
func ownerCaseFiles() []database.BookFile {
	return []database.BookFile{
		{ID: "f1", BookID: "b1", FileSize: 4_000_000, Duration: 480},
		{ID: "f2", BookID: "b1", FileSize: 4_000_000},
	}
}

// tenHourFiles: 20 x 30 min chapters of 28.8 MB each (128 kbps), 2 measured.
func tenHourFiles() []database.BookFile {
	files := make([]database.BookFile, 20)
	for i := range files {
		files[i] = database.BookFile{ID: fmt.Sprintf("c%02d", i), BookID: "b1", FileSize: 28_800_000}
		if i < 2 {
			files[i].Duration = 1800
		}
	}
	return files
}

func runtimeOf(files []database.BookFile) localRuntime {
	return localRuntimeOf(database.ComputeBookRuntime(nil, files))
}

func TestLocalRuntime_PartialUpperBound(t *testing.T) {
	owner := runtimeOf(ownerCaseFiles())
	require.Zero(t, owner.Sec, "a partial runtime is still not a total")
	require.Equal(t, 8000, owner.Partial.UpperSec, "fixture: 8 MB at the 1000 B/s physical floor")
	upper := owner.Partial.UpperSec

	t.Run("owner case: a 14h42m candidate is penalised as a >100% mismatch", func(t *testing.T) {
		const cand = 14*3600 + 42*60
		assert.Equal(t, 0.5, owner.multiplier(cand))
		assert.Equal(t, -20.0, owner.score(cand))
		assert.True(t, owner.exceedsUpper(cand))
	})
	t.Run("the penalty is exactly the complete-runtime >100% tier", func(t *testing.T) {
		assert.Equal(t, durationScoreMultiplier(100, 1000), owner.multiplier(upper+1))
		assert.Equal(t, computeDurationScore(100, 1000), owner.score(upper+1))
	})
	t.Run("boundary: the upper bound itself is allowed, one second more is not", func(t *testing.T) {
		assert.Equal(t, 1.0, owner.multiplier(upper))
		assert.Zero(t, owner.score(upper))
		assert.Equal(t, 0.5, owner.multiplier(upper+1))
		assert.Equal(t, -20.0, owner.score(upper+1))
	})
	t.Run("a candidate inside the bounds is neither rewarded nor penalised", func(t *testing.T) {
		for _, cand := range []int{1, 300, 480, 960, 2000} {
			assert.Equal(t, 1.0, owner.multiplier(cand), "cand %d", cand)
			assert.Zero(t, owner.score(cand), "cand %d", cand)
		}
	})
	t.Run("an unknown candidate runtime is never penalised", func(t *testing.T) {
		assert.Equal(t, 1.0, owner.multiplier(0))
		assert.False(t, owner.exceedsUpper(0))
	})
}

func TestLocalRuntime_TenHourBookWithTwoChaptersMeasured(t *testing.T) {
	ten := runtimeOf(tenHourFiles())
	require.Zero(t, ten.Sec)
	require.Equal(t, 36000, ten.Partial.EstimateSec)
	require.Equal(t, 576000, ten.Partial.UpperSec, "fixture: 576 MB at the 1000 B/s physical floor")
	// A 10 h candidate - and a 10 h 30 m one, and a 20 h one - are plausible
	// for 576 MB of audio: none may be penalised.
	for _, cand := range []int{36000, 37800, 72000, ten.Partial.UpperSec} {
		assert.Equal(t, 1.0, ten.multiplier(cand), "cand %d", cand)
		assert.Zero(t, ten.score(cand), "cand %d", cand)
		assert.False(t, ten.exceedsUpper(cand), "cand %d", cand)
	}
	// A 40 h candidate cannot fit.
	assert.Equal(t, 0.5, ten.multiplier(ten.Partial.UpperSec+1))
}

func TestLocalRuntime_NoEstimateBehavesAsBefore(t *testing.T) {
	sizeless := ownerCaseFiles()
	sizeless[1].FileSize = 0
	zeroMeasured := ownerCaseFiles()
	zeroMeasured[0].FileSize = 0
	allSizeless := ownerCaseFiles()
	allSizeless[0].FileSize, allSizeless[1].FileSize = 0, 0
	unmeasured := ownerCaseFiles()
	unmeasured[0].Duration = 0

	for name, files := range map[string][]database.BookFile{
		"unmeasured file has no size": sizeless,
		"measured file has no size":   zeroMeasured,
		"no sizes at all":             allSizeless,
		"nothing measured (unknown)":  unmeasured,
	} {
		t.Run(name, func(t *testing.T) {
			rt := runtimeOf(files)
			assert.Zero(t, rt.Partial.UpperSec)
			assert.Equal(t, 1.0, rt.multiplier(14*3600+42*60))
			assert.Zero(t, rt.score(14*3600+42*60))
			assert.False(t, rt.informative())
		})
	}

	t.Run("complete runtime keeps the tier table", func(t *testing.T) {
		rt := knownRuntime(1000)
		for _, cand := range []int{0, 500, 1000, 1049, 1500, 2001, 9999} {
			assert.Equal(t, durationScoreMultiplier(1000, cand), rt.multiplier(cand))
			assert.Equal(t, computeDurationScore(1000, cand), rt.score(cand))
		}
	})
}

// pickBestMatchFromScored is one of the two scoring sites: with the same base
// score the candidate that fits the files must beat the 14h42m one. Fails if
// the multiplier is read from the complete total only.
func TestPickBestMatch_PartialRuntimePenalisesTooLongCandidate(t *testing.T) {
	long := metadata.BookMetadata{Title: "Zeta Quest", Author: "Ann Writer", Narrator: "Bo Reader", DurationSec: 14*3600 + 42*60}
	fits := metadata.BookMetadata{Title: "Zeta Quest", Author: "Ann Writer", Narrator: "Bo Reader", DurationSec: 1000}
	words := map[string]bool{"zeta": true, "quest": true}
	results := []metadata.BookMetadata{long, fits}
	scores := []float64{0.9, 0.9}

	partial := runtimeOf(ownerCaseFiles())
	got := pickBestMatchFromScored(results, scores, "f1", words, "Ann Writer", "", partial)
	require.Len(t, got, 1)
	assert.Equal(t, 1000, got[0].DurationSec, "the candidate that fits the files wins")

	// And with no size information nothing separates them: first wins, as before.
	sizeless := ownerCaseFiles()
	sizeless[1].FileSize = 0
	got = pickBestMatchFromScored(results, scores, "f1", words, "Ann Writer", "", runtimeOf(sizeless))
	require.Len(t, got, 1)
	assert.Equal(t, long.DurationSec, got[0].DurationSec)
}

// The search path (the Review page's): the owner's case end to end - 2 files,
// one measured at 8 min, a 14h42m Audible candidate - gets the x0.50 runtime
// step with the estimate in its detail, a -20 duration score and the mismatch
// flag; a candidate that fits the bytes is untouched.
func TestSearch_PartialRuntimeTooLongCandidateIsPenalised(t *testing.T) {
	audible := &fakeProvider{id: metadata.SourceIDAudible, name: "Audible", answer: func(string, string) []metadata.BookMetadata {
		return []metadata.BookMetadata{
			{Title: "Zeta Quest", Author: "Ann Writer", Narrator: "Bo Reader", ASIN: "B0LONG0001", DurationSec: 14*3600 + 42*60, CoverURL: "c"},
			{Title: "Zeta Quest", Author: "Ann Writer", Narrator: "Bo Reader", ASIN: "B0FITS0001", DurationSec: 1000, CoverURL: "c"},
		}
	}}
	book := &database.Book{ID: "b1", Title: "Zeta Quest"}
	svc := fanoutHarnessFiles(t, book, ownerCaseFiles(), audible)
	resp, err := svc.SearchMetadataForBookWithOptions("b1", "", "Ann Writer", "", "", SearchOptions{})
	require.NoError(t, err)

	var long, fits *MetadataCandidate
	for i := range resp.Results {
		switch resp.Results[i].ASIN {
		case "B0LONG0001":
			long = &resp.Results[i]
		case "B0FITS0001":
			fits = &resp.Results[i]
		}
	}
	require.NotNil(t, long)
	require.NotNil(t, fits)

	assert.Equal(t, -20.0, long.DurationScore)
	assert.True(t, long.DurationMismatch)
	require.NotNil(t, long.ScoreBreakdown)
	var step *ScoreStep
	for i := range long.ScoreBreakdown.Steps {
		if long.ScoreBreakdown.Steps[i].ID == "duration" {
			step = &long.ScoreBreakdown.Steps[i]
		}
	}
	require.NotNil(t, step, "the penalty is recorded as a duration step")
	assert.Equal(t, 0.5, step.Operand)
	assert.Contains(t, step.Detail, "1 of 2 local files")
	assert.Contains(t, step.Detail, "16m")
	assert.Contains(t, step.Detail, "14h42m")

	assert.Zero(t, fits.DurationScore)
	assert.False(t, fits.DurationMismatch)
	for _, s := range fits.ScoreBreakdown.Steps {
		assert.NotEqual(t, "duration", s.ID, "a candidate inside the bounds carries no runtime step")
	}
	assert.Greater(t, fits.Score, long.Score)
}

// The 10 h book with 2 of 20 chapters measured: a 10 h candidate is not
// penalised through the search path either.
func TestSearch_PartialRuntimeTenHourCandidateIsNotPenalised(t *testing.T) {
	audible := &fakeProvider{id: metadata.SourceIDAudible, name: "Audible", answer: func(string, string) []metadata.BookMetadata {
		return []metadata.BookMetadata{
			{Title: "Zeta Quest", Author: "Ann Writer", Narrator: "Bo Reader", ASIN: "B0TEN00001", DurationSec: 36000, CoverURL: "c"},
		}
	}}
	book := &database.Book{ID: "b1", Title: "Zeta Quest"}
	svc := fanoutHarnessFiles(t, book, tenHourFiles(), audible)
	resp, err := svc.SearchMetadataForBookWithOptions("b1", "", "Ann Writer", "", "", SearchOptions{})
	require.NoError(t, err)
	require.Len(t, resp.Results, 1)
	c := resp.Results[0]
	assert.Zero(t, c.DurationScore)
	assert.False(t, c.DurationMismatch)
	for _, s := range c.ScoreBreakdown.Steps {
		assert.NotEqual(t, "duration", s.ID)
	}
}

// The browse path scores through scoreBrowseAnswer with the same runtime.
func TestScoreBrowseAnswer_PartialRuntimePenalty(t *testing.T) {
	r := metadata.BookMetadata{Title: "Zeta Quest", Author: "Ann Writer", Narrator: "Bo Reader", DurationSec: 14*3600 + 42*60}
	words := map[string]bool{"zeta": true, "quest": true}
	partial := runtimeOf(ownerCaseFiles())
	penalised := scoreBrowseAnswer(r, "Audible", "Zeta Quest", words, "Ann Writer", "", partial)
	neutral := scoreBrowseAnswer(r, "Audible", "Zeta Quest", words, "Ann Writer", "", localRuntime{})
	assert.Equal(t, -20.0, penalised.DurationScore)
	assert.InDelta(t, neutral.Score*0.5, penalised.Score, 1e-9)
}

// A pooled answer with an ASIN but no runtime is enriched from Audnexus only
// when the book's own runtime can change the ranking. A partial runtime with a
// size-derived bound now can; one without sizes still cannot, so the book does
// not wait on Audnexus for nothing.
func TestSearch_PartialRuntimeEnrichesRuntimelessASINAnswers(t *testing.T) {
	run := func(t *testing.T, files []database.BookFile) (*MetadataCandidate, int32) {
		t.Helper()
		audible := &fakeProvider{id: metadata.SourceIDAudible, name: "Audible", answer: func(string, string) []metadata.BookMetadata {
			return []metadata.BookMetadata{{Title: "Zeta Quest", Author: "Ann Writer", Narrator: "Bo Reader", ASIN: "B0ENRICH01", CoverURL: "c"}}
		}}
		audnexus := &fakeProvider{id: metadata.SourceIDAudnexus, name: "Audnexus (Audible)"}
		svc := fanoutHarnessFiles(t, &database.Book{ID: "b1", Title: "Zeta Quest"}, files, audible, audnexus)
		var lookups atomic.Int32
		svc.asinLookupOverride = func(_ context.Context, _, asin string) (*metadata.BookMetadata, error) {
			lookups.Add(1)
			return &metadata.BookMetadata{Title: "Zeta Quest", ASIN: asin, DurationSec: 14*3600 + 42*60}, nil
		}
		resp, err := svc.SearchMetadataForBookWithOptions("b1", "", "Ann Writer", "", "", SearchOptions{})
		require.NoError(t, err)
		require.Len(t, resp.Results, 1)
		return &resp.Results[0], lookups.Load()
	}

	c, lookups := run(t, ownerCaseFiles())
	assert.NotZero(t, lookups, "the runtime-less answer is looked up")
	assert.Equal(t, 14*3600+42*60, c.DurationSec)
	assert.Equal(t, -20.0, c.DurationScore)

	sizeless := ownerCaseFiles()
	sizeless[1].FileSize = 0
	c, lookups = run(t, sizeless)
	assert.Zero(t, lookups, "no bound: the runtime could not change the ranking, so no lookup")
	assert.Zero(t, c.DurationSec)
}

// A complete total always wins over any partial bound a hand-built value
// carries: exceedsUpper is for PARTIAL runtimes only.
func TestLocalRuntime_CompleteTotalIgnoresPartialBound(t *testing.T) {
	rt := localRuntime{Sec: 36000, Partial: partialBound{UpperSec: 100}}
	assert.False(t, rt.exceedsUpper(36000))
	assert.Equal(t, 1.3, rt.multiplier(36000))
}

// The direct own-ASIN lookup is the second scoring site in the search path: a
// 14h42m answer for a book whose files cannot hold it gets the same penalty.
func TestSearch_PartialRuntimePenalisesDirectASINAnswer(t *testing.T) {
	audible := &fakeProvider{id: metadata.SourceIDAudible, name: "Audible"}
	own := "B0OWNASIN1"
	book := &database.Book{ID: "b1", Title: "Zeta Quest", ASIN: &own}
	svc := fanoutHarnessFiles(t, book, ownerCaseFiles(), audible)
	svc.asinLookupOverride = func(_ context.Context, _, asin string) (*metadata.BookMetadata, error) {
		return &metadata.BookMetadata{Title: "Zeta Quest", Author: "Ann Writer", Narrator: "Bo Reader", ASIN: asin,
			DurationSec: 14*3600 + 42*60, CoverURL: "c"}, nil
	}
	resp, err := svc.SearchMetadataForBookWithOptions("b1", "", "Ann Writer", "", "", SearchOptions{})
	require.NoError(t, err)
	var got *MetadataCandidate
	for i := range resp.Results {
		if resp.Results[i].Source == "Audnexus (Audible)" {
			got = &resp.Results[i]
		}
	}
	require.NotNil(t, got, "the direct ASIN answer is a candidate")
	assert.Equal(t, -20.0, got.DurationScore)
	var found bool
	for _, s := range got.ScoreBreakdown.Steps {
		if s.ID == "duration" {
			found = true
			assert.Equal(t, 0.5, s.Operand)
		}
	}
	assert.True(t, found, "a duration step with x0.50")
}

// coverArtIntroFiles: a 10 h 64 kbps book with 500 KB of cover art in every
// file; only the 25 s intro file is measured. The measured bytes-per-second is
// 3.5x the book's, so x3 over the estimate (~3 h) is far below the real 10 h.
func coverArtIntroFiles() []database.BookFile {
	files := []database.BookFile{{ID: "intro", BookID: "b1", FileSize: 25*8000 + 500_000, Duration: 25}}
	for i := range 20 {
		files = append(files, database.BookFile{ID: fmt.Sprintf("ch%02d", i), BookID: "b1", FileSize: 1800*8000 + 500_000})
	}
	return files
}

// mixedEncodeFiles: two measured 30 min parts at 128 kbps, 18 unmeasured at
// 32 kbps; 10 h in all, measured rate 4x the rest.
func mixedEncodeFiles() []database.BookFile {
	files := []database.BookFile{
		{ID: "p0", BookID: "b1", FileSize: 1800 * 16000, Duration: 1800},
		{ID: "p1", BookID: "b1", FileSize: 1800 * 16000, Duration: 1800},
	}
	for i := range 18 {
		files = append(files, database.BookFile{ID: fmt.Sprintf("q%02d", i), BookID: "b1", FileSize: 1800 * 4000})
	}
	return files
}

// Regression: a sample denser than the book must not make the RIGHT 10 h
// candidate a mismatch, in scoring and through the search path.
func TestPartialRuntime_NonRepresentativeSampleDoesNotPenaliseTheRightCandidate(t *testing.T) {
	for name, files := range map[string][]database.BookFile{
		"cover art in every file, only the intro measured": coverArtIntroFiles(),
		"measured parts denser than the rest":              mixedEncodeFiles(),
	} {
		t.Run(name, func(t *testing.T) {
			rt := runtimeOf(files)
			require.Greater(t, rt.Partial.UpperSec, 36000, "the bound must cover the real 10 h")
			assert.Equal(t, 1.0, rt.multiplier(36000))
			assert.Zero(t, rt.score(36000))
			assert.False(t, rt.exceedsUpper(36000))

			audible := &fakeProvider{id: metadata.SourceIDAudible, name: "Audible", answer: func(string, string) []metadata.BookMetadata {
				return []metadata.BookMetadata{{Title: "Zeta Quest", Author: "Ann Writer", Narrator: "Bo Reader", ASIN: "B0TEN00001", DurationSec: 36000, CoverURL: "c"}}
			}}
			svc := fanoutHarnessFiles(t, &database.Book{ID: "b1", Title: "Zeta Quest"}, files, audible)
			resp, err := svc.SearchMetadataForBookWithOptions("b1", "", "Ann Writer", "", "", SearchOptions{})
			require.NoError(t, err)
			require.Len(t, resp.Results, 1)
			assert.Zero(t, resp.Results[0].DurationScore)
			assert.False(t, resp.Results[0].DurationMismatch)
			for _, st := range resp.Results[0].ScoreBreakdown.Steps {
				assert.NotEqual(t, "duration", st.ID)
			}
		})
	}
}

// A strong match stops the fan-out. With a partial runtime and no way to
// confirm the runtime, an otherwise strong answer still stops it (as before),
// but one longer than the files can hold is no strong match: asking goes on.
func TestFanout_PartialRuntimeTooLongAnswerIsNotStrong(t *testing.T) {
	run := func(t *testing.T, dur int, files []database.BookFile) int {
		t.Helper()
		audible := &fakeProvider{id: metadata.SourceIDAudible, name: "Audible", answer: func(title, author string) []metadata.BookMetadata {
			return []metadata.BookMetadata{{Title: "Zeta Quest", Author: "Ann Writer", Narrator: "Bo Reader", DurationSec: dur, CoverURL: "c"}}
		}}
		svc := fanoutHarnessFiles(t, &database.Book{ID: "b1", Title: "Zeta Quest (Bo Reader)"}, files, audible)
		_, err := svc.SearchMetadataForBookWithOptions("b1", "Zeta Quest (Bo Reader)", "Ann Writer", "", "", SearchOptions{})
		require.NoError(t, err)
		return audible.callCount()
	}
	assert.Equal(t, 1, run(t, 1000, ownerCaseFiles()), "inside the bound: strong, the fan-out stops")
	assert.Greater(t, run(t, 14*3600+42*60, ownerCaseFiles()), 1, "beyond the bound: not strong, later variants are asked")

	sizeless := ownerCaseFiles()
	sizeless[1].FileSize = 0
	assert.Equal(t, 1, run(t, 14*3600+42*60, sizeless), "no bound: behaves as before")
}

// strongCriteria.matches itself, through the carried runtime.
func TestStrongCriteria_MatchesRefusesAnswerBeyondPartialBound(t *testing.T) {
	p := parseSearchTitle("Zeta Quest", "Ann Writer", "")
	long := metadata.BookMetadata{Title: "Zeta Quest", Author: "Ann Writer", Narrator: "Bo Reader", DurationSec: 14*3600 + 42*60}
	fits := long
	fits.DurationSec = 1000

	partial := newStrongCriteria(p, p.Title, "Zeta Quest", "", "Ann Writer", runtimeOf(ownerCaseFiles()))
	require.True(t, partial.matches(fits), "fixture: an answer inside the bound is strong")
	assert.False(t, partial.matches(long))

	none := newStrongCriteria(p, p.Title, "Zeta Quest", "", "Ann Writer", localRuntime{})
	assert.True(t, none.matches(long), "no runtime at all: unchanged")
}

// A stored own ASIN does not vouch for an answer the files cannot hold: the
// ASIN may be the earlier bad match that stored it.
func TestStrongCriteria_OwnASINBeyondPartialBoundIsNotStrong(t *testing.T) {
	p := parseSearchTitle("Zeta Quest", "Ann Writer", "")
	long := metadata.BookMetadata{Title: "Zeta Quest", Author: "Ann Writer", ASIN: "B000TEST01", DurationSec: 14*3600 + 42*60}
	fits := long
	fits.DurationSec = 1000

	c := newStrongCriteria(p, p.Title, "Zeta Quest", "B000TEST01", "Ann Writer", runtimeOf(ownerCaseFiles()))
	require.True(t, c.ownASINAgrees(fits), "fixture: own ASIN inside the bound agrees")
	assert.False(t, c.ownASINAgrees(long))
	assert.False(t, c.matches(long))
}

// knownRuntime is a localRuntime with a complete total (or 0 for unknown).
// Test-only: production builds localRuntime through bookRuntime.
func knownRuntime(sec int) localRuntime { return localRuntime{Sec: sec} }
