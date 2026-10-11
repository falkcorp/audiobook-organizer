// file: internal/audiobooks/filter_testseams_test.go
// version: 1.0.0
// guid: e5ce678f-b1bc-41aa-af30-9e83e2c1b296
// last-edited: 2026-10-11

package audiobooks

import "github.com/falkcorp/audiobook-organizer/internal/database"

// Single-row filter entry points for tests. Production evaluates filters
// through compileFieldFilters / matchesCompiledFilters (list paths) and
// mustCompileForPredicate; these four compile per call and had no production
// caller (deadcode-unreachable), so they live with the tests that drive them.

// matchesFieldFilters returns true if a book matches all the given field filters.
// All filters are ANDed: every filter must match for the book to be included.
func matchesFieldFilters(book database.Book, filters []FieldFilter) bool {
	return matchesFieldFiltersRT(book, filters, nil)
}

// matchesFieldFiltersRT is matchesFieldFilters with the runtime source the
// duration: filter compares against (nil = stored Book.Duration).
func matchesFieldFiltersRT(book database.Book, filters []FieldFilter, rt runtimeFunc) bool {
	// Fail CLOSED on an empty value, as the production predicate paths do (see
	// FirstEmptyFilterValue and mustCompileForPredicate): falling through would
	// reach strings.Contains(x, "") == true and match every book. Negated is
	// deliberately not consulted.
	for _, f := range filters {
		if f.Value == "" {
			return false
		}
	}
	cfs, ok := mustCompileForPredicate(filters)
	if !ok {
		return false
	}
	return matchesCompiledFilters(&book, cfs, rt)
}

// fieldMatchesValue checks whether a book's field value matches the search
// value under the unified grammar (internal/querygrammar). An invalid value or
// an unknown field matches nothing.
func fieldMatchesValue(book database.Book, field, value string) bool {
	return fieldMatchesValueRT(book, field, value, nil)
}

// fieldMatchesValueRT is fieldMatchesValue with an explicit runtime source for
// the duration fields (nil = stored Book.Duration). See filter_duration.go.
func fieldMatchesValueRT(book database.Book, field, value string, rt runtimeFunc) bool {
	cf, err := compileFieldFilter(FieldFilter{Field: field, Value: value})
	if err != nil {
		return false
	}
	return fieldMatchesCompiled(&book, &cf, rt)
}
