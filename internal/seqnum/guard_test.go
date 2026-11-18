// file: internal/seqnum/guard_test.go
// version: 2.0.0
// guid: 5a91c3e7-2b6d-4f08-8e14-d70b9a3c6f52
// last-edited: 2026-11-18

package seqnum

import (
	"math/rand/v2"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// guardPieces are the fragments the generated texts are built from: every
// literal a guard looks for and the neighbours that make an expression match or
// almost match, in the spellings a title has. (Lower case, as the readers see
// the text; one upper-case piece checks that a case-sensitive expression and its
// guard agree about it.)
var guardPieces = []string{
	"book", "books", "bk", "bks", "volume", "volumes", "vol", "vols", "vol.", "number", "no", "no.", "#", "# ",
	"part", "parts", "pt", "pt.", "disc", "discs", "disk", "cd", "cds", "track", "trk", "chapter", "ch", "ch.",
	"1", "2", "3", "5", "07", "10", "12", "22", "001", "002", "1.5", "2.5", "199", "1999", "2033", "2100", "1000", "2101",
	"i", "ii", "iii", "iv", "v", "vi", "vii", "viii", "ix", "x", "xi", "xx", "xxi", "l", "c", "civic", "mix", "ill",
	"one", "two", "three", "ten", "eleven", "twenty", "first", "second", "third", "1st", "22nd", "twelfth",
	" ", " ", " ", "  ", ", ", ",", "&", "+", " and ", " to ", " through ", " thru ", "-", "–", "—", " - ", " – ", " — ", ":", ": ", ";",
	"(", ")", "[", "]", "()", "[]", "( )", "(2)", "[03]", "(2 of 3)", "1 of 2", " of ", "of", "/",
	"kbps", "khz", "hz", "mb", "gb", "k", "64kbps", "128k", "44.1khz", "700mb", "kb/s", "kbits",
	"hush", "knightfall", "batman", "robin", "gotham", "year", "dark", "victory", "the", "a", "unabridged", "novel",
	"_", "é", "BOOK", "Part", "Vol", "I", "\xff", "日本",
}

// guardShapes are the sentences the rarer expressions match, with holes: K a
// range keyword, N a number, W a number word, O an ordinal, D a dash, T a title
// word. A hole is filled at random; the rest is literal.
var guardShapes = []string{
	"T K N D N", "T K N D K N", "T K N to N", "T K W D W", "T K N D W T", "T K N and N", "T K N, N & N", "T K N D N & N", "T K N D N, N: T",
	"T K N D T", "T - 00N", "T N D N", "T K N (N of N)", "O K T", "K the O T", "K O", "T K N", "T (N)", "T [0N]", "T N", "T, N", "T N - T",
	"T part N of N", "T disc N", "T cd N", "T N of N", "T (N of N)", "T 64kbps N", "T N hz", "T 19N9 T", "T ()", "T [ ] N", "T II", "T i", "T (T)", "T N (T)",
}

func fillShape(r *rand.Rand, shape string) string {
	pick := func(xs ...string) string { return xs[r.IntN(len(xs))] }
	var b strings.Builder
	for i := 0; i < len(shape); i++ {
		switch shape[i] {
		case 'K':
			b.WriteString(pick("book", "books", "bk", "bks", "volume", "volumes", "vol", "vol.", "vols", "#", "no", "number"))
		case 'N':
			b.WriteString(pick("1", "2", "3", "5", "7", "10", "12", "1.5", "22"))
		case 'W':
			b.WriteString(pick("two", "three", "ten", "iii", "iv", "ii", "xii"))
		case 'O':
			b.WriteString(pick("first", "second", "third", "1st", "22nd"))
		case 'D':
			b.WriteString(pick("-", "–", "—", " - ", " – ", " to "))
		case 'T':
			b.WriteString(pick("hush", "knightfall", "gotham", "dark victory", "year one", "batman"))
		default:
			b.WriteByte(shape[i])
		}
	}
	return b.String()
}

// scaled returns full in a normal build and a fifth of it under -race, where a
// regexp match costs many times more (sync.Pool drops Puts, so the matcher's
// state is allocated again). The floors below scale with it.
func scaled(full int) int {
	if raceEnabled {
		return max(full/5, 1)
	}
	return full
}

func randomGuardText(r *rand.Rand, pieces int) string {
	if r.IntN(2) == 0 {
		return fillShape(r, guardShapes[r.IntN(len(guardShapes))])
	}
	var b strings.Builder
	for n := r.IntN(pieces) + 1; n > 0; n-- {
		b.WriteString(guardPieces[r.IntN(len(guardPieces))])
	}
	return b.String()
}

// unguardedOK is the sources of expressions that are meant to run on every text
// because they need nothing in particular of it. It is empty: every expression
// in the package needs a marker word, a digit, a bracket or a dash.
var unguardedOK = map[string]bool{}

// guardTexts is n generated texts, the same for every caller with the same seed.
func guardTexts(seed1, seed2 uint64, n, pieces int) []string {
	r := rand.New(rand.NewPCG(seed1, seed2))
	texts := make([]string, 0, n)
	for i := 0; i < n; i++ {
		texts = append(texts, randomGuardText(r, pieces))
	}
	return texts
}

// holdGuardToItsExpression fails the test when the guard says no to a text its
// expression matches, and returns how many of texts the expression matched and
// how many the guard turned away.
func holdGuardToItsExpression(t *testing.T, g *guardedRe, texts []string) (matched, skipped int) {
	t.Helper()
	for _, s := range texts {
		m := g.re.MatchString(s)
		if m {
			matched++
		}
		if g.skip(s) {
			skipped++
			if m {
				t.Fatalf("the guard of %q says no to %q, which the expression matches", g.re, s)
			}
		}
	}
	return matched, skipped
}

// TestGuards_NeverHideAMatch holds every guard to the expression it fronts: a
// guard that says "no" about a text its expression matches would change what the
// readers see. It also requires that each expression is matched by a share of
// the generated texts, so a guard cannot pass by never being asked, and that
// every expression has a guard (an expression that needs nothing of the text,
// say one that gained an optional branch, would silently lose the speed-up: add
// its source to unguardedOK to say so on purpose).
func TestGuards_NeverHideAMatch(t *testing.T) {
	if len(guards) == 0 {
		t.Fatal("no guarded expressions registered")
	}
	n := scaled(60000)
	texts := guardTexts(41, 43, n, 8)
	for _, g := range guards {
		if g.need == nil && !unguardedOK[g.re.String()] {
			t.Errorf("%q needs nothing of the text, so it has no guard; if that is intended add it to unguardedOK", g.re)
			continue
		}
		matched, skipped := holdGuardToItsExpression(t, g, texts)
		if matched < n/2400 {
			t.Errorf("%q matched only %d of %d generated texts: its guard is not being tested", g.re, matched, len(texts))
		}
		if g.need != nil && skipped == 0 {
			t.Errorf("the guard of %q never said no over %d texts: it saves nothing", g.re, len(texts))
		}
	}
}

// TestGuards_ReadersAreUnchangedByThem runs every reader over generated texts
// with the guards in force and again with every guard answering "maybe" (so
// every expression runs, as before the guards), and requires the same answers.
// It is not parallel: it switches the guards off for the first pass, and puts
// them back with t.Cleanup so a panic in a reader cannot leave them off.
func TestGuards_ReadersAreUnchangedByThem(t *testing.T) {
	texts := guardTexts(53, 59, scaled(10000), 9)
	type answers struct {
		Clean                  string
		Parse, ParseTitle      [2]any
		ParseRange             [2]any
		Position, PositionNoRg [2]any
		PartSuffix             []any
		Stated, StatedPair     any
		PairNumeral            bool
	}
	read := func(s, other string) answers {
		var a answers
		a.Clean = clean(s)
		n, ok := Parse(s)
		a.Parse = [2]any{n, ok}
		n, ok = ParseTitle(s)
		a.ParseTitle = [2]any{n, ok}
		n, ok = ParseRange(s)
		a.ParseRange = [2]any{n, ok}
		n, ok = ParsePosition(s)
		a.Position = [2]any{n, ok}
		n, ok = ParsePositionNoRange(s)
		a.PositionNoRg = [2]any{n, ok}
		pn, stem, pok := PartSuffix(s)
		a.PartSuffix = []any{pn, stem, pok}
		a.Stated = ReadStated(s)
		sa, sb := ReadPair(s, other)
		a.StatedPair = [2][]Stated{sa, sb}
		a.PairNumeral = numeralBeside(s, other)
		return a
	}
	want := make([]answers, len(texts))
	saved := make([]*requirement, len(guards))
	restore := func() {
		for i, g := range guards {
			g.need = saved[i]
		}
	}
	for i, g := range guards {
		saved[i] = g.need
	}
	t.Cleanup(restore)
	for _, g := range guards {
		g.need = nil // every expression runs, as before the guards
	}
	for i, s := range texts {
		want[i] = read(s, texts[(i*7+3)%len(texts)])
	}
	restore()
	for i, s := range texts {
		if got := read(s, texts[(i*7+3)%len(texts)]); !reflect.DeepEqual(got, want[i]) {
			t.Fatalf("with the guards, %q (beside %q) reads %+v; without them %+v", s, texts[(i*7+3)%len(texts)], got, want[i])
		}
	}
}

func TestIsPlainNumber_MatchesTheExpression(t *testing.T) {
	re := regexp.MustCompile(`^\d+(?:\.\d+)?$`)
	everyString([]string{"1", "2", ".", "a", "٣", " ", "-"}, 6, func(s string) {
		if got := isPlainNumber(s); got != re.MatchString(s) {
			t.Fatalf("isPlainNumber(%q) = %v, the expression says %v", s, got, re.MatchString(s))
		}
	})
	for _, s := range []string{"1\n", "\n1", "12.50", "12.", ".5", "1.2.3", "007", "1e5"} {
		if got := isPlainNumber(s); got != re.MatchString(s) {
			t.Fatalf("isPlainNumber(%q) = %v, the expression says %v", s, got, re.MatchString(s))
		}
	}
}

func TestIsRomanOnly_MatchesTheExpression(t *testing.T) {
	re := regexp.MustCompile(`^[ivxlc]+$`)
	everyString([]string{"i", "v", "x", "l", "c", "d", "1", "I", "é"}, 5, func(s string) {
		if got := isRomanOnly(s); got != re.MatchString(s) {
			t.Fatalf("isRomanOnly(%q) = %v, the expression says %v", s, got, re.MatchString(s))
		}
	})
	if isRomanOnly("ii\n") || isRomanOnly("\nii") {
		t.Fatal("isRomanOnly accepted a newline")
	}
}

// everyString calls f with every string of length 0..maxLen over alphabet.
func everyString(alphabet []string, maxLen int, f func(string)) {
	var walk func(prefix string, left int)
	walk = func(prefix string, left int) {
		f(prefix)
		if left == 0 {
			return
		}
		for _, a := range alphabet {
			walk(prefix+a, left-1)
		}
	}
	walk("", maxLen)
}
