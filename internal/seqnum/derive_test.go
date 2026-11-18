// file: internal/seqnum/derive_test.go
// version: 1.0.0
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
// the condition its guard checks, to three things: the hand-written conditions
// it replaced (TestDerive_AgainstTheHandWrittenGuards), the expression's own
// behaviour on generated expressions (TestDerive_NeverRejectsAMatch), and an
// exact table of what each node kind needs (TestDerive_Table).

// --- the hand-written conditions the walker replaced ------------------------

func oldAny(lits ...string) func(string) bool {
	return func(s string) bool {
		for _, l := range lits {
			if strings.Contains(s, l) {
				return true
			}
		}
		return false
	}
}

func oldDigit(s string) bool { return strings.ContainsAny(s, "0123456789") }
func oldDash(s string) bool  { return strings.ContainsAny(s, "-–—") }

func oldAll(conds ...func(string) bool) func(string) bool {
	return func(s string) bool {
		for _, c := range conds {
			if !c(s) {
				return false
			}
		}
		return true
	}
}

func oldAnyOf(conds ...func(string) bool) func(string) bool {
	return func(s string) bool {
		for _, c := range conds {
			if c(s) {
				return true
			}
		}
		return false
	}
}

// handWritten is the condition each of the 26 guarded expressions carried before
// guards were derived, copied from the commit that introduced them. They are
// the reference the derived conditions are compared with, never used by
// production code.
func handWritten() map[*guardedRe]func(string) bool {
	rangeNeeds := oldAny("book", "bk", "vol", "#")
	return map[*guardedRe]func(string) bool{
		reDiscPart:      oldAny("dis", "cd", "part", "pt", "tr", "ch"),
		reBitrate:       oldAll(oldDigit, oldAny("k", "hz", "mb", "gb")),
		reYear:          oldAny("1", "2"),
		reMarker:        oldAny("book", "bk", "vol", "number", "no", "#"),
		reBracket:       oldAll(oldAny("[", "("), oldDigit),
		reLeading:       oldDigit,
		reTrailing:      oldDigit,
		reTrailingRoman: oldAny("i", "x"),
		reEmptyBrackets: oldAny("[", "("),
		reMidTitle:      oldAll(oldDigit, oldAny("-", "–", "—", ":")),
		rePartSuffix:    oldAll(oldAny("0"), oldDash),
		reSpanDigits:    rangeNeeds,
		reSpanSpaced:    rangeNeeds,
		reSpanWords:     rangeNeeds,
		reListDigits:    rangeNeeds,
		reListWords:     rangeNeeds,
		rePartStated:    oldAny("part", "pt", "disc", "disk", "cd"),
		reParenOf:       oldAll(oldAny("("), oldAny("of"), oldDigit),
		reBareOf:        oldAll(oldAny("of"), oldDigit),
		reOrdBefore:     oldAny("book", "vol", "part"),
		reOrdAfter:      oldAny("book", "vol", "part"),
		reLoneRomanI:    oldAny("i"),
		reTrailingNum:   oldAnyOf(oldDigit, oldAny("i", "x")),
		reNumeralMarker: oldAny("book", "bk", "vol", "no", "number"),
		reClosingNote:   oldAll(oldAny("(", "["), oldAny(")", "]")),
		reTrailingSpan:  oldAll(oldDigit, oldDash),
	}
}

// TestDerive_AgainstTheHandWrittenGuards holds each derived condition to the
// hand-written one it replaced, over the texts the guard tests use, in the one
// direction that is a matter of correctness: every text the expression matches
// is accepted by the derived condition, which includes every match the
// hand-written one accepted. (A derived condition may turn away a text the old
// one let through when the expression does not match it: it is exact where the
// old list used a shorter literal, "dis" for disc and disk, and the number of
// such texts is logged. That is why this is not "at least as permissive" in the
// literal sense; soundness is this test plus TestGuards_NeverHideAMatch and
// TestDerive_NeverRejectsAMatch.)
func TestDerive_AgainstTheHandWrittenGuards(t *testing.T) {
	old := handWritten()
	if len(old) != 26 {
		t.Fatalf("the reference holds %d conditions, the 26 hand-written guards were expected", len(old))
	}
	texts := guardTexts(41, 43, scaled(60000), 8)
	for g, oldCond := range old {
		if g.need == nil {
			t.Errorf("%q has no derived condition, but the hand-written one was a real one", g.re)
			continue
		}
		tighter := 0
		for _, s := range texts {
			derived := g.need.holds(s)
			if g.re.MatchString(s) && !derived {
				t.Fatalf("the derived condition of %q rejects %q, which it matches", g.re, s)
			}
			if oldCond(s) && !derived {
				tighter++
			}
		}
		t.Logf("%-70.70q derived turns away %6d of %d texts the hand-written one let through", g.re, tighter, len(texts))
	}
}

