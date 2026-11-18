// file: internal/seqnum/derive_test.go
// version: 1.1.0
// guid: 3e7c0a52-91d4-4b6f-a8c3-5d12f6e9b407
// last-edited: 2026-11-18

package seqnum

import (
	"math/rand/v2"
	"regexp"
	"strings"
	"testing"
)

// The tests here hold derive, the walker that turns a compiled expression into
// the condition its guard checks, to two things: the expression's own behaviour
// on generated expressions (TestDerive_NeverRejectsAMatch), and an exact table of
// what each node kind needs (TestDerive_Table). The guards of the package's own
// expressions are held to those expressions by TestGuards_NeverHideAMatch.

// --- the walker against the regexp package ----------------------------------

// randomExpr builds a random expression over a small alphabet, using every
// construct derive distinguishes: literals, case-folded groups, classes (small,
// multi-range, negated), \d \s \S ., the assertions, groups, alternations (with
// an empty branch now and then) and the quantifiers.
func randomExpr(r *rand.Rand, depth int) string {
	atoms := []string{"a", "b", "c", "ab", "bc", "x", "-", "–", "[ab]", "[a-cx-z]", "[^a]", "[b-d]", `\d`, `\s`, `\S`, `\w`, ".", `\b`, "^", "$", "[0-9]", "é",
		`[a\x{FFFD}]`, `\x{FFFD}`, `(?m:^)`, `(?m:$)`, `\B`, `\A`, `\z`, `(?s:.)`, `[^\x00-\x{10FFFF}]`,
		"[a-p]", "[a-q]"} // [a-p] holds 16 runes, the most a class may and still be searched for; [a-q] one more
	if depth <= 0 || r.IntN(4) == 0 {
		return atoms[r.IntN(len(atoms))]
	}
	sub := func() string { return randomExpr(r, depth-1) }
	switch r.IntN(9) {
	case 0, 1, 2:
		var b strings.Builder
		for n := r.IntN(3) + 2; n > 0; n-- {
			b.WriteString(sub())
		}
		return b.String()
	case 3, 4:
		branches := []string{sub(), sub()}
		if r.IntN(3) == 0 {
			branches = append(branches, sub())
		}
		if r.IntN(8) == 0 {
			branches = append(branches, "")
		}
		return "(?:" + strings.Join(branches, "|") + ")"
	case 5:
		return "(" + sub() + ")"
	case 6:
		return "(?i:" + sub() + ")"
	default:
		quants := []string{"*", "+", "?", "{0,2}", "{1,2}", "{2}", "{0}", "{1,}", "*?", "+?"}
		return "(?:" + sub() + ")" + quants[r.IntN(len(quants))]
	}
}

func randomText(r *rand.Rand) string {
	alphabet := []string{"a", "b", "c", "d", "p", "q", "x", "y", "z", "A", "B", "1", "2", "0", " ", " ", "-", "–", "é", "\n", "_", "\xff"}
	var b strings.Builder
	for n := r.IntN(9); n > 0; n-- {
		b.WriteString(alphabet[r.IntN(len(alphabet))])
	}
	return b.String()
}

// TestDerive_NeverRejectsAMatch is a sampled soundness check of the walker: over
// thousands of generated expressions and texts, a text the compiled expression
// matches is never turned away by the derived condition. Floors keep the test from passing vacuously:
// enough expressions have a condition, enough texts are matched, and the
// conditions turn enough texts away that a walker that returned nothing for
// everything would fail.
func TestDerive_NeverRejectsAMatch(t *testing.T) {
	r := rand.New(rand.NewPCG(71, 73))
	exprs, texts := scaled(4000), 160
	withNeed, matchesUnderNeed, rejected := 0, 0, 0
	for i := 0; i < exprs; i++ {
		expr := randomExpr(r, 4)
		re, err := regexp.Compile(expr)
		if err != nil {
			t.Fatalf("the generator built an invalid expression %q: %v", expr, err)
		}
		g := newGuard(expr)
		if g.need != nil {
			withNeed++
		}
		for j := 0; j < texts; j++ {
			s := randomText(r)
			if re.MatchString(s) {
				if g.need != nil {
					matchesUnderNeed++
				}
				if g.skip(s) {
					t.Fatalf("the derived condition of %q rejects %q, which it matches", expr, s)
				}
			} else if g.skip(s) {
				rejected++
			}
		}
	}
	if withNeed < exprs/3 {
		t.Errorf("only %d of %d generated expressions have a derived condition", withNeed, exprs)
	}
	// Only a match of an expression that has a condition can fail the test above.
	if matchesUnderNeed < exprs*texts/100 {
		t.Errorf("only %d matches of expressions that have a condition, in %d pairs: the texts do not exercise the conditions", matchesUnderNeed, exprs*texts)
	}
	if rejected < exprs*texts/20 {
		t.Errorf("the conditions turned away only %d of %d pairs: they save nothing", rejected, exprs*texts)
	}
}

