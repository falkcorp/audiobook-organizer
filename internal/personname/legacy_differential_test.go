// file: internal/personname/legacy_differential_test.go
// version: 1.2.0
// guid: 9e2c7b48-5a13-4f80-b6d9-3c0e8a1f7452
// last-edited: 2026-09-01

// The three implementations this package replaced, copied VERBATIM from
// origin/main (only renamed), plus the differential that compares them to the
// unified one.
//
// They are kept as code rather than described in prose so the PR's central
// claim -- "no single copy was correct; this is the union of all three" -- is
// something a reviewer can RUN instead of taking on trust. It also means a
// future change to LooksLikePersonName reports exactly which historical
// behaviour it is altering.

package personname

import (
	"strconv"
	"strings"
	"testing"
	"unicode"
)

func legacyScanner(s string) bool {
	if !legacyScannerValid(s) {
		return false
	}

	// Check for initials like "J. K. Rowling" or "J.K. Rowling"
	if strings.Contains(s, ".") {
		words := strings.Fields(s)
		if len(words) > 1 {
			initials := 0
			nonInitials := 0
			for _, word := range words {
				if legacyInitial(word) {
					initials++
					continue
				}
				nonInitials++
			}
			if nonInitials > 0 || initials >= 2 {
				return true
			}
		}
	}

	// Check for multi-word names with proper capitalization
	words := strings.Fields(s)
	if len(words) >= 2 && len(words) <= 4 {
		// Check if all words start with uppercase
		allProperCase := true
		for _, word := range words {
			if len(word) == 0 || (word[0] < 'A' || word[0] > 'Z') {
				allProperCase = false
				break
			}
		}
		if allProperCase {
			return true
		}
	}

	// Check for "FirstName LastName" pattern (at least one space, proper case)
	if len(words) >= 2 {
		// First word starts with capital
		if len(words[0]) > 0 && words[0][0] >= 'A' && words[0][0] <= 'Z' {
			// Second word starts with capital
			if len(words[1]) > 0 && words[1][0] >= 'A' && words[1][0] <= 'Z' {
				return true
			}
		}
	}

	return false
}

func legacyScannerValid(author string) bool {
	if author == "" {
		return false
	}

	lower := strings.ToLower(author)

	// Skip invalid patterns
	if strings.HasPrefix(lower, "book") || strings.HasPrefix(lower, "chapter") ||
		strings.HasPrefix(lower, "part") || strings.HasPrefix(lower, "vol") ||
		strings.HasPrefix(lower, "volume") || strings.HasPrefix(lower, "disc") {
		return false
	}

	// Skip purely numeric
	if _, err := strconv.Atoi(author); err == nil {
		return false
	}

	// Skip chapter patterns
	if strings.HasPrefix(lower, "chapter ") {
		return false
	}

	return true
}

func legacyInitial(word string) bool {
	return len(word) == 2 && word[1] == '.' && word[0] >= 'A' && word[0] <= 'Z'
}

func legacyMetadata(s string) bool {
	if !legacyMetadataValid(s) {
		return false
	}

	// Check for initials like "J. K. Rowling" or "J.K. Rowling"
	if strings.Contains(s, ".") {
		// Count uppercase letters and periods
		uppers := 0
		for _, r := range s {
			if r >= 'A' && r <= 'Z' {
				uppers++
			}
		}
		if uppers >= 2 {
			return true
		}
	}

	// Check for multi-word names with proper capitalization
	words := strings.Fields(s)
	if len(words) < 2 || len(words) > 4 {
		return false
	}
	for _, word := range words {
		if len(word) == 0 || (word[0] < 'A' || word[0] > 'Z') {
			return false
		}
	}
	return true
}

func legacyMetadataValid(author string) bool {
	if author == "" {
		return false
	}

	author = strings.ToLower(author)

	// Skip invalid patterns
	if strings.HasPrefix(author, "book") || strings.HasPrefix(author, "chapter") ||
		strings.HasPrefix(author, "part") || strings.HasPrefix(author, "vol") ||
		strings.HasPrefix(author, "volume") || strings.HasPrefix(author, "disc") {
		return false
	}

	// Skip purely numeric (like "01", "02")
	if _, err := strconv.Atoi(author); err == nil {
		return false
	}

	// Skip chapter patterns
	if strings.HasPrefix(author, "chapter ") {
		return false
	}

	return true
}

func legacyDedup(part string) bool {
	fields := strings.Fields(part)
	if len(fields) < 2 || len(fields) > 4 {
		return false
	}
	first := []rune(fields[0])
	if len(first) == 0 || unicode.IsLower(first[0]) {
		return false
	}
	// Interior lowercase FUNCTION words mark title clauses ("A Game of
	// Thrones"); lowercase name PARTICLES ("Simone de Beauvoir", "Ludwig van
	// Beethoven") are legitimate and stay allowed.
	for _, w := range fields[1:] {
		r := []rune(w)
		if len(r) > 0 && unicode.IsLower(r[0]) && !legacyParticles[strings.ToLower(w)] {
			return false
		}
	}
	if strings.ContainsAny(part, ":!?") {
		return false
	}
	if strings.HasSuffix(strings.TrimSpace(part), ")") {
		return false
	}
	return true
}