// TestDerive_NeverLooserThanTheHandWrittenGuards is about speed, not
// correctness: a derived condition that accepts a text the hand-written one
// turned away would run an expression the old guard skipped. Over the guard
// test texts it never does, so the speed-up the hand-written guards gave is kept
// (and the derived ones add to it).
func TestDerive_NeverLooserThanTheHandWrittenGuards(t *testing.T) {
	texts := guardTexts(41, 43, scaled(60000), 8)
	for g, oldCond := range handWritten() {
		for _, s := range texts {
			if g.need != nil && g.need.holds(s) && !oldCond(s) {
				t.Fatalf("the derived condition of %q accepts %q, which the hand-written one turned away", g.re, s)
			}
		}
	}
}

// --- the walker against the regexp package ----------------------------------

// randomExpr builds a random expression over a small alphabet, using every
// construct derive distinguishes: literals, case-folded groups, classes (small,
// multi-range, negated), \d \s \S ., the assertions, groups, alternations (with
// an empty branch now and then) and the quantifiers.
func randomExpr(r *rand.Rand, depth int) string {
	atoms := []string{"a", "b", "c", "ab", "bc", "x", "-", "–", "[ab]", "[a-cx-z]", "[^a]", "[b-d]", `\d`, `\s`, `\S`, `\w`, ".", `\b`, "^", "$", "[0-9]", "é"}
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
	alphabet := []string{"a", "b", "c", "d", "x", "y", "z", "A", "B", "1", "2", "0", " ", " ", "-", "–", "é", "\n", "_", "\xff"}
	var b strings.Builder
	for n := r.IntN(9); n > 0; n-- {
		b.WriteString(alphabet[r.IntN(len(alphabet))])
	}
	return b.String()
}

// TestDerive_NeverRejectsAMatch is the soundness proof of the walker: over
// thousands of generated expressions and texts, a text the compiled expression
// matches is never turned away by the derived condition. Floors keep the test from passing vacuously:
// enough expressions have a condition, enough texts are matched, and the
// conditions turn enough texts away that a walker that returned nothing for
// everything would fail.
func TestDerive_NeverRejectsAMatch(t *testing.T) {
	r := rand.New(rand.NewPCG(71, 73))
	exprs, texts := scaled(4000), 160
	withNeed, matches, rejected := 0, 0, 0
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
				matches++
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
	if matches < exprs*texts/20 {
		t.Errorf("only %d matches in %d pairs: the texts do not exercise the conditions", matches, exprs*texts)
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
	g := newGuard("a�b")
	if g.need != nil {
		t.Fatal("an expression with U+FFFD in it is guarded")
	}
	if s := "a\xffb"; !g.re.MatchString(s) {
		t.Skipf("the regexp package no longer reads an invalid byte as U+FFFD: %q", s)
	}
}

// --- an edit to an expression changes its guard with it ----------------------

// TestDerive_AKeywordAddedToAnExpressionIsNotHidden is the case the derived
// guards exist for. reMarker's source gains a keyword and nothing else changes:
// the new expression matches "episode 4", the guard built from it accepts that
// text, and the whole guard test still holds it to the expression. The
// hand-written condition of the unedited expression turns the same text away,
// which is what an edit to an expression, with a copy of its words kept beside
// it, used to do while the tests stayed green.
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
	if handWritten()[reMarker](text) {
		t.Fatalf("the hand-written condition accepted %q: the case no longer shows the drift", text)
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
