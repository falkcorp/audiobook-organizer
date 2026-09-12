// file: internal/database/pebble_store_name_index_test.go
// version: 1.0.1
// guid: f51fb8d5-ac26-4268-85ee-c4bdb523de0b
// last-edited: 2026-09-12

package database

import (
	"strconv"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// These tests pin the data-safety half of TASK-086. NormalizeAuthor collapses
// internal whitespace since 2026-09-12; index entries written before that are
// keyed by NormalizeAuthorLegacy. The tests seed such pre-change entries by
// moving an index value from its current key to its legacy key, which is
// byte-for-byte what the old code wrote.

func newNameIndexTestStore(t *testing.T) *PebbleStore {
	t.Helper()
	store, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	ps, ok := store.(*PebbleStore)
	require.True(t, ok, "setupTestDB must return a *PebbleStore")
	return ps
}

// moveToLegacyKey rewrites the index entry for name from the current key to
// the legacy key, simulating a row written before the normalization change.
func moveToLegacyKey(t *testing.T, p *PebbleStore, keyFor nameIndexKeyFunc, name string) {
	t.Helper()
	cur := []byte(keyFor(util.NormalizeAuthor(name)))
	legacy := []byte(keyFor(util.NormalizeAuthorLegacy(name)))
	require.NotEqual(t, string(cur), string(legacy), "test name must differ under the two normalizations")
	v, err := p.getValueCopy(cur)
	require.NoError(t, err)
	require.NotNil(t, v, "current key %q must exist before the move", cur)
	require.NoError(t, p.db.Delete(cur, pebble.Sync))
	require.NoError(t, p.db.Set(legacy, v, pebble.Sync))
}

func keyExists(t *testing.T, p *PebbleStore, key string) bool {
	t.Helper()
	v, err := p.getValueCopy([]byte(key))
	require.NoError(t, err)
	return v != nil
}

func TestNameIndex_NewAuthorsDedupeAcrossWhitespace(t *testing.T) {
	p := newNameIndexTestStore(t)
	a, err := p.CreateAuthor("Raymond L. Weil")
	require.NoError(t, err)
	for _, spelling := range []string{"Raymond  L.  Weil", "raymond\tl. weil", " RAYMOND L. WEIL "} {
		b, err := p.CreateAuthor(spelling)
		require.NoError(t, err)
		require.Equal(t, a.ID, b.ID, "CreateAuthor(%q) minted a duplicate", spelling)
	}
}

func TestNameIndex_LegacyAuthorKeyStillResolves(t *testing.T) {
	p := newNameIndexTestStore(t)
	a, err := p.CreateAuthor("Karen  Joy Fowler")
	require.NoError(t, err)
	moveToLegacyKey(t, p, authorNameIndexKey, a.Name)

	got, err := p.GetAuthorByName("Karen  Joy Fowler")
	require.NoError(t, err)
	require.NotNil(t, got, "a row indexed under the legacy key became unfindable")
	require.Equal(t, a.ID, got.ID)

	again, err := p.CreateAuthor("Karen  Joy Fowler")
	require.NoError(t, err)
	require.Equal(t, a.ID, again.ID, "legacy-keyed row was duplicated on the next import")
}

// The defect the collapse would introduce without ownership checks: two rows
// whose names collapse to one key, and deleting one takes the other's entry.
func TestNameIndex_DeleteDoesNotRemoveAnotherAuthorsEntry(t *testing.T) {
	p := newNameIndexTestStore(t)
	legacyRow, err := p.CreateAuthor("John  Smith")
	require.NoError(t, err)
	moveToLegacyKey(t, p, authorNameIndexKey, legacyRow.Name)
	canonical, err := p.CreateAuthor("John Smith")
	require.NoError(t, err)
	require.NotEqual(t, legacyRow.ID, canonical.ID, "setup: two rows expected")

	require.NoError(t, p.DeleteAuthor(legacyRow.ID))

	got, err := p.GetAuthorByName("John Smith")
	require.NoError(t, err)
	require.NotNil(t, got, "deleting the double-spaced row removed the canonical row's index entry")
	require.Equal(t, canonical.ID, got.ID)
	require.False(t, keyExists(t, p, authorNameIndexKey("john  smith")), "the deleted row's own legacy entry must go")
}

func TestNameIndex_RenameDoesNotRemoveAnotherAuthorsEntry(t *testing.T) {
	p := newNameIndexTestStore(t)
	legacyRow, err := p.CreateAuthor("Valery  Starsky")
	require.NoError(t, err)
	moveToLegacyKey(t, p, authorNameIndexKey, legacyRow.Name)
	canonical, err := p.CreateAuthor("Valery Starsky")
	require.NoError(t, err)

	require.NoError(t, p.UpdateAuthorName(legacyRow.ID, "Someone Else"))

	got, err := p.GetAuthorByName("Valery Starsky")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, canonical.ID, got.ID, "renaming the double-spaced row removed the canonical row's entry")
	renamed, err := p.GetAuthorByName("someone else")
	require.NoError(t, err)
	require.NotNil(t, renamed)
	require.Equal(t, legacyRow.ID, renamed.ID)
	require.False(t, keyExists(t, p, authorNameIndexKey("valery  starsky")))
}

