// file: internal/search/field_routing.go
// version: 1.4.1
// guid: 4e8a1d27-9b5c-4f63-a0d2-7c3b6e9f1a54
// last-edited: 2026-10-26

package search

import (
	"fmt"
	"math"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/querygrammar"
)

// Which query fields the Bleve index can answer, and the duration grammar
// shared by the index translator and the Go-side library filter.
//
// WHY: a fielded term on a name the index does not hold matches no document.
// Under a NOT that becomes "every document" (NOT over nothing + MatchAll), so
// GET /api/v1/audiobooks?search=-review:matched answered the WHOLE library
// (100,856 on prod, 2026-09-27) while looking like a narrowed query; and
// duration:>1200 asked the unindexed field "duration" (the index holds
// "duration_seconds") and answered 0.

// indexedFields is the set of field names present in the Bleve document,
// derived from BookDocument's json tags so it cannot drift from the mapping's
// source struct.
var indexedFields = func() map[string]struct{} {
	out := map[string]struct{}{}
	t := reflect.TypeFor[BookDocument]()
	for field := range t.Fields() {
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name != "" && name != "-" {
			out[name] = struct{}{}
		}
	}
	// Query-side aliases the translator rewrites onto an indexed field.
	for alias := range fieldAliases {
		out[alias] = struct{}{}
	}
	return out
}()

// fieldAliases maps a query field name onto the indexed field it means.
var fieldAliases = map[string]string{
	"duration": "duration_seconds",
	"added":    "date_added",
	"modified": "date_modified",
}

// IsIndexedField reports whether the search index can answer a fielded term
// on name (directly or via an alias), or it is a per-user field the search
// path post-filters in Go.
func IsIndexedField(name string) bool {
	if _, ok := perUserFieldSet[name]; ok {
		return true
	}
	_, ok := indexedFields[name]
	return ok
}

// IsPerUserField reports whether name is a per-user field: its value comes from
// the evaluating user's own state (UserBookState), not from the book.
func IsPerUserField(name string) bool {
	_, ok := perUserFieldSet[name]
	return ok
}

// QueryMayUsePerUserField reports whether the query text may reference a per-user
// field, and answers true whenever it cannot show that it does not:
//
//   - the text does not parse;
//   - the parsed query names a per-user field (read_status:finished, in any boolean
//     shape);
//   - the raw text contains a per-user field name as a word anywhere, even where
//     the parser reads it as something else (author:read_status, a quoted
//     "read_status:finished", a bare read_status). Such a query is not evaluated
//     per user today, but it is one parser change away from being so, and the cost
//     of the guess being wrong is asymmetric: a collection wrongly kept per-viewer
//     shows each viewer their own answer, a collection wrongly kept shared would
//     serve one user's listening state to everyone.
//
// It is the guard for stored member lists (is this collection evaluated per
// viewer, is its stored list served or kept): a query it answers true for shows
// nobody a stored list and has its stored list cleared. Callers that would evaluate
// the query get their parse error from the evaluator, so for them "true" never
// reads anyone's state.
func QueryMayUsePerUserField(query string) bool {
	ast, err := ParseQuery(query)
	if err != nil {
		return true
	}
	for _, name := range FieldNames(ast) {
		if IsPerUserField(name) {
			return true
		}
	}
	return perUserNameInText.MatchString(query)
}

// perUserNameInText matches any per-user field name as a whole word in raw query
// text. It is built from the translator's own set, so a field added there is
// covered here (TestPerUserFieldNamesAgree pins the other copy of the list).
var perUserNameInText = func() *regexp.Regexp {
	names := make([]string, 0, len(perUserFieldSet))
	for name := range perUserFieldSet {
		names = append(names, regexp.QuoteMeta(name))
	}
	slices.Sort(names)
	return regexp.MustCompile(`(?i)\b(?:` + strings.Join(names, "|") + `)\b`)
}()

// The store cannot import this package (it imports the store), so the fail-closed
// detector is handed to it here: every collection write then drops the stored
// member list of a per-viewer collection (database.SetCollectionPerViewerClassifier).
func init() {
	database.SetCollectionPerViewerClassifier(QueryMayUsePerUserField)
}

// FieldNames returns every field name referenced by a parsed query, in order
// of first appearance, without duplicates.
func FieldNames(n Node) []string {
	var out []string
	seen := map[string]bool{}
	var walk func(Node)
	walk = func(n Node) {
		switch v := n.(type) {
		case *AndNode:
			for _, c := range v.Children {
				walk(c)
			}
		case *OrNode:
			for _, c := range v.Children {
				walk(c)
			}
		case *NotNode:
			walk(v.Child)
		case *FieldNode:
			if !seen[v.Field] {
				seen[v.Field] = true
				out = append(out, v.Field)
			}
		case *ValueAltNode:
			if !seen[v.Field] {
				seen[v.Field] = true
				out = append(out, v.Field)
			}
		}
	}
	if n != nil {
		walk(n)
	}
	return out
}

// ParseDurationSeconds parses a human duration into whole seconds: a bare
// number is seconds ("1200", "90.5"); otherwise anything time.ParseDuration
// accepts ("20m", "1h30m", "1.5h", "45s"). Spaces are ignored ("1h 30m").
// Negative values are rejected.
func ParseDurationSeconds(s string) (int, error) {
	s = strings.ReplaceAll(strings.TrimSpace(strings.ToLower(s)), " ", "")
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		if f < 0 || math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, fmt.Errorf("invalid duration %q", querygrammar.ShortToken(s))
		}
		return int(math.Round(f)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: use seconds or units like 20m, 1h30m", querygrammar.ShortToken(s))
	}
	if d < 0 {
		return 0, fmt.Errorf("invalid duration %q", querygrammar.ShortToken(s))
	}
	return int(math.Round(d.Seconds())), nil
}

// durationBound converts one side of a duration comparison or range to the
// seconds string buildNumericRange expects; "" and "*" stay open.
func durationBound(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "*" {
		return "", nil
	}
	v, err := ParseDurationSeconds(s)
	if err != nil {
		return "", err
	}
	return strconv.Itoa(v), nil
}
