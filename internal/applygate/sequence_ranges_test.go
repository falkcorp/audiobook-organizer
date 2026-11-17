// file: internal/applygate/sequence_ranges_test.go
// version: 1.0.0
// guid: 4d9c2a71-8e35-4f60-b2a8-17c6e3d05f94
// last-edited: 2026-11-16

package applygate

import (
	"fmt"
	"strings"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

type seqCase struct {
	name       string
	book       database.Book
	cand       metafetch.MetadataCandidate
	wantPass   bool
	wantReason string
}

func runSeqCases(t *testing.T, cases []seqCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := CheckSequence(&c.book, &c.cand)
			if v.Pass != c.wantPass || v.Reason != c.wantReason {
				t.Fatalf("CheckSequence = pass %v reason %q (%s), want pass %v reason %q",
					v.Pass, v.Reason, v.Detail, c.wantPass, c.wantReason)
			}
		})
	}
}

// Cause A: a track or list ordinal read as the volume. Each pass row is a
// right match the gate refused as book_sequence_conflict; each refuse row is
// the same shape against a genuinely different volume, which must still stop.
func TestCheckSequence_TrackOrdinalIsNotTheVolume(t *testing.T) {
	cases := []seqCase{
		// Leading ordinal in the title, series position says otherwise.
		{"leading title ordinal, position wins", database.Book{Title: "08 - The Long Halloween", SeriesPositionRaw: new("6")},
			metafetch.MetadataCandidate{Title: "The Long Halloween", SeriesPosition: "6"}, true, ""},
		{"leading title ordinal, stored int position wins", database.Book{Title: "23 - Dark Victory", SeriesSequence: new(3)},
			metafetch.MetadataCandidate{Title: "Dark Victory", SeriesPosition: "3"}, true, ""},
		{"leading title ordinal with a dot separator", database.Book{Title: "08. The Long Halloween", SeriesPositionRaw: new("6")},
			metafetch.MetadataCandidate{Title: "The Long Halloween", SeriesPosition: "6"}, true, ""},
		// Must still refuse: the position is the volume, and the candidate is another.
		{"leading title ordinal, candidate is the ordinal's number", database.Book{Title: "08 - The Long Halloween", SeriesPositionRaw: new("6")},
			metafetch.MetadataCandidate{Title: "The Long Halloween", SeriesPosition: "8"}, false, ReasonSequenceMismatch},
		{"leading title ordinal, candidate is another volume", database.Book{Title: "08 - The Long Halloween", SeriesPositionRaw: new("6")},
			metafetch.MetadataCandidate{Title: "Dark Victory", SeriesPosition: "7"}, false, ReasonSequenceMismatch},
		// With nothing stronger the ordinal still counts, so it cannot hide the book's only number.
		{"leading title ordinal alone vs different volume", database.Book{Title: "08 - The Long Halloween"},
			metafetch.MetadataCandidate{Title: "The Long Halloween", SeriesPosition: "6"}, false, ReasonSequenceMismatch},
		{"leading title ordinal alone vs same number", database.Book{Title: "08 - The Long Halloween"},
			metafetch.MetadataCandidate{Title: "The Long Halloween", SeriesPosition: "8"}, true, ""},
		{"leading title ordinal alone, candidate has none", database.Book{Title: "08 - The Long Halloween"},
			metafetch.MetadataCandidate{Title: "The Long Halloween"}, false, ReasonSequenceMissingOnCandidate},

		// Track number in the file name.
		{"chapter file name, position wins", database.Book{Title: "Hush", SeriesPositionRaw: new("8"), FilePath: "/lib/A/Hush/02 Chapter 1.m4b"},
			metafetch.MetadataCandidate{Title: "Hush", SeriesPosition: "8"}, true, ""},
		{"opening credits file name, position wins", database.Book{Title: "Hush", SeriesPositionRaw: new("16"), FilePath: "/lib/A/Hush/01 Opening Credits 57.mp3"},
			metafetch.MetadataCandidate{Title: "Hush", SeriesPosition: "16"}, true, ""},
		{"track word file name, position wins", database.Book{Title: "Hush", SeriesPositionRaw: new("8"), FilePath: "/lib/A/Hush/Hush Track 03 of 20.mp3"},
			metafetch.MetadataCandidate{Title: "Hush", SeriesPosition: "8"}, true, ""},
		{"chapter file name, candidate is the file's number", database.Book{Title: "Hush", SeriesPositionRaw: new("8"), FilePath: "/lib/A/Hush/02 Chapter 1.m4b"},
			metafetch.MetadataCandidate{Title: "Hush", SeriesPosition: "2"}, false, ReasonSequenceMismatch},
		{"chapter file name alone vs different volume", database.Book{Title: "Hush", FilePath: "/lib/A/Hush/02 Chapter 1.m4b"},
			metafetch.MetadataCandidate{Title: "Hush", SeriesPosition: "8"}, false, ReasonSequenceMismatch},

		// A trailing number that no volume marker and no tag word names, but whose
		// file name carries a disc or track number (metadata.ExtractTrackInfoFromFilename):
		// the file is a part of the book, so its number labels the part.
		{"trailing number beside a disc number in the file name", database.Book{Title: "Year One", SeriesPositionRaw: new("1"), FilePath: "/lib/A/Year One/Year One 3 - Disc 1 of 2.mp3"},
			metafetch.MetadataCandidate{Title: "Year One", SeriesPosition: "1"}, true, ""},
		{"trailing number beside a track number in the file name", database.Book{Title: "Year One", SeriesPositionRaw: new("1"), FilePath: "/lib/A/Year One/Year One 3 - Track 07.mp3"},
			metafetch.MetadataCandidate{Title: "Year One", SeriesPosition: "1"}, true, ""},

		// Leading ordinal in the folder name.
		{"leading folder ordinal, position wins", database.Book{Title: "Year One", SeriesPositionRaw: new("3"), FilePath: "/lib/A/23 - Year One/Year One.m4b"},
			metafetch.MetadataCandidate{Title: "Year One", SeriesPosition: "3"}, true, ""},
		{"leading ordinal on title, file and folder, position wins",
			database.Book{Title: "08 - Year One", SeriesPositionRaw: new("6"), FilePath: "/lib/A/08 - Year One/08 - Year One.m4b"},
			metafetch.MetadataCandidate{Title: "Year One", SeriesPosition: "6"}, true, ""},

		// Volume-like evidence is NOT weak: two volume-like numbers that disagree still conflict.
		{"marker title vs position", database.Book{Title: "Year One, Book 3", SeriesPositionRaw: new("1")},
			metafetch.MetadataCandidate{Title: "Year One", SeriesPosition: "1"}, false, ReasonBookSequenceConflict},
		{"trailing title vs position", database.Book{Title: "Year One 3", SeriesPositionRaw: new("1")},
			metafetch.MetadataCandidate{Title: "Year One", SeriesPosition: "1"}, false, ReasonBookSequenceConflict},
		{"bracket title vs position", database.Book{Title: "Year One (3)", SeriesPositionRaw: new("1")},
			metafetch.MetadataCandidate{Title: "Year One", SeriesPosition: "1"}, false, ReasonBookSequenceConflict},
		{"trailing file name without track words vs position", database.Book{Title: "Year One", SeriesPositionRaw: new("1"), FilePath: "/lib/A/Year One/Year One 3.m4b"},
			metafetch.MetadataCandidate{Title: "Year One", SeriesPosition: "1"}, false, ReasonBookSequenceConflict},
		{"marker file name vs position", database.Book{Title: "Year One", SeriesPositionRaw: new("1"), FilePath: "/lib/A/Year One/Year One Book 3 Chapter 1.m4b"},
			metafetch.MetadataCandidate{Title: "Year One", SeriesPosition: "1"}, false, ReasonBookSequenceConflict},
		{"strong title vs strong file", database.Book{Title: "Year One 3", FilePath: "/lib/A/Year One/Year One 1.m4b"},
			metafetch.MetadataCandidate{Title: "Year One 3", SeriesPosition: "3"}, false, ReasonBookSequenceConflict},
		// Two weak numbers and nothing stronger: ambiguous, so the conflict stays.
		{"leading title ordinal vs chapter file number, no position", database.Book{Title: "08 - Year One", FilePath: "/lib/A/Year One/02 Chapter 1.m4b"},
			metafetch.MetadataCandidate{Title: "Year One", SeriesPosition: "8"}, false, ReasonBookSequenceConflict},
	}
	runSeqCases(t, cases)
}

