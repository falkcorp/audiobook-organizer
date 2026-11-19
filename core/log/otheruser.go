// file: core/log/otheruser.go
// version: 1.1.0
// guid: 6ab16966-3ac0-453d-9880-dcebd76d0903
// last-edited: 2026-11-19

package log

import (
	"log/slog"
	"maps"
	"slices"
)

// OtherUserPredicate reports whether a journal record (from the native store
// or from an app's own JournalStore) names another user's data; viewer is the
// principal's user ID. A module registers at most one through
// module.Registrar.LogOtherUserData; the journal query and the live tail
// return a flagged record only to a principal that also holds core:users.view
// and blank its user_id otherwise (05-logging §5). It must be pure and fast
// (called per record) and fail closed: true when unsure.
//
// The record a predicate is handed is the same on every surface (the query
// route and the live tail): it is passed through PredicateInput, so its
// attributes are text only (every Value has Kind slog.KindString, rendered as
// the journal renders it), flat under their dotted keys, sorted by key, with
// one attribute per key (the last value logged wins). A predicate therefore
// reads attribute values as strings and must not depend on a value's Kind or on
// attribute order or repeats.
type OtherUserPredicate func(rec Record, viewer string) bool

// PredicateInput is rec as an OtherUserPredicate is shown it (see the contract
// above): the one normalisation every surface applies before calling a
// predicate, so the query route and the live tail classify a record
// identically. rec's attributes must already be flat, as the journal stores
// them. rec itself is not modified.
func PredicateInput(rec Record) Record {
	text := make(map[string]string, len(rec.Attrs))
	for _, a := range rec.Attrs {
		text[a.Key] = a.Value.Resolve().String()
	}
	out := rec
	out.Attrs = make([]slog.Attr, 0, len(text))
	for _, k := range slices.Sorted(maps.Keys(text)) {
		out.Attrs = append(out.Attrs, slog.String(k, text[k]))
	}
	return out
}
