// file: internal/config/path_alias_test.go
// version: 1.2.0
// guid: 864e867a-dbd9-47fb-a731-300899c5e5b8
// last-edited: 2026-08-21

package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSeedPathAliasesFromMappings(t *testing.T) {
	// Production holds exactly one mapping in this shape; seeding means the
	// W: fact is copied rather than retyped into a second config field.
	mappings := []ITunesPathMap{{From: "W:", To: "/library/books"}}

	got := SeedPathAliases(nil, mappings)

	assert.Equal(t, []PathAlias{{Root: "/library/books", Windows: "W:"}}, got,
		"seeded alias takes Root from To and Windows from From, leaving UNC and SMBURL empty")
}

func TestSeedPathAliasesLeavesConfiguredAliasesAlone(t *testing.T) {
	existing := []PathAlias{{Root: "/library/books", Windows: "X:", SMBURL: "smb://host/books"}}
	mappings := []ITunesPathMap{{From: "W:", To: "/library/books"}}

	got := SeedPathAliases(existing, mappings)

	assert.Equal(t, existing, got, "an explicitly configured alias is never overwritten by seeding")
}

func TestSeedPathAliasesWithNoMappingsIsEmpty(t *testing.T) {
	assert.Empty(t, SeedPathAliases(nil, nil),
		"with neither configured the feature stays dormant and the UI is unchanged")
}

func TestSeedPathAliasesSkipsIncompleteMappings(t *testing.T) {
	mappings := []ITunesPathMap{
		{From: "", To: "/library/books"},
		{From: "W:", To: ""},
		{From: "W:", To: "/library/books"},
	}

	got := SeedPathAliases(nil, mappings)

	assert.Equal(t, []PathAlias{{Root: "/library/books", Windows: "W:"}}, got,
		"a mapping missing either half cannot describe an alias and is skipped")
}

// TestPathAliasesDoNotContradictMappings pins the duplication recorded in
// Decision 1: after seeding, the same W: fact lives in two config fields and
// nothing forces them to agree. Drift should fail here rather than silently
// mis-render a path in the review UI.
func TestPathAliasesDoNotContradictMappings(t *testing.T) {
	aliases := []PathAlias{{Root: "/library/books", Windows: "Z:"}}
	mappings := []ITunesPathMap{{From: "W:", To: "/library/books"}}

	err := ValidatePathAliases(aliases, mappings)

	assert.Error(t, err, "an alias claiming Z: for a root that PathMappings calls W: is a contradiction")
	assert.Contains(t, err.Error(), "/library/books")
}

func TestValidatePathAliasesAcceptsAgreement(t *testing.T) {
	aliases := []PathAlias{{Root: "/library/books", Windows: "W:", SMBURL: "smb://host/books"}}
	mappings := []ITunesPathMap{{From: "W:", To: "/library/books"}}

	assert.NoError(t, ValidatePathAliases(aliases, mappings),
		"extra fields on the alias are fine; only a differing Windows prefix is a contradiction")
}

// TestSeedPathAliasesNormalizesWindowsPrefix pins the shapes a mapping prefix
// actually arrives in. extractPathPrefixes emits a percent-encoded
// file://localhost/ URL and the Settings UI auto-populates the From field with
// it, so a verbatim copy renders
// "file://localhost/W:/itunes/iTunes%20Media\Author\Title.m4b" on the review
// page -- labelled, tooltipped and copyable as if it were authoritative.
func TestSeedPathAliasesNormalizesWindowsPrefix(t *testing.T) {
	cases := []struct {
		name        string
		from        string
		to          string
		wantWindows string
		wantRoot    string
	}{
		{"a bare drive letter is already valid", "W:", "/library/books", `W:`, "/library/books"},
		{"forward slashes become backslashes", "W:/audiobook-organizer", "/mnt/x/books", `W:\audiobook-organizer`, "/mnt/x/books"},
		{"a file url is stripped and percent-decoded", "file://localhost/W:/itunes/iTunes%20Media", "/x", `W:\itunes\iTunes Media`, "/x"},
		{"a file:/// url is stripped too", "file:///W:/itunes", "/x", `W:\itunes`, "/x"},
		{"trailing separators are trimmed on both halves", "Z:/", "/x/", `Z:`, "/x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SeedPathAliases(nil, []ITunesPathMap{{From: tc.from, To: tc.to}})
			assert.Equal(t, []PathAlias{{Root: tc.wantRoot, Windows: tc.wantWindows}}, got)
		})
	}
}

// TestSeedThenValidateIsSilent is the trap guard. Normalizing the seed without
// normalizing the validator would leave the seeded Windows value no longer
// equal to the raw From it came from, so every correctly-seeded install would
// log a contradiction at error level on every config load. Seeding and
// validating the same mappings must always agree.
func TestSeedThenValidateIsSilent(t *testing.T) {
	for _, from := range []string{
		"W:",
		"W:/audiobook-organizer",
		"file://localhost/W:/itunes/iTunes%20Media",
		"Z:/",
	} {
		t.Run(from, func(t *testing.T) {
			mappings := []ITunesPathMap{{From: from, To: "/library/books/"}}
			assert.NoError(t, ValidatePathAliases(SeedPathAliases(nil, mappings), mappings),
				"a freshly seeded alias can never contradict the mapping it was seeded from")
		})
	}
}

// TestValidatePathAliasesNormalizesRootsBeforeComparing pins the second half of
// the same missing mechanism: the root was keyed on an exact string while the
// frontend trims trailing slashes, so an alias root of "/x" against a mapping
// To of "/x/" never compared at all and the guard failed open on precisely the
// drift it exists to catch.
func TestValidatePathAliasesNormalizesRootsBeforeComparing(t *testing.T) {
	aliases := []PathAlias{{Root: "/library/books", Windows: "Z:"}}
	mappings := []ITunesPathMap{{From: "W:", To: "/library/books/"}}

	assert.Error(t, ValidatePathAliases(aliases, mappings),
		"a trailing slash on the mapping root must not hide a genuine contradiction")
}

// TestValidatePathAliasesComparesNormalizedWindows keeps the guard from firing
// on two spellings of the same prefix -- a hand-written alias and a mapping
// that merely differ in separator or encoding do not contradict each other.
func TestValidatePathAliasesComparesNormalizedWindows(t *testing.T) {
	aliases := []PathAlias{{Root: "/x", Windows: `W:\itunes\iTunes Media`}}
	mappings := []ITunesPathMap{{From: "file://localhost/W:/itunes/iTunes%20Media", To: "/x"}}

	assert.NoError(t, ValidatePathAliases(aliases, mappings),
		"the same prefix written two ways is agreement, not drift")
}

// TestNormalizeWindowsPrefixKeepsAnEncodedSlashDistinct pins that decoding
// happens per segment. Decoding the whole prefix first would turn an encoded
// literal slash into a separator indistinguishable from a real one, which also
// let the drift guard read two genuinely different prefixes as agreement.
func TestNormalizeWindowsPrefixKeepsAnEncodedSlashDistinct(t *testing.T) {
	assert.Equal(t, `W:\a/b`, normalizeWindowsPrefix("file://localhost/W:/a%2Fb"),
		"an encoded slash stays inside its segment rather than becoming a separator")
	assert.NotEqual(t, normalizeWindowsPrefix("W:/a%2Fb"), normalizeWindowsPrefix("W:/a/b"),
		"two different prefixes must not normalize to the same value")
}
