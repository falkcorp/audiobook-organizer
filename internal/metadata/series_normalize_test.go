// file: internal/metadata/series_normalize_test.go
// version: 2.0.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-09-02

package metadata

import (
	"strings"
	"testing"
)

// TestStripSeriesContamination is the regression oracle for every write path.
//
// ⚠️ FIVE ASSERTIONS IN THIS TABLE WERE DELIBERATELY INVERTED. "Babylon 5",
// "World War 2", "Section 31", "Fahrenheit 451" and "The Long Earth 2" used to
// assert that a bare trailing number is NOT stripped. They now assert that it
// IS. That is not a regression and not an accident — it is the library owner's
// explicit instruction:
//
//	"There is absolutely zero series that have a number in them. And when we
//	 find one I'll manually override."
//
// The old assertions encoded the opposite policy (guard the rare legitimate
// numeric name at the cost of leaving 6,859 contaminated rows). Anyone restoring
// them is reversing a product decision, not fixing a bug — read the header
// comment on StripSeriesContamination first.
func TestStripSeriesContamination(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		title        string
		wantSeries   string
		wantPosition string
		wantRule     string
		wantFlag     bool
		wantReason   SeriesFlagReason
	}{
		// ── Rule 1: dash-embedded position + title ──
		{
			name:         "dash embedded position and title",
			input:        "The Long Earth - 1 - The Long Earth",
			wantSeries:   "The Long Earth",
			wantPosition: "1",
			wantRule:     RuleDashPositionTitle,
		},
		{
			name:         "dash embedded with different title",
			input:        "My Long Series - 3 - The Third Book",
			wantSeries:   "My Long Series",
			wantPosition: "3",
			wantRule:     RuleDashPositionTitle,
		},

		// ── Rule 2: trailing keyword position. The owner's headline case. ──
		//
		// "Nameless Sovereign #5" matched NOTHING before this change, in either
		// normalizer: `#` sat inside a \b-prefixed keyword group, and \b between
		// a space and a `#` is not a word boundary.
		{
			name:         "hash suffix, the owner's example",
			input:        "Nameless Sovereign #5",
			wantSeries:   "Nameless Sovereign",
			wantPosition: "5",
			wantRule:     RuleTrailingKeyword,
		},
		{
			name:         "comma Book N",
			input:        "Adeptus Mechanicus, Book 1",
			wantSeries:   "Adeptus Mechanicus",
			wantPosition: "1",
			wantRule:     RuleTrailingKeyword,
		},
		{
			// Zero-padding is dropped by the round-trip through int, matching
			// what ParseSeriesFromTitle already does with TrimLeft(pos, "0").
			name:         "comma Book zero-padded",
			input:        "Alex McKnight, Book 01",
			wantSeries:   "Alex McKnight",
			wantPosition: "1",
			wantRule:     RuleTrailingKeyword,
		},
		{
			// The trailing word "Series" is left in the base ON PURPOSE. This
			// function's job is to get the NUMBER out; "Series" is not a number,
			// and stripping trailing nouns is a different (and much riskier)
			// change. Noted so nobody reads this as an oversight.
			name:         "Series Book N leaves the word Series in the base",
			input:        "A Court of Thorns and Roses Series Book 5",
			wantSeries:   "A Court of Thorns and Roses Series",
			wantPosition: "5",
			wantRule:     RuleTrailingKeyword,
		},

		// ── Rule 3: trailing bare number (INVERTED, see the header) ──
		{
			name:         "zero-padded bare trailing number",
			input:        "Discworld 05",
			wantSeries:   "Discworld",
			wantPosition: "5",
			wantRule:     RuleTrailingBare,
		},
		{
			name:         "unpadded bare trailing number",
			input:        "Succubus Lord 5",
			wantSeries:   "Succubus Lord",
			wantPosition: "5",
			wantRule:     RuleTrailingBare,
		},
		{
			name:         "INVERTED: bare space trailing digit now stripped",
			input:        "The Long Earth 2",
			wantSeries:   "The Long Earth",
			wantPosition: "2",
			wantRule:     RuleTrailingBare,
		},
		{
			name:         "trailing digit with dash-space (was reTrailingDigit)",
			input:        "The Long Earth - 2",
			wantSeries:   "The Long Earth",
			wantPosition: "2",
			wantRule:     RuleTrailingBare,
		},
		{
			name:         "trailing digit 99 with dash-space (was reTrailingDigit)",
			input:        "Big Series - 99",
			wantSeries:   "Big Series",
			wantPosition: "99",
			wantRule:     RuleTrailingBare,
		},
		{
			name:         "INVERTED: Babylon 5 now stripped",
			input:        "Babylon 5",
			wantSeries:   "Babylon",
			wantPosition: "5",
			wantRule:     RuleTrailingBare,
		},
		{
			name:         "INVERTED: World War 2 now stripped",
			input:        "World War 2",
			wantSeries:   "World War",
			wantPosition: "2",
			wantRule:     RuleTrailingBare,
		},
		{
			// NOT inverted, and the reason is the junk-base guard rather than
			// the old "don't strip bare numbers" policy: the base would be
			// "Section", which is a disc/chapter tag word. Kept in the table
			// because it was one of the original must-NOT-strip cases and it now
			// passes for a completely different reason.
			name:       "Section 31 held back by the junk-base guard",
			input:      "Section 31",
			wantSeries: "Section 31",
		},
		{
			name:         "INVERTED: Fahrenheit 451 now stripped",
			input:        "Fahrenheit 451",
			wantSeries:   "Fahrenheit",
			wantPosition: "451",
			wantRule:     RuleTrailingBare,
		},

		// ── Rule 4: bracketed trailing number ──
		{
			name:         "bracketed padded position",
			input:        "Dragon Born [04]",
			wantSeries:   "Dragon Born",
			wantPosition: "4",
			wantRule:     RuleBracketed,
		},
		{
			name:         "parenthesised position",
			input:        "The Hollows (7)",
			wantSeries:   "The Hollows",
			wantPosition: "7",
			wantRule:     RuleBracketed,
		},

		// ── Rule 5: keyword-vouched embedded position ──
		{
			name:         "embedded Book N with trailing title",
			input:        "Evil Genius: Book 4: Becoming the Apex Supervillain",
			wantSeries:   "Evil Genius",
			wantPosition: "4",
			wantRule:     RuleEmbeddedKeyword,
		},
		{
			name:         "embedded Vol NN with trailing title",
			input:        "Vampire Hunter D: Vol 09: The Rose Princess",
			wantSeries:   "Vampire Hunter D",
			wantPosition: "9",
			wantRule:     RuleEmbeddedKeyword,
		},

		// ── Rule 6: un-vouched — MUST NOT STRIP ──
		{
			// A leading number with list-item punctuation. Really is a position
			// (Horus Heresy book 8), but "86—EIGHTY-SIX" is the same shape with
			// the opposite meaning, so this is reported and never applied.
			name:       "leading bare number is flagged, not stripped",
			input:      "08. Battle for the Abyss",
			wantSeries: "08. Battle for the Abyss",
			wantRule:   string(ShapeLeadingBare),
			wantFlag:   true,
			wantReason: FlagUnvouchedPosition,
		},
		{
			name:       "mid-colon bare number is flagged, not stripped",
			input:      "Station 64: The Doll Dungeon",
			wantSeries: "Station 64: The Doll Dungeon",
			wantRule:   string(ShapeMidColon),
			wantFlag:   true,
			wantReason: FlagUnvouchedPosition,
		},
		{
			// NOT flagged, and that is correct rather than a miss. The number is
			// fused to the next word by an UNSPACED em-dash, which
			// leadingBarePosition's separator class excludes on purpose — so no
			// shape claims it at all and there is nothing to review. Adding a
			// catch-all "name contains a digit" flag would bury the real review
			// queue under every legitimate numeric name in the library.
			name:       "86-EIGHTY-SIX is untouched and unflagged",
			input:      "86—EIGHTY-SIX",
			wantSeries: "86—EIGHTY-SIX",
		},

		// ── Rule 7: trailing ordinal word ──
		{
			name:         "trailing ordinal One",
			input:        "The Long Earth One",
			wantSeries:   "The Long Earth",
			wantPosition: "1",
			wantRule:     RuleTrailingOrdinal,
		},
		{
			name:         "trailing ordinal Two lowercase",
			input:        "the long earth two",
			wantSeries:   "the long earth",
			wantPosition: "2",
			wantRule:     RuleTrailingOrdinal,
		},
		{
			name:         "trailing Twenty",
			input:        "My Series Twenty",
			wantSeries:   "My Series",
			wantPosition: "20",
			wantRule:     RuleTrailingOrdinal,
		},

		// ── Rule 8: series equals title ──
		{
			name:       "exact series==title with no other match",
			input:      "Just A Title",
			title:      "Just A Title",
			wantSeries: "Just A Title",
			wantFlag:   true,
			wantReason: FlagNameEqualsTitle,
		},
		{
			name:       "series==title with whitespace on title triggers flag",
			input:      "Just A Title",
			title:      "  Just A Title  ",
			wantSeries: "Just A Title",
			wantFlag:   true,
			wantReason: FlagNameEqualsTitle,
		},

		// ── Junk bases: a disc/chapter tag in the series field must NOT be
		// stripped down to the bare keyword. Doing so would fold hundreds of
		// unrelated rows into one bogus series called "Chapter".
		{
			name:       "Chapter 12 is not stripped to Chapter",
			input:      "Chapter 12",
			wantSeries: "Chapter 12",
		},
		{
			name:       "Disc 3 is not stripped to Disc",
			input:      "Disc 3",
			wantSeries: "Disc 3",
		},

		// ── No-op cases ──
		{
			name:       "clean series name unchanged",
			input:      "The Expanse",
			wantSeries: "The Expanse",
		},
		{
			name:       "Discworld unchanged",
			input:      "Discworld",
			wantSeries: "Discworld",
		},
		{
			name:       "ordinal Twenty-One not matched (out of range)",
			input:      "My Series Twenty-One",
			wantSeries: "My Series Twenty-One",
		},
		{
			name:       "word Someone not matched as ordinal",
			input:      "Someone",
			wantSeries: "Someone",
		},
		{
			name:       "empty name unchanged",
			input:      "",
			wantSeries: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripSeriesContamination(tt.input, tt.title)
			if got.Name != tt.wantSeries {
				t.Errorf("series: got %q, want %q", got.Name, tt.wantSeries)
			}
			if got.Position != tt.wantPosition {
				t.Errorf("position: got %q, want %q", got.Position, tt.wantPosition)
			}
			if got.Rule != tt.wantRule {
				t.Errorf("rule: got %q, want %q", got.Rule, tt.wantRule)
			}
			if got.Flag != tt.wantFlag {
				t.Errorf("flag: got %v, want %v", got.Flag, tt.wantFlag)
			}
			if got.FlagReason != tt.wantReason {
				t.Errorf("flagReason: got %q, want %q", got.FlagReason, tt.wantReason)
			}
			// A flagged name is NEVER rewritten. This is the invariant the whole
			// un-vouched tier exists to protect, so assert it independently of
			// the per-case expectation above.
			if got.Flag && got.Name != strings.TrimSpace(tt.input) {
				t.Errorf("flagged name was rewritten: got %q, want %q", got.Name, tt.input)
			}
		})
	}
}

