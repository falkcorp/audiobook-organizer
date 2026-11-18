// file: internal/seqnum/guard.go
// version: 1.0.0
// guid: 8d2f61b4-0a7e-4c93-b5e8-34f9c1d70a26
// last-edited: 2026-11-18

package seqnum

import (
	"regexp"
	"strings"
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
// A guard may only be too permissive, never too strict: a guard that says "no"
// for a text the expression matches changes what Parse reads. Every guard is
// registered here, and TestGuards_NeverHideAMatch runs each one against the
// expression it fronts over generated texts. The conditions are on lower-case
// ASCII literals because the expressions are case-sensitive and the text they
// read has been lower-cased.
type guardedRe struct {
	re       *regexp.Regexp
	mayMatch func(string) bool
}

// guards is every guarded expression, for the test that holds each guard to its
// expression.
var guards []*guardedRe

func guard(expr string, mayMatch func(string) bool) *guardedRe {
	g := &guardedRe{re: regexp.MustCompile(expr), mayMatch: mayMatch}
	guards = append(guards, g)
	return g
}

// The methods are the ones the readers use, each answering what the expression
// answers when it does not match.

func (g *guardedRe) MatchString(s string) bool {
	return g.mayMatch(s) && g.re.MatchString(s)
}

func (g *guardedRe) ReplaceAllString(s, repl string) string {
	if !g.mayMatch(s) {
		return s
	}
	return g.re.ReplaceAllString(s, repl)
}

func (g *guardedRe) FindStringIndex(s string) []int {
	if !g.mayMatch(s) {
		return nil
	}
	return g.re.FindStringIndex(s)
}

func (g *guardedRe) FindStringSubmatch(s string) []string {
	if !g.mayMatch(s) {
		return nil
	}
	return g.re.FindStringSubmatch(s)
}

func (g *guardedRe) FindStringSubmatchIndex(s string) []int {
	if !g.mayMatch(s) {
		return nil
	}
	return g.re.FindStringSubmatchIndex(s)
}

func (g *guardedRe) FindAllStringSubmatchIndex(s string, n int) [][]int {
	if !g.mayMatch(s) {
		return nil
	}
	return g.re.FindAllStringSubmatchIndex(s, n)
}

// hasAny is "the text contains at least one of these literals".
func hasAny(lits ...string) func(string) bool {
	return func(s string) bool {
		for _, l := range lits {
			if strings.Contains(s, l) {
				return true
			}
		}
		return false
	}
}

// hasDigit is "the text contains an ASCII digit" (\d is ASCII in RE2).
func hasDigit(s string) bool { return strings.ContainsAny(s, "0123456789") }

// all is "every one of these conditions holds".
func all(conds ...func(string) bool) func(string) bool {
	return func(s string) bool {
		for _, c := range conds {
			if !c(s) {
				return false
			}
		}
		return true
	}
}

// hasAnyOr is "at least one of these conditions holds".
func hasAnyOr(conds ...func(string) bool) func(string) bool {
	return func(s string) bool {
		for _, c := range conds {
			if c(s) {
				return true
			}
		}
		return false
	}
}

// hasDash is "the text contains a hyphen, en dash or em dash".
func hasDash(s string) bool { return strings.ContainsAny(s, "-–—") }

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
