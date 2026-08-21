// file: internal/config/path_alias_test.go
// version: 1.0.0
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
