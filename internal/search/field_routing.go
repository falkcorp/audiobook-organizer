// file: internal/search/field_routing.go
// version: 1.0.0
// guid: 4e8a1d27-9b5c-4f63-a0d2-7c3b6e9f1a54
// last-edited: 2026-09-27

package search

import (
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"
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
	t := reflect.TypeOf(BookDocument{})
	for i := 0; i < t.NumField(); i++ {
		name := strings.Split(t.Field(i).Tag.Get("json"), ",")[0]
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
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		return int(math.Round(f)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: use seconds or units like 20m, 1h30m", s)
	}
	if d < 0 {
		return 0, fmt.Errorf("invalid duration %q", s)
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
