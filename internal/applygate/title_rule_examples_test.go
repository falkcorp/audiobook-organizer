// file: internal/applygate/title_rule_examples_test.go
// version: 1.3.0
// guid: 6d1c58a2-9e04-4b73-8f25-a3e07d94b160
// last-edited: 2026-11-17

package applygate

import (
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
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
		{"a side that holds the other's part among more of its own is still a conflict",
			database.Book{Title: "Hush, Part 1 and Part 2"},
			metafetch.MetadataCandidate{Title: "Hush, Part 2"}, "block"},
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

		// a candidate with a real subtitle offers only the forms that state it:
		// the series-and-number and credit forms that leave it out would agree
		// with a stored title that never says the subtitle, and a stored title
		// the candidate's subtitle is not part of is not the candidate's title
		{"a real subtitle is not left out of the series-and-number form",
			database.Book{Title: "Batman, Book 2: Hush"},
			metafetch.MetadataCandidate{Title: "Hush", Subtitle: "A Gotham Story", Series: "Batman", SeriesPosition: "2"}, "block"},
		{"a real subtitle is not left out of the title-then-series form",
			database.Book{Title: "Hush Batman 2"},
			metafetch.MetadataCandidate{Title: "Hush", Subtitle: "A Gotham Story", Series: "Batman", SeriesPosition: "2"}, "block"},
		{"a real subtitle is not left out of the credit form",
			database.Book{Title: "Hush Alfred Pennyworth"},
			metafetch.MetadataCandidate{Title: "Hush", Subtitle: "A Gotham Story", Narrator: "Alfred Pennyworth"}, "block"},

		// a word subset: a candidate that adds words to the stored title is
		// neutral at any size; one that is a few words OF a longer stored title
		// must share half of them
		{"a candidate that adds words to the stored title is neutral",
			database.Book{Title: "Gotham"},
			metafetch.MetadataCandidate{Title: "Gotham Knights Detective Tales"}, "neutral"},
		{"a candidate that is one word of a longer stored title is another book",
			database.Book{Title: "Gotham Knights Detective Tales"},
			metafetch.MetadataCandidate{Title: "Gotham"}, "block"},
		{"a candidate that is half of the stored title's words is neutral",
			database.Book{Title: "Gotham Knights"},
			metafetch.MetadataCandidate{Title: "Gotham"}, "neutral"},
		{"a candidate that is a third of the stored title's words is another book",
			database.Book{Title: "Gotham Knights Detective"},
			metafetch.MetadataCandidate{Title: "Gotham"}, "block"},

		// a year is not a number the titles state: it never causes a
		// disagreement, whichever side carries it
		{"a year in the stored title is no disagreement with a stated volume",
			database.Book{Title: "Knightfall (1993)"},
			metafetch.MetadataCandidate{Title: "Knightfall: Book 1"}, "neutral"},
		{"two different years each side carries are the numbers that title carries",
			database.Book{Title: "Gotham 2033"},
			metafetch.MetadataCandidate{Title: "Gotham 2034"}, "block"},
		{"a bare year only the stored title carries is no disagreement with a volume",
			database.Book{Title: "Knightfall 1993"},
			metafetch.MetadataCandidate{Title: "Knightfall: Book 1"}, "neutral"},
		{"a year in the candidate's title is no disagreement with a stated volume",
			database.Book{Title: "Knightfall: Book 1"},
			metafetch.MetadataCandidate{Title: "Knightfall (1993)"}, "neutral"},

		// a leading list number is neutral, not a volume the title states
		{"a leading list number is no disagreement with a stated volume",
			database.Book{Title: "08 - The Long Halloween"},
			metafetch.MetadataCandidate{Title: "The Long Halloween: Book 2"}, "neutral"},
		{"a leading list number with a dot is no disagreement either",
			database.Book{Title: "08. The Long Halloween"},
			metafetch.MetadataCandidate{Title: "The Long Halloween: Book 2"}, "neutral"},

		// an ordinal beside a volume or part marker is that marker's number
		{"the Third Book is Book 3",
			database.Book{Title: "Hush: Book 3"},
			metafetch.MetadataCandidate{Title: "Hush: Third Book"}, "agree"},
		{"Book the Third is Book 3",
			database.Book{Title: "Hush: Book 3"},
			metafetch.MetadataCandidate{Title: "Hush: Book the Third"}, "agree"},
		{"the 3rd Volume is Volume 3",
			database.Book{Title: "Hush: Volume 3"},
			metafetch.MetadataCandidate{Title: "Hush: 3rd Volume"}, "agree"},
		{"the Second Part is Part 2",
			database.Book{Title: "Hush: Part 2"},
			metafetch.MetadataCandidate{Title: "Hush: Second Part"}, "agree"},
		{"the Fourth Book is not Book 3",
			database.Book{Title: "Hush: Book 3"},
			metafetch.MetadataCandidate{Title: "Hush: Fourth Book"}, "block"},
		{"the Second Part is not Book 2",
			database.Book{Title: "Hush: Book 2"},
			metafetch.MetadataCandidate{Title: "Hush: Second Part"}, "block"},
		{"an ordinal that is no marker's number stays a word",
			database.Book{Title: "The Second Robin"},
			metafetch.MetadataCandidate{Title: "The Third Robin"}, "neutral"},

		// a lone trailing I is a numeral beside any other volume spelling
		{"Hush I against Hush, Book 2 conflicts",
			database.Book{Title: "Hush I"},
			metafetch.MetadataCandidate{Title: "Hush, Book 2"}, "block"},
		{"Hush I against Hush #2 conflicts",
			database.Book{Title: "Hush I"},
			metafetch.MetadataCandidate{Title: "Hush #2"}, "block"},
		{"Hush I against Hush (2) conflicts",
			database.Book{Title: "Hush I"},
			metafetch.MetadataCandidate{Title: "Hush (2)"}, "block"},
		{"Hush I against Hush Volume II conflicts",
			database.Book{Title: "Hush I"},
			metafetch.MetadataCandidate{Title: "Hush Volume II"}, "block"},
		{"Hush, Book 2 against Hush I conflicts",
			database.Book{Title: "Hush, Book 2"},
			metafetch.MetadataCandidate{Title: "Hush I"}, "block"},
		{"Hush I against Hush, Book 1 is no conflict",
			database.Book{Title: "Hush I"},
			metafetch.MetadataCandidate{Title: "Hush, Book 1"}, "neutral"},
		{"the pronoun I beside a volume marker is still a word",
			database.Book{Title: "Who Am I"},
			metafetch.MetadataCandidate{Title: "Who Am I: Book 2"}, "neutral"},
		{"the pronoun I beside a hash number is still a word",
			database.Book{Title: "Who Am I"},
			metafetch.MetadataCandidate{Title: "Who Am I #2"}, "neutral"},
		{"the pronoun I beside a parenthesised number is still a word",
			database.Book{Title: "Who Am I"},
			metafetch.MetadataCandidate{Title: "Who Am I (2)"}, "neutral"},

		// a candidate's declared series and position only help a pair agree:
		// the subtitle that merely restates them is not dropped for the segment
		// key either
		{"a statement subtitle gives the segment key whatever the position says",
			database.Book{Title: "Book 2 of Batman: Hush"},
			metafetch.MetadataCandidate{Title: "Hush", Subtitle: "Book 2 of Batman", Series: "Batman"}, "agree"},
		{"the segment key ignores the statement subtitle when the segments are reordered",
			database.Book{Title: "Dark: Hush"},
			metafetch.MetadataCandidate{Title: "Hush: Dark", Subtitle: "Batman, Book 2", Series: "Batman", SeriesPosition: "2"}, "agree"},
		{"adding the position keeps that pair in agreement",
			database.Book{Title: "Book 2 of Batman: Hush"},
			metafetch.MetadataCandidate{Title: "Hush", Subtitle: "Book 2 of Batman", Series: "Batman", SeriesPosition: "2"}, "agree"},

		// a candidate that is nothing but its own series name says no volume:
		// the bare title is neutral, and a form that states the position agrees
		{"a self-titled candidate with a position is neutral against the bare series name",
			database.Book{Title: "Batman"},
			metafetch.MetadataCandidate{Title: "Batman", Series: "Batman", SeriesPosition: "4"}, "neutral"},
		{"a self-titled candidate agrees with a form that states the position",
			database.Book{Title: "Batman 4 Batman"},
			metafetch.MetadataCandidate{Title: "Batman", Series: "Batman", SeriesPosition: "4"}, "agree"},
		// The candidate's series-and-position forms spell "S N T", never "S N":
		// a stored "Batman 4" says the same thing, but no form of a self-titled
		// candidate equals it, so it stays a refusal (series name as title, with
		// words the stored title lacks). Pinned as the known recall cost.
		{"a stored series-and-position title is not a form of a self-titled candidate",
			database.Book{Title: "Batman 4"},
			metafetch.MetadataCandidate{Title: "Batman", Series: "Batman", SeriesPosition: "4"}, "block"},
		{"a self-titled candidate is neutral against a stored title in the same series",
			database.Book{Title: "Batman", Series: series("Batman")},
			metafetch.MetadataCandidate{Title: "Batman", Series: "Batman", SeriesPosition: "4"}, "neutral"},
		{"a self-titled candidate with no declared series agrees as before",
			database.Book{Title: "Batman"},
			metafetch.MetadataCandidate{Title: "Batman"}, "agree"},
		// the same segments in another order are the same bare claim
		{"a self-titled candidate with the same segments in another order is neutral",
			database.Book{Title: "Batman: Dark Victory"},
			metafetch.MetadataCandidate{Title: "Dark Victory: Batman", Series: "Dark Victory: Batman"}, "neutral"},
		// a real subtitle is no bare series name: the exception does not apply
		{"a candidate with a real subtitle is not self-titled and keeps the series-name block",
			database.Book{Title: "Batman"},
			metafetch.MetadataCandidate{Title: "Batman", Subtitle: "Dark Victory", Series: "Batman"}, "block"},

		// a path form stands in only for a title that says nothing: a bare
		// trailing capital letter ("Robin X") is a word, not a part token
		{"a trailing capital letter is a word of the stored title, not a placeholder",
			database.Book{Title: "Robin X", FilePath: "/lib/Bruce Wayne/Robin/Robin X.m4b"},
			metafetch.MetadataCandidate{Title: "Robin"}, "neutral"},
		{"a trailing digit stem is still a placeholder the folder may stand in for",
			database.Book{Title: "Hush 1", FilePath: "/lib/Bruce Wayne/Hush/Hush.m4b"},
			metafetch.MetadataCandidate{Title: "Hush"}, "agree"},

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
		// B1: a candidate that is itself a part is compared with the book's
		// whole side, the folder included: a stale folder marker is the best
		// evidence of which part the files are once the candidate names one
		{"B1a: a part only the folder states is a different part when the candidate's subtitle names another",
			database.Book{Title: "Hush", FilePath: "/lib/Bruce Wayne/Hush Part 1/Hush.m4b"},
			metafetch.MetadataCandidate{Title: "Hush", Subtitle: "Part 2"},
			[2]string{ReasonPartialBook, ReasonPartialBook}},
		{"B1b: a part only a folder book's own name states is a different part when the candidate's title names another",
			database.Book{Title: "Hush", FilePath: "/lib/Bruce Wayne/Hush - Part 1"},
			metafetch.MetadataCandidate{Title: "Hush: Part 2"},
			[2]string{ReasonPartialBook, ReasonPartialBook}},
		{"B1c: the same folder with a whole-book candidate is a stale marker and applies on an agreeing runtime",
			database.Book{Title: "Hush", FilePath: "/lib/Bruce Wayne/Hush Part 1/Hush.m4b"},
			metafetch.MetadataCandidate{Title: "Hush"},
			[2]string{"", ReasonPartialBook}},
		{"B1d: the folder's part is the candidate's part",
			database.Book{Title: "Hush", FilePath: "/lib/Bruce Wayne/Hush Part 2/Hush.m4b"},
			metafetch.MetadataCandidate{Title: "Hush", Subtitle: "Part 2"},
			[2]string{"", ReasonInsufficientEvidence}},
		{"B1e: the title's own part decides before a stale folder's",
			database.Book{Title: "Hush, Part 2", FilePath: "/lib/Bruce Wayne/Hush Part 1/Hush.m4b"},
			metafetch.MetadataCandidate{Title: "Hush", Subtitle: "Part 2"},
			[2]string{"", ""}},

		// m3: a stale part marker in the stored title or the single file's own
		// name refuses a whole-book candidate whose runtime agrees. Rule
		// change, kept and disclosed in the changelog (partial_book is not
		// lifted by an owner review).
		{"m3: a part marker in the stored title refuses a whole-book candidate on an agreeing runtime",
			database.Book{Title: "Hush CD1", FilePath: "/lib/Bruce Wayne/Hush/Hush.m4b"},
			metafetch.MetadataCandidate{Title: "Hush"},
			[2]string{ReasonPartialBook, ReasonRuntimeUnknownOverwrite}},
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

// storedTitleSaysNothing is storedTitleCarriesEvidence's negation except for
// one shape: a bare trailing capital letter ("Robin X", "Plan B"). The two are
// kept apart on purpose (the first lets a folder name stand in for a title, the
// second keeps a title conflict out of a transcription's reach), so this pins
// that the shape is the ONLY difference.
func TestStoredTitleSaysNothing_DiffersOnlyForTheCapitalLetterShape(t *testing.T) {
	letterShape := func(title string) bool {
		_, tok, ok := metadata.SiblingPartStem(title)
		return ok && tok != "" && tok[0] >= 'A' && tok[0] <= 'Z'
	}
	differ := 0
	for _, title := range []string{
		"", "   ", "Chapter 3", "Track 07", "Part 1 of 3", "Disc 2", "The Sunrise Lands 1", "Hush 1",
		"Hush [64kbps]", "Hush (Unabridged)", "Hush", "Hush: Part 2", "Hush: Dark Victory", "Who Am I",
		"Robin X", "Plan B", "Batman 4", "Gotham 2033", "08 - The Long Halloween", "Hush CD1",
	} {
		says, carries := storedTitleSaysNothing(title), storedTitleCarriesEvidence(title)
		wantDiffer := !carries && letterShape(title)
		if (says == carries) != wantDiffer {
			t.Errorf("%q: saysNothing=%v carriesEvidence=%v, letter shape %v: the two may differ only for the letter shape",
				title, says, carries, letterShape(title))
		}
		if says == carries { // not each other's negation
			differ++
		}
	}
	if differ != 3 {
		t.Fatalf("%d titles are not each other's negation, want exactly the three letter-shape rows (Who Am I, Robin X, Plan B)", differ)
	}
}
