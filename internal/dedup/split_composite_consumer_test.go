// file: internal/dedup/split_composite_consumer_test.go
// version: 1.5.0
// guid: 3f7c1a94-8d02-4e6b-b5a1-2c9e07f4d813
// last-edited: 2026-09-25

package dedup

import (
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/personname"
)

// TestSplitCompositeNeverMintsANonPersonPart is the CONSUMER-level check that
// internal/personname's predicate-level subset test is not.
//
// Why this file exists: unifying the three person-name copies was shipped with a
// safety claim measured on the predicate -- "unified only ever goes true->false,
// so it can never mint an author the deployed code would not have". The premise
// was true and the conclusion was wrong. SplitCompositeAuthorName's comma branch
// `break`s on refusal instead of returning, so a newly-FALSE predicate does not
// stop the split, it hands it to a weaker branch. Review measured 886 author
// strings newly minted that way.
//
// The invariant below is the thing that actually protects author rows, and it is
// stated over the function callers reach, not over a helper it happens to use.
func TestSplitCompositeNeverMintsANonPersonPart(t *testing.T) {
	firsts := []string{
		"Volker", "Volney", "Volodymyr", "Voltaire", "Booker", "Partha",
		"Partridge", "Bookbinder", "Discworld", "Jane", "Ida", "Ursula",
	}
	lasts := []string{"Kutscher", "Wells", "Smith", "Chatterjee", "Le Guin", "Jones"}
	// "/" is in this list because leaving it out is exactly how the slash branch
	// -- the FIRST branch SplitCompositeAuthorName tries -- stayed ungated while
	// a comment two files away claimed every branch was gated.
	seps := []string{", ", "; ", " & ", " and ", ", and ", " (", " / "}

	var corpus []string
	for _, f1 := range firsts {
		for _, l1 := range lasts {
			for _, l2 := range lasts {
				a, b := f1+" "+l1, "Bob "+l2
				for _, s := range seps {
					c := a + s + b
					if s == " (" {
						c += ")"
					}
					corpus = append(corpus, c)
				}
			}
		}
	}
	// Shapes that historically laundered a title fragment into an author name.
	corpus = append(corpus,
		"Ann Petry (DBY), Ida Wells; Bob Jones",
		"One Two Three Four Five, Ida Wells; Bob Jones",
		"the quick brown, Ida Wells; Bob Jones",
		"Book 3, Ida Wells; Bob Jones",
		"So Long, and Thanks for All the Fish; Bob Jones",
		"So Long, and Thanks for All the Fish",
		"Do Androids Dream?, Ida Wells; Bob Jones",
		"Smith, John; Doe, Jane",
		"R.A. Mejia Charles Dean",
		// Bracket branch specifically. It runs BEFORE the semicolon and "and"
		// branches, so a comma-free string with a parenthetical reaches it first.
		// Without these, ungating the bracket branch back to
		// `len(x) > 2 && strings.Contains(x, " ")` survives the whole suite.
		"the quick brown (Bob Jones Smith)",
		"One Two Three Four Five (Bob Jones)",
		"Do Androids Dream? (Bob Jones)",
		"So Long and Thanks (for All the Fish)",
		// Slash branch. Its only gate was `len(p) > 2` -- no space required.
		"Book 3 / Ida Wells",
		"Chapter 1 / Chapter 2",
		"the quick brown / Ida Wells",
		"Ann Petry (DBY) / Ida Wells",
		"Unabridged / Ida Wells",
		// Particle-as-surname: admitting "Ludwig van" unlocks a 3-way split that
		// outscores the correct 2-way one.
		"Ludwig van Beethoven Wolfgang Amadeus Mozart",
		"Vincent van Gogh Pablo Diego Picasso",
		"Simone de Beauvoir Jean Paul Sartre",
		"Jane St Clair Wolfgang Amadeus Mozart",
		"Klaus Zu Guttenberg Wolfgang Amadeus Mozart",
		"Volker Le Guin Wolfgang Amadeus Mozart",
		// Mononym slash pairs. origin/main split these, but the slash branch was
		// the ONLY separator on main that accepted mononym pairs -- comma,
		// semicolon, and, & and bracket all returned nil for "Homer, Virgil" and
		// friends. Gating slash brings the outlier into line, and stops
		// "Discworld / Mort" being filed as two authors at the same time.
		"Homer / Virgil", "Discworld / Mort",
	)

	bad := 0
	for _, in := range corpus {
		for _, part := range SplitCompositeAuthorName(in) {
			if personname.LooksLikePersonName(part) {
				continue
			}
			bad++
			if bad <= 10 {
				t.Errorf("SplitCompositeAuthorName(%q) minted a non-person part %q.\n"+
					"Every branch of the splitter must gate on the same shared "+
					"predicate; a branch that asks only for a space re-admits exactly "+
					"the title fragments C414 removed from the comma branch.", in, part)
			}
		}
	}
	if bad > 10 {
		t.Errorf("... and %d more non-person parts", bad-10)
	}
	t.Logf("checked %d composites, %d non-person parts minted", len(corpus), bad)
}

