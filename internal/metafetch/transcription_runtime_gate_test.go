// file: internal/metafetch/transcription_runtime_gate_test.go
// version: 1.0.1
// guid: 5b0c1e7e-39f4-4d0e-9a53-6c1d2f8a4b17
// last-edited: 2026-10-29

package metafetch

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

const (
	hour = 3600
)

// The owner's rule (2026-10-29): the transcription boost needs the candidate's
// runtime within 10% of the book's COMPLETE runtime.
func TestTranscriptionBoost_RuntimeGate(t *testing.T) {
	th := transcriptionHints{title: "Year One"}
	cand := func(sec int) metadata.BookMetadata {
		return metadata.BookMetadata{Title: "Year One", DurationSec: sec}
	}
	tenHours := localRuntime{Sec: 10 * hour}
	// The real case: 2 files, sizes 0 KB, one measured at 7h46m.
	partial := localRuntime{FilesKnown: 1, FilesCounted: 2, MeasuredSec: 7*hour + 46*60}

	tests := []struct {
		name        string
		rt          localRuntime
		candSec     int
		wantBoosted bool
		wantVerdict string
		wantDetail  string
	}{
		{"10h vs 10h30m is within 10%", tenHours, 10*hour + 30*60, true, "", ""},
		{"exactly 10% off still passes", tenHours, 11 * hour, true, "", ""},
		{"10h vs 12h is a runtime mismatch", tenHours, 12 * hour, false, TranscriptionRuntimeMismatch, "differ by 20%"},
		{"10h vs 9h is a runtime mismatch", tenHours, 9*hour - 600, false, TranscriptionRuntimeMismatch, "differ by"},
		{"10h of a 30h candidate is a fragment", tenHours, 30 * hour, false, TranscriptionRuntimeFragment, "about 10h00m of a 30h00m book"},
		{"1h of a 10h candidate is a fragment", localRuntime{Sec: hour}, 10 * hour, false, TranscriptionRuntimeFragment, "about 1h00m of a 10h00m book"},
		{"partial local runtime is unknown", partial, 10 * hour, false, TranscriptionRuntimeUnknown, "only 1 of 2 files measured, at least 7h46m; file sizes 0 KB"},
		{"unknown local runtime, no file facts", localRuntime{}, 10 * hour, false, TranscriptionRuntimeUnknown, "the book's total runtime is unknown"},
		{"candidate without a runtime is unknown", tenHours, 0, false, TranscriptionRuntimeUnknown, "the candidate's runtime is unknown"},
		{"neither side known", localRuntime{}, 0, false, TranscriptionRuntimeUnknown, "and the candidate's runtime is unknown"},
		{
			"partial runtime whose size UPPER bound is under half the candidate is a fragment",
			localRuntime{FilesKnown: 1, FilesCounted: 2, MeasuredSec: hour, Partial: partialBound{EstimateSec: 2 * hour, UpperSec: 3 * hour}},
			10 * hour, false, TranscriptionRuntimeFragment, "no more than 3h00m of a 10h00m book (from the file sizes)",
		},
		{
			"a bitrate estimate alone never proves anything",
			localRuntime{FilesKnown: 1, FilesCounted: 2, MeasuredSec: hour, Partial: partialBound{EstimateSec: 2 * hour}},
			10 * hour, false, TranscriptionRuntimeUnknown, "only 1 of 2 files measured",
		},
		{
			"upper bound between half and 90% of the candidate is a mismatch, not a fragment",
			localRuntime{FilesKnown: 1, FilesCounted: 2, MeasuredSec: hour, Partial: partialBound{UpperSec: 7 * hour}},
			10 * hour, false, TranscriptionRuntimeMismatch, "can hold at most 7h00m (from their sizes)",
		},
		{
			"upper bound that reaches the candidate proves nothing",
			localRuntime{FilesKnown: 1, FilesCounted: 2, MeasuredSec: hour, Partial: partialBound{UpperSec: 12 * hour}},
			10 * hour, false, TranscriptionRuntimeUnknown, "only 1 of 2 files measured",
		},
		{
			"measured files alone running over the candidate by more than 10% is a mismatch",
			localRuntime{FilesKnown: 1, FilesCounted: 2, MeasuredSec: 12 * hour},
			10 * hour, false, TranscriptionRuntimeMismatch, "measured files alone run at least 12h00m",
		},
		{
			"a lower bound alone never proves a fragment",
			localRuntime{FilesKnown: 1, FilesCounted: 2, MeasuredSec: hour, SizesComplete: true, TotalBytes: 10},
			10 * hour, false, TranscriptionRuntimeUnknown, "at least 1h00m",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, out := transcriptionBoost(1.0, cand(tc.candSec), th, tc.rt)
			assert.True(t, out.Matched)
			assert.Equal(t, tc.wantBoosted, out.Boosted)
			if tc.wantBoosted {
				assert.InDelta(t, scoringKnobs().TranscriptionTitleExactBoost, got, 1e-9)
				assert.Nil(t, out.Check)
				return
			}
			assert.Equal(t, 1.0, got, "a withheld boost leaves the score untouched")
			require.NotNil(t, out.Check)
			assert.Equal(t, tc.wantVerdict, out.Check.Verdict)
			assert.Contains(t, out.Check.Detail, tc.wantDetail)
		})
	}

	t.Run("no match means no check", func(t *testing.T) {
		got, out := transcriptionBoost(1.0, metadata.BookMetadata{Title: "Hush", DurationSec: hour}, th, tenHours)
		assert.Equal(t, 1.0, got)
		assert.Equal(t, transcriptionOutcome{}, out)
	})
}

