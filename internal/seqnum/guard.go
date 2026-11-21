// file: internal/seqnum/guard.go
// version: 2.0.0
// guid: 8d2f61b4-0a7e-4c93-b5e8-34f9c1d70a26
// last-edited: 2026-11-18

package seqnum

import (
	"regexp"
	"regexp/syntax"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

// A guarded expression is a regular expression with a cheap necessary condition
// in front of it: when the text cannot contain what the expression needs (a
// marker word, a digit, a bracket) the expression is not run, and the answer is
// the one a run with no match gives.
//
// Why: the readers in this package run a dozen or more expressions over every
// title, and a title check or a bulk apply reads the same few titles over and
// over, nearly all of them with no number in them at all. Each run is a
// backtracking match; under the race detector (every CI run) sync.Pool drops a
// share of its Puts, so the matcher's table is allocated again for a large part
// of the runs (97% of the bytes a title check allocated under -race were that
// one table, measured with -memprofilerate=1). A guard decides in a few
// nanoseconds what the match would have decided in microseconds.
//
// The condition is DERIVED from the compiled expression, never written by hand,
// so it cannot drift from it: an edit that adds a keyword to an expression
// changes its guard with it. derive walks the expression's regexp/syntax tree:
//
//   - a literal needs itself, and a character class of at most maxClassRunes
//     runes needs one of them (a larger or negated class needs nothing);
//   - a concatenation needs everything each child needs, and adjacent children
//     that match only a few strings are multiplied out first, so that "no" then
//     "te" needs "note" and not an "n", an "o", a "t" and an "e" somewhere;
//   - an alternation needs what any one child needs, so it needs nothing as soon
//     as one child needs nothing;
//   - a capture, "+" and a repeat with a minimum of at least one need what their
//     child needs; "?", "*", a repeat with a minimum of zero, a case-folded
//     literal and any other node need nothing, and an assertion (^ $ \b) needs
//     nothing and consumes nothing.
//
// An expression that needs nothing of the text is left unguarded and always
// runs. TestGuards_NeverHideAMatch holds every registered guard to its
// expression over generated texts, and TestDerive_* hold the walker itself to the
// regexp package over generated expressions.
type guardedRe struct {
	re   *regexp.Regexp
	need *requirement // nil: the expression needs nothing of the text, or the guard is switched off in a test
}

// guards is every guarded expression, for the tests that hold each guard to its
// expression.
var guards []*guardedRe

// maxClassRunes is the largest character class that is turned into a
// requirement: "one of these characters". A larger class (\w, a negated class)
// is almost always present in a text, so it would save nothing.
const maxClassRunes = 16

// guard compiles expr, derives its guard and registers it for the tests.
func guard(expr string) *guardedRe {
	g := newGuard(expr)
	guards = append(guards, g)
	return g
}

// newGuard is guard without the registration: the tests build guards of
// expressions that are not the package's own.
func newGuard(expr string) *guardedRe {
	parsed, err := syntax.Parse(expr, syntax.Perl)
	if err != nil {
		panic(err) // regexp.MustCompile below would panic on it too
	}
	return &guardedRe{re: regexp.MustCompile(expr), need: derive(parsed)}
}

// skip reports whether the expression cannot match s, so a reader may answer
// with the result of a run that finds nothing.
func (g *guardedRe) skip(s string) bool {
	return g.need != nil && !g.need.holds(s)
}

// The methods are the ones the readers use, each answering what the expression
// answers when it does not match.

func (g *guardedRe) MatchString(s string) bool {
	return !g.skip(s) && g.re.MatchString(s)
}

func (g *guardedRe) ReplaceAllString(s, repl string) string {
	if g.skip(s) {
		return s
	}
	return g.re.ReplaceAllString(s, repl)
}

func (g *guardedRe) FindStringIndex(s string) []int {
	if g.skip(s) {
		return nil
	}
	return g.re.FindStringIndex(s)
}

func (g *guardedRe) FindStringSubmatch(s string) []string {
	if g.skip(s) {
		return nil
	}
	return g.re.FindStringSubmatch(s)
}

func (g *guardedRe) FindStringSubmatchIndex(s string) []int {
	if g.skip(s) {
		return nil
	}
	return g.re.FindStringSubmatchIndex(s)
}

func (g *guardedRe) FindAllStringSubmatchIndex(s string, n int) [][]int {
	if g.skip(s) {
		return nil
	}
	return g.re.FindAllStringSubmatchIndex(s, n)
}

type requirementKind uint8

const (
	needLiteral requirementKind = iota // the text contains text
	needAnyRune                        // the text contains one of the runes of text
	needAll                            // every kid holds
	needAny                            // at least one kid holds
)

// requirement is what a text must contain for an expression to match it. A nil
// *requirement is "nothing".
type requirement struct {
	kind requirementKind
	text string
	kids []*requirement
}

// cost is how many searches holds may make at most.
func (r *requirement) cost() int {
	if r.kind == needLiteral || r.kind == needAnyRune {
		return 1
	}
	n := 0
	for _, k := range r.kids {
		n += k.cost()
	}
	return n
}

func (r *requirement) holds(s string) bool {
	switch r.kind {
	case needLiteral:
		return strings.Contains(s, r.text)
	case needAnyRune:
		return strings.ContainsAny(s, r.text)
	case needAll:
		for _, k := range r.kids {
			if !k.holds(s) {
				return false
			}
		}
		return true
	default: // needAny
		for _, k := range r.kids {
			if k.holds(s) {
				return true
			}
		}
		return false
	}
}

// maxExact is the most strings a concatenation's run of exact children may hold
// before the walker stops multiplying them together and turns the run into a
// condition instead: the product of a few classes ([0-9][0-9][0-9]) grows
// geometrically, and a literal search per string is the price of each. Eight
// keeps what the expressions here need (n(?:o|umber) is two, vol(?:ume)?s? is
// four) and leaves a digit class (ten strings) a one-rune search of its own.
const maxExact = 8

// shape is what the walker learns about a node of the syntax tree.
//
// exact, when isExact, is every string the node can match (an empty string
// stands for matching with nothing consumed, as an assertion or an optional
// part does); a concatenation multiplies its children's sets together so
// that "no" followed by "te" is the one string "note", which is what lets a
// condition keep the adjacency the parser's prefix factoring (b(?:k|ook))
// would otherwise cost it.
//
// need is the condition on the text when the node is not exact: nil, none.
type shape struct {
	isExact bool
	exact   []string
	need    *requirement
}

// derive returns what a text must contain for re to match it, nil when re
// needs nothing. See guardedRe for the rules.
func derive(re *syntax.Regexp) *requirement {
	return walk(re).requirement()
}

func (sh shape) requirement() *requirement {
	if sh.isExact {
		return stringsRequirement(sh.exact)
	}
	return sh.need
}

func exactOf(strs ...string) shape { return shape{isExact: true, exact: strs} }

func walk(re *syntax.Regexp) shape {
	switch re.Op {
	case syntax.OpEmptyMatch, syntax.OpBeginLine, syntax.OpEndLine, syntax.OpBeginText,
		syntax.OpEndText, syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		return exactOf("") // consumes nothing
	case syntax.OpLiteral:
		if re.Flags&syntax.FoldCase != 0 {
			return shape{} // matches in either case; a literal search would be too strict
		}
		if lit := string(re.Rune); searchable(lit) {
			return exactOf(lit)
		}
		return shape{}
	case syntax.OpCharClass:
		return classShape(re.Rune)
	case syntax.OpCapture:
		return walk(re.Sub[0])
	case syntax.OpQuest:
		if sub := walk(re.Sub[0]); sub.isExact {
			return unionShape([]shape{exactOf(""), sub})
		}
		return shape{}
	case syntax.OpPlus:
		return shape{need: walk(re.Sub[0]).requirement()}
	case syntax.OpRepeat:
		if re.Min >= 1 {
			return shape{need: walk(re.Sub[0]).requirement()}
		}
		return shape{}
	case syntax.OpConcat:
		return concatShape(re.Sub)
	case syntax.OpAlternate:
		subs := make([]shape, len(re.Sub))
		for i, sub := range re.Sub {
			subs[i] = walk(sub)
		}
		return unionShape(subs)
	}
	return shape{} // OpStar, OpAnyChar, OpAnyCharNotNL, OpNoMatch: nothing to require
}

// searchable reports whether a byte search for lit finds every text the
// expression reads as lit. An invalid byte in the text reads as U+FFFD to the
// regexp package, which a search for that rune's encoding does not find.
func searchable(lit string) bool { return !strings.ContainsRune(lit, utf8.RuneError) }

// classShape is the exact set of a small character class, and nothing for a
// large or negated one (almost every text has one of its characters, so a
// condition on it saves nothing), or for an empty one, which matches nothing.
func classShape(ranges []rune) shape {
	var strs []string
	for i := 0; i+1 < len(ranges); i += 2 {
		for r := ranges[i]; r <= ranges[i+1]; r++ {
			if len(strs) == maxClassRunes || !searchable(string(r)) {
				return shape{}
			}
			strs = append(strs, string(r))
		}
	}
	if len(strs) == 0 {
		return shape{}
	}
	return exactOf(strs...)
}

// unionShape is an alternation: exact when every branch is, otherwise "one of
// the branches' conditions", which is nothing as soon as one branch has none.
func unionShape(subs []shape) shape {
	var union []string
	allExact := true
	for _, sub := range subs {
		if !sub.isExact {
			allExact = false
			break
		}
		union = appendStrings(union, sub.exact...)
	}
	if allExact {
		return exactOf(union...)
	}
	kids := make([]*requirement, 0, len(subs))
	for _, sub := range subs {
		kids = append(kids, sub.requirement())
	}
	return shape{need: anyOf(kids)}
}

// concatShape multiplies exact children together while the product stays small
// and ANDs the conditions of what is left: a child that is not exact, or one
// that would make the product too large, closes the run of exact children before
// it into a condition of its own.
func concatShape(subs []*syntax.Regexp) shape {
	run := []string{""}
	var needs []*requirement
	allExact := true
	for _, sub := range subs {
		sh := walk(sub)
		if sh.isExact {
			if len(run)*len(sh.exact) <= maxExact {
				run = cross(run, sh.exact)
				continue
			}
			allExact = false
			needs = append(needs, stringsRequirement(run))
			run = sh.exact
			continue
		}
		allExact = false
		needs = append(needs, stringsRequirement(run), sh.need)
		run = []string{""}
	}
	if allExact {
		return exactOf(run...)
	}
	return shape{need: allOf(append(needs, stringsRequirement(run)))}
}

func cross(a, b []string) []string {
	out := make([]string, 0, len(a)*len(b))
	for _, x := range a {
		for _, y := range b {
			out = append(out, x+y)
		}
	}
	return appendStrings(nil, out...)
}

// appendStrings appends the strings not already in out.
func appendStrings(out []string, more ...string) []string {
next:
	for _, m := range more {
		for _, o := range out {
			if o == m {
				continue next
			}
		}
		out = append(out, m)
	}
	return out
}

// stringsRequirement is "the text contains one of strs", nil when strs holds an
// empty string (every text does). A string that holds another of the set adds
// nothing (a text with "parts" has "part"), so it is dropped, and the one-rune
// strings are searched for together.
func stringsRequirement(strs []string) *requirement {
	if slices.Contains(strs, "") {
		return nil
	}
	var runes strings.Builder
	var lits []string
next:
	for i, s := range strs {
		for j, other := range strs {
			if i != j && strings.Contains(s, other) && (s != other || j < i) {
				continue next
			}
		}
		if utf8.RuneCountInString(s) == 1 {
			runes.WriteString(s)
		} else {
			lits = append(lits, s)
		}
	}
	var kids []*requirement
	if runes.Len() > 0 {
		kids = append(kids, &requirement{kind: needAnyRune, text: runes.String()})
	}
	for _, l := range lits {
		kids = append(kids, &requirement{kind: needLiteral, text: l})
	}
	if len(kids) == 1 {
		return kids[0]
	}
	return &requirement{kind: needAny, kids: kids}
}

// allOf is every requirement of kids, dropping the empty ones.
func allOf(kids []*requirement) *requirement {
	var out []*requirement
	for _, k := range kids {
		switch {
		case k == nil:
		case k.kind == needAll:
			out = appendDistinct(out, k.kids...)
		default:
			out = appendDistinct(out, k)
		}
	}
	switch len(out) {
	case 0:
		return nil
	case 1:
		return out[0]
	}
	// The cheap conditions first: holds stops at the first that fails, and a
	// title nearly always fails a one-rune or one-word search long before it
	// has been through twenty ordinal words.
	sort.SliceStable(out, func(i, j int) bool { return out[i].cost() < out[j].cost() })
	return &requirement{kind: needAll, kids: out}
}

// anyOf is "at least one of kids", which is nothing as soon as one of them needs
// nothing.
func anyOf(kids []*requirement) *requirement {
	var out []*requirement
	for _, k := range kids {
		switch {
		case k == nil:
			return nil
		case k.kind == needAny:
			out = appendDistinct(out, k.kids...)
		default:
			out = appendDistinct(out, k)
		}
	}
	if len(out) == 1 {
		return out[0]
	}
	return &requirement{kind: needAny, kids: out}
}

// appendDistinct appends the requirements that are not already there (the same
// literal twice in a concatenation is one condition).
func appendDistinct(out []*requirement, more ...*requirement) []*requirement {
next:
	for _, m := range more {
		if m.kind == needLiteral || m.kind == needAnyRune {
			for _, o := range out {
				if o.kind == m.kind && o.text == m.text {
					continue next
				}
			}
		}
		out = append(out, m)
	}
	return out
}

// isPlainNumber is ^\d+(?:\.\d+)?$ : ASCII digits, optionally a point and more
// digits, and nothing else.
func isPlainNumber(s string) bool {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 {
		return false
	}
	if i == len(s) {
		return true
	}
	if s[i] != '.' {
		return false
	}
	i++
	j := i
	for j < len(s) && s[j] >= '0' && s[j] <= '9' {
		j++
	}
	return j > i && j == len(s)
}

// isRomanOnly is ^[ivxlc]+$ .
func isRomanOnly(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case 'i', 'v', 'x', 'l', 'c':
		default:
			return false
		}
	}
	return true
}