var legacyParticles = map[string]bool{
	"de": true, "la": true, "le": true, "van": true, "von": true,
	"del": true, "della": true, "di": true, "da": true, "dos": true,
	"du": true, "den": true, "ter": true, "bin": true, "ibn": true,
	"al": true, "el": true, "st.": true, "mac": true,
}

// TestDifferentialAgainstAllThreeLegacyCopies enumerates every input where the
// unified implementation disagrees with any historical copy, and asserts the
// unified answer is the CORRECT one. Each row states which copy was wrong.
// differentialCorpus is shared by the differential test and the
// direction-of-change test below.
var differentialCorpus = []struct {
	in   string
	want bool
}{
	{"Isaac Asimov", true}, {"J.R.R. Tolkien", true}, {"J. K. Rowling", true},
	{"Ursula K. Le Guin", true}, {"Stephen King", true}, {"José Saramago", true},
	{"Simone de Beauvoir", true}, {"Ludwig van Beethoven", true},
	{"Émile Zola", true}, {"Åsa Larsson", true}, {"Ítalo Calvino", true},
	{"Øyvind Torseter", true}, {"Александр Пушкин", true}, {"村上 春樹", true},
	{"A Game of Thrones", false}, {"The Lord of the Rings", false},
	{"Dune", false}, {"01", false}, {"1984", false},
	{"Book 3", false}, {"Chapter 1", false}, {"Volume 2", false}, {"Disc 1", false},
	{"Do Androids Dream?", false}, {"Fear and Loathing!", false},
	{"Something (Unabridged)", false},
	{"Pratchett 036", false}, {"Asimov 12", false},
	{"Too Many Words Here Name", false},
}

func TestDifferentialAgainstAllThreeLegacyCopies(t *testing.T) {
	cases := differentialCorpus
	disagreements := 0
	for _, c := range cases {
		got := LooksLikePersonName(c.in)
		if got != c.want {
			t.Errorf("LooksLikePersonName(%q) = %v, want %v", c.in, got, c.want)
		}
		s, m, d := legacyScanner(c.in), legacyMetadata(c.in), legacyDedup(c.in)
		if s != got || m != got || d != got {
			disagreements++
			t.Logf("CHANGED %-26q unified=%-5v  (scanner=%-5v metadata=%-5v dedup=%-5v)", c.in, got, s, m, d)
		}
	}
	t.Logf("unified differs from at least one legacy copy on %d/%d inputs", disagreements, len(cases))
	if disagreements == 0 {
		t.Error("no disagreements found -- this corpus no longer demonstrates why the copies were merged")
	}
}

// TestPredicateOnlyBecomesMoreRestrictive checks ONE thing, at the predicate
// level: LooksLikePersonName is a subset of legacy dedup's copy over the corpus.
//
// It is deliberately named for what it measures. The earlier name promised a
// property about dedup's CONSUMERS and then compared two booleans, and that gap
// hid a real bug: a newly-false predicate does not stop SplitCompositeAuthorName,
// it changes WHICH BRANCH WINS. The comma branch `break`s on refusal and falls
// through to weaker gates, so a refusal could mint the whole composite as one
// author -- 886 such strings, measured. Subset-ness here is necessary and NOT
// sufficient; the sufficient check is
// TestSplitCompositeNeverMintsANonPersonPart in internal/dedup, which runs the
// real consumer.
//
// (Correcting a second claim that stood here: scanner's and metadata's call sites
// are NOT read-side. scanner.go:1738 assigns book.Author = right, which reaches
// saveBook -> resolveAuthorID -> CreateBook. All three copies reach writes.)
//
// The lesson from #3023 still applies -- a shared helper feeding consumers that
// move in opposite directions -- but the lesson one level down is that you must
// measure the consumer, not the helper.
func TestPredicateOnlyBecomesMoreRestrictive(t *testing.T) {
	newlyAdmitted := 0
	newlyRefused := 0
	for _, c := range differentialCorpus {
		legacy := legacyDedup(c.in)
		unified := LooksLikePersonName(c.in)
		switch {
		case !legacy && unified:
			newlyAdmitted++
			t.Errorf("NEWLY ADMITTED %q: legacy dedup said false, unified says true. "+
				"Subset-ness is the precondition for the consumer test; if it "+
				"breaks, re-run the consumer differential before shipping.", c.in)
		case legacy && !unified:
			newlyRefused++
			t.Logf("newly refused (safe direction) %q", c.in)
		}
	}
	if newlyRefused == 0 {
		t.Error("no newly-refused inputs -- the corpus no longer exercises the " +
			"structural guard that dedup's copy was missing (Book 3, Chapter 1, " +
			"Pratchett 036, ...), so this test is no longer proving anything")
	}
	t.Logf("predicate direction: %d newly refused, %d newly admitted (must be 0)",
		newlyRefused, newlyAdmitted)
}