func TestTranscriptionRuntimeAgrees(t *testing.T) {
	assert.True(t, TranscriptionRuntimeAgrees(10*hour, 10*hour+30*60))
	assert.False(t, TranscriptionRuntimeAgrees(10*hour, 12*hour))
	assert.False(t, TranscriptionRuntimeAgrees(0, 10*hour), "unknown local runtime")
	assert.False(t, TranscriptionRuntimeAgrees(10*hour, 0), "unknown candidate runtime")
}

// searchOne runs a search for a single Audible answer against a book the audio
// intro named "Year One".
func searchOne(t *testing.T, files []database.BookFile, answer metadata.BookMetadata) MetadataCandidate {
	t.Helper()
	heard := "Year One"
	audible := &fakeProvider{id: metadata.SourceIDAudible, name: "Audible", answer: func(string, string) []metadata.BookMetadata {
		return []metadata.BookMetadata{answer}
	}}
	svc := fanoutHarnessFiles(t, &database.Book{ID: "b1", Title: "Year One", TranscribedTitle: &heard}, files, audible)
	resp, err := svc.SearchMetadataForBookWithOptions("b1", "", "Alfred Pennyworth", "", "", SearchOptions{})
	require.NoError(t, err)
	require.Len(t, resp.Results, 1)
	return resp.Results[0]
}

func batmanAnswer(sec int) metadata.BookMetadata {
	return metadata.BookMetadata{Title: "Year One", Author: "Alfred Pennyworth", Narrator: "Lucius Fox",
		ASIN: "B0BATMAN01", CoverURL: "c", DurationSec: sec}
}

func stepByID(c MetadataCandidate, id string) *ScoreStep {
	for i := range c.ScoreBreakdown.Steps {
		if c.ScoreBreakdown.Steps[i].ID == id {
			return &c.ScoreBreakdown.Steps[i]
		}
	}
	return nil
}

// The breakdown and the candidate flag, end to end, for each verdict.
func TestSearch_TranscriptionRuntimeGate_Breakdown(t *testing.T) {
	tenHourBook := []database.BookFile{{ID: "f1", BookID: "b1", FileSize: 288_000_000, Duration: 10 * hour}}
	// The 2-file, 0 KB, one-file-measured case from the owner's report.
	partialZeroSize := []database.BookFile{
		{ID: "f1", BookID: "b1", Duration: 7*hour + 46*60},
		{ID: "f2", BookID: "b1"},
	}

	t.Run("within 10%: boosted, no check, a x2 step", func(t *testing.T) {
		c := searchOne(t, tenHourBook, batmanAnswer(10*hour+30*60))
		assert.True(t, c.TranscriptionBoosted)
		assert.Nil(t, c.TranscriptionRuntimeCheck)
		st := stepByID(c, "transcription")
		require.NotNil(t, st)
		assert.InDelta(t, 2.0, st.Operand, 1e-9)
	})

	withheld := func(t *testing.T, c MetadataCandidate, verdict, detail string) {
		t.Helper()
		assert.False(t, c.TranscriptionBoosted, "a withheld match is not a boosted one")
		require.NotNil(t, c.TranscriptionRuntimeCheck)
		assert.Equal(t, verdict, c.TranscriptionRuntimeCheck.Verdict)
		st := stepByID(c, "transcription")
		require.NotNil(t, st, "the withheld step still shows")
		assert.Equal(t, "Transcription match", st.Label)
		assert.Equal(t, 1.0, st.Operand)
		assert.Contains(t, st.Detail, "The audio intro agrees, but the boost needs the runtime within 10%: ")
		assert.Contains(t, st.Detail, detail)
		assert.Contains(t, c.TranscriptionRuntimeCheck.Detail, detail)
		dur := stepByID(c, "duration")
		require.NotNil(t, dur, "the runtime step shows beside it, even at x1.00")
		assert.NotEmpty(t, dur.Detail)
		assert.True(t, c.ScoreBreakdown.IsConsistent(1e-9), "steps still replay to the score")
	}

	t.Run("10h vs 12h: runtime_mismatch", func(t *testing.T) {
		withheld(t, searchOne(t, tenHourBook, batmanAnswer(12*hour)), TranscriptionRuntimeMismatch, "runtimes differ by 20%")
	})
	t.Run("10h vs 30h: fragment", func(t *testing.T) {
		withheld(t, searchOne(t, tenHourBook, batmanAnswer(30*hour)), TranscriptionRuntimeFragment, "about 10h00m of a 30h00m book")
	})
	t.Run("partial local runtime: runtime_unknown with the why", func(t *testing.T) {
		c := searchOne(t, partialZeroSize, batmanAnswer(20*hour))
		withheld(t, c, TranscriptionRuntimeUnknown, "only 1 of 2 files measured, at least 7h46m; file sizes 0 KB")
		assert.Contains(t, stepByID(c, "duration").Detail, "Only 1 of 2 local files have a measured runtime")
	})
	t.Run("candidate without a runtime: runtime_unknown", func(t *testing.T) {
		withheld(t, searchOne(t, tenHourBook, batmanAnswer(0)), TranscriptionRuntimeUnknown, "the candidate's runtime is unknown")
	})
}