// TestStripSeriesContamination_FlagCarriesCandidate pins that an un-vouched flag
// reports WHAT it declined to do. A flag with no candidate is a question mark
// the owner cannot act on, and "I'll manually override" needs a review surface.
func TestStripSeriesContamination_FlagCarriesCandidate(t *testing.T) {
	got := StripSeriesContamination("08. Battle for the Abyss", "")
	if !got.Flag || got.FlagReason != FlagUnvouchedPosition {
		t.Fatalf("want un-vouched flag, got flag=%v reason=%q", got.Flag, got.FlagReason)
	}
	if got.CandidateName != "Battle for the Abyss" || got.CandidatePosition != "8" {
		t.Errorf("candidate: got %q/%q, want %q/%q",
			got.CandidateName, got.CandidatePosition, "Battle for the Abyss", "8")
	}
	// The name-equals-title flag carries no candidate: nothing was declined.
	eq := StripSeriesContamination("Just A Title", "Just A Title")
	if eq.CandidateName != "" || eq.CandidatePosition != "" {
		t.Errorf("name==title flag should carry no candidate, got %q/%q",
			eq.CandidateName, eq.CandidatePosition)
	}
}

// TestSplitSeriesPosition_HashSuffix pins the regex fix that made the owner's
// headline case work at all. `#` must live OUTSIDE the \b-prefixed keyword group.
func TestSplitSeriesPosition_HashSuffix(t *testing.T) {
	for _, in := range []string{"Nameless Sovereign #5", "Nameless Sovereign#5", "Nameless Sovereign # 5"} {
		sp, ok := SplitSeriesPosition(in)
		if !ok {
			t.Fatalf("SplitSeriesPosition(%q) did not match; `#` is back inside the \\b group", in)
		}
		if sp.Base != "Nameless Sovereign" || sp.Position != 5 || sp.Shape != ShapeTrailingKeyword {
			t.Errorf("%q: got base=%q pos=%d shape=%s", in, sp.Base, sp.Position, sp.Shape)
		}
	}
}