// TestSplitCompositeRefusesRatherThanLaunders pins the specific strings the
// ungated semicolon and bracket branches used to mint. Refusing leaves the
// composite VISIBLY wrong for repair, which is what author.go's C414 comment
// says the design intent is -- until 2026-09-01 that comment described an
// intent the code did not implement.
func TestSplitCompositeRefusesRatherThanLaunders(t *testing.T) {
	for _, in := range []string{
		"Ann Petry (DBY), Ida Wells; Bob Jones",
		"One Two Three Four Five, Ida Wells; Bob Jones",
		"the quick brown, Ida Wells; Bob Jones",
		"Book 3, Ida Wells; Bob Jones",
		"So Long, and Thanks for All the Fish; Bob Jones",
		"the quick brown (Bob Jones Smith)",
		"One Two Three Four Five (Bob Jones)",
		"Do Androids Dream? (Bob Jones)",
		"So Long and Thanks (for All the Fish)",
		"Book 3 / Ida Wells",
		"the quick brown / Ida Wells",
		"Ann Petry (DBY) / Ida Wells",
		"Unabridged / Ida Wells",
	} {
		if got := SplitCompositeAuthorName(in); got != nil {
			t.Errorf("SplitCompositeAuthorName(%q) = %q; want nil (refuse, do not launder)", in, got)
		}
	}
}

// TestSplitCompositeRefusesSpaceJoinedRuns pins the removal of the word-run
// splitter (2026-09-25). A space-joined run carries no separator, and person
// shape cannot tell "Charles Dean" from "Three Worlds", so every one of these
// stays a single string -- including the real two-person credits the heuristic
// used to get right. The cost is deliberate; see the note in
// splitCompositeAuthorName.
func TestSplitCompositeRefusesSpaceJoinedRuns(t *testing.T) {
	for _, in := range []string{
		"Wraith Knight Three Worlds",
		"A Memory Called Empire",
		"Joseph Sheridan Le Fanu",
		"Ch02 The Kasari Nexus",
		"Logan Jacobs Backyard Dungeon",
		"R.A. Mejia Charles Dean",
		"John Smith Jane Doe",
		"Ludwig van Beethoven Wolfgang Amadeus Mozart",
		"Émile Zola Wolfgang Amadeus Mozart",
		"村上 春樹 Wolfgang Amadeus Mozart",
	} {
		if got := SplitCompositeAuthorName(in); len(got) > 1 {
			t.Errorf("SplitCompositeAuthorName(%q) = %q; want no split", in, got)
		}
	}
}

// TestSplitCompositeRefusesAPartThatFailsTheCreationGate: the split ops create a
// row per part, so no branch may return a part the author creation gate refuses.
func TestSplitCompositeRefusesAPartThatFailsTheCreationGate(t *testing.T) {
	for _, in := range []string{
		"Jane Smith; Read by Bob Jones",
		"Jane Smith / 14 BBY",
		"Jane Smith, Book 1 (Unabridged)",
		"Jane Smith and Various Artists",
	} {
		if got := SplitCompositeAuthorName(in); len(got) > 1 {
			t.Errorf("SplitCompositeAuthorName(%q) = %q; want no split", in, got)
		}
	}
}

// TestSplitCompositeStillSplitsLegitimateComposites is the known-good twin: a
// guard that refuses everything would pass the two tests above. These must split.
func TestSplitCompositeStillSplitsLegitimateComposites(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Jane Smith, Bob Jones", "Jane Smith|Bob Jones"},
		{"Jane Smith; Bob Jones", "Jane Smith|Bob Jones"},
		{"Jane Smith and Bob Jones", "Jane Smith|Bob Jones"},
		{"Jane Smith (Bob Jones)", "Jane Smith|Bob Jones"},
		{"Ursula Le Guin; Haruki Murakami", "Ursula Le Guin|Haruki Murakami"},
		// Real surnames that a bare-prefix structural test rejected.
		{"Volker Kutscher, Niall Sellar", "Volker Kutscher|Niall Sellar"},
		{"Partha Chatterjee, Gayatri Spivak", "Partha Chatterjee|Gayatri Spivak"},
		{"Booker T. Washington; Ida Wells", "Booker T. Washington|Ida Wells"},
		// Last-first with semicolons: the case that rules out simply turning the
		// comma branch's `break` into `return nil`.
		{"Smith, John; Doe, Jane", "Smith, John|Doe, Jane"},
		{"Jane Smith / Bob Jones", "Jane Smith|Bob Jones"},
		{"Émile Zola; Wolfgang Amadeus Mozart", "Émile Zola|Wolfgang Amadeus Mozart"},
		{"Simone de Beauvoir, Jean Paul Sartre", "Simone de Beauvoir|Jean Paul Sartre"},
	} {
		got := strings.Join(SplitCompositeAuthorName(tc.in), "|")
		if got != tc.want {
			t.Errorf("SplitCompositeAuthorName(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}