// The weak flag is reported per source, so a refusal says which numbers were set aside.
func TestBookNumbers_WeakSources(t *testing.T) {
	b := database.Book{Title: "08 - The Long Halloween", SeriesPositionRaw: new("6"), FilePath: "/lib/A/Halloween/02 Chapter 1.m4b"}
	got := map[string]SourceNumber{}
	for _, s := range BookNumbers(&b) {
		got[s.Source] = s
	}
	want := map[string]bool{"series_position": false, "title": true, "file_name": true, "folder_name": false}
	for src, weak := range want {
		s, ok := got[src]
		if src == "folder_name" {
			if ok {
				t.Errorf("folder %q should carry no number, got %+v", "Halloween", s)
			}
			continue
		}
		if !ok || s.Weak != weak {
			t.Errorf("source %s = %+v (present %v), want weak=%v", src, s, ok, weak)
		}
	}
	v := CheckSequence(&b, &metafetch.MetadataCandidate{Title: "The Long Halloween", SeriesPosition: "6"})
	if !strings.Contains(describe(v.BookNumbers), "track or file number") {
		t.Errorf("describe does not name the set-aside numbers: %s", describe(v.BookNumbers))
	}
}

// Cause B: collected editions. rangeForms are the same edition as a title, a
// subtitle or a position field would write it.
func TestCheckSequence_SameCollectedEditionPasses(t *testing.T) {
	bookForms := map[string]database.Book{
		"title plural":          {Title: "Knightfall, Books 1-3"},
		"title singular":        {Title: "Knightfall, Book 1-3"},
		"title en dash":         {Title: "Knightfall, Vols 1–3"},
		"title to":              {Title: "Knightfall, Books 1 to 3"},
		"title list":            {Title: "Knightfall, Books 1, 2 & 3"},
		"position only":         {Title: "Knightfall", SeriesPositionRaw: new("1-3")},
		"position en dash":      {Title: "Knightfall", SeriesPositionRaw: new("1–3")},
		"position and title":    {Title: "Knightfall, Books 1-3", SeriesPositionRaw: new("1-3")},
		"title with endpoint":   {Title: "Knightfall, Books 1-3", SeriesPositionRaw: new("3")},
		"title with first end":  {Title: "Knightfall, Book 1-3", SeriesPositionRaw: new("1")},
		"stored int endpoint":   {Title: "Knightfall, Books 1-3", SeriesSequence: new(3)},
		"folder range, no else": {Title: "Knightfall", FilePath: "/lib/A/Knightfall, Books 1-3"},
	}
	candForms := map[string]metafetch.MetadataCandidate{
		"position":          {Title: "Knightfall", SeriesPosition: "1-3"},
		"subtitle plural":   {Title: "Knightfall", Subtitle: "Books 1-3"},
		"title vols":        {Title: "Knightfall, Vols 1–3"},
		"subtitle to":       {Title: "Knightfall", Subtitle: "Books 1 to 3"},
		"subtitle list":     {Title: "Knightfall", Subtitle: "Books 1, 2 & 3"},
		"position+subtitle": {Title: "Knightfall", Subtitle: "Books 1-3", SeriesPosition: "1-3"},
		"position bare to":  {Title: "Knightfall", SeriesPosition: "1 to 3"},
	}
	var cases []seqCase
	for bn, b := range bookForms {
		for cn, c := range candForms {
			cases = append(cases, seqCase{"book " + bn + " / candidate " + cn, b, c, true, ""})
		}
	}
	runSeqCases(t, cases)
}

