// file: internal/dedup/jaro_equivalence_test.go
// version: 1.0.0
// guid: af915e77-caa9-4339-a23e-6a9405a0e485
// last-edited: 2026-10-30

package dedup

import (
	"math"
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"
)

// refJaroWinklerSimilarity is the PRE-optimization implementation, kept
// verbatim as the oracle for the scratch-buffer version. It allocates two rune
// slices and two bitmaps per call, which is exactly what the shipped one no
// longer does.
func refJaroWinklerSimilarity(s1, s2 string) float64 {
	s1 = strings.ToLower(s1)
	s2 = strings.ToLower(s2)

	if s1 == s2 {
		return 1.0
	}

	len1 := utf8.RuneCountInString(s1)
	len2 := utf8.RuneCountInString(s2)

	if len1 == 0 || len2 == 0 {
		return 0.0
	}

	r1 := []rune(s1)
	r2 := []rune(s2)

	matchDistance := max(int(math.Max(float64(len1), float64(len2)))/2-1, 0)

	s1Matches := make([]bool, len1)
	s2Matches := make([]bool, len2)

	matches := 0
	transpositions := 0

	for i := range len1 {
		start := int(math.Max(0, float64(i-matchDistance)))
		end := int(math.Min(float64(len2-1), float64(i+matchDistance)))

		for j := start; j <= end; j++ {
			if s2Matches[j] || r1[i] != r2[j] {
				continue
			}
			s1Matches[i] = true
			s2Matches[j] = true
			matches++
			break
		}
	}

	if matches == 0 {
		return 0.0
	}

	k := 0
	for i := range len1 {
		if !s1Matches[i] {
			continue
		}
		for !s2Matches[k] {
			k++
		}
		if r1[i] != r2[k] {
			transpositions++
		}
		k++
	}

	jaro := (float64(matches)/float64(len1) +
		float64(matches)/float64(len2) +
		float64(matches-transpositions/2)/float64(matches)) / 3.0

	prefixLen := 0
	for i := 0; i < int(math.Min(4, math.Min(float64(len1), float64(len2)))); i++ {
		if r1[i] == r2[i] {
			prefixLen++
		} else {
			break
		}
	}

	return jaro + float64(prefixLen)*0.1*(1-jaro)
}

// jwAlphabet mixes upper and lower case, accented Latin, Greek (with final
// sigma), Cyrillic, CJK, dotted capital I, punctuation, and raw bytes that are
// not valid UTF-8.
var jwAlphabet = []string{
	"a", "b", "c", "d", "e", "g", "h", "k", "n", "o", "r", "s", "t", "u", "w", "y",
	"A", "B", "G", "K", "N", "O", "T", "W", "Y", " ", ".", "-", "'",
	"é", "É", "ö", "ß", "İ", "Σ", "σ", "ς", "Ж", "ж", "東", "京",
	"\xff", "\xc3", "\xe6\x9d",
}

var jwSurnames = []string{
	"Wayne", "Gordon", "Grayson", "Cobblepot", "Quinzel", "Kyle", "Todd", "Drake",
	"Pennyworth", "Fox", "Dent", "Nygma", "Crane", "Isley", "Elliot", "Cain",
}

func jwRandomString(rng *rand.Rand) string {
	var sb strings.Builder
	for i, n := 0, rng.Intn(22); i < n; i++ {
		sb.WriteString(jwAlphabet[rng.Intn(len(jwAlphabet))])
	}
	return sb.String()
}

// jwPairs yields deterministic pairs: noise over the mixed alphabet, unrelated
// Batman surnames, and one-edit variants of a surname.
func jwPairs(seed int64, n int, each func(a, b string)) {
	rng := rand.New(rand.NewSource(seed))
	for i := 0; i < n; i++ {
		switch i % 3 {
		case 0:
			each(jwRandomString(rng), jwRandomString(rng))
		case 1:
			each(jwSurnames[rng.Intn(len(jwSurnames))], jwSurnames[rng.Intn(len(jwSurnames))])
		default:
			a := []rune(jwSurnames[rng.Intn(len(jwSurnames))])
			b := append([]rune(nil), a...)
			switch rng.Intn(3) {
			case 0:
				b[rng.Intn(len(b))] = rune('a' + rng.Intn(26))
			case 1:
				b = append(b, rune('a'+rng.Intn(26)))
			default:
				p := rng.Intn(len(b) - 1)
				b[p], b[p+1] = b[p+1], b[p]
			}
			each(string(a), strings.ToUpper(string(b)))
		}
	}
}

