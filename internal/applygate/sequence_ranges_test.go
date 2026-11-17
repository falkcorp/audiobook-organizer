// file: internal/applygate/sequence_ranges_test.go
// version: 1.2.0
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

		// A number beside a tag word that is NOT the parsed number says nothing
		// about the parsed number: "Year One 3 - Disc 1 of 2" is volume 3 on
		// a disc, so it conflicts with a stored position of 1 (the review's
		// trace 1). The stored position wins every disagreement.
		{"trailing number beside a disc number in the file name conflicts with the position", database.Book{Title: "Year One", SeriesPositionRaw: new("1"), FilePath: "/lib/A/Year One/Year One 3 - Disc 1 of 2.mp3"},
			metafetch.MetadataCandidate{Title: "Year One", SeriesPosition: "1"}, false, ReasonBookSequenceConflict},
		{"trailing number beside a track number in the file name conflicts with the position", database.Book{Title: "Year One", SeriesPositionRaw: new("1"), FilePath: "/lib/A/Year One/Year One 3 - Track 07.mp3"},
			metafetch.MetadataCandidate{Title: "Year One", SeriesPosition: "1"}, false, ReasonBookSequenceConflict},
		// The tag's own number is the parsed one: it is the track.
		{"trailing number that IS the track number", database.Book{Title: "Year One", SeriesPositionRaw: new("1"), FilePath: "/lib/A/Year One/Year One Track 07.mp3"},
			metafetch.MetadataCandidate{Title: "Year One", SeriesPosition: "1"}, true, ""},
		// A bundle word is never a tag word: "Omnibus 2" is volume 2 of the omnibuses.
		{"omnibus number is a volume number", database.Book{Title: "Year One", SeriesPositionRaw: new("1"), FilePath: "/lib/A/Year One/Year One Omnibus 2.m4b"},
			metafetch.MetadataCandidate{Title: "Year One", SeriesPosition: "1"}, false, ReasonBookSequenceConflict},
		{"pack number is a volume number", database.Book{Title: "Year One", SeriesPositionRaw: new("1"), FilePath: "/lib/A/Year One/Year One Pack 2.m4b"},
			metafetch.MetadataCandidate{Title: "Year One", SeriesPosition: "1"}, false, ReasonBookSequenceConflict},

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
	if !strings.Contains(describe(v.BookNumbers), "list or track number") {
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

// partial_book compares the spans themselves, by the shared range reader: one
// edition is the same span on whichever side it is written, a single volume is
// not a span, and two different spans refuse (review of #4682, MAJOR-2).
func TestCheckPartialBook_SpanByRange(t *testing.T) {
	cases := []struct {
		name string
		book database.Book
		cand metafetch.MetadataCandidate
		want string
	}{
		// Equal spans pass, in every spelling.
		{"Book 1-3 against Books 1-3", database.Book{ID: "b1", Title: "Knightfall, Book 1-3"}, metafetch.MetadataCandidate{Title: "Knightfall", Subtitle: "Books 1-3"}, ""},
		{"Vols 1–3 against Books 1 to 3", database.Book{ID: "b1", Title: "Knightfall, Vols 1–3"}, metafetch.MetadataCandidate{Title: "Knightfall", Subtitle: "Books 1 to 3"}, ""},
		{"Vol. 01-02 against Books 1-2", database.Book{ID: "b1", Title: "Dark Detective Vol. 01-02"}, metafetch.MetadataCandidate{Title: "Dark Detective", Subtitle: "Books 1-2"}, ""},
		{"Books 1-3 against Books 1, 2 & 3", database.Book{ID: "b1", Title: "Knightfall, Books 1-3"}, metafetch.MetadataCandidate{Title: "Knightfall", Subtitle: "Books 1, 2 & 3"}, ""},
		{"the plural form on both sides (passed on main)", database.Book{ID: "b1", Title: "Knightfall, Books 1-3"}, metafetch.MetadataCandidate{Title: "Knightfall", Subtitle: "Books 1-3"}, ""},
		{"omnibus against omnibus", database.Book{ID: "b1", Title: "Knightfall Omnibus"}, metafetch.MetadataCandidate{Title: "Knightfall Omnibus"}, ""},
		{"boxed set against box set", database.Book{ID: "b1", Title: "Year One Boxed Set"}, metafetch.MetadataCandidate{Title: "Year One Box Set"}, ""},
		// One side only refuses.
		{"singular range book against a single volume", database.Book{ID: "b1", Title: "Knightfall, Book 1-3"}, metafetch.MetadataCandidate{Title: "Knightfall"}, ReasonPartialBook},
		{"single volume against a singular range candidate", database.Book{ID: "b1", Title: "Knightfall"}, metafetch.MetadataCandidate{Title: "Knightfall, Book 1-3"}, ReasonPartialBook},
		{"single volume against Books 1, 2 & 3", database.Book{ID: "b1", Title: "Knightfall, Book 1"}, metafetch.MetadataCandidate{Title: "Knightfall", Subtitle: "Books 1, 2 & 3"}, ReasonPartialBook},
		// Different spans refuse (the review's trace 4: both sides are spans).
		{"Books 1-3 against Books 1-5", database.Book{ID: "b1", Title: "Knightfall, Books 1-3"}, metafetch.MetadataCandidate{Title: "Knightfall", Subtitle: "Books 1-5"}, ReasonPartialBook},
		{"Book 1-3 against Vols 2–4", database.Book{ID: "b1", Title: "Knightfall, Book 1-3"}, metafetch.MetadataCandidate{Title: "Knightfall", Subtitle: "Vols 2–4"}, ReasonPartialBook},
		{"Books 1-3 against a list that adds a volume", database.Book{ID: "b1", Title: "Knightfall, Books 1-3"}, metafetch.MetadataCandidate{Title: "Knightfall", Subtitle: "Books 1-3 & 5"}, ReasonPartialBook},
		{"omnibus against a box set", database.Book{ID: "b1", Title: "Knightfall Omnibus"}, metafetch.MetadataCandidate{Title: "Knightfall Box Set"}, ReasonPartialBook},
		{"omnibus against a range", database.Book{ID: "b1", Title: "Knightfall Omnibus"}, metafetch.MetadataCandidate{Title: "Knightfall", Subtitle: "Books 1-3"}, ReasonPartialBook},
		{"a plain dash in a title is not a span", database.Book{ID: "b1", Title: "Batman-22"}, metafetch.MetadataCandidate{Title: "Batman-22"}, ""},
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

// evalAll runs one pair through the whole gate and reports whether it was
// allowed and the reason. Every must-still-refuse case below goes through here
// (review of #4682, MINOR-2), not through a helper alone.
//
// Author and narrator agree on both sides, so the evidence leg's two required
// agreements never depend on the runtime: whatever refuses is the rule under
// test, not a shortage of evidence.
func evalAll(b *database.Book, c *metafetch.MetadataCandidate) (bool, string) {
	bb, cc := *b, *c
	bb.Author = &database.Author{Name: "Bruce Wayne"}
	bb.Narrator = new("Alfred Pennyworth")
	cc.Author, cc.Narrator = "Bruce Wayne", "Alfred Pennyworth"
	v := evalRuntime(&bb, &cc)
	return v.Allowed, v.Reason
}

// Review trace 1 (MAJOR-1): the stored position is the field most likely to be
// stale. "Year One 3 - Track 07" is volume 3; a candidate for volume 1 must not
// be applied to it.
func TestEvaluate_Trace1_TrailingVolumeBesideTrackTag(t *testing.T) {
	cand := metafetch.MetadataCandidate{Title: "Year One", Series: "Gotham Years", SeriesPosition: "1", Score: 0.95, DurationSec: 36000}
	for name, path := range map[string]string{
		"track tag": "/lib/A/Year One/Year One 3 - Track 07.mp3",
		"disc tag":  "/lib/A/Year One/Year One 3 - Disc 1 of 2.mp3",
	} {
		for runtime, dur := range map[string]*int{"unknown": nil, "agreeing": new(36000)} {
			book := &database.Book{Title: "Year One", SeriesPositionRaw: new("1"), FilePath: path, Duration: dur}
			if ok, reason := evalAll(book, &cand); ok || reason != ReasonBookSequenceConflict {
				t.Errorf("%s, runtime %s: allowed=%v reason=%q, want book_sequence_conflict", name, runtime, ok, reason)
			}
		}
	}
}

// Review trace 2 (MAJOR-1): a leading ordinal against a stale position, where
// the book's title is only the series' own words. Setting the ordinal aside is
// not enough; the title says nothing about the volume and an unknown runtime
// confirms nothing.
func TestEvaluate_Trace2_LeadingOrdinalWithSeriesNameTitle(t *testing.T) {
	cand := metafetch.MetadataCandidate{Title: "Knightfall", Series: "Batman: Knightfall Saga", SeriesPosition: "1", Score: 0.95, DurationSec: 36000}
	for runtime, dur := range map[string]*int{"unknown": nil, "agreeing": new(36000), "neutral": new(34000)} {
		book := &database.Book{Title: "Knightfall", SeriesPositionRaw: new("1"), FilePath: "/lib/A/Knightfall/03 - Knightfall.m4b", Duration: dur}
		ok, reason := evalAll(book, &cand)
		if ok || reason != ReasonSequenceUnconfirmed {
			t.Errorf("runtime %s: allowed=%v reason=%q, want sequence_unconfirmed", runtime, ok, reason)
		}
	}
	// A title that is not the series name, agreeing, with the runtime agreeing, is the measured fix and applies.
	good := &database.Book{Title: "03 - Knightfall", SeriesPositionRaw: new("1"), Duration: new(36000)}
	other := metafetch.MetadataCandidate{Title: "Knightfall", Series: "Gotham Years", SeriesPosition: "1", Score: 0.95, DurationSec: 36000}
	if ok, reason := evalAll(good, &other); !ok {
		t.Errorf("distinct title, agreeing runtime: refused (%s)", reason)
	}
	// The same pair with an unknown or merely neutral runtime stays refused:
	// by this rule with a neutral runtime, and by the unknown-runtime overwrite
	// check (which runs first) when the runtime is unknown.
	neutral := &database.Book{Title: "03 - Knightfall", SeriesPositionRaw: new("1"), Duration: new(34000)}
	if ok, reason := evalAll(neutral, &other); ok || reason != ReasonSequenceUnconfirmed {
		t.Errorf("distinct title, neutral runtime: allowed=%v reason=%q, want sequence_unconfirmed", ok, reason)
	}
	unknown := &database.Book{Title: "03 - Knightfall", SeriesPositionRaw: new("1")}
	if ok, _ := evalAll(unknown, &other); ok {
		t.Error("distinct title, unknown runtime: applied")
	}
	// A title that disagrees with the candidate never applies.
	wrong := &database.Book{Title: "03 - Dark Victory", SeriesPositionRaw: new("1"), Duration: new(36000)}
	if ok, _ := evalAll(wrong, &other); ok {
		t.Error("a different title applied through a set-aside ordinal")
	}
}

// Review trace 3 (MAJOR-1): a bundle word is not a tag word, so the number
// after it is a volume number.
func TestEvaluate_Trace3_PackNumberIsAVolume(t *testing.T) {
	cand := metafetch.MetadataCandidate{Title: "Knightfall", Subtitle: "Omnibus 1", Series: "Gotham Years", SeriesPosition: "1", Score: 0.95, DurationSec: 108000}
	book := &database.Book{Title: "Knightfall Omnibus 2", SeriesPositionRaw: new("1"), Duration: new(108000)}
	if ok, reason := evalAll(book, &cand); ok || reason != ReasonBookSequenceConflict {
		t.Fatalf("allowed=%v reason=%q, want book_sequence_conflict", ok, reason)
	}
}

// Review trace 4 (MAJOR-2): a span on each side is not the same span. An
// omnibus of volumes 1-3 is not "Book 4-6", whatever the runtime-neutral legs
// say; with the runtime unknown or merely neutral the gate keeps refusing.
func TestEvaluate_Trace4_DifferentSpansRefuse(t *testing.T) {
	cand := metafetch.MetadataCandidate{Title: "Knightfall", Subtitle: "Book 4-6", Series: "Gotham Years", Score: 0.95, DurationSec: 108000}
	for runtime, dur := range map[string]*int{"unknown": nil, "neutral": new(101000)} {
		book := &database.Book{Title: "Knightfall Omnibus", Duration: dur}
		if ok, reason := evalAll(book, &cand); ok {
			t.Errorf("runtime %s: an omnibus was given another span's metadata (reason %q)", runtime, reason)
		}
	}
	// The same spans spelled differently on the two sides still match, with a confirming runtime.
	same := metafetch.MetadataCandidate{Title: "Knightfall", Subtitle: "Books 1 to 3", Series: "Gotham Years", SeriesPosition: "1-3", Score: 0.95, DurationSec: 108000}
	book := &database.Book{Title: "Knightfall, Book 1-3", SeriesPositionRaw: new("1-3"), Duration: new(108000)}
	if ok, reason := evalAll(book, &same); !ok {
		t.Errorf("the same span in two spellings refused: %s", reason)
	}
}

// Review trace 5 (MAJOR-4): two editions with the same wording and nothing
// else to tell them apart. A runtime that is unknown or only neutral is review.
func TestEvaluate_Trace5_UnnumberedEditionsNeedAnAgreeingRuntime(t *testing.T) {
	cand := metafetch.MetadataCandidate{Title: "Knightfall Omnibus", Series: "Gotham Years", Score: 0.95, DurationSec: 108000}
	for runtime, dur := range map[string]*int{"unknown": nil, "neutral": new(101000)} {
		book := &database.Book{Title: "Knightfall Omnibus", Duration: dur}
		if ok, reason := evalAll(book, &cand); ok || reason != ReasonCollectedRuntimeUnconfirmed {
			t.Errorf("runtime %s: allowed=%v reason=%q, want collected_edition_runtime_unconfirmed", runtime, ok, reason)
		}
	}
	book := &database.Book{Title: "Knightfall Omnibus", Duration: new(108000)}
	if ok, reason := evalAll(book, &cand); !ok {
		t.Errorf("the same omnibus with an agreeing runtime refused: %s", reason)
	}
	// A different collection is kept at the compilation penalty by the scorer
	// (metafetch TestApplyNonBaseAdjustments_CollectedEdition); this test
	// injects the score, so it covers only the gate's runtime rule.
}

// The measured fix of MAJOR-3: "Book 2 - Three Jokers" is volume 2 of a title
// that starts with a number word; its right single-volume candidate applies.
func TestEvaluate_BookNumberThenNumberWordTitle(t *testing.T) {
	book := &database.Book{Title: "Batman, Book 2 - Three Jokers", SeriesPositionRaw: new("2"), Duration: new(36000)}
	cand := metafetch.MetadataCandidate{Title: "Three Jokers", Series: "Batman", SeriesPosition: "2", Score: 0.95, DurationSec: 36000}
	if ok, reason := evalAll(book, &cand); !ok {
		t.Fatalf("right match refused: %s", reason)
	}
	// And volume 2 is not given volume 3's metadata.
	three := cand
	three.SeriesPosition = "3"
	if ok, reason := evalAll(book, &three); ok || reason != ReasonSequenceMismatch {
		t.Fatalf("volume 3 applied to volume 2: allowed=%v reason=%q", ok, reason)
	}
}

// A collected edition refuses without a confirming runtime, whichever side it
// is on, and a single volume is not affected by the rule.
func TestEvaluate_CollectedRuntimeRuleIsOneSidedOrBoth(t *testing.T) {
	// Book is a box set: the candidate carries the same range (sequence passes).
	same := metafetch.MetadataCandidate{Title: "Knightfall", Subtitle: "Books 1-3", Series: "Gotham Years", SeriesPosition: "1-3", Score: 0.95, DurationSec: 108000}
	for runtime, dur := range map[string]*int{"unknown": nil, "neutral": new(101000)} {
		book := &database.Book{Title: "Knightfall, Books 1-3", Duration: dur}
		if ok, reason := evalAll(book, &same); ok || reason != ReasonCollectedRuntimeUnconfirmed {
			t.Errorf("runtime %s: allowed=%v reason=%q, want collected_edition_runtime_unconfirmed", runtime, ok, reason)
		}
	}
	// A single volume against a single volume has no such rule: an unknown runtime still applies it
	// when nothing is overwritten and two other signals agree.
	single := &database.Book{Title: "Hush", SeriesPositionRaw: new("8"), Duration: new(36000)}
	sc := metafetch.MetadataCandidate{Title: "Hush", Series: "Gotham Years", SeriesPosition: "8", Score: 0.95, DurationSec: 36000}
	if ok, reason := evalAll(single, &sc); !ok {
		t.Errorf("a single volume refused: %s", reason)
	}
}

// Both new refusals are structural and an owner review may lift them.
func TestNewRefusals_OwnerReviewOverridableAndStructural(t *testing.T) {
	for _, reason := range []string{ReasonSequenceUnconfirmed, ReasonCollectedRuntimeUnconfirmed} {
		if !StructuralRefusal(reason) {
			t.Errorf("%s is not structural", reason)
		}
		v := Verdict{Reason: reason}
		if !v.OwnerReviewOverridable() {
			t.Errorf("%s is not owner-review overridable", reason)
		}
		if got := v.RefusingReasons(); len(got) != 1 || got[0] != reason {
			t.Errorf("RefusingReasons for %s = %v", reason, got)
		}
	}
}

// A collected edition that shows only in the candidate's series position field
// (the partial-book check reads title and subtitle, not the position) still
// needs an agreeing runtime.
func TestEvaluate_CollectedPositionOnlyOnCandidate(t *testing.T) {
	cand := metafetch.MetadataCandidate{Title: "Knightfall", Series: "Gotham Years", SeriesPosition: "1-3", Score: 0.95, DurationSec: 108000}
	book := &database.Book{Title: "Knightfall", Duration: new(101000)}
	if ok, reason := evalAll(book, &cand); ok || reason != ReasonCollectedRuntimeUnconfirmed {
		t.Fatalf("neutral runtime: allowed=%v reason=%q, want collected_edition_runtime_unconfirmed", ok, reason)
	}
}

// A stored title with no words of its own (only stop words and numbers) names
// no volume, so it never corroborates a set-aside number.
func TestTitleIsSeriesWords(t *testing.T) {
	cand := &metafetch.MetadataCandidate{Series: "Gotham Years"}
	cases := []struct {
		title, series string
		want          bool
	}{
		{"The", "", true},
		{"08", "", true},
		{"Gotham Years", "", true},
		{"Gotham", "Gotham Years", true},
		{"Year One", "Gotham Years", false},
		{"The Long Halloween", "Gotham Years", false},
	}
	for _, c := range cases {
		b := &database.Book{Title: c.title}
		if c.series != "" {
			b.Series = &database.Series{Name: c.series}
		}
		if got := titleIsSeriesWords(b, cand); got != c.want {
			t.Errorf("titleIsSeriesWords(%q, series %q) = %v, want %v", c.title, c.series, got, c.want)
		}
	}
}