func TestNameIndex_LegacySeriesKeyAndGuardedDelete(t *testing.T) {
	p := newNameIndexTestStore(t)
	author, err := p.CreateAuthor("Series Author")
	require.NoError(t, err)
	scope := seriesNameIndexKey(strconv.Itoa(author.ID))

	legacySeries, err := p.CreateSeries("The  Long Earth", &author.ID)
	require.NoError(t, err)
	moveToLegacyKey(t, p, scope, legacySeries.Name)

	got, err := p.GetSeriesByName("The  Long Earth", &author.ID)
	require.NoError(t, err)
	require.NotNil(t, got, "legacy-keyed series became unfindable")
	require.Equal(t, legacySeries.ID, got.ID)
	again, err := p.CreateSeries("The  Long Earth", &author.ID)
	require.NoError(t, err)
	require.Equal(t, legacySeries.ID, again.ID, "legacy-keyed series was duplicated")

	canonical, err := p.CreateSeries("The Long Earth", &author.ID)
	require.NoError(t, err)
	require.NotEqual(t, legacySeries.ID, canonical.ID)
	require.NoError(t, p.DeleteSeries(legacySeries.ID))
	got, err = p.GetSeriesByName("The Long Earth", &author.ID)
	require.NoError(t, err)
	require.NotNil(t, got, "deleting the double-spaced series removed the canonical series' entry")
	require.Equal(t, canonical.ID, got.ID)
}

func TestNameIndex_LegacyNarratorAndAliasKeysResolve(t *testing.T) {
	p := newNameIndexTestStore(t)
	n, err := p.CreateNarrator("Ray  Porter")
	require.NoError(t, err)
	moveToLegacyKey(t, p, narratorNameIndexKey, n.Name)
	gotN, err := p.GetNarratorByName("Ray  Porter")
	require.NoError(t, err)
	require.NotNil(t, gotN, "legacy-keyed narrator became unfindable")
	require.Equal(t, n.ID, gotN.ID)
	againN, err := p.CreateNarrator("Ray  Porter")
	require.NoError(t, err)
	require.Equal(t, n.ID, againN.ID, "legacy-keyed narrator was duplicated")

	author, err := p.CreateAuthor("Alias Owner")
	require.NoError(t, err)
	alias, err := p.CreateAuthorAlias(author.ID, "A.  Owner", "pen_name")
	require.NoError(t, err)
	moveToLegacyKey(t, p, aliasNameIndexKey, alias.AliasName)
	gotA, err := p.FindAuthorByAlias("A.  Owner")
	require.NoError(t, err)
	require.NotNil(t, gotA, "legacy-keyed alias became unfindable")
	require.Equal(t, author.ID, gotA.ID)
	_, err = p.CreateAuthorAlias(author.ID, "A.  Owner", "pen_name")
	require.Error(t, err, "duplicate alias must still be refused when the existing one sits under the legacy key")
	require.NoError(t, p.DeleteAuthorAlias(alias.ID))
	require.False(t, keyExists(t, p, aliasNameIndexKey("a.  owner")), "alias delete must remove its legacy entry")
}

// DeleteNarrator took the index entry by raw key, so deleting a legacy-keyed
// "john  smith" removed the canonical "john smith" narrator's current entry
// and left its own legacy entry behind.
func TestNameIndex_DeleteNarratorDoesNotRemoveAnotherNarratorsEntry(t *testing.T) {
	p := newNameIndexTestStore(t)
	legacyRow, err := p.CreateNarrator("John  Smith")
	require.NoError(t, err)
	moveToLegacyKey(t, p, narratorNameIndexKey, legacyRow.Name)
	canonical, err := p.CreateNarrator("John Smith")
	require.NoError(t, err)
	require.NotEqual(t, legacyRow.ID, canonical.ID, "setup: two rows expected")

	require.NoError(t, p.DeleteNarrator(legacyRow.ID))

	got, err := p.GetNarratorByName("John Smith")
	require.NoError(t, err)
	require.NotNil(t, got, "deleting the double-spaced narrator removed the canonical narrator's index entry")
	require.Equal(t, canonical.ID, got.ID)
	again, err := p.CreateNarrator("John Smith")
	require.NoError(t, err)
	require.Equal(t, canonical.ID, again.ID, "canonical narrator was duplicated after the other row's delete")
	require.False(t, keyExists(t, p, narratorNameIndexKey("john  smith")), "the deleted narrator's own legacy entry must go")
}

// Roles and playlists are not person names: their indexes keep the plain
// trim+lowercase key and are unaffected by the collapse.
func TestNameIndex_RoleAndPlaylistLookupsKeepTheirOwnKey(t *testing.T) {
	p := newNameIndexTestStore(t)
	role, err := p.CreateRole(&Role{Name: "Library  Curator", Permissions: []string{}})
	require.NoError(t, err)
	gotR, err := p.GetRoleByName("Library  Curator")
	require.NoError(t, err)
	require.NotNil(t, gotR, "double-spaced role name became unfindable")
	require.Equal(t, role.ID, gotR.ID)
	require.NoError(t, p.DeleteRole(role.ID))
	require.False(t, keyExists(t, p, "idx:role:name:library  curator"), "role delete must remove its index entry")

	pl, err := p.CreateUserPlaylist(&UserPlaylist{Name: "My  List", Type: "static"})
	require.NoError(t, err)
	gotP, err := p.GetUserPlaylistByName("My  List")
	require.NoError(t, err)
	require.NotNil(t, gotP, "double-spaced playlist name became unfindable")
	require.Equal(t, pl.ID, gotP.ID)
}