// TestJaroWinklerMatchesReference is the property test: the scratch-buffer
// implementation must return bit-identical scores to the previous one on
// random input, through both the pooled entry point and a single long-lived
// jwScratch (the shape the phase-3 scan workers use). The strings vary wildly
// in length from call to call, so a bitmap or rune buffer that is not reset
// between uses shows up as a mismatch.
func TestJaroWinklerMatchesReference(t *testing.T) {
	var sc jwScratch
	distinct := map[float64]struct{}{}
	jwPairs(20261030, 40000, func(a, b string) {
		want := refJaroWinklerSimilarity(a, b)
		if got := jaroWinklerSimilarity(a, b); got != want {
			t.Fatalf("jaroWinklerSimilarity(%q, %q) = %v, reference %v", a, b, got, want)
		}
		if got := sc.similarity(a, b); got != want {
			t.Fatalf("jwScratch.similarity(%q, %q) = %v, reference %v", a, b, got, want)
		}
		distinct[want] = struct{}{}
	})
	if len(distinct) < 200 {
		t.Fatalf("only %d distinct scores across 40000 pairs; the generator is not exercising the metric", len(distinct))
	}
}

// TestJaroWinklerScratchIsResetBetweenCalls pins the specific hazard of a
// reused match bitmap: score a long pair that sets many matches, then a short
// pair on the same scratch. If the bitmaps were not cleared the short pair
// would see the long pair's trues.
func TestJaroWinklerScratchIsResetBetweenCalls(t *testing.T) {
	var sc jwScratch
	long1, long2 := "pennyworthpennyworth", "pennyworthpennyworth!"
	short1, short2 := "wayne", "wanye"
	want := refJaroWinklerSimilarity(short1, short2)
	for range 3 {
		if sc.similarity(long1, long2) != refJaroWinklerSimilarity(long1, long2) {
			t.Fatal("long pair diverged from reference")
		}
		if got := sc.similarity(short1, short2); got != want {
			t.Fatalf("short pair after a long one = %v, want %v (stale match bitmap)", got, want)
		}
	}
}

// TestJaroWinklerDegenerateInputs pins the early returns by name, including
// the one that makes two empty strings identical.
func TestJaroWinklerDegenerateInputs(t *testing.T) {
	cases := []struct {
		a, b string
		want float64
	}{
		{"", "", 1.0},
		{"", "wayne", 0.0},
		{"wayne", "", 0.0},
		{"Wayne", "WAYNE", 1.0},
		{"wayne", "xxxxx", 0.0},
	}
	for _, c := range cases {
		if got := jaroWinklerSimilarity(c.a, c.b); got != c.want {
			t.Errorf("jaroWinklerSimilarity(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
		if ref := refJaroWinklerSimilarity(c.a, c.b); ref != c.want {
			t.Errorf("reference(%q, %q) = %v, want %v (test table is wrong)", c.a, c.b, ref, c.want)
		}
	}
}

// TestJaroWinklerAllocatesNothing asserts the zero-allocation claim. The
// scratch method is checked unconditionally; the pooled entry point is skipped
// under -race, where sync.Pool drops Puts on purpose.
func TestJaroWinklerAllocatesNothing(t *testing.T) {
	a, b := "Cobblepot", "COBBLEPOTT"
	var sc jwScratch
	sc.similarity(a, b) // warm the buffers
	if n := testing.AllocsPerRun(200, func() { _ = sc.similarity(a, b) }); n != 0 {
		t.Errorf("jwScratch.similarity allocates %v times per call, want 0", n)
	}
	if raceEnabled {
		t.Skip("sync.Pool drops Puts under -race; the pooled entry point is checked in a normal build")
	}
	if n := testing.AllocsPerRun(200, func() { _ = jaroWinklerSimilarity(a, b) }); n != 0 {
		t.Errorf("jaroWinklerSimilarity allocates %v times per call, want 0", n)
	}
}