// The refusals the gate exists for. A range never matches a single volume
// (either direction) and never a different range.
func TestCheckSequence_CollectedEditionStillRefuses(t *testing.T) {
	box := metafetch.MetadataCandidate{Title: "Knightfall", Subtitle: "Books 1-3", SeriesPosition: "1-3"}
	cases := []seqCase{
		// volume vs box set
		{"volume 1 title vs Books 1-3", database.Book{Title: "Knightfall, Book 1"}, box, false, ReasonSequenceMismatch},
		{"volume 1 position vs Books 1-3", database.Book{Title: "Knightfall", SeriesPositionRaw: new("1")}, box, false, ReasonSequenceMismatch},
		{"volume 3 position vs position-only 1-3", database.Book{Title: "Knightfall", SeriesPositionRaw: new("3")},
			metafetch.MetadataCandidate{Title: "Knightfall", SeriesPosition: "1-3"}, false, ReasonSequenceMismatch},
		{"volume 2 vs Books 1-3", database.Book{Title: "Knightfall, Book 2"}, box, false, ReasonSequenceMismatch},
		// box set vs volume
		{"Books 1-3 vs volume 3 position", database.Book{Title: "Knightfall, Books 1-3"},
			metafetch.MetadataCandidate{Title: "Knightfall, Book 3", SeriesPosition: "3"}, false, ReasonSequenceMismatch},
		{"Books 1-3 vs volume 1", database.Book{Title: "Knightfall, Books 1-3"},
			metafetch.MetadataCandidate{Title: "Knightfall", SeriesPosition: "1"}, false, ReasonSequenceMismatch},
		{"Books 1-3 with endpoint position vs the endpoint volume", database.Book{Title: "Knightfall, Books 1-3", SeriesPositionRaw: new("3")},
			metafetch.MetadataCandidate{Title: "Knightfall, Book 3", SeriesPosition: "3"}, false, ReasonSequenceMismatch},
		{"Books 1-3 position-only vs volume", database.Book{Title: "Knightfall", SeriesPositionRaw: new("1-3")},
			metafetch.MetadataCandidate{Title: "Knightfall", SeriesPosition: "3"}, false, ReasonSequenceMismatch},
		{"Books 1-3 vs a candidate with no number", database.Book{Title: "Knightfall, Books 1-3"},
			metafetch.MetadataCandidate{Title: "Knightfall"}, false, ReasonSequenceMissingOnCandidate},
		// different ranges
		{"Books 1-3 vs Books 1-5", database.Book{Title: "Knightfall, Books 1-3"},
			metafetch.MetadataCandidate{Title: "Knightfall", Subtitle: "Books 1-5", SeriesPosition: "1-5"}, false, ReasonSequenceMismatch},
		{"Books 1-3 vs Books 2-4", database.Book{Title: "Knightfall, Books 1-3"},
			metafetch.MetadataCandidate{Title: "Knightfall", SeriesPosition: "2-4"}, false, ReasonSequenceMismatch},
		{"Books 1-3 vs Books 1 and 3 (skips 2)", database.Book{Title: "Knightfall, Books 1-3"},
			metafetch.MetadataCandidate{Title: "Knightfall", Subtitle: "Books 1, 3"}, false, ReasonSequenceMismatch},
		{"position 1-3 vs position 1-5", database.Book{Title: "Knightfall", SeriesPositionRaw: new("1-3")},
			metafetch.MetadataCandidate{Title: "Knightfall", SeriesPosition: "1-5"}, false, ReasonSequenceMismatch},
		// ranges inside one source set disagreeing
		{"book position inside the title's range", database.Book{Title: "Knightfall, Books 1-3", SeriesPositionRaw: new("2")}, box, false, ReasonBookSequenceConflict},
		{"book title range vs book position range", database.Book{Title: "Knightfall, Books 1-3", SeriesPositionRaw: new("1-5")}, box, false, ReasonBookSequenceConflict},
		{"candidate position inside its own range", database.Book{Title: "Knightfall, Books 1-3"},
			metafetch.MetadataCandidate{Title: "Knightfall", Subtitle: "Books 1-3", SeriesPosition: "2"}, false, ReasonCandidateSequenceConflict},
		{"candidate range vs candidate other range", database.Book{Title: "Knightfall, Books 1-3"},
			metafetch.MetadataCandidate{Title: "Knightfall", Subtitle: "Books 1-3", SeriesPosition: "1-5"}, false, ReasonCandidateSequenceConflict},

		// A range written only on a folder or file name is weak: it labels the
		// files, never overrides a volume position, and never lets a volume
		// pass as the box set.
		{"volume 3 in a box-set folder vs box set", database.Book{Title: "Knightfall", SeriesPositionRaw: new("3"), FilePath: "/lib/A/Knightfall, Books 1-3/03 - Knightfall.m4b"},
			box, false, ReasonSequenceMismatch},
		{"volume 3 in a box-set folder vs volume 3", database.Book{Title: "Knightfall", SeriesPositionRaw: new("3"), FilePath: "/lib/A/Knightfall, Books 1-3/03 - Knightfall.m4b"},
			metafetch.MetadataCandidate{Title: "Knightfall", SeriesPosition: "3"}, true, ""},
		{"volume 3 title in a box-set folder vs box set", database.Book{Title: "Knightfall, Book 3", FilePath: "/lib/A/Knightfall, Books 1-3/Knightfall.m4b"},
			box, false, ReasonSequenceMismatch},
	}
	runSeqCases(t, cases)
}

