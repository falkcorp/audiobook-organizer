// file: internal/applygate/sequence_unconfirmed_test.go
// version: 1.1.0
// guid: 5d0b7e43-92c1-4a68-b3f7-0e8a1c64d2b9
// last-edited: 2026-11-17

package applygate

import (
	"strings"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/seqnum"
)

// The five inputs review r3 raised against the title-word corroboration, as
// Evaluate-level tests. At 950ca3736 every one of them APPLIED (measured); the
// corroboration path is cut, so each is now refused. A is refused as the
// book_sequence_conflict it is (the title's own "Year 2" disagrees with the
// stored position 1, as on main); the others as sequence_unconfirmed.
func TestEvaluate_R3Majors_WeakNumberAgainstStalePositionNeverApplies(t *testing.T) {
	legends := "Legends of the Dark Knight"
	for name, tc := range map[string]struct {
		book database.Book
		cand metafetch.MetadataCandidate
		want string
	}{
		"A: digit volume word, ordinal 02, stale position 1": {
			database.Book{Title: "02 - Batman: Year 2", SeriesPositionRaw: new("1"), Duration: new(36000)},
			metafetch.MetadataCandidate{Title: "Batman: Year One", Series: legends, SeriesPosition: "1", Score: 0.95, DurationSec: 36000}, ReasonBookSequenceConflict},
		"B: Part 1 stored, file 01, stale position 2": {
			database.Book{Title: "Hush: Part 1", FilePath: "/lib/Bruce Wayne/Hush/01 - Hush: Part 1.m4b", SeriesPositionRaw: new("2"), Duration: new(36000)},
			metafetch.MetadataCandidate{Title: "Hush: Part 2", Series: "Batman", SeriesPosition: "2", Score: 0.95, DurationSec: 36000}, ReasonSequenceUnconfirmed},
		"C: ordinal 02 on Part 2 against Part 1": {
			database.Book{Title: "02 - Hush: Part 2", SeriesPositionRaw: new("1"), Duration: new(36000)},
			metafetch.MetadataCandidate{Title: "Hush: Part 1", Series: "Batman", SeriesPosition: "1", Score: 0.95, DurationSec: 36000}, ReasonSequenceUnconfirmed},
		"D: roman Part I stored, file 01, stale position 2": {
			database.Book{Title: "Hush: Part I", FilePath: "/lib/Bruce Wayne/Hush/01 - Hush Part I.m4b", SeriesPositionRaw: new("2"), Duration: new(36000)},
			metafetch.MetadataCandidate{Title: "Hush: Part II", Series: "Batman", SeriesPosition: "2", Score: 0.95, DurationSec: 36000}, ReasonSequenceUnconfirmed},
		"E: a title word that only the candidate's subtitle supplies": {
			database.Book{Title: "02 - Batman: Dark Victory", SeriesPositionRaw: new("1"), Duration: new(36000)},
			metafetch.MetadataCandidate{Title: "Batman: The Long Halloween", Subtitle: "The Prelude to Dark Victory", Series: legends, SeriesPosition: "1", Score: 0.95, DurationSec: 36000}, ReasonSequenceUnconfirmed},
	} {
		book, cand := tc.book, tc.cand
		if ok, reason := evalAll(&book, &cand); ok || reason != tc.want {
			t.Errorf("%s: allowed=%v reason=%q, want %s", name, ok, reason, tc.want)
		}
	}
}

// R3-MINOR-4: when both sides are collected editions worded differently, the
// detail an owner reads says so.
func TestCollectedRuntimeRefusal_DetailNamesDifferingWording(t *testing.T) {
	book := &database.Book{Title: "Knightfall Book 1-3", SeriesPositionRaw: new("1-3")}
	cand := &metafetch.MetadataCandidate{Title: "Knightfall Omnibus", Subtitle: "Books 1-3", SeriesPosition: "1-3"}
	seq := CheckSequence(book, cand)
	got := collectedRuntimeRefusal(book, cand, seq, EvidenceVerdict{})
	for _, want := range []string{"edition wording differs", "the book: none", `the candidate: "omnibus"`} {
		if !strings.Contains(got, want) {
			t.Errorf("detail %q does not mention %q", got, want)
		}
	}
	// Identical wording on both sides adds nothing.
	same := &metafetch.MetadataCandidate{Title: "Knightfall Book 1-3", SeriesPosition: "1-3"}
	if got := collectedRuntimeRefusal(book, same, CheckSequence(book, same), EvidenceVerdict{}); strings.Contains(got, "wording differs") {
		t.Errorf("same wording: %q", got)
	}
}

// R3-NIT-1: the combined-leg reasons are computed for every verdict, so a nil
// book or candidate answers "no refusal" instead of panicking.
func TestCollectedRuntimeRefusal_NilInputs(t *testing.T) {
	if got := collectedRuntimeRefusal(nil, &metafetch.MetadataCandidate{}, SequenceVerdict{}, EvidenceVerdict{}); got != "" {
		t.Errorf("nil book: %q", got)
	}
	if got := collectedRuntimeRefusal(&database.Book{}, nil, SequenceVerdict{}, EvidenceVerdict{}); got != "" {
		t.Errorf("nil candidate: %q", got)
	}
}

