// file: internal/audiobooks/filter_ident.go
// version: 1.0.1
// guid: 181ccbf7-b0ca-4392-83cf-f054eb73e20d
// last-edited: 2026-10-11

package audiobooks

import (
	"errors"
	"fmt"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/querygrammar"
)

// The ident: Library filter. A book's identification state is not a field of
// database.Book -- it is derived from the book AND its metadata_cache row and
// kept as an index by the store (database/ident_state.go) -- so it cannot be
// matched row by row in bookFieldValue. Instead every list entry point
// resolves ident: filters, before the walk, to the set of book ids in the
// wanted states (read from the index's membership keys) and intersects that
// set with the request's RestrictToIDs, the way tag narrowing does. A filter
// that reaches the row matcher unresolved is refused there loudly
// (compileFieldFilter), never matched as text.

// IdentFilterField is the ident: filter's field name.
const IdentFilterField = "ident"

// IdentMissingValue selects the books with no fetched candidate and no
// applied metadata, i.e. unasked or asked_empty (appendix C's definition,
// before its exclusion of the franchise, which this filter cannot make yet;
// see database.DeriveIdentState).
const IdentMissingValue = "missing"

// IdentFilterValues lists every value ident: accepts.
func IdentFilterValues() []string {
	out := make([]string, 0, len(database.AllIdentStates)+1)
	for _, s := range database.AllIdentStates {
		out = append(out, string(s))
	}
	return append(out, IdentMissingValue)
}

// identFilterStates maps an ident: value to the states it selects.
func identFilterStates(value string) ([]database.IdentState, bool) {
	v := strings.ToLower(strings.TrimSpace(value))
	if v == IdentMissingValue {
		return []database.IdentState{database.IdentUnasked, database.IdentAskedEmpty}, true
	}
	if s := database.IdentState(v); database.ValidIdentState(s) {
		return []database.IdentState{s}, true
	}
	return nil, false
}

// validateIdentFilterValue is ValidateFilterValue for ident:. The value is
// an exact state name (or missing): a pattern cannot be resolved to a state
// set, and a typo must be a 400 that lists the valid values, not a 0.
func validateIdentFilterValue(f FieldFilter) error {
	if !querygrammar.IsPlainLiteral(f.Value, f.Quoted) {
		return fmt.Errorf("%s:%s — ident takes one of %s, not a pattern",
			f.Field, querygrammar.ShortToken(f.Value), strings.Join(IdentFilterValues(), ", "))
	}
	if _, ok := identFilterStates(f.Value); !ok {
		return fmt.Errorf("%s:%s — unknown identification state; valid values: %s",
			f.Field, querygrammar.ShortToken(f.Value), strings.Join(IdentFilterValues(), ", "))
	}
	return nil
}

// HasIdentFilter reports whether filters hold an ident: filter. The handler
// keeps such a request out of its library-generation-keyed list cache: an
// identification state moves on a cache-row write, which does not move the
// library generation.
func HasIdentFilter(filters []FieldFilter) bool {
	for _, f := range filters {
		if f.Field == IdentFilterField {
			return true
		}
	}
	return false
}

// identWantedStates folds every ident: filter in filters into the one state
// set they select together (ANDed). A negated filter selects the complement:
// the states are exhaustive, so -ident:applied is every other state.
func identWantedStates(filters []FieldFilter) ([]database.IdentState, error) {
	want := make(map[database.IdentState]bool, len(database.AllIdentStates))
	for _, s := range database.AllIdentStates {
		want[s] = true
	}
	for _, f := range filters {
		if f.Field != IdentFilterField {
			continue
		}
		if err := validateIdentFilterValue(f); err != nil {
			return nil, err
		}
		states, _ := identFilterStates(f.Value)
		sel := make(map[database.IdentState]bool, len(states))
		for _, s := range states {
			sel[s] = true
		}
		for s := range want {
			if sel[s] == f.Negated {
				delete(want, s)
			}
		}
	}
	out := make([]database.IdentState, 0, len(want))
	for _, s := range database.AllIdentStates {
		if want[s] {
			out = append(out, s)
		}
	}
	return out, nil
}

// errIdentIndexUnavailable is returned for an ident: filter on a store that
// does not keep the identification index.
var errIdentIndexUnavailable = errors.New("the identification index is not available on this store")

// resolveIdentFilters removes every ident: filter from f.FieldFilters and
// narrows f.RestrictToIDs to the books in the states they select. A request
// without one is returned unchanged. It returns database.ErrIdentIndexBuilding
// while the index is being built (the Library answers 503), never a partial
// set.
//
// The caller's RestrictToIDs is never written through: a fresh set is built.
// A non-nil empty result is the walkers' "no book is eligible".
func (svc *AudiobookService) resolveIdentFilters(f ListFilters) (ListFilters, error) {
	if !HasIdentFilter(f.FieldFilters) {
		return f, nil
	}
	states, err := identWantedStates(f.FieldFilters)
	if err != nil {
		return f, fmt.Errorf("invalid filter value: %w", err)
	}
	rest := make([]FieldFilter, 0, len(f.FieldFilters))
	for _, ff := range f.FieldFilters {
		if ff.Field != IdentFilterField {
			rest = append(rest, ff)
		}
	}
	ids := map[string]struct{}{}
	if len(states) > 0 {
		reader, ok := database.AsCapability[database.IdentIndexReader](svc.store)
		if !ok {
			return f, errIdentIndexUnavailable
		}
		ids, err = reader.IdentBookIDs(states)
		if err != nil {
			return f, err
		}
	}
	if f.RestrictToIDs != nil {
		both := make(map[string]struct{}, min(len(ids), len(f.RestrictToIDs)))
		for id := range f.RestrictToIDs {
			if _, ok := ids[id]; ok {
				both[id] = struct{}{}
			}
		}
		ids = both
	}
	f.FieldFilters = rest
	f.RestrictToIDs = ids
	return f, nil
}

// identCounterCount answers a count O(1) from the index's primary-scope
// counters when the request is exactly "ident: filters over the Library's
// default view": primary versions, quarantined books excluded, trash
// excluded (no marked_for_deletion filter), and nothing else. That is the
// scope the primary counters count (database.identInScope), so the counter
// and the walk agree. ok is false for any other request, which then takes
// the restricted walk.
func (svc *AudiobookService) identCounterCount(f ListFilters) (n int, ok bool, err error) {
	if !HasIdentFilter(f.FieldFilters) {
		return 0, false, nil
	}
	for _, ff := range f.FieldFilters {
		if ff.Field != IdentFilterField {
			return 0, false, nil
		}
	}
	if f.IsPrimaryVersion == nil || !*f.IsPrimaryVersion || !f.ExcludeQuarantined ||
		f.LibraryState != "" || f.Tag != "" || len(f.Tags) > 0 || len(f.PerUserFilters) > 0 ||
		f.FingerprintStatus != "" || f.CoveragePercentMin != nil || f.CoveragePercentMax != nil ||
		f.RestrictToIDs != nil {
		return 0, false, nil
	}
	states, err := identWantedStates(f.FieldFilters)
	if err != nil {
		return 0, false, fmt.Errorf("invalid filter value: %w", err)
	}
	reader, has := database.AsCapability[database.IdentIndexReader](svc.store)
	if !has {
		return 0, false, errIdentIndexUnavailable
	}
	counts := reader.IdentCounts()
	if !counts.Ready {
		return 0, false, database.ErrIdentIndexBuilding
	}
	var total int64
	for _, s := range states {
		total += counts.Primary[s]
	}
	return int(total), true, nil
}