// TestDerive_Table pins what each construct needs. A text in must is one the
// condition has to accept (and, for the expression, usually match); a text in
// mustNot is one a correct walker turns away. nil need means the expression is
// left unguarded.
func TestDerive_Table(t *testing.T) {
	tests := []struct {
		expr    string
		nothing bool
		must    []string
		mustNot []string
	}{
		{expr: `abc`, must: []string{"abc", "xabcx"}, mustNot: []string{"", "ab", "xyz"}},
		{expr: `a.c`, must: []string{"a_c", "ac"}, mustNot: []string{"a", "c", ""}},
		{expr: `a?`, nothing: true},
		{expr: `a*`, nothing: true},
		{expr: `a{0,3}`, nothing: true},
		{expr: `a{0}`, nothing: true},
		{expr: `a+`, must: []string{"a", "bab"}, mustNot: []string{"", "b"}},
		{expr: `a{2}`, must: []string{"aa"}, mustNot: []string{"b"}},
		{expr: `a{1,}`, must: []string{"a"}, mustNot: []string{"b"}},
		{expr: `a*b`, must: []string{"b", "aab"}, mustNot: []string{"a", ""}},
		{expr: `ab?c`, must: []string{"ac", "abc"}, mustNot: []string{"a", "c", "b"}},
		{expr: `(?:ab|cd)e`, must: []string{"abe", "cde"}, mustNot: []string{"e", "ab", "cd", "xyz"}},
		{expr: `book|vol|#`, must: []string{"book", "vol", "#"}, mustNot: []string{"", "bo", "vo", "x"}},
		{expr: `a|b*`, nothing: true},
		{expr: `a|`, nothing: true},
		{expr: `(?:a|b?)c`, must: []string{"c"}, mustNot: []string{"a", ""}},
		{expr: `a\Bb`, must: []string{"ab", "xaby"}, mustNot: []string{"a b", "a x b", "a", "b"}},
		{expr: `(a)(b)`, must: []string{"ab", "xaby"}, mustNot: []string{"ba", "a", "b", ""}},
		{expr: `n(?:o|umber)`, must: []string{"no", "number"}, mustNot: []string{"n", "o", "umber", "nu", "on"}},
		{expr: `vol(?:ume)?s?`, must: []string{"vol", "volumes"}, mustNot: []string{"vo", "ol", "v"}},
		{expr: `(a|b)+c`, must: []string{"ac", "bc"}, mustNot: []string{"c", "a", "xyz"}},
		{expr: `\bfoo\b`, must: []string{"foo"}, mustNot: []string{"", "fo", "bar"}},
		{expr: `^$`, nothing: true},
		{expr: `.`, nothing: true},
		{expr: `.*`, nothing: true},
		{expr: `\S`, nothing: true},
		{expr: `\w+`, nothing: true},
		{expr: `[^a]`, nothing: true},
		{expr: `\d`, must: []string{"7", "a7"}, mustNot: []string{"", "a"}},
		{expr: `\d{1,3}x`, must: []string{"1x"}, mustNot: []string{"x", "1", "ab"}},
		{expr: `[ivxlc]+`, must: []string{"i", "ax"}, mustNot: []string{"", "ab", "d"}},
		{expr: `[-–—]`, must: []string{"-", "–", "—"}, mustNot: []string{"", "a"}},
		{expr: `\s+x`, must: []string{" x"}, mustNot: []string{"x", " "}},
		{expr: `(?i)abc`, nothing: true},
		{expr: `(?i:abc)d`, must: []string{"ABCd"}, mustNot: []string{"abc"}},
		{expr: `a(?i:b)c`, must: []string{"aBc"}, mustNot: []string{"a", "c"}},
		{expr: `[a\x{FFFD}]`, nothing: true},
		{expr: `[a\x{FFFD}]b`, must: []string{"\xffb", "ab", "a\uFFFDb"}, mustNot: []string{"a", "xyz"}},
		{expr: `[a-p]x`, must: []string{"ax", "px"}, mustNot: []string{"qx", "x", "a"}},
		{expr: `[a-q]x`, must: []string{"qx", "x"}, mustNot: []string{"a", "qz"}},
		{expr: `a[^\x{FFFD}]b`, must: []string{"axb", "ab"}, mustNot: []string{"a", "b"}},
	}
	for _, tc := range tests {
		g := newGuard(tc.expr)
		if tc.nothing {
			if g.need != nil {
				t.Errorf("%q needs nothing of the text but has a guard", tc.expr)
			}
			continue
		}
		if g.need == nil {
			t.Errorf("%q needs something of the text but has no guard", tc.expr)
			continue
		}
		for _, s := range tc.must {
			if g.skip(s) {
				t.Errorf("the guard of %q rejects %q, which it must accept", tc.expr, s)
			}
		}
		for _, s := range tc.mustNot {
			if !g.skip(s) {
				t.Errorf("the guard of %q accepts %q, which it must reject", tc.expr, s)
			}
		}
	}
}

