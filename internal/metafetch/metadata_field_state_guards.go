// file: internal/metafetch/metadata_field_state_guards.go
// version: 1.1.0
// guid: 7377aa81-4186-49ac-9e47-8bcefb000ac2
// last-edited: 2026-09-01

package metafetch

// MetadataFieldState is the decoded twin of database.MetadataFieldState: the
// stored *string values have been through metastate.Decode into any. These
// two predicates must answer identically for the same stored row, which
// TestMetadataFieldStateGuardsConform (internal/metafetch) pins.
//
// The types are NOT interchangeable and the difference is load-bearing:
// database's OverrideValue is *string (non-nil means "a value is stored"),
// metafetch's is any (non-nil means "a value decoded"). Raw "null" decodes to an
// untyped nil, so it is stored-but-not-decoded and the two sides disagree. No
// current writer can produce it -- see the conformance test for the proof and
// the reachability argument.

// HasUserOverride reports whether a human has spoken for this field: either they
// locked it, or they set an explicit value. Mirrors
// database.MetadataFieldState.HasUserOverride.
func (s MetadataFieldState) HasUserOverride() bool {
	return s.OverrideLocked || s.OverrideValue != nil
}

// HasProviderValue reports whether a metadata provider supplied a value for this
// field. Mirrors database.MetadataFieldState.HasProviderValue.
func (s MetadataFieldState) HasProviderValue() bool {
	return s.FetchedValue != nil
}
