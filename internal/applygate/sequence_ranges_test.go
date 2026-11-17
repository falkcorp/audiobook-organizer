// file: internal/applygate/sequence_ranges_test.go
// version: 1.4.1
// guid: 4d9c2a71-8e35-4f60-b2a8-17c6e3d05f94
// last-edited: 2026-11-17

package applygate

import (
	"fmt"
	"slices"
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
// survives the trip through resolve.
func TestAgree_RangeTextRoundTrips(t *testing.T) {
	for _, in := range []string{"Knightfall, Books 1-3", "Knightfall, Books 1, 3", "Knightfall, Books 1.5-2"} {
		nums := BookNumbers(&database.Book{Title: in})
		if len(nums) != 1 {
			t.Fatalf("%q: %v", in, nums)
		}
		cand := CandidateNumbers(&metafetch.MetadataCandidate{Title: in})
		a := resolve(nums, false).Num
		b := resolve(cand, false).Num
		if !a.IsRange() || a.Text != nums[0].Number || fmt.Sprint(a) != fmt.Sprint(b) {
			t.Errorf("%q: book %+v candidate %+v", in, a, b)
		}
	}
}

func evalRuntime(b *database.Book, c *metafetch.MetadataCandidate) Verdict {
	return Evaluate(b, snap(b), database.ComputeBookRuntime(b, nil), c, nil)
}

// End to end, through the whole gate: a track-ordinal title whose ordinal
// disagrees with the stored position is never applied unattended, whichever
// volume the candidate is: against the stored position's volume it goes to
// review (sequence_unconfirmed), against the ordinal's own number it is a
// mismatch.
func TestEvaluate_TrackOrdinalTitle(t *testing.T) {
	book := &database.Book{Title: "08 - The Long Halloween", SeriesPositionRaw: new("6"), Duration: new(36000)}
	good := metafetch.MetadataCandidate{Title: "The Long Halloween", Series: "Gotham Years", SeriesPosition: "6", Score: 0.95, DurationSec: 36000}
	if v := evalRuntime(book, &good); v.Allowed || v.Reason != ReasonSequenceUnconfirmed {
		t.Fatalf("ordinal 8 against stored position 6: allowed=%v reason=%q, want sequence_unconfirmed", v.Allowed, v.Reason)
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
	// A title that is not the series name does not change that: the ordinal
	// disagrees with the stored position, so the pair is never applied
	// unattended, whatever the runtime says (round 4).
	other := metafetch.MetadataCandidate{Title: "Knightfall", Series: "Gotham Years", SeriesPosition: "1", Score: 0.95, DurationSec: 36000}
	for runtime, dur := range map[string]*int{"agreeing": new(36000), "neutral": new(34000)} {
		b := &database.Book{Title: "03 - Knightfall", SeriesPositionRaw: new("1"), Duration: dur}
		if ok, reason := evalAll(b, &other); ok || reason != ReasonSequenceUnconfirmed {
			t.Errorf("distinct title, %s runtime: allowed=%v reason=%q, want sequence_unconfirmed", runtime, ok, reason)
		}
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

// A weak number that disagrees with the stored title, the stored position or
// an unmarked file name is never confirmed by them: they are all fields an
// earlier apply can have written. The verdict passes the sequence leg with
// WeakSetAside set and the gate refuses it as sequence_unconfirmed, whatever
// the runtime says (round 4: no corroboration path remains).
func TestCheckSequence_WeakDisagreeingWithWritableSourcesIsAlwaysUnconfirmed(t *testing.T) {
	cand := metafetch.MetadataCandidate{Title: "Year One", Series: "Gotham Years", SeriesPosition: "1", Score: 0.95, DurationSec: 36000}
	for name, b := range map[string]database.Book{
		"title, position and an unmarked file number":           {Title: "Year One 1", SeriesPositionRaw: new("1"), FilePath: "/lib/A/Year One/02 - Year Two.m4b"},
		"position and a leading ordinal":                        {Title: "02 - Year One", SeriesPositionRaw: new("1")},
		"a Book N marker in the file name is not independent":   {Title: "Year One", SeriesPositionRaw: new("1"), FilePath: "/lib/A/08 - Year One/Year One Book 1.m4b"},
		"a Book N marker in the folder name is not independent": {Title: "Year One", SeriesPositionRaw: new("1"), FilePath: "/lib/A/Year One Book 1/08 - Year One.m4b"},
	} {
		v := CheckSequence(&b, &cand)
		if !v.Pass || v.WeakSetAside == "" {
			t.Errorf("%s: pass=%v weakSetAside=%q, want a pass that carries WeakSetAside", name, v.Pass, v.WeakSetAside)
			continue
		}
		for runtime, dur := range map[string]*int{"unknown": nil, "neutral": new(34000), "agreeing": new(36000)} {
			bb := b
			bb.Duration = dur
			ok, reason := evalAll(&bb, &cand)
			if ok {
				t.Errorf("%s, runtime %s: applied", name, runtime)
			}
			// An unknown runtime is refused first by the overwrite check; the
			// override record still lists sequence_unconfirmed.
			if runtime != "unknown" && reason != ReasonSequenceUnconfirmed {
				t.Errorf("%s, runtime %s: reason=%q, want sequence_unconfirmed", name, runtime, reason)
			}
		}
	}
}

// A weak number that AGREES with the strong one is just the same number
// twice, and needs nothing more.
func TestCheckSequence_WeakAgreeingWithStrongPasses(t *testing.T) {
	cand := metafetch.MetadataCandidate{Title: "The Long Halloween", Series: "Legends of the Dark Knight", SeriesPosition: "8", Score: 0.95, DurationSec: 36000}
	b := database.Book{Title: "08 - The Long Halloween", SeriesPositionRaw: new("8"), Duration: new(36000)}
	if v := CheckSequence(&b, &cand); !v.Pass || v.WeakSetAside != "" {
		t.Fatalf("pass=%v weakSetAside=%q", v.Pass, v.WeakSetAside)
	}
	if ok, reason := evalAll(&b, &cand); !ok {
		t.Errorf("refused (%s)", reason)
	}
}

// The detail an owner reads says which number was set aside, that it looks
// like a list or track number, and why the rest does not settle it.
func TestEvaluate_SequenceUnconfirmedDetailNamesTheNumber(t *testing.T) {
	book := &database.Book{Title: "02 - Batman: Year One", SeriesPositionRaw: new("1"), Duration: new(36000)}
	cand := metafetch.MetadataCandidate{Title: "Batman: Year One", Series: "Legends of the Dark Knight", SeriesPosition: "1", Score: 0.95, DurationSec: 36000}
	bb, cc := *book, cand
	bb.Author = &database.Author{Name: "Bruce Wayne"}
	bb.Narrator = new("Alfred Pennyworth")
	cc.Author, cc.Narrator = "Bruce Wayne", "Alfred Pennyworth"
	v := evalRuntime(&bb, &cc)
	for _, want := range []string{"leading number 2", "list or track number", "series_position 1", "earlier apply"} {
		if !strings.Contains(v.Detail, want) {
			t.Errorf("detail %q does not mention %q", v.Detail, want)
		}
	}
}

// A metamorphic property of what remains: prefixing a stored title T with a
// list number NN that the reader captured as such ("NN - T") is never MORE
// permissive than T alone, for the same candidate and every other field.
func TestEvaluate_LeadingTrackNumberNeverMorePermissive(t *testing.T) {
	legends := "Legends of the Dark Knight"
	type row struct {
		title string
		pos   string
		cand  metafetch.MetadataCandidate
	}
	rows := []row{
		{"Batman: Year One", "1", metafetch.MetadataCandidate{Title: "Batman: Year One", Series: legends, SeriesPosition: "1"}},
		{"Batman: Year One", "3", metafetch.MetadataCandidate{Title: "Batman: Year One", Series: legends, SeriesPosition: "1"}},
		{"Batman: Year 2", "1", metafetch.MetadataCandidate{Title: "Batman: Year One", Series: legends, SeriesPosition: "1"}},
		{"Hush: Part 2", "1", metafetch.MetadataCandidate{Title: "Hush: Part 1", Series: "Batman", SeriesPosition: "1"}},
		{"Hush: Part I", "2", metafetch.MetadataCandidate{Title: "Hush: Part II", Series: "Batman", SeriesPosition: "2"}},
		{"Batman: Dark Victory", "1", metafetch.MetadataCandidate{Title: "Batman: The Long Halloween", Subtitle: "The Prelude to Dark Victory", Series: legends, SeriesPosition: "1"}},
		{"The Long Halloween", "6", metafetch.MetadataCandidate{Title: "The Long Halloween", Series: legends, SeriesPosition: "6"}},
		{"The Long Halloween", "", metafetch.MetadataCandidate{Title: "The Long Halloween", Series: legends, SeriesPosition: "6"}},
		{"Knightfall", "2", metafetch.MetadataCandidate{Title: "Knightfall", Series: "Gotham Years", SeriesPosition: "2"}},
		{"Knightfall Omnibus", "", metafetch.MetadataCandidate{Title: "Knightfall Omnibus", Series: "Gotham Years"}},
	}
	for _, r := range rows {
		for _, nn := range []string{"01", "02", "08"} {
			for runtime, dur := range map[string]*int{"unknown": nil, "neutral": new(34000), "agreeing": new(36000)} {
				cand := r.cand
				cand.Score, cand.DurationSec = 0.95, 36000
				plain := database.Book{Title: r.title, Duration: dur}
				prefixed := database.Book{Title: nn + " - " + r.title, Duration: dur}
				if r.pos != "" {
					plain.SeriesPositionRaw, prefixed.SeriesPositionRaw = new(r.pos), new(r.pos)
				}
				okPlain, _ := evalAll(&plain, &cand)
				okPrefixed, reasonPrefixed := evalAll(&prefixed, &cand)
				if okPrefixed && !okPlain {
					t.Errorf("%q + %q (position %q, runtime %s): prefixed APPLIES but the plain title is refused", nn, r.title, r.pos, runtime)
				}
				_ = reasonPrefixed
			}
		}
	}
}

// R2-MINOR-2: the shapes round 2 refused that main did not.
func TestEvaluate_R2Minor2_NewFalseRefusalsFixed(t *testing.T) {
	neutral := new(34000)
	// 1: a single volume inside a box-set FOLDER is not a collected edition by the folder name.
	box := &database.Book{Title: "Knightfall", FilePath: "/lib/A/Knightfall, Books 1-3/Knightfall Book 2.m4b", Duration: neutral}
	single := metafetch.MetadataCandidate{Title: "Knightfall", Series: "Gotham Years", SeriesPosition: "2", Score: 0.95, DurationSec: 36000}
	// An agreeing runtime lifts partial_book (main's rule, unchanged); the
	// collected-edition rule must then not add a refusal of its own.
	agreeing := *box
	agreeing.Duration = new(36000)
	// The folder's range disagrees with the file's own "Book 2" marker, so the
	// pair goes to review as sequence_unconfirmed (main refused it as a
	// conflict); the collected-edition rule itself adds nothing.
	if ok, reason := evalAll(&agreeing, &single); ok || reason != ReasonSequenceUnconfirmed {
		t.Errorf("single volume in a box-set folder, agreeing runtime: allowed=%v reason=%q, want sequence_unconfirmed", ok, reason)
	}
	// With a neutral runtime main already refused it as partial_book (a range in the folder, none in the candidate).
	if ok, reason := evalAll(box, &single); ok || reason != ReasonPartialBook {
		t.Errorf("single volume in a box-set folder, neutral runtime: allowed=%v reason=%q, want main's partial_book", ok, reason)
	}
	if got := collectedSide(box, &single, CheckSequence(box, &single)); got != "" {
		t.Errorf("collectedSide for a single volume in a box-set folder = %q", got)
	}
	// 2: "Recollection" is a single volume (word-boundary test, not a substring).
	rec := &database.Book{Title: "Batman: Recollection", SeriesPositionRaw: new("2"), Duration: neutral}
	recCand := metafetch.MetadataCandidate{Title: "Batman: Recollection", Series: "Gotham Years", SeriesPosition: "2", Score: 0.95, DurationSec: 36000}
	if ok, reason := evalAll(rec, &recCand); !ok {
		t.Errorf("Recollection: refused (%s)", reason)
	}
	// 3: a candidate whose title states a range and whose position is an end of it.
	box13 := &database.Book{Title: "Knightfall, Books 1-3", Duration: new(108000)}
	endpoint := metafetch.MetadataCandidate{Title: "Knightfall, Books 1-3", Series: "Gotham Years", SeriesPosition: "3", Score: 0.95, DurationSec: 108000}
	if ok, reason := evalAll(box13, &endpoint); !ok {
		t.Errorf("range title with an endpoint position: refused (%s)", reason)
	}
	// ... and that candidate is still never given to one volume, nor is its endpoint a licence for a different range.
	vol3 := &database.Book{Title: "Knightfall, Book 3", SeriesPositionRaw: new("3"), Duration: new(108000)}
	if ok, reason := evalAll(vol3, &endpoint); ok || reason != ReasonSequenceMismatch {
		t.Errorf("volume 3 against the range with an endpoint position: allowed=%v reason=%q", ok, reason)
	}
	// 4: the same edition with extra wording on one side is not a hard partial_book.
	omni := metafetch.MetadataCandidate{Title: "Knightfall Omnibus", Subtitle: "Books 1-3", Series: "Gotham Years", SeriesPosition: "1-3", Score: 0.95, DurationSec: 108000}
	for runtime, dur := range map[string]*int{"unknown": nil, "neutral": new(101000)} {
		b := &database.Book{Title: "Knightfall, Books 1-3", SeriesPositionRaw: new("1-3"), Duration: dur}
		v := evalRuntime(b, &omni)
		if v.Allowed || v.Reason == ReasonPartialBook {
			t.Errorf("runtime %s: allowed=%v reason=%q, want an overridable refusal that is not partial_book", runtime, v.Allowed, v.Reason)
		}
		if !v.OwnerReviewOverridable() {
			t.Errorf("runtime %s: an owner review cannot lift %q", runtime, v.Reason)
		}
	}
	// Different ranges are still the hard refusal.
	other := omni
	other.Subtitle, other.SeriesPosition = "Books 1-5", "1-5"
	b := &database.Book{Title: "Knightfall, Books 1-3", SeriesPositionRaw: new("1-3"), Duration: neutral}
	if v := evalRuntime(b, &other); v.Allowed || v.OwnerReviewOverridable() && v.Reason == ReasonPartialBook {
		t.Errorf("different ranges: allowed=%v reason=%q", v.Allowed, v.Reason)
	}
}

// R2-MINOR-3: a candidate collection that no span reader sees (wording only
// in the subtitle) still needs an agreeing runtime.
func TestEvaluate_R2Minor3_CandidateCompleteCollection(t *testing.T) {
	cand := metafetch.MetadataCandidate{Title: "Knightfall", Subtitle: "The Complete Collection", Series: "Gotham Years", Score: 0.95, DurationSec: 108000}
	book := &database.Book{Title: "Knightfall", Duration: new(101000)}
	if ok, reason := evalAll(book, &cand); ok || reason != ReasonCollectedRuntimeUnconfirmed {
		t.Fatalf("allowed=%v reason=%q, want collected_edition_runtime_unconfirmed", ok, reason)
	}
}

// R2-MINOR-4 and NIT-1: an override record names both combined-leg reasons
// whenever they would fire, whatever refused first, and the hashless marker
// lifts neither.
func TestVerdict_UnconfirmedReasonsAlwaysListed(t *testing.T) {
	// A collected edition with an unknown runtime whose score is also below the floor.
	cand := metafetch.MetadataCandidate{Title: "Knightfall Omnibus", Series: "Gotham Years", Score: 0.5, DurationSec: 108000}
	book := &database.Book{Title: "Knightfall Omnibus", Author: &database.Author{Name: "Bruce Wayne"}, Narrator: new("Alfred Pennyworth")}
	cand.Author, cand.Narrator = "Bruce Wayne", "Alfred Pennyworth"
	v := evalRuntime(book, &cand)
	if v.Allowed || v.Reason != ReasonScoreBelowFloor {
		t.Fatalf("setup: allowed=%v reason=%q, want score_below_floor first", v.Allowed, v.Reason)
	}
	if !slices.Contains(v.RefusingReasons(), ReasonCollectedRuntimeUnconfirmed) {
		t.Errorf("override record omits the collected-edition reason: %v", v.RefusingReasons())
	}
	if v.UnseenOwnerReviewOverridable() {
		t.Error("the hashless marker lifts a pair that also needs a confirming runtime")
	}
	// A weak ordinal over a stale position, refused by the evidence leg first (unknown runtime on an overwrite).
	b := &database.Book{Title: "03 - Knightfall", SeriesPositionRaw: new("1")}
	c := metafetch.MetadataCandidate{Title: "Knightfall", Series: "Gotham Years", SeriesPosition: "1", Score: 0.95, DurationSec: 36000}
	v2 := evalRuntime(b, &c)
	if v2.Allowed || !slices.Contains(v2.RefusingReasons(), ReasonSequenceUnconfirmed) {
		t.Errorf("allowed=%v reasons=%v, want sequence_unconfirmed listed", v2.Allowed, v2.RefusingReasons())
	}
	// Neither new reason is lifted by the hashless marker even as the first reason.
	for _, r := range []string{ReasonSequenceUnconfirmed, ReasonCollectedRuntimeUnconfirmed} {
		if (Verdict{Reason: r}).UnseenOwnerReviewOverridable() {
			t.Errorf("%s is lifted by the hashless marker", r)
		}
		if !(Verdict{Reason: r}).OwnerReviewOverridable() {
			t.Errorf("%s is not lifted by an owner review", r)
		}
	}
}

// R2-MINOR-6: a digit list needs a terminator, so a number that starts the
// title after a comma is not a list member.
func TestEvaluate_R2Minor6_NumberStartingTheTitle(t *testing.T) {
	book := &database.Book{Title: "Batman, Book 2, 10 Nights of the Beast", SeriesPositionRaw: new("2"), Duration: new(36000)}
	cand := metafetch.MetadataCandidate{Title: "Batman, Book 2, 10 Nights of the Beast", Series: "Batman", SeriesPosition: "2", Score: 0.95, DurationSec: 36000}
	if ok, reason := evalAll(book, &cand); !ok {
		t.Fatalf("right match refused: %s", reason)
	}
}

// The endpoint rule is symmetric: a range in the candidate's SUBTITLE with a
// position equal to an end of it is the range stated twice.
func TestEvaluate_CandidateSubtitleRangeWithEndpointPosition(t *testing.T) {
	book := &database.Book{Title: "Knightfall, Books 1-3", Duration: new(108000)}
	cand := metafetch.MetadataCandidate{Title: "Knightfall", Subtitle: "Books 1-3", Series: "Gotham Years", SeriesPosition: "3", Score: 0.95, DurationSec: 108000}
	if ok, reason := evalAll(book, &cand); !ok {
		t.Errorf("subtitle range with an endpoint position: refused (%s)", reason)
	}
}

// collectedSide reads each side on its own: book wording with no range (which
// partial_book would mask in a full evaluation), and the hidden-reason lift.
func TestCollectedSide_EachSideOnItsOwn(t *testing.T) {
	single := &metafetch.MetadataCandidate{Title: "Knightfall", Series: "Gotham Years", SeriesPosition: "2"}
	book := &database.Book{Title: "Knightfall Omnibus"}
	if got := collectedSide(book, single, CheckSequence(book, single)); got != "the book" {
		t.Errorf("book wording only: %q, want the book", got)
	}
	vol := &database.Book{Title: "Knightfall", SeriesPositionRaw: new("1-3")}
	if got := collectedSide(vol, single, CheckSequence(vol, single)); got != "the book" {
		t.Errorf("stored range position: %q, want the book", got)
	}
	omni := &metafetch.MetadataCandidate{Title: "Knightfall", Subtitle: "Books 1-3", SeriesPosition: "3"}
	sb := &database.Book{Title: "Knightfall", SeriesPositionRaw: new("2")}
	if got := collectedSide(sb, omni, CheckSequence(sb, omni)); got != "the candidate" {
		t.Errorf("candidate range in the subtitle: %q, want the candidate", got)
	}
}

func TestVerdict_HiddenUnconfirmedReasonIsNeverUnseenOverridable(t *testing.T) {
	for _, r := range []string{ReasonSequenceUnconfirmed, ReasonCollectedRuntimeUnconfirmed} {
		v := Verdict{Reason: ReasonScoreBelowFloor, Unconfirmed: []string{r}}
		if !v.OwnerReviewOverridable() {
			t.Fatalf("setup: score_below_floor is not owner-overridable")
		}
		if v.UnseenOwnerReviewOverridable() {
			t.Errorf("a pair that also needs %s is lifted by the hashless marker", r)
		}
		if !slices.Contains(v.RefusingReasons(), r) {
			t.Errorf("override record omits %s: %v", r, v.RefusingReasons())
		}
	}
}
