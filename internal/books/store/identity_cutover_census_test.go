// file: internal/books/store/identity_cutover_census_test.go
// version: 1.0.0
// guid: c9748fa5-c374-4f6a-a5cf-82e6c68b0b17
// last-edited: 2026-11-12

package store

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m3"
)

// The identity cutover (internal/books/cutover/m3) names HEAD's key families
// from outside this package (it is a leaf under the cutover layer and imports
// no books package), so three lists are copies that must follow this package's
// own: the families M3 moves, the families a user merge rekeys, and the
// journal prefix. These are the censuses that fail when one side moves.

// TestIdentityCutover_SourceIsTheIdentityClaims: M3's Source prefixes are
// exactly the families the ownership table rows 1-6 Serialize (the seven
// families and the six idx: sub-claims), so a family added to that list
// without M3 learning of it, or the other way round, fails here; and the
// journal prefix and the two exact bootstrap rows lie inside books' claims.
func TestIdentityCutover_SourceIsTheIdentityClaims(t *testing.T) {
	var want []string
	for p := range serializedFamilies {
		want = append(want, p)
	}
	for _, c := range serializedSubClaims {
		if c.Prefix != "" {
			want = append(want, c.Prefix)
		}
	}
	slices.Sort(want)
	if got := m3.SourcePrefixes(); !slices.Equal(got, want) {
		t.Errorf("m3.SourcePrefixes() = %v\nthe Serialized identity claims = %v", got, want)
	}
	claimed := func(key string) bool {
		for _, c := range BooksClaims() {
			if (c.Prefix != "" && strings.HasPrefix(key, c.Prefix)) || (c.Key != "" && key == c.Key) {
				return true
			}
		}
		return false
	}
	for _, k := range append(m3.SourceKeys(), m3.JournalPrefix) {
		if !claimed(k) {
			t.Errorf("%s is under no books claim", k)
		}
	}
	if m3.JournalPrefix != UserMergeJournalPrefix {
		t.Errorf("m3.JournalPrefix = %q, the merge writes %q", m3.JournalPrefix, UserMergeJournalPrefix)
	}
}

// TestIdentityCutover_RefFamiliesAreTheMergesList: Verify's referential pass
// reads the families whose rows name a user. They are the merge's: every
// per-user family it rekeys (minus the two idx: ones, which are identity's and
// deleted), and the owner-field families (playlists and collections; the
// invites and the allow-list are Source). A family the merge learns to
// rekey and the pass does not read would leave orphans uncounted.
func TestIdentityCutover_RefFamiliesAreTheMergesList(t *testing.T) {
	want := map[string]bool{"upl:": true, "col:": true}
	for _, p := range userScopedPrefixes {
		identity := false // idx:oauth:user: and idx:apikey:user: sit under Source ranges
		for _, src := range m3.SourcePrefixes() {
			identity = identity || strings.HasPrefix(p, src)
		}
		if !identity {
			want[p] = true
		}
	}
	for _, p := range userScopedExact {
		want[p] = true
	}
	got := map[string]bool{}
	for _, p := range m3.RefFamilyPrefixes() {
		got[p] = true
	}
	if !maps.Equal(got, want) {
		t.Errorf("m3.RefFamilyPrefixes() = %v\nthe merge's families = %v", slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(want)))
	}
	// Every one is a registered family or sits inside one (the stats: family
	// holds stats:user:listen_seconds:).
	for p := range got {
		inside := false
		for _, f := range KeyFamilies() {
			inside = inside || strings.HasPrefix(p, f.Prefix)
		}
		if !inside {
			t.Errorf("%s is under no registered key family", p)
		}
	}
}
