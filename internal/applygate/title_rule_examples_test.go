// file: internal/applygate/title_rule_examples_test.go
// version: 1.0.0
// guid: 6d1c58a2-9e04-4b73-8f25-a3e07d94b160
// last-edited: 2026-11-17

package applygate

import (
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// By-example tests for the title rule's decisions. The property and oracle
// tests in titlecompare_test.go cover the rule in bulk; each row here is one
// decision the rule makes, named so a failure says which.

func TestCheckTitle_RuleExamples(t *testing.T) {
	series := func(n string) *database.Series { return &database.Series{Name: n} }
	for _, c := range []struct {
		name string
		book database.Book
		cand metafetch.MetadataCandidate
		want string // agree, neutral, block
	}{
		// a path form only stands in for a title that says nothing
		{"blank stored title: the file name is the book's title",
			database.Book{Title: "", FilePath: "/lib/Bruce Wayne/Hush/Hush.m4b"},
			metafetch.MetadataCandidate{Title: "Hush"}, "agree"},
		{"placeholder stored title: the file name is the book's title",
			database.Book{Title: "Unknown Title", FilePath: "/lib/Bruce Wayne/Hush/Hush.m4b"},
			metafetch.MetadataCandidate{Title: "Hush"}, "agree"},
		{"a stored title that carries text is not replaced by the file name",
			database.Book{Title: "Knightfall", FilePath: "/lib/Bruce Wayne/Hush/Hush.m4b"},
			metafetch.MetadataCandidate{Title: "Hush"}, "block"},

		// numbers are compared by kind
		{"a volume only the candidate states is not a conflict",
			database.Book{Title: "Who Am I"},
			metafetch.MetadataCandidate{Title: "Who Am I 2"}, "neutral"},
		{"two different volumes conflict",
			database.Book{Title: "Hush 1"},
			metafetch.MetadataCandidate{Title: "Hush 2"}, "block"},
		{"a part against a part conflicts",
			database.Book{Title: "Hush, Part 1 of 3"},
			metafetch.MetadataCandidate{Title: "Hush, Part 3"}, "block"},
		{"a part and a volume with the same digit are not the same number",
			database.Book{Title: "Hush, Part 2"},
			metafetch.MetadataCandidate{Title: "Hush 2"}, "neutral"},
		{"part 1 of 3 against part 3 of 3 conflicts",
			database.Book{Title: "Hush, Part 1 of 3"},
			metafetch.MetadataCandidate{Title: "Hush, Part 3 of 3"}, "block"},
		{"swapped numbers that each side contains are still a conflict",
			database.Book{Title: "Hush 1 and 2"},
			metafetch.MetadataCandidate{Title: "Hush 2 and 3"}, "block"},

		// a lone trailing I is a numeral only beside a title, not the word "I"
		{"Hush I against Hush II conflicts",
			database.Book{Title: "Hush I"},
			metafetch.MetadataCandidate{Title: "Hush II"}, "block"},
		{"Hush I against Hush 2 conflicts",
			database.Book{Title: "Hush I"},
			metafetch.MetadataCandidate{Title: "Hush 2"}, "block"},
		// the lone I is read as a numeral for the conflict test only, so equal
		// numbers are no conflict, and the words still differ ("i" and "1"), so
		// the pair is neither a conflict nor an agreement
		{"Hush I against Hush 1 is no conflict and no agreement",
			database.Book{Title: "Hush I"},
			metafetch.MetadataCandidate{Title: "Hush 1"}, "neutral"},
		{"a title that opens with I is not a numbered title",
			database.Book{Title: "I, Joker"},
			metafetch.MetadataCandidate{Title: "I, Joker"}, "agree"},

		// the candidate's subtitle is never left out of a form
		{"a bare candidate title agrees with the stored title",
			database.Book{Title: "Hush"},
			metafetch.MetadataCandidate{Title: "Hush"}, "agree"},
		{"an added subtitle on the candidate is neutral",
			database.Book{Title: "Hush"},
			metafetch.MetadataCandidate{Title: "Hush", Subtitle: "A Gotham Story"}, "neutral"},
		{"series, number and title with no subtitle agree",
			database.Book{Title: "Hush", Series: series("Batman")},
			metafetch.MetadataCandidate{Title: "Hush", Series: "Batman", SeriesPosition: "2"}, "agree"},
		{"the same candidate with a real subtitle is neutral",
			database.Book{Title: "Hush", Series: series("Batman")},
			metafetch.MetadataCandidate{Title: "Hush", Subtitle: "A Gotham Story", Series: "Batman", SeriesPosition: "2"}, "neutral"},
		{"a closed edition phrase is not a subtitle",
			database.Book{Title: "Hush"},
			metafetch.MetadataCandidate{Title: "Hush", Subtitle: "Unabridged"}, "agree"},
		{"Unknown Author is not a subtitle",
			database.Book{Title: "Hush"},
			metafetch.MetadataCandidate{Title: "Hush", Subtitle: "Unknown Author"}, "agree"},
		{"read by narrator is not a subtitle",
			database.Book{Title: "Hush"},
			metafetch.MetadataCandidate{Title: "Hush", Subtitle: "Read by Narrator"}, "agree"},
		{"the candidate's series statement is not a subtitle",
			database.Book{Title: "Hush", Series: series("Batman")},
			metafetch.MetadataCandidate{Title: "Hush", Subtitle: "Batman, Book 2", Series: "Batman", SeriesPosition: "2"}, "agree"},

		// the stored subtitle is the book's own text
		{"the stored subtitle completes the candidate's title",
			database.Book{Title: "Batman", Subtitle: new("Hush")},
			metafetch.MetadataCandidate{Title: "Batman: Hush"}, "agree"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := outcomeClass(runTitleCheck(c.book, c.cand))
			if got != c.want {
				t.Fatalf("got %s, want %s", got, c.want)
			}
		})
	}
}