// A transcription-matched Audible answer with no runtime is looked up (once,
// through the shared cached ASIN lookup) so the comparison can run, and the
// lookup goes to the matched answer, not to whichever came first.
func TestSearch_TranscriptionMatchedAnswerGetsRuntimeLookup(t *testing.T) {
	heard := "Year One"
	audible := &fakeProvider{id: metadata.SourceIDAudible, name: "Audible", answer: func(string, string) []metadata.BookMetadata {
		return []metadata.BookMetadata{
			{Title: "Hush", Author: "Alfred Pennyworth", Narrator: "Lucius Fox", ASIN: "B0HUSH0001", CoverURL: "c"},
			{Title: "Year One", Author: "Alfred Pennyworth", Narrator: "Lucius Fox", ASIN: "B0YEARONE1", CoverURL: "c"},
		}
	}}
	audnexus := &fakeProvider{id: metadata.SourceIDAudnexus, name: "Audnexus (Audible)"}
	files := []database.BookFile{{ID: "f1", BookID: "b1", FileSize: 288_000_000, Duration: 10 * hour}}
	svc := fanoutHarnessFiles(t, &database.Book{ID: "b1", Title: "Year One", TranscribedTitle: &heard}, files, audible, audnexus)
	var mu sync.Mutex
	var asked []string
	svc.asinLookupOverride = func(_ context.Context, _, asin string) (*metadata.BookMetadata, error) {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, asin)
		return &metadata.BookMetadata{Title: "Year One", ASIN: asin, DurationSec: 10*hour + 600}, nil
	}
	resp, err := svc.SearchMetadataForBookWithOptions("b1", "", "Alfred Pennyworth", "", "", SearchOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"B0YEARONE1"}, asked, "one lookup, for the transcription-matched answer")
	var got *MetadataCandidate
	for i := range resp.Results {
		if resp.Results[i].Title == "Year One" {
			got = &resp.Results[i]
		}
	}
	require.NotNil(t, got)
	assert.True(t, got.TranscriptionBoosted, "the looked-up runtime is within 10%, so the boost applies")
	assert.Nil(t, got.TranscriptionRuntimeCheck)
}