// The same collected edition stored with and without a series position must
// get one verdict, against every kind of candidate.
func TestCheckSequence_EmptyVsSetPositionSameVerdict(t *testing.T) {
	title := "Knightfall, Books 1-3"
	books := map[string]database.Book{
		"empty":            {Title: title},
		"last end":         {Title: title, SeriesPositionRaw: new("3")},
		"first end":        {Title: title, SeriesPositionRaw: new("1")},
		"stored int":       {Title: title, SeriesSequence: new(3)},
		"written as range": {Title: title, SeriesPositionRaw: new("1-3")},
	}
	cands := map[string]struct {
		c    metafetch.MetadataCandidate
		pass bool
	}{
		"same range":      {metafetch.MetadataCandidate{Title: "Knightfall", Subtitle: "Books 1-3", SeriesPosition: "1-3"}, true},
		"single last":     {metafetch.MetadataCandidate{Title: "Knightfall, Book 3", SeriesPosition: "3"}, false},
		"single first":    {metafetch.MetadataCandidate{Title: "Knightfall", SeriesPosition: "1"}, false},
		"longer range":    {metafetch.MetadataCandidate{Title: "Knightfall", Subtitle: "Books 1-5"}, false},
		"no number":       {metafetch.MetadataCandidate{Title: "Knightfall"}, false},
		"inner volume":    {metafetch.MetadataCandidate{Title: "Knightfall", SeriesPosition: "2"}, false},
		"shifted range":   {metafetch.MetadataCandidate{Title: "Knightfall", SeriesPosition: "2-4"}, false},
		"range via title": {metafetch.MetadataCandidate{Title: "Knightfall, Vols 1–3"}, true},
	}
	for cn, c := range cands {
		var ref *SequenceVerdict
		var refName string
		for bn, b := range books {
			v := CheckSequence(&b, &c.c)
			if v.Pass != c.pass {
				t.Errorf("book %s vs candidate %s: pass=%v (%s %s), want %v", bn, cn, v.Pass, v.Reason, v.Detail, c.pass)
			}
			if ref == nil {
				ref, refName = &v, bn
			} else if v.Pass != ref.Pass || v.Reason != ref.Reason {
				t.Errorf("candidate %s: book %s verdict (%v, %q) differs from book %s (%v, %q)", cn, bn, v.Pass, v.Reason, refName, ref.Pass, ref.Reason)
			}
		}
	}
}

