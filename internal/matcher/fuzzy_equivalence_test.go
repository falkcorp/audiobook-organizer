// file: internal/matcher/fuzzy_equivalence_test.go
// version: 1.0.0
// guid: e0ff10db-d313-422f-b511-cc1598ccdccf
// last-edited: 2026-10-30

package matcher

import (
	"math/rand"
	"sort"
	"strings"
	"testing"
)

// This file keeps the PRE-optimization implementations (allocating
// Levenshtein, string-based ScoreMatch / TokenSetRatio) as reference code and
// checks the shipped allocation-free and precomputed versions against them on
// random input. The references live here, in the test file, deliberately: they
// are the oracle, not part of the package.

func refLevenshteinCaseSensitive(a, b string) int {
	ra := []rune(a)
	rb := []rune(b)
	la, lb := len(ra), len(rb)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	prev := make([]int, lb+1)
	curr := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			curr[j] = min(curr[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[lb]
}

func refLevenshtein(a, b string) int {
	return refLevenshteinCaseSensitive(strings.ToLower(a), strings.ToLower(b))
}

func refScoreMatch(query, target string) int {
	if query == "" || target == "" {
		return 0
	}
	q := normalize(query)
	t := normalize(target)
	if q == "" || t == "" {
		return 0
	}
	if q == t {
		return 100
	}
	score := 0
	if strings.HasPrefix(t, q) {
		score = max(score, 90)
	}
	if strings.Contains(t, q) {
		ratio := float64(len([]rune(q))) / float64(len([]rune(t)))
		substringScore := 60 + int(ratio*25)
		score = max(score, substringScore)
	}
	words := strings.Fields(t)
	for _, w := range words {
		if strings.HasPrefix(w, q) {
			score = max(score, 80)
			break
		}
	}
	dist := refLevenshtein(q, t)
	maxLen := max(len([]rune(q)), len([]rune(t)))
	if maxLen > 0 {
		similarity := 1.0 - float64(dist)/float64(maxLen)
		fuzzyScore := max(int(similarity*50), 0)
		score = max(score, fuzzyScore)
	}
	for _, w := range words {
		dist := refLevenshtein(q, w)
		wLen := max(len([]rune(q)), len([]rune(w)))
		if wLen > 0 {
			similarity := 1.0 - float64(dist)/float64(wLen)
			wordScore := max(int(similarity*70), 0)
			score = max(score, wordScore)
		}
	}
	if tokenScore := int(refTokenSetRatio(query, target) * tokenSetScale); tokenScore > score {
		score = tokenScore
	}
	return score
}

func refTokenSetRatio(a, b string) float64 {
	fieldsA := strings.Fields(normalize(a))
	fieldsB := strings.Fields(normalize(b))
	if len(fieldsA) == 0 || len(fieldsB) == 0 {
		return 0
	}
	setA := make(map[string]struct{}, len(fieldsA))
	for _, tok := range fieldsA {
		setA[tok] = struct{}{}
	}
	setB := make(map[string]struct{}, len(fieldsB))
	for _, tok := range fieldsB {
		setB[tok] = struct{}{}
	}
	var inter, onlyA, onlyB []string
	for tok := range setA {
		if _, ok := setB[tok]; ok {
			inter = append(inter, tok)
		} else {
			onlyA = append(onlyA, tok)
		}
	}
	for tok := range setB {
		if _, ok := setA[tok]; !ok {
			onlyB = append(onlyB, tok)
		}
	}
	sort.Strings(inter)
	sort.Strings(onlyA)
	sort.Strings(onlyB)
	t0 := strings.Join(inter, " ")
	t1 := strings.TrimSpace(t0 + " " + strings.Join(onlyA, " "))
	t2 := strings.TrimSpace(t0 + " " + strings.Join(onlyB, " "))
	return max(refTokenSimilarity(t0, t1), refTokenSimilarity(t0, t2), refTokenSimilarity(t1, t2))
}

func refTokenSimilarity(a, b string) float64 {
	if a == "" && b == "" {
		return 1.0
	}
	maxLen := max(len([]rune(a)), len([]rune(b)))
	if maxLen == 0 {
		return 0
	}
	return 1.0 - float64(refLevenshtein(a, b))/float64(maxLen)
}

// equivalenceAlphabet mixes everything that has bitten the Levenshtein code:
// upper and lower case, digits, punctuation the normalizer strips, accented
// Latin, Greek (final sigma), Cyrillic, CJK, dotted capital I, and raw bytes
// that are not valid UTF-8.
var equivalenceAlphabet = []string{
	"a", "b", "c", "d", "e", "h", "k", "n", "o", "r", "s", "t", "u", "w", "y",
	"A", "B", "H", "K", "N", "O", "T", "W", "Y",
	" ", " ", " ", "  ", "-", ":", ".", "'", "1", "2", "3",
	"é", "É", "ö", "ß", "İ", "Σ", "σ", "ς", "Ж", "ж", "東", "京",
	"\xff", "\xc3", "\xe6\x9d",
}

func randomString(rng *rand.Rand, maxLen int) string {
	n := rng.Intn(maxLen + 1)
	var sb strings.Builder
	for i := 0; i < n; i++ {
		sb.WriteString(equivalenceAlphabet[rng.Intn(len(equivalenceAlphabet))])
	}
	return sb.String()
}

// batmanWords build related pairs, where scores land between 0 and 100 rather
// than all collapsing to the unrelated floor.
var batmanWords = []string{
	"Batman", "Year", "One", "Long", "Halloween", "Hush", "Knightfall", "Court",
	"of", "Owls", "The", "Dark", "Victory", "Killing", "Joke", "Gotham", "Gothic",
	"Robin", "Nightwing", "Batgirl", "Azrael", "Bane", "Ra's", "Penguin",
}

func randomTitle(rng *rand.Rand) string {
	n := 1 + rng.Intn(5)
	parts := make([]string, n)
	for i := range parts {
		parts[i] = batmanWords[rng.Intn(len(batmanWords))]
	}
	sep := []string{" ", " - ", ": ", "  "}[rng.Intn(4)]
	return strings.Join(parts, sep)
}

// relatedTitle perturbs title: reorder, truncate, extend, retype a letter, or
// change case, so the pair exercises the prefix / substring / word-start /
// token-set tiers and not just the fuzzy floor.
func relatedTitle(rng *rand.Rand, title string) string {
	words := strings.Fields(title)
	switch rng.Intn(6) {
	case 0:
		rng.Shuffle(len(words), func(i, j int) { words[i], words[j] = words[j], words[i] })
		return strings.Join(words, " ")
	case 1:
		if len(words) > 1 {
			return strings.Join(words[:1+rng.Intn(len(words)-1)], " ")
		}
	case 2:
		return title + " " + batmanWords[rng.Intn(len(batmanWords))]
	case 3:
		r := []rune(title)
		if len(r) > 0 {
			r[rng.Intn(len(r))] = rune('a' + rng.Intn(26))
		}
		return string(r)
	case 4:
		return strings.ToUpper(title)
	}
	return strings.ToLower(title) + "."
}

// equivalencePairs yields n deterministic pairs: random-alphabet noise,
// unrelated titles and perturbed titles.
func equivalencePairs(seed int64, n int, each func(a, b string)) {
	rng := rand.New(rand.NewSource(seed))
	for i := 0; i < n; i++ {
		switch i % 3 {
		case 0:
			each(randomString(rng, 24), randomString(rng, 24))
		case 1:
			each(randomTitle(rng), randomTitle(rng))
		default:
			a := randomTitle(rng)
			each(a, relatedTitle(rng, a))
		}
	}
}

// TestLevenshteinMatchesReference is the property test for the pooled,
// allocation-free distance: it must equal the previous implementation on
// random mixed-case, non-ASCII and invalid-UTF-8 input, in both the folding
// and the case-preserving flavour, and in both argument orders. The pool
// recycles buffers between iterations, so a stale-state bug shows up as a
// mismatch here.
func TestLevenshteinMatchesReference(t *testing.T) {
	equivalencePairs(20261030, 30000, func(a, b string) {
		if got, want := LevenshteinDistance(a, b), refLevenshtein(a, b); got != want {
			t.Fatalf("LevenshteinDistance(%q, %q) = %d, reference %d", a, b, got, want)
		}
		if got, want := LevenshteinDistanceCaseSensitive(a, b), refLevenshteinCaseSensitive(a, b); got != want {
			t.Fatalf("LevenshteinDistanceCaseSensitive(%q, %q) = %d, reference %d", a, b, got, want)
		}
		if LevenshteinDistance(a, b) != LevenshteinDistance(b, a) {
			t.Fatalf("LevenshteinDistance(%q, %q) is not symmetric", a, b)
		}
	})
}

// TestSymmetricScoreMatchesScoreMatch is the property test for the
// precomputed scorer: SymmetricScore over Prepared forms must equal
// max(ScoreMatch(a,b), ScoreMatch(b,a)) as the PRE-optimization ScoreMatch
// computed it, and the token-set ratio inside it must equal the reference
// TokenSetRatio bit for bit. One Scratch is reused across all pairs so a buffer that is not
// reset between calls is caught.
func TestSymmetricScoreMatchesScoreMatch(t *testing.T) {
	var sc Scratch
	nonTrivial := 0
	equivalencePairs(20261031, 30000, func(a, b string) {
		want := max(refScoreMatch(a, b), refScoreMatch(b, a))
		pa, pb := Prepare(a), Prepare(b)
		got := SymmetricScore(&pa, &pb, &sc)
		if got != want {
			t.Fatalf("SymmetricScore(%q, %q) = %d, max of reference ScoreMatch = %d", a, b, got, want)
		}
		if rev := SymmetricScore(&pb, &pa, &sc); rev != got {
			t.Fatalf("SymmetricScore(%q, %q) = %d but reversed = %d", a, b, got, rev)
		}
		if got, want := tokenSetRatioPrepared(&pa, &pb, &sc), refTokenSetRatio(a, b); got != want {
			t.Fatalf("token-set ratio of (%q, %q) = %v, reference %v", a, b, got, want)
		}
		if want > 0 && want < 100 {
			nonTrivial++
		}
	})
	if nonTrivial < 5000 {
		t.Fatalf("only %d of 30000 pairs scored strictly between 0 and 100; the generator is not exercising the scorer", nonTrivial)
	}
}

// TestSymmetricScoreEdgeCases pins the degenerate inputs by name.
func TestSymmetricScoreEdgeCases(t *testing.T) {
	var sc Scratch
	cases := []struct{ a, b string }{
		{"", ""}, {"", "Batman"}, {"Batman", ""}, {"!!!", "Batman"}, {"!!!", "???"},
		{"Batman", "batman"}, {"Batman", "BATMAN!"}, {"Batman", "Batman Year One"},
		{"Year One", "Batman Year One"}, {"Hush Hush", "Hush"}, {"a", "a a a a"},
		{"Batman: Year One", "Year One - Batman"},
	}
	for _, c := range cases {
		want := max(refScoreMatch(c.a, c.b), refScoreMatch(c.b, c.a))
		pa, pb := Prepare(c.a), Prepare(c.b)
		if got := SymmetricScore(&pa, &pb, &sc); got != want {
			t.Errorf("SymmetricScore(%q, %q) = %d, want %d", c.a, c.b, got, want)
		}
	}
}

// TestPreparedIsImmutableAcrossScorers checks the concurrency contract: many
// goroutines scoring the same Prepared values with their own Scratch get the
// same answers as a serial pass. Run under -race it also proves no scorer
// writes to a Prepared.
func TestPreparedIsImmutableAcrossScorers(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	prep := make([]Prepared, 64)
	for i := range prep {
		prep[i] = Prepare(randomTitle(rng))
	}
	score := func() []int {
		sc := GetScratch()
		defer PutScratch(sc)
		out := make([]int, 0, len(prep)*len(prep))
		for i := range prep {
			for j := range prep {
				out = append(out, SymmetricScore(&prep[i], &prep[j], sc))
			}
		}
		return out
	}
	want := score()
	results := make(chan []int, 4)
	for range 4 {
		go func() { results <- score() }()
	}
	for range 4 {
		got := <-results
		for k := range want {
			if got[k] != want[k] {
				t.Fatalf("concurrent score[%d] = %d, serial %d", k, got[k], want[k])
			}
		}
	}
}

// TestDistanceFunctionsAllocateNothing asserts the zero-allocation claim.
// levenshteinRunes and SymmetricScore take caller-owned scratch, so they are
// checked unconditionally; the string entry points go through a sync.Pool and
// are skipped under -race, where the pool drops Puts on purpose.
func TestDistanceFunctionsAllocateNothing(t *testing.T) {
	a, b := "Batman: The Long Halloween", "BATMAN - The Long Hallowe'en Deluxe"

	var ls levScratch
	ra, rb := []rune(a), []rune(b)
	if n := testing.AllocsPerRun(200, func() { _ = levenshteinRunes(ra, rb, &ls) }); n != 0 {
		t.Errorf("levenshteinRunes allocates %v times per call, want 0", n)
	}

	pa, pb := Prepare(a), Prepare(b)
	var sc Scratch
	if n := testing.AllocsPerRun(200, func() { _ = SymmetricScore(&pa, &pb, &sc) }); n != 0 {
		t.Errorf("SymmetricScore allocates %v times per call, want 0", n)
	}

	if raceEnabled {
		t.Skip("sync.Pool drops Puts under -race; pooled entry points are checked in a normal build")
	}
	if n := testing.AllocsPerRun(200, func() { _ = LevenshteinDistance(a, b) }); n != 0 {
		t.Errorf("LevenshteinDistance allocates %v times per call, want 0", n)
	}
	if n := testing.AllocsPerRun(200, func() { _ = LevenshteinDistanceCaseSensitive(a, b) }); n != 0 {
		t.Errorf("LevenshteinDistanceCaseSensitive allocates %v times per call, want 0", n)
	}
}