// TestDerive_InvalidBytesAreNotHidden: an invalid byte in the text reads as
// U+FFFD to the regexp package, which a byte search for that rune does not see,
// so an expression that needs U+FFFD is left unguarded rather than turn such a
// text away.
func TestDerive_InvalidBytesAreNotHidden(t *testing.T) {
	for _, expr := range []string{"a\uFFFDb", `[a\x{FFFD}]`} {
		g := newGuard(expr)
		if g.need != nil {
			t.Errorf("%q has U+FFFD in it and is guarded: %+v", expr, g.need)
		}
	}
	// The text the guard must not hide from them.
	if !regexp.MustCompile("a\uFFFDb").MatchString("a\xffb") || !regexp.MustCompile(`[a\x{FFFD}]`).MatchString("\xff") {
		t.Skip("the regexp package no longer reads an invalid byte as U+FFFD")
	}
	if g := newGuard(`[a\x{FFFD}]b`); g.skip("\xffb") {
		t.Error("the guard of [a\\x{FFFD}]b hides \"\\xffb\", which the expression matches")
	}
}

// --- an edit to an expression changes its guard with it ----------------------

// TestDerive_AKeywordAddedToAnExpressionIsNotHidden is the case the derived
// guards exist for. reMarker's source gains a keyword and nothing else changes:
// the new expression matches "episode 4", the guard built from it accepts that
// text, and the whole guard test still holds it to the expression. A condition
// kept beside the expression as a copy of its words would turn that text away,
// which is what an edit to an expression used to do while the tests stayed
// green.
func TestDerive_AKeywordAddedToAnExpressionIsNotHidden(t *testing.T) {
	src := reMarker.re.String()
	edited := strings.Replace(src, "volume|vol|number|no", "volume|vol|episode|number|no", 1)
	if edited == src {
		t.Fatalf("the keyword list moved: %q has no volume|vol|number|no", src)
	}
	g := newGuard(edited)
	const text = "knightfall episode 4"
	if !g.re.MatchString(text) {
		t.Fatalf("%q does not match %q", edited, text)
	}
	if g.skip(text) {
		t.Fatalf("the guard of the edited expression hides %q from it", text)
	}
	if reMarker.re.MatchString(text) || !reMarker.skip(text) {
		t.Fatalf("the unedited expression and guard should both pass over %q", text)
	}
	texts := append(guardTexts(41, 43, scaled(20000), 8), text, "episode 12", "no episode 1")
	matched, skipped := holdGuardToItsExpression(t, g, texts)
	if matched == 0 || skipped == 0 {
		t.Fatalf("the edited guard matched %d and skipped %d of %d texts", matched, skipped, len(texts))
	}
}

// TestDerive_StaysSmall: the product of a few classes grows geometrically
// ([0-9]{4} written out is ten thousand strings), so a run of exact children is
// cut off and each class is a condition of its own, and the same condition twice
// is checked once.
func TestDerive_StaysSmall(t *testing.T) {
	g := newGuard(`\d\d\d\d`)
	if g.need == nil || g.need.kind != needAnyRune || g.need.text != "0123456789" {
		t.Fatalf(`the condition of \d\d\d\d is %+v, want one search for a digit`, g.need)
	}
	g = newGuard(`(?:ab|cd)f|e+`)
	if g.need == nil || g.need.kind != needAny || len(g.need.kids) != 3 {
		t.Fatalf("the condition of (?:ab|cd)f|e+ is %+v, want one flat choice of abf, cdf and e", g.need)
	}
	g = newGuard(`(a\d)(b\d)`)
	if g.need == nil || g.need.kind != needAll || len(g.need.kids) != 3 {
		t.Fatalf("the condition of (a\\d)(b\\d) is %+v, want a, b and a digit once each", g.need)
	}
}

// TestDerive_CheapSearchesComeFirst: holds stops at the first condition that
// fails, so the conditions are ordered by how many searches each may cost.
func TestDerive_CheapSearchesComeFirst(t *testing.T) {
	g := newGuard(`(?:\d+x|first|second|third|fourth|fifth)\s+book`)
	if g.need == nil || g.need.kind != needAll {
		t.Fatalf("unexpected condition %+v", g.need)
	}
	last := g.need.kids[len(g.need.kids)-1]
	if last.cost() < 6 {
		t.Fatalf("the dearest condition (%d searches) is not last: %+v", last.cost(), g.need.kids)
	}
	for i := 1; i < len(g.need.kids); i++ {
		if g.need.kids[i-1].cost() > g.need.kids[i].cost() {
			t.Fatalf("conditions are not in cost order: %+v", g.need.kids)
		}
	}
}