// Candidate side: one source read two ways used to conflict with itself.
func TestCheckSequence_CandidateReadsItselfOneWay(t *testing.T) {
	cases := []seqCase{
		{"position 1-2 and subtitle Books 1-2", database.Book{Title: "Dark Detective, Books 1-2"},
			metafetch.MetadataCandidate{Title: "Dark Detective", Subtitle: "Publisher's Pack, books 1-2", SeriesPosition: "1-2"}, true, ""},
		{"position 3-4 and subtitle Books 3 and 4", database.Book{Title: "Dark Detective, Books 3 and 4"},
			metafetch.MetadataCandidate{Title: "Dark Detective", Subtitle: "Books 3 and 4", SeriesPosition: "3-4"}, true, ""},
		{"position 1-2 and subtitle Books 1 & 2", database.Book{Title: "Dark Detective", SeriesPositionRaw: new("1–2")},
			metafetch.MetadataCandidate{Title: "Dark Detective", Subtitle: "Books 1 & 2", SeriesPosition: "1-2"}, true, ""},
		{"position 1-2 vs subtitle Books 1-3", database.Book{Title: "Dark Detective, Books 1-2"},
			metafetch.MetadataCandidate{Title: "Dark Detective", Subtitle: "Books 1-3", SeriesPosition: "1-2"}, false, ReasonCandidateSequenceConflict},
	}
	runSeqCases(t, cases)
}