// Even a book with no runtime information at all (no files measured, no
// sizes) gets the single lookup for its transcription-matched answer: the
// runtime is cached on the candidate and the gate is evaluated live at apply,
// so a book measured later can pass without a re-search. The candidate is
// flagged unknown meanwhile, and no other answer is looked up.
func TestSearch_TranscriptionMatchedAnswerLookedUpWithoutLocalRuntime(t *testing.T) {
	heard := "Year One"
	audible := &fakeProvider{id: metadata.SourceIDAudible, name: "Audible", answer: func(string, string) []metadata.BookMetadata {
		return []metadata.BookMetadata{
			{Title: "Hush", Author: "Alfred Pennyworth", Narrator: "Lucius Fox", ASIN: "B0HUSH0001", CoverURL: "c"},
			{Title: "Year One", Author: "Alfred Pennyworth", Narrator: "Lucius Fox", ASIN: "B0YEARONE1", CoverURL: "c"},
		}
	}}
	audnexus := &fakeProvider{id: metadata.SourceIDAudnexus, name: "Audnexus (Audible)"}
	svc := fanoutHarnessFiles(t, &database.Book{ID: "b1", Title: "Year One", TranscribedTitle: &heard}, nil, audible, audnexus)
	var mu sync.Mutex
	var asked []string
	svc.asinLookupOverride = func(_ context.Context, _, asin string) (*metadata.BookMetadata, error) {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, asin)
		return &metadata.BookMetadata{Title: "Year One", ASIN: asin, DurationSec: 10 * hour}, nil
	}
	resp, err := svc.SearchMetadataForBookWithOptions("b1", "", "Alfred Pennyworth", "", "", SearchOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"B0YEARONE1"}, asked, "the one lookup, for the transcription-matched answer only")
	for _, c := range resp.Results {
		if c.Title == "Year One" {
			assert.Equal(t, 10*hour, c.DurationSec, "the looked-up runtime is on the candidate")
			require.NotNil(t, c.TranscriptionRuntimeCheck)
			assert.Equal(t, TranscriptionRuntimeUnknown, c.TranscriptionRuntimeCheck.Verdict)
		}
	}
}

// Auto-fetch applies on its own: when the audio intro is the only thing that
// identifies a candidate (the book's own title is junk) the candidate's
// runtime must fit the book's files, or nothing is written and the book stays
// for review. A book whose own title agrees is excused only an UNKNOWN runtime
// (owner rule); a proven fragment or mismatch is never auto-applied.
func TestFetchMetadataForBook_TranscriptionOnlyMatchNeedsRuntime(t *testing.T) {
	const (
		heard = "Year One"
		tenH  = 10 * hour
	)
	tests := []struct {
		name      string
		bookTitle string
		candSec   int
		wantApply bool
	}{
		{name: "intro-only match, runtime fits", bookTitle: "CD 01- 001", candSec: tenH, wantApply: true},
		{name: "intro-only match, fragment of a longer candidate", bookTitle: "CD 01- 001", candSec: 100000},
		{name: "intro-only match, candidate runtime unknown", bookTitle: "CD 01- 001", candSec: 0},
		{name: "own title agrees, runtime unknown (candidate has none)", bookTitle: heard, candSec: 0, wantApply: true},
		{name: "own title agrees, proven fragment is still not applied", bookTitle: heard, candSec: 100000},
		{name: "own title agrees, proven runtime mismatch is still not applied", bookTitle: heard, candSec: 12 * hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _, book := fileWorkHarness(t, "", false, false, nil)
			book.Title = tt.bookTitle
			book.TranscribedTitle = new(heard)
			svc.db.(*database.MockStore).GetBookFilesFunc = func(id string) ([]database.BookFile, error) {
				return []database.BookFile{{ID: "f1", BookID: id, FilePath: "/lib/a/a.m4b", FileSize: 288_000_000, Duration: tenH}}, nil
			}
			svc.overrideSources = []metadata.MetadataSource{fakeSource{
				name:    "Audible",
				results: []metadata.BookMetadata{{Title: heard, Author: "Bruce Wayne", Narrator: "Alfred Pennyworth", DurationSec: tt.candSec}},
			}}
			resp, err := svc.FetchMetadataForBook(context.Background(), "b1")
			if tt.wantApply {
				require.NoError(t, err)
				require.NotNil(t, resp)
				assert.Equal(t, heard, resp.Book.Title)
				return
			}
			require.Error(t, err, "a transcription-only match whose runtime does not fit must not apply")
			assert.Equal(t, tt.bookTitle, book.Title, "nothing written")
		})
	}
}

// A candidate the audio intro agreed with but whose runtime did not confirm
// (it carries a TranscriptionRuntimeCheck, not the boosted flag) is exempt
// from the cover filter like a boosted one: review must see it, flagged.
func TestFilterCoverlessCandidates_KeepsRuntimeFlagged(t *testing.T) {
	in := []MetadataCandidate{
		{Title: "Year One", Score: 3.0, CoverURL: "c"},
		{Title: "Year Two", Score: 1.0},
		{Title: "Year Three", Score: 0.5, TranscriptionRuntimeCheck: &TranscriptionRuntimeCheck{Verdict: TranscriptionRuntimeFragment}},
	}
	out := filterCoverlessCandidates(in)
	var got []string
	for _, c := range out {
		got = append(got, c.Title)
	}
	assert.Equal(t, []string{"Year One", "Year Three"}, got, "the flagged coverless candidate stays, the plain one goes")
}
