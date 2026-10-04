// file: internal/database/keyfamilies_test.go
// version: 1.1.0
// guid: 8b2a931b-6ce8-4a3d-89ad-38f8bb08e66b
// last-edited: 2026-10-04

package database

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestKeyFamilies_NoDuplicatePrefixes(t *testing.T) {
	seen := map[string]bool{}
	for _, f := range keyFamilies {
		require.NotEmpty(t, f.Prefix)
		require.NotEmpty(t, f.Description, "family %s needs a description", f.Prefix)
		require.NotEmpty(t, f.Owner, "family %s needs an owner", f.Prefix)
		require.False(t, seen[f.Prefix], "duplicate prefix %s", f.Prefix)
		require.NotEqual(t, unregisteredFamily, f.Prefix)
		seen[f.Prefix] = true
	}
	// KeyFamilies is a copy: mutating it must not touch the registry.
	cp := KeyFamilies()
	require.Len(t, cp, len(keyFamilies))
	cp[0].Prefix = "mutated:"
	for _, f := range keyFamilies {
		require.NotEqual(t, "mutated:", f.Prefix)
	}
}

func TestKeyFamilyRanges_PartitionWholeKeyspace(t *testing.T) {
	ranges := keyFamilyRanges(keyFamilies)
	require.NotEmpty(t, ranges)
	require.Nil(t, ranges[0].Lo, "first range must start at the beginning of the key space")
	require.Nil(t, ranges[len(ranges)-1].Hi, "last range must be unbounded")
	for i := range ranges {
		if ranges[i].Hi != nil {
			require.True(t, ranges[i].Lo == nil || bytes.Compare(ranges[i].Lo, ranges[i].Hi) < 0,
				"range %d (%s) must be non-empty: [%q, %q)", i, ranges[i].Family, ranges[i].Lo, ranges[i].Hi)
		}
		if i+1 < len(ranges) {
			require.Equal(t, ranges[i].Hi, ranges[i+1].Lo, "ranges %d and %d must be contiguous", i, i+1)
		}
	}
	// Every registered family owns at least one piece.
	owned := map[string]bool{}
	for _, r := range ranges {
		owned[r.Family] = true
	}
	for _, f := range keyFamilies {
		require.True(t, owned[f.Prefix], "family %s has no range", f.Prefix)
	}
	require.True(t, owned[unregisteredFamily])
}

func TestKeyFamilyRanges_ParentExcludesChildren(t *testing.T) {
	ranges := keyFamilyRanges(keyFamilies)
	cases := map[string]string{
		"book:asin:x":                       "book:asin:",
		"book:01ABC":                        "book:",
		"book:":                             "book:",
		"book;":                             unregisteredFamily,
		"opv2:log:x":                        "opv2:log:",
		"opv2:open:x":                       "opv2:open:",
		"opv2:op:01X":                       "opv2:op:",
		"opv2:zzz":                          "opv2:",
		"zzz:":                              unregisteredFamily,
		"":                                  unregisteredFamily,
		"\xff\xff":                          unregisteredFamily,
		"act:info:000:1":                    "act:info:",
		"act:x":                             "act:",
		"book_file:1:2":                     "book_file:",
		"book_file_acoustid:abc":            "book_file_acoustid:",
		"author:name:smith":                 "author:name:",
		"author:12":                         "author:",
		"sync_alias_use_seeded:u":           "sync_alias_use_seeded:",
		"system:backfill:x":                 "system:backfill:",
		"system:flag:y":                     "system:",
		"chapters:01ABC":                    "chapters:",
		"narrator_counter":                  "narrator_counter",
		"sync_alias_use_seed_cutoff":        "sync_alias_use_seed_cutoff",
		"merge:combine-journal:01X":         "merge:combine-journal:",
		"scanner:ai_parse_single_fail:ab12": "scanner:ai_parse_single_fail:",
		"pref:_system:outbox:writeback:B1":  "pref:_system:outbox:writeback:",
		"pref:_system:other":                "pref:_system:",
		"pref:u1:theme":                     "pref:",
		"setting:repairs_last_plan_op:fx":   "setting:repairs_last_plan_op:",
	}
	for key, want := range cases {
		require.Equal(t, want, familyForKey(ranges, []byte(key)), "key %q", key)
	}
}

func TestKeyFamilyRanges_SmallRegistry(t *testing.T) {
	fams := []KeyFamily{{Prefix: "b:"}, {Prefix: "a:"}, {Prefix: "a:x:"}}
	got := keyFamilyRanges(fams)
	want := []keyRange{
		{unregisteredFamily, nil, []byte("a:")},
		{"a:", []byte("a:"), []byte("a:x:")},
		{"a:x:", []byte("a:x:"), []byte("a:x;")},
		{"a:", []byte("a:x;"), []byte("a;")},
		{unregisteredFamily, []byte("a;"), []byte("b:")},
		{"b:", []byte("b:"), []byte("b;")},
		{unregisteredFamily, []byte("b;"), nil},
	}
	require.Equal(t, want, got)
}

func TestKeyFamilies_A5IndexesPreRegistered(t *testing.T) {
	have := map[string]bool{}
	for _, f := range KeyFamilies() {
		have[f.Prefix] = true
	}
	require.True(t, have["opv2:open:"])
	require.True(t, have["opv2:done:"])
}