// SourceNumber text is reported and may be read again: every range shape
// survives the trip through agree.
func TestAgree_RangeTextRoundTrips(t *testing.T) {
	for _, in := range []string{"Knightfall, Books 1-3", "Knightfall, Books 1, 3", "Knightfall, Books 1.5-2"} {
		nums := BookNumbers(&database.Book{Title: in})
		if len(nums) != 1 {
			t.Fatalf("%q: %v", in, nums)
		}
		cand := CandidateNumbers(&metafetch.MetadataCandidate{Title: in})
		a, _, _ := agree(nums, false)
		b, _, _ := agree(cand, false)
		if !a.IsRange() || a.Text != nums[0].Number || fmt.Sprint(a) != fmt.Sprint(b) {
			t.Errorf("%q: book %+v candidate %+v", in, a, b)
		}
	}
}

func evalRuntime(b *database.Book, c *metafetch.MetadataCandidate) Verdict {
	return Evaluate(b, snap(b), database.ComputeBookRuntime(b, nil), c, nil)
}

// End to end, through the whole gate: the right match for a track-ordinal
// title is applied, and the same ordinal against another volume is refused.
func TestEvaluate_TrackOrdinalTitle(t *testing.T) {
	book := &database.Book{Title: "08 - The Long Halloween", SeriesPositionRaw: new("6"), Duration: new(36000)}
	good := metafetch.MetadataCandidate{Title: "The Long Halloween", Series: "Gotham Years", SeriesPosition: "6", Score: 0.95, DurationSec: 36000}
	if v := evalRuntime(book, &good); !v.Allowed {
		t.Fatalf("right match refused: %s %s", v.Reason, v.Detail)
	}
	other := metafetch.MetadataCandidate{Title: "The Long Halloween", Series: "Gotham Years", SeriesPosition: "8", Score: 0.95, DurationSec: 36000}
	if v := evalRuntime(book, &other); v.Allowed || v.Reason != ReasonSequenceMismatch {
		t.Fatalf("the ordinal's number as another volume: allowed=%v reason=%q", v.Allowed, v.Reason)
	}
	// Nothing but the ordinal on the book: still compared, still refused on a mismatch.
	bare := &database.Book{Title: "08 - The Long Halloween", Duration: new(36000)}
	if v := evalRuntime(bare, &good); v.Allowed || v.Reason != ReasonSequenceMismatch {
		t.Fatalf("ordinal alone vs volume 6: allowed=%v reason=%q", v.Allowed, v.Reason)
	}
}

// The same collected edition, with an empty and with a set series position,
// gets one verdict from the whole gate, not only from the sequence leg.
func TestEvaluate_CollectedEditionEmptyVsSetPosition(t *testing.T) {
	cand := metafetch.MetadataCandidate{Title: "Knightfall, Books 1-3", Series: "Knightfall", SeriesPosition: "1-3", Score: 0.95, DurationSec: 108000}
	single := metafetch.MetadataCandidate{Title: "Knightfall", Series: "Knightfall", SeriesPosition: "3", Score: 0.95, DurationSec: 108000}
	for name, pos := range map[string]*string{"empty": nil, "last end": new("3"), "first end": new("1"), "range": new("1-3")} {
		book := &database.Book{Title: "Knightfall, Books 1-3", SeriesPositionRaw: pos, Duration: new(108000)}
		if v := evalRuntime(book, &cand); !v.Allowed {
			t.Errorf("position %s: the same collected edition refused: %s %s", name, v.Reason, v.Detail)
		}
		if v := evalRuntime(book, &single); v.Allowed {
			t.Errorf("position %s: a box set was given one volume's metadata", name)
		}
	}
}