// The text after a leading list number is read with the same weak-number
// rules as any other text: a number there that is also named by a part tag is a
// track number, not a second volume, so it cannot turn into a conflict of its
// own. Plain "Hush, Part 3 - 3" is read the same way.
func TestBookNumbers_RestAfterLeadingNumberKeepsWeakRules(t *testing.T) {
	b := &database.Book{Title: "01 - Hush, Part 3 - 3", SeriesPositionRaw: new("8")}
	c := &metafetch.MetadataCandidate{Title: "Hush", Series: "Batman", SeriesPosition: "8"}
	v := CheckSequence(b, c)
	if !v.Pass || v.WeakSetAside == "" {
		t.Fatalf("pass=%v reason=%q weakSetAside=%q numbers=%s", v.Pass, v.Reason, v.WeakSetAside, describe(v.BookNumbers))
	}
}

func TestBookNumbers_PlainTitleReadsTheSameWeakNumber(t *testing.T) {
	b := &database.Book{Title: "Hush, Part 3 - 3", SeriesPositionRaw: new("8")}
	c := &metafetch.MetadataCandidate{Title: "Hush", Series: "Batman", SeriesPosition: "8"}
	if v := CheckSequence(b, c); !v.Pass || v.WeakSetAside == "" {
		t.Fatalf("pass=%v reason=%q weakSetAside=%q numbers=%s", v.Pass, v.Reason, v.WeakSetAside, describe(v.BookNumbers))
	}
}

// Review r4 MAJOR: a part suffix on the file name is "strong" to effective()
// (it is not weak), so the folder's leading ordinal 02 was dropped and the
// suffix 001 agreed with the candidate's #1. weakSetAside skipped the suffix
// as uncounted, found no strong source and returned "", so nothing refused.
// At 719347773 this APPLIED (measured); main refuses it (book_sequence_conflict,
// 02 against 001).
func TestEvaluate_R4Major_PartSuffixStrongWeakDroppedStillRefuses(t *testing.T) {
	for runtime, dur := range map[string]*int{"unknown": nil, "agreeing": new(36000)} {
		book := &database.Book{Title: "Batman", FilePath: "/lib/Bruce Wayne/02 Batman/Batman - 001.mp3", Duration: dur}
		cand := &metafetch.MetadataCandidate{Title: "Batman", SeriesPosition: "1", Score: 0.95, DurationSec: 36000}
		if ok, reason := evalAll(book, cand); ok || reason != ReasonSequenceUnconfirmed {
			t.Errorf("runtime %s: allowed=%v reason=%q, want sequence_unconfirmed", runtime, ok, reason)
		}
	}
}

// The invariant behind the weak-number rule, over generated source sets: the
// sequence leg (a CheckSequence pass that also carries no WeakSetAside, which
// is what lets EvaluateTranscribed go on) passes ONLY IF every book-side
// number, weak ones included, equals the candidate's number. So the passes are
// a subset of what an "every source must agree" rule passes: weak numbers
// change the reason (review, with a track-number explanation, instead of a
// hard conflict), never the outcome from refuse to allow. Ranges are excluded
// (their own endpoint rule is tested elsewhere); the candidate always names a
// number, because a part suffix is set aside when it names none.
func TestCheckSequence_PassImpliesEverySourceAgrees(t *testing.T) {
	titles := []string{"", "Batman", "Batman: Year 2", "01 - Batman", "02 - Batman: Year 2", "Batman, Book 2",
		"Batman Book 3", "Batman - 001", "Batman, Part 2", "03 Batman", "Batman 451", "01 - Batman 451", "Batman 2 of 20"}
	paths := []string{"", "/lib/A/Batman/Batman.m4b", "/lib/A/02 Batman/Batman - 001.mp3", "/lib/A/Batman/03 Chapter 1.mp3",
		"/lib/A/Batman/Batman Book 3 - Track 07.mp3", "/lib/A/01 Batman/Batman.m4b", "/lib/A/Batman - 002/Batman 02 of 20.mp3",
		"/lib/A/Batman Book 1/01 Batman.m4b", "/lib/A/Batman"}
	positions := []string{"", "1", "2", "3"}
	candPositions := []string{"1", "2", "3"}
	var passes, weakRefusals, total int
	for _, title := range titles {
		for _, path := range paths {
			for _, pos := range positions {
				for _, cp := range candPositions {
					book := &database.Book{Title: title, FilePath: path}
					if pos != "" {
						book.SeriesPositionRaw = new(pos)
					}
					cand := &metafetch.MetadataCandidate{Title: "Batman", SeriesPosition: cp}
					v := CheckSequence(book, cand)
					total++
					if v.Pass && v.WeakSetAside != "" {
						weakRefusals++
					}
					if !v.Pass || v.WeakSetAside != "" {
						continue
					}
					passes++
					want, _ := seqnum.ParsePosition(cp)
					for _, s := range v.BookNumbers {
						if s.Form == FormRangeEndpoint {
							continue
						}
						if got, _ := seqnum.ParsePosition(s.Number); !seqnum.Equal(got, want) {
							t.Fatalf("title %q path %q position %q candidate %q: sequence leg passed but %s=%s differs (%s)",
								title, path, pos, cp, s.Source, s.Number, describe(v.BookNumbers))
						}
					}
				}
			}
		}
	}
	// The table must exercise both sides, or it proves nothing.
	if passes < 20 || weakRefusals < 20 {
		t.Fatalf("degenerate table: %d of %d passed, %d held for a weak number", passes, total, weakRefusals)
	}
}