// The part-marker check (partial.go) runs when the runtime agrees too, reads
// the book's own text and not its folder in that case, and a part only the
// candidate states blocks.
func TestGate_PartMarkerRules(t *testing.T) {
	for _, c := range []struct {
		name string
		book database.Book
		cand metafetch.MetadataCandidate
		want [2]string // runtime 5% off, no book runtime ("" is an apply)
	}{
		{"a part only the candidate states is refused even when the runtime agrees",
			database.Book{Title: "Hush", FilePath: "/lib/Bruce Wayne/Hush/Hush.m4b"},
			metafetch.MetadataCandidate{Title: "Hush: Part 2"},
			[2]string{ReasonPartialBook, ReasonPartialBook}},
		{"a part in the candidate's subtitle is refused the same way",
			database.Book{Title: "Hush", FilePath: "/lib/Bruce Wayne/Hush/Hush.m4b"},
			metafetch.MetadataCandidate{Title: "Hush", Subtitle: "Part 2"},
			[2]string{ReasonPartialBook, ReasonPartialBook}},
		{"a part in the stored subtitle is the book's own part marker",
			database.Book{Title: "Hush", Subtitle: new("Part 2"), FilePath: "/lib/Bruce Wayne/Hush/Hush.m4b"},
			metafetch.MetadataCandidate{Title: "Hush"},
			[2]string{ReasonPartialBook, ReasonPartialBook}},
		{"a stale disc folder does not block when the runtime agrees",
			database.Book{Title: "Hush", FilePath: "/lib/Bruce Wayne/Hush Disc 2/Hush.m4b"},
			metafetch.MetadataCandidate{Title: "Hush"},
			[2]string{"", ReasonPartialBook}},
		{"a volume only the candidate states passes with an agreeing runtime",
			database.Book{Title: "Who Am I", FilePath: "/lib/Bruce Wayne/Who Am I/Who Am I.m4b"},
			metafetch.MetadataCandidate{Title: "Who Am I 2"},
			[2]string{"", ReasonInsufficientEvidence}},
	} {
		for i, name := range []string{"runtime 5% off", "no book runtime"} {
			t.Run(c.name+"/"+name, func(t *testing.T) {
				v := step1bRun(i*2, c.book, "", c.cand)
				got := ""
				if !v.Allowed {
					got = v.Reason
				}
				if got != c.want[i] {
					t.Fatalf("reason %q, want %q (detail: %s)", got, c.want[i], v.Detail)
				}
			})
		}
	}
}