// checkSeriesNumberLost reads the same title: a leading list number that the
// stored position contradicts is no volume number to lose.
func TestCheckSeriesNumberLost_LeadingOrdinal(t *testing.T) {
	title := []string{"title"}
	cand := metafetch.MetadataCandidate{Title: "The Long Halloween", Series: "Gotham Years", SeriesPosition: "6"}
	cases := []struct {
		name string
		book database.Book
		want string
	}{
		{"position contradicts the leading number", database.Book{Title: "08 - The Long Halloween", SeriesPositionRaw: new("6")}, ""},
		{"stored int position contradicts it", database.Book{Title: "08 - The Long Halloween", SeriesSequence: new(6)}, ""},
		// No stored position: the number may be the volume and is dropped by the new title, so it still blocks.
		{"no stored position, result keeps another number", database.Book{Title: "08 - The Long Halloween"}, ReasonSeriesNumberLost},
		// The stored position equals the leading number: the existing rules decide, unchanged.
		{"position equals the leading number, kept as the position", database.Book{Title: "06 - The Long Halloween", SeriesPositionRaw: new("6")}, ""},
		{"position equals the leading number, candidate has another", database.Book{Title: "06 - The Long Halloween", SeriesPositionRaw: new("6")}, "other"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cc := cand
			want := c.want
			if want == "other" {
				// The result keeps position 7, the title's 6 is lost: the leading
				// number equals the stored position, so it is a real volume number.
				cc.SeriesPosition, want = "7", ReasonSeriesNumberLost
			}
			r := checkSeriesNumberLost(&c.book, &cc, title)
			if r.Reason != want {
				t.Fatalf("reason %q (%s), want %q", r.Reason, r.Detail, c.want)
			}
		})
	}
}

// A range kept by the new title is kept; one dropped is lost.
func TestCheckSeriesNumberLost_Range(t *testing.T) {
	title := []string{"title"}
	book := database.Book{Title: "Knightfall, Books 1-3"}
	if r := checkSeriesNumberLost(&book, &metafetch.MetadataCandidate{Title: "Knightfall", Subtitle: "Vols 1–3"}, title); r.Reason != "" {
		t.Errorf("same range in the subtitle: %q (%s)", r.Reason, r.Detail)
	}
	if r := checkSeriesNumberLost(&book, &metafetch.MetadataCandidate{Title: "Knightfall", Subtitle: "Books 1-5"}, title); r.Reason != ReasonSeriesNumberLost {
		t.Errorf("a different range: %q (%s)", r.Reason, r.Detail)
	}
	if r := checkSeriesNumberLost(&book, &metafetch.MetadataCandidate{Title: "Knightfall", SeriesPosition: "3"}, title); r.Reason != ReasonSeriesNumberLost {
		t.Errorf("one volume's position for a box set: %q (%s)", r.Reason, r.Detail)
	}
}

// partial_book decides span by the shared range reader, so one edition is a
// span on whichever side it is written, and a single volume still is not.
func TestCheckPartialBook_SpanByRange(t *testing.T) {
	cases := []struct {
		name string
		book database.Book
		cand metafetch.MetadataCandidate
		want string
	}{
		{"Book 1-3 against Books 1-3", database.Book{ID: "b1", Title: "Knightfall, Book 1-3"}, metafetch.MetadataCandidate{Title: "Knightfall", Subtitle: "Books 1-3"}, ""},
		{"Vols 1–3 against Books 1 to 3", database.Book{ID: "b1", Title: "Knightfall, Vols 1–3"}, metafetch.MetadataCandidate{Title: "Knightfall", Subtitle: "Books 1 to 3"}, ""},
		{"Vol. 01-02 against Books 1-2", database.Book{ID: "b1", Title: "Dark Detective Vol. 01-02"}, metafetch.MetadataCandidate{Title: "Dark Detective", Subtitle: "Books 1-2"}, ""},
		{"singular range book against a single volume", database.Book{ID: "b1", Title: "Knightfall, Book 1-3"}, metafetch.MetadataCandidate{Title: "Knightfall"}, ReasonPartialBook},
		{"single volume against a singular range candidate", database.Book{ID: "b1", Title: "Knightfall"}, metafetch.MetadataCandidate{Title: "Knightfall, Book 1-3"}, ReasonPartialBook},
		{"single volume against Books 1, 2 & 3", database.Book{ID: "b1", Title: "Knightfall, Book 1"}, metafetch.MetadataCandidate{Title: "Knightfall", Subtitle: "Books 1, 2 & 3"}, ReasonPartialBook},
		{"a book titled with a plain dash is not a span", database.Book{ID: "b1", Title: "Catch-22"}, metafetch.MetadataCandidate{Title: "Catch-22"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := checkPartialBook(&c.book, &c.cand, OutcomeUnknown, nil)
			if r.Reason != c.want {
				t.Fatalf("reason %q (%s), want %q", r.Reason, r.Detail, c.want)
			}
		})
	}
}
