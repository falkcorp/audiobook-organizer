// file: internal/seriesdenumber/demote.go
// version: 2.0.0
// guid: 609bfb39-82de-4bb5-b398-b84db96913fa
// last-edited: 2026-11-17

// Package seriesdenumber holds the pure decisions behind the
// maintenance.series-denumber op: which de-numbering plans may be applied
// unattended (grammar.go, demote.go), which books may actually move once the target series
// is known (decide.go), how an apply is ordered and scoped for a canary
// (select.go) and the undo-journal rows a move writes (journal.go).
//
// It is a package of its own, and not part of internal/plugins/maintenance,
// so these decisions have table tests that compile and run on their own: the
// maintenance test binary is too large to build per change. Nothing here reads
// a store.
package seriesdenumber

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/personname"
)

// Code names why a plan was held out of the apply set. The codes are stable
// strings: they appear in the report and in the operator-facing summary. The
// grammar's codes are in grammar.go.
type Code string

// Candidate is what the demotion checks read about one plan.
type Candidate struct {
	// FromName is the numbered series' full name, as stored.
	FromName string
	// Base is the planner's base: the name with the position stripped.
	Base string
	// Position is the planner's parsed position.
	Position int
	// AuthorID is the numbered series' author row, 0 when it has none.
	AuthorID int
	// AuthorPlaceholder is true when that author row is a stand-in for "no known
	// author" ("Unknown", "Various", ...). Such a row folds unrelated series of
	// one name together exactly as author id 0 does, so it is held the same way.
	AuthorPlaceholder bool
	// AuthorName is that author's name, "" when unknown. The base-equals-author
	// check is skipped without it.
	AuthorName string
	// Authors is every author name in the library. A base equal to ANY of them is
	// held, not only the series' own author: "Name 05" under a different author
	// row for the same person (a pen name, a duplicate) is the same mistake. nil
	// skips the check.
	Authors AuthorSet
}

// Verdict is a reason a plan must not be applied.
type Verdict struct {
	Code   Code
	Reason string
}

// Demote returns a verdict when the plan must not be applied unattended, and ok
// false when it may be. A plan is applicable only when its name passes the
// positive grammar (ParseName) and the checks below; the first failing check
// wins.
//
// "Applicable" is a necessary condition, not a sufficient one: the numbers the
// library itself corroborates are a second, separate condition (Decide).
func Demote(c Candidate) (Verdict, bool) {
	switch {
	case c.AuthorID == 0:
		return Verdict{CodeNoAuthor, "the series has no author, so every no-author series of this name would fold into one base"}, true
	case c.AuthorPlaceholder:
		return Verdict{CodePlaceholderAuthor, "the series' author is a placeholder (unknown, various), so unrelated series of this name would fold into one base"}, true
	}
	parsed, v, ok := ParseName(c.FromName)
	if !ok {
		return v, true
	}
	if !strings.EqualFold(normalizeName(c.Base), parsed.Base) || c.Position != parsed.N {
		return Verdict{CodeNamesDisagree, "the planner read base \"" + c.Base + "\" at " + strconv.Itoa(c.Position) +
			" but the name reads \"" + parsed.Base + "\" at " + strconv.Itoa(parsed.N)}, true
	}
	if c.Authors.Has(parsed.Base) || (c.AuthorName != "" && NewAuthorSet([]string{c.AuthorName}).Has(parsed.Base)) {
		return Verdict{CodeAuthorBase, "the base is an author's name, not a series"}, true
	}
	return Verdict{}, false
}

// AuthorSet is a set of author-name keys (personname.LettersKey: NFC,
// lower-cased, letters and digits only), so case, spacing, punctuation and the
// composed or decomposed spelling of an accent do not hide a match.
type AuthorSet map[string]struct{}

// NewAuthorSet builds an AuthorSet from names; blank names are skipped. Each
// name contributes every key AuthorKeys gives it.
func NewAuthorSet(names []string) AuthorSet {
	set := make(AuthorSet, len(names))
	for _, n := range names {
		for _, k := range AuthorKeys(n) {
			set[k] = struct{}{}
		}
	}
	return set
}

// Has reports whether any key of name is in the set, so a base spelled
// "Last, First" matches an author stored "First Last" and the other way round.
func (a AuthorSet) Has(name string) bool {
	for _, k := range AuthorKeys(name) {
		if _, ok := a[k]; ok {
			return true
		}
	}
	return false
}

var creditSplit = regexp.MustCompile(`(?i)\s*(?:&|/|;|\+|\band\b)\s*`)

// AuthorKeys returns the comparison keys of one author name: its own key; for
// "Last, First" the key of "First Last"; and, for a composite credit
// ("A & B", "A and B", "A / B", "A; B", "A, B and C"), the keys of every part.
// A name with no letters has none.
func AuthorKeys(name string) []string {
	name = normalizeName(name)
	if name == "" {
		return nil
	}
	seen := map[string]bool{}
	var keys []string
	add := func(s string) {
		if k := personname.LettersKey(s); k != "" && !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	add(name)
	var parts []string
	for _, seg := range creditSplit.Split(name, -1) {
		if seg = strings.TrimSpace(seg); seg != "" {
			parts = append(parts, seg)
		}
	}
	if len(parts) > 1 {
		for _, p := range personname.SplitCompositeAuthorName(name) {
			add(p)
		}
	}
	for _, part := range parts {
		add(part)
		pieces := strings.Split(part, ",")
		switch {
		case len(pieces) == 2 && len(strings.Fields(pieces[0])) == 1 && len(strings.Fields(pieces[1])) >= 1:
			// "Wayne, Bruce": one family name, then the given names.
			add(strings.TrimSpace(pieces[1]) + " " + strings.TrimSpace(pieces[0]))
		case len(pieces) > 1:
			// "Bruce Wayne, Dick Grayson": a list of credits.
			for _, pc := range pieces {
				add(pc)
			}
		}
	}
	return keys
}
