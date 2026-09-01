// file: internal/metafetch/metadata_field_state_guards_test.go
// version: 1.0.0
// guid: 4673e12e-5c70-456a-a4c6-11a63dd02ed2
// last-edited: 2026-08-23

package metafetch

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/metastate"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// database.MetadataFieldState and metafetch.MetadataFieldState are two views of
// one stored row, and the provenance guards read BOTH: the maintenance repair
// jobs hold the database view, the bulk-fetch handler holds the metafetch view.
// The types are not interchangeable -- database stores *string, metafetch stores
// the decodeMetadataValue output as any -- so "x != nil" is not the same question
// on the two sides, and a shared method NAME does not by itself make it one.
//
// This test is the only thing that makes the pair trustworthy. Extracting the
// three inline expressions into HasUserOverride/HasProviderValue without it would
// have renamed three expressions and cemented the difference behind a name that
// reads as deliberate. Same shape as the repo's other two-implementation
// conformance tests: worth exactly its selector, so the table below is the
// asset, not the assertion loop.
func TestMetadataFieldStateGuardsConform(t *testing.T) {
	// typedNil is the ONE input on which the two sides disagree. Kept in the
	// table rather than omitted, because a silent divergence is the failure this
	// file exists to prevent.
	var typedNil *string

	cases := []struct {
		name string
		// value is what a caller assigns to metafetch's any-typed field before
		// it is encoded for storage.
		value any
		// diverges records a KNOWN, currently-unreachable disagreement. Any case
		// that is false here must agree on both sides.
		diverges bool
		why      string
	}{
		{name: "absent", value: nil},
		{name: "empty string", value: ""},
		{name: "zero int", value: 0},
		{name: "false", value: false},
		{name: "ordinary string", value: "Dune"},
		{name: "whitespace", value: "   "},
		{
			name:     "typed-nil pointer",
			value:    typedNil,
			diverges: true,
			why: "encodeMetadataValue json.Marshals a typed nil to the 4-byte raw " +
				"\"null\", so the stored *string is NON-nil, but decodeMetadataValue " +
				"json.Unmarshals \"null\" back to an untyped nil. Stored-but-not-decoded. " +
				"UNREACHABLE through every current writer: audiobooks/service_mutation.go's " +
				"fieldExtractors all dereference before returning and return (nil, false) " +
				"when the pointer is nil, and decodeRawValue yields untyped nil or a decoded " +
				"JSON value, never a typed nil. If a future writer changes that, this case " +
				"turns the silent clobber into a build failure.",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := metastate.Encode(tc.value)
			if err != nil {
				t.Fatalf("metastate.Encode(%#v): %v", tc.value, err)
			}

			// The database view holds the stored *string verbatim.
			dbRow := database.MetadataFieldState{OverrideValue: raw, FetchedValue: raw}
			// The metafetch view holds it decoded, exactly as loadMetadataState builds it.
			mfRow := MetadataFieldState{
				OverrideValue: metastate.Decode(raw),
				FetchedValue:  metastate.Decode(raw),
			}

			for _, g := range []struct {
				guard  string
				db, mf bool
			}{
				{"HasUserOverride", dbRow.HasUserOverride(), mfRow.HasUserOverride()},
				{"HasProviderValue", dbRow.HasProviderValue(), mfRow.HasProviderValue()},
			} {
				agree := g.db == g.mf
				switch {
				case tc.diverges && agree:
					t.Errorf("%s: sides now AGREE (both %v) on a case documented as diverging.\n"+
						"That is good news, but the recorded reason is stale -- delete the "+
						"diverges flag and this comment rather than leaving a false warning:\n  %s",
						g.guard, g.db, tc.why)
				case !tc.diverges && !agree:
					t.Errorf("%s DIVERGES: database=%v metafetch=%v for value %#v (stored raw %s).\n"+
						"The maintenance repair jobs read the database side and the bulk-fetch "+
						"handler reads the metafetch side, so this is a live clobber disagreement: "+
						"one path will refuse to overwrite the field and the other will overwrite it.",
						g.guard, g.db, g.mf, tc.value, rawString(raw))
				}
			}
		})
	}

	// The OverrideLocked half of HasUserOverride has no encoding at all -- it is a
	// plain bool on both types -- so it is pinned directly rather than through the
	// round trip. Without this, a guard that dropped the locked clause entirely
	// would still pass every case above, since they all exercise OverrideValue.
	t.Run("locked alone is an override", func(t *testing.T) {
		db := database.MetadataFieldState{OverrideLocked: true}
		mf := MetadataFieldState{OverrideLocked: true}
		if !db.HasUserOverride() || !mf.HasUserOverride() {
			t.Errorf("OverrideLocked=true with no OverrideValue must count as a user override; "+
				"database=%v metafetch=%v", db.HasUserOverride(), mf.HasUserOverride())
		}
		if db.HasProviderValue() || mf.HasProviderValue() {
			t.Errorf("OverrideLocked must not leak into HasProviderValue; database=%v metafetch=%v",
				db.HasProviderValue(), mf.HasProviderValue())
		}
	})
}

func rawString(raw *string) string {
	if raw == nil {
		return "<nil>"
	}
	return "\"" + *raw + "\""
}
