// file: internal/database/metadata_field_state_guards.go
// version: 1.0.0
// guid: 34bc346c-8d4e-4097-89a5-810dc4e9807d
// last-edited: 2026-08-23

package database

// The two provenance questions every "may I overwrite this field?" guard asks.
//
// Three call sites asked them inline and spelled them differently, which made a
// deliberate difference look like a typo:
//
//   - plugins/maintenance/repair_junk_titles.go tested locked || override || fetched
//   - plugins/maintenance/title_repair.go       tested locked || override, then
//     fetched separately so it could report a different skip reason
//   - server/handlers/metadata/handler.go       tested locked || override only
//
// Naming them separates the clause all three agree on from the clause they
// legitimately differ on. handler.go's omission of the fetched check is NOT
// recorded anywhere in its source -- the comments at handler.go:31 and :122
// explain why loadMetadataState is injected with a concrete type, not why the
// guard skips FetchedValue -- so it is preserved verbatim here rather than
// "unified", and flagged in the conformance test as unexplained.

// HasUserOverride reports whether a human has spoken for this field: either they
// locked it, or they set an explicit value. Neither is ours to rewrite.
func (s MetadataFieldState) HasUserOverride() bool {
	return s.OverrideLocked || s.OverrideValue != nil
}

// HasProviderValue reports whether a metadata provider supplied a value for this
// field. Weaker than HasUserOverride: a fetched value blocks the repair jobs,
// which only ever want to fix file-derived junk, but must NOT block the
// bulk-fetch handler, whose entire job is to write a newly fetched value over an
// older one.
func (s MetadataFieldState) HasProviderValue() bool {
	return s.FetchedValue != nil
}
