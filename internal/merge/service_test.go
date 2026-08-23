// file: internal/merge/service_test.go
// version: 1.2.0
// guid: 9b3d7e21-4a6c-4f08-8e15-7c2a9d4b6e30
// last-edited: 2026-08-23

package merge

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/dbtest"
	ulid "github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestStore(t *testing.T) database.Store {
	t.Helper()
	pebblePath := filepath.Join(t.TempDir(), "pebble")
	store, err := database.NewPebbleStore(pebblePath)
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	origStore := database.GetGlobalStore()
	database.SetGlobalStore(store)
	t.Cleanup(func() {
		database.SetGlobalStore(origStore)
		store.Close()
	})
	return store
}

func TestService_MergeBooks(t *testing.T) {
	store := setupTestStore(t)

	book1 := &database.Book{
		ID:       ulid.Make().String(),
		Title:    "Test Book MP3",
		Format:   "mp3",
		FilePath: "/tmp/test1.mp3",
	}
	book2 := &database.Book{
		ID:       ulid.Make().String(),
		Title:    "Test Book M4B",
		Format:   "m4b",
		FilePath: "/tmp/test2.m4b",
	}

	_, err := store.CreateBook(book1)
	require.NoError(t, err)
	_, err = store.CreateBook(book2)
	require.NoError(t, err)

	ms := NewService(store)
	result, err := ms.MergeBooks([]string{book1.ID, book2.ID}, "")
	require.NoError(t, err)

	assert.Equal(t, 2, result.MergedCount)
	assert.NotEmpty(t, result.VersionGroupID)
	// M4B should be selected as primary since it's the preferred format
	assert.Equal(t, book2.ID, result.PrimaryID)

	// Verify books in database
	b1, err := store.GetBookByID(book1.ID)
	require.NoError(t, err)
	require.NotNil(t, b1.VersionGroupID)
	assert.Equal(t, result.VersionGroupID, *b1.VersionGroupID)
	require.NotNil(t, b1.IsPrimaryVersion)
	assert.False(t, *b1.IsPrimaryVersion)

	b2, err := store.GetBookByID(book2.ID)
	require.NoError(t, err)
	require.NotNil(t, b2.VersionGroupID)
	assert.Equal(t, result.VersionGroupID, *b2.VersionGroupID)
	require.NotNil(t, b2.IsPrimaryVersion)
	assert.True(t, *b2.IsPrimaryVersion)

	// Data-loss invariant: a merge must never leave the store inconsistent
	// (e.g. a book both live-primary and soft-deleted, or a survivor vanished).
	dbtest.AssertStoreInvariants(t, store)
}

func TestService_MergeBooks_ExplicitPrimary(t *testing.T) {
	store := setupTestStore(t)

	book1 := &database.Book{
		ID:       ulid.Make().String(),
		Title:    "Book A",
		Format:   "mp3",
		FilePath: "/tmp/a.mp3",
	}
	book2 := &database.Book{
		ID:       ulid.Make().String(),
		Title:    "Book B",
		Format:   "m4b",
		FilePath: "/tmp/b.m4b",
	}

	_, err := store.CreateBook(book1)
	require.NoError(t, err)
	_, err = store.CreateBook(book2)
	require.NoError(t, err)

	ms := NewService(store)
	// Force MP3 as primary even though M4B would normally win
	result, err := ms.MergeBooks([]string{book1.ID, book2.ID}, book1.ID)
	require.NoError(t, err)

	assert.Equal(t, book1.ID, result.PrimaryID)
}

func TestService_MergeBooks_TooFew(t *testing.T) {
	ms := NewService(nil)
	_, err := ms.MergeBooks([]string{"one"}, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least 2")
}

// TestService_MergeBooks_SoftDeletesLosers verifies the
// post-2026-04-11 merge semantics: losers get soft-deleted
// (MarkedForDeletion=true) after merge.
func TestService_MergeBooks_SoftDeletesLosers(t *testing.T) {
	store := setupTestStore(t)

	book1 := &database.Book{
		ID:       ulid.Make().String(),
		Title:    "Loser MP3",
		Format:   "mp3",
		FilePath: "/tmp/loser.mp3",
	}
	book2 := &database.Book{
		ID:       ulid.Make().String(),
		Title:    "Winner M4B",
		Format:   "m4b",
		FilePath: "/tmp/winner.m4b",
	}

	_, err := store.CreateBook(book1)
	require.NoError(t, err)
	_, err = store.CreateBook(book2)
	require.NoError(t, err)

	ms := NewService(store)
	result, err := ms.MergeBooks([]string{book1.ID, book2.ID}, "")
	require.NoError(t, err)
	require.Equal(t, book2.ID, result.PrimaryID, "M4B should auto-win")

	// Winner is NOT soft-deleted.
	winner, err := store.GetBookByID(book2.ID)
	require.NoError(t, err)
	require.NotNil(t, winner)
	require.NotNil(t, winner.IsPrimaryVersion)
	assert.True(t, *winner.IsPrimaryVersion)
	if winner.MarkedForDeletion != nil {
		assert.False(t, *winner.MarkedForDeletion, "winner must not be soft-deleted")
	}

	// Loser IS soft-deleted.
	loser, err := store.GetBookByID(book1.ID)
	require.NoError(t, err)
	require.NotNil(t, loser)
	require.NotNil(t, loser.IsPrimaryVersion)
	assert.False(t, *loser.IsPrimaryVersion)
	require.NotNil(t, loser.MarkedForDeletion, "loser must be soft-deleted")
	assert.True(t, *loser.MarkedForDeletion)
	require.NotNil(t, loser.MarkedForDeletionAt, "loser must have deletion timestamp")
	assert.WithinDuration(t, time.Now(), *loser.MarkedForDeletionAt, 5*time.Second)
	require.NotNil(t, loser.VersionGroupID)
	assert.Equal(t, result.VersionGroupID, *loser.VersionGroupID)
}

// TestService_MergeBooks_PrefersCuratedOverPristine verifies that a
// curated book wins the primary slot over a pristine duplicate.
func TestService_MergeBooks_PrefersCuratedOverPristine(t *testing.T) {
	store := setupTestStore(t)

	pristineM4B := &database.Book{
		ID:       ulid.Make().String(),
		Title:    "Foundation and Empire",
		Format:   "m4b",
		FilePath: "/mnt/bigdata/books/audiobook-organizer/asimov/foundation-and-empire.m4b",
	}
	highBitrate := 192
	pristineM4B.Bitrate = &highBitrate

	curatedMP3 := &database.Book{
		ID:       ulid.Make().String(),
		Title:    "Foundation and Empire",
		Format:   "mp3",
		FilePath: "/mnt/bigdata/books/audiobook-organizer/asimov/foundation-and-empire.mp3",
	}
	lowBitrate := 64
	curatedMP3.Bitrate = &lowBitrate

	_, err := store.CreateBook(pristineM4B)
	require.NoError(t, err)
	_, err = store.CreateBook(curatedMP3)
	require.NoError(t, err)

	matched := "matched"
	curatedMP3.MetadataReviewStatus = &matched
	_, err = store.UpdateBook(curatedMP3.ID, curatedMP3)
	require.NoError(t, err)
	require.NoError(t, store.SetLastWrittenAt(curatedMP3.ID, time.Now()))

	ms := NewService(store)
	result, err := ms.MergeBooks([]string{pristineM4B.ID, curatedMP3.ID}, "")
	require.NoError(t, err)

	assert.Equal(t, curatedMP3.ID, result.PrimaryID,
		"curated MP3 should beat pristine M4B — user's work is the strongest signal")
}

func TestBookCurationScore(t *testing.T) {
	matched := "matched"
	noMatch := "no_match"
	now := time.Now()
	earlier := now.Add(-1 * time.Hour)

	cases := []struct {
		name string
		book *database.Book
		want int
	}{
		{"empty", &database.Book{}, 0},
		{"matched only", &database.Book{MetadataReviewStatus: &matched}, 1},
		{"no_match does not count", &database.Book{MetadataReviewStatus: &noMatch}, 0},
		{"last written only", &database.Book{LastWrittenAt: &now}, 1},
		{
			"metadata edited after create",
			&database.Book{CreatedAt: &earlier, MetadataUpdatedAt: &now},
			1,
		},
		{
			"metadata edited at same time as create does not count",
			&database.Book{CreatedAt: &now, MetadataUpdatedAt: &now},
			0,
		},
		{
			"fully curated",
			&database.Book{
				MetadataReviewStatus: &matched,
				LastWrittenAt:        &now,
				CreatedAt:            &earlier,
				MetadataUpdatedAt:    &now,
			},
			3,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, BookCurationScore(tc.book))
		})
	}
}

func TestService_MergeBooks_PrefersOrganizedOverITunesGhost(t *testing.T) {
	store := setupTestStore(t)

	ghost := &database.Book{
		ID:       ulid.Make().String(),
		Title:    "Foundation and Empire",
		Format:   "m4b",
		FilePath: "/mnt/bigdata/books/itunes/iTunes Media/Audiobooks/Isaac Asimov/Foundation and Empire.m4b",
	}
	bitrate := 128
	ghost.Bitrate = &bitrate

	organized := &database.Book{
		ID:       ulid.Make().String(),
		Title:    "Foundation and Empire",
		Format:   "mp3",
		FilePath: "/mnt/bigdata/books/audiobook-organizer/Isaac Asimov/Foundation and Empire/Foundation and Empire.mp3",
	}
	lowBitrate := 64
	organized.Bitrate = &lowBitrate

	_, err := store.CreateBook(ghost)
	require.NoError(t, err)
	_, err = store.CreateBook(organized)
	require.NoError(t, err)

	ms := NewService(store)
	result, err := ms.MergeBooks([]string{ghost.ID, organized.ID}, "")
	require.NoError(t, err)

	assert.Equal(t, organized.ID, result.PrimaryID,
		"organized library path should beat iTunes ghost regardless of format")
}

func TestIsITunesGhostPath(t *testing.T) {
	cases := []struct {
		name string
		path string
		want bool
	}{
		{"itunes media absolute", "/mnt/bigdata/books/itunes/iTunes Media/Audiobooks/x.m4b", true},
		{"itunes media mixed case", "/mnt/bigdata/books/iTunes/iTunes Media/x.m4b", true},
		{"organized library", "/mnt/bigdata/books/audiobook-organizer/author/book.mp3", false},
		{"empty", "", false},
		{"relative", "itunes/iTunes Media/x.m4b", true},
		{"generic linux tmp", "/tmp/x.mp3", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, IsITunesGhostPath(tc.path))
		})
	}
}

func TestService_MergeBooks_NotFound(t *testing.T) {
	store := setupTestStore(t)

	ms := NewService(store)
	_, err := ms.MergeBooks([]string{"nonexistent1", "nonexistent2"}, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// --- VG-DOUBLE-PRIMARY (TASK-042) -------------------------------------------
//
// A merge that REUSES an existing version group's ID used to write the
// primary/non-primary flag only for the books in its own argument slice, so a
// member that joined the group in a PRIOR merge and is absent from this call
// kept its is_primary_version=true and the group ended up with TWO primaries.
// Downstream every reader assumes "primary" is unique, so the group surfaces
// as duplicate books, wrong counts and wrong dedup decisions.

// effectivePrimary reports whether a stored flag counts as primary. nil is NOT
// false anywhere in this codebase: pebble_store.go and memdb_reads.go both use
// `IsPrimaryVersion == nil || *IsPrimaryVersion`, so an unset flag reads as
// PRIMARY. Any invariant check that treats nil as non-primary is vacuous.
func effectivePrimary(v *bool) bool { return v == nil || *v }

// versionGroupsWithMultiplePrimaries is the invariant scan the item asked for:
// walk every version group in the store and report the ones with more than one
// effectively-primary live member. Returns one string per violating group.
//
// Scoped to "more than one" on purpose. The zero-primary half of the invariant
// is reconcile.ElectMissingPrimaries' territory and a group can legitimately be
// mid-repair; two primaries is never legitimate.
func versionGroupsWithMultiplePrimaries(t *testing.T, store database.Store) []string {
	t.Helper()
	cores, err := store.GetAllBooksCore(0, 0)
	require.NoError(t, err)

	groups := map[string]bool{}
	for i := range cores {
		if cores[i].VersionGroupID != nil && *cores[i].VersionGroupID != "" {
			groups[*cores[i].VersionGroupID] = true
		}
	}

	var violations []string
	for gid := range groups {
		members, err := store.GetBooksByVersionGroup(gid)
		require.NoError(t, err)
		var primaries []string
		for i := range members {
			if effectivePrimary(members[i].IsPrimaryVersion) {
				primaries = append(primaries, members[i].ID)
			}
		}
		if len(primaries) > 1 {
			violations = append(violations,
				fmt.Sprintf("version group %s has %d primaries: %v", gid, len(primaries), primaries))
		}
	}
	return violations
}

// TestVersionGroupPrimaryInvariant_DetectsDoublePrimary proves the invariant
// scan above is not vacuous: hand-build the exact prod shape (one group, two
// members both flagged primary) and require that the scan reports it.
func TestVersionGroupPrimaryInvariant_DetectsDoublePrimary(t *testing.T) {
	store := setupTestStore(t)
	groupID := ulid.Make().String()
	yes := true

	for i, title := range []string{"Double A", "Double B"} {
		_, err := store.CreateBook(&database.Book{
			ID:               ulid.Make().String(),
			Title:            title,
			Format:           "mp3",
			FilePath:         filepath.Join(t.TempDir(), fmt.Sprintf("dbl%d.mp3", i)),
			VersionGroupID:   &groupID,
			IsPrimaryVersion: &yes,
		})
		require.NoError(t, err)
	}

	violations := versionGroupsWithMultiplePrimaries(t, store)
	require.Len(t, violations, 1, "invariant scan must flag the seeded double-primary group")
	assert.Contains(t, violations[0], groupID)
}

// seedGroupMember creates a live book already carrying groupID with the given
// stored primary flag (pass nil to reproduce the unset-flag shape).
func seedGroupMember(t *testing.T, store database.Store, groupID, title, format string, primary *bool) *database.Book {
	t.Helper()
	b := &database.Book{
		ID:               ulid.Make().String(),
		Title:            title,
		Format:           format,
		FilePath:         filepath.Join(t.TempDir(), title+"."+format),
		VersionGroupID:   &groupID,
		IsPrimaryVersion: primary,
	}
	_, err := store.CreateBook(b)
	require.NoError(t, err)
	return b
}

// countGroupPrimaries returns the IDs of every effectively-primary LIVE member
// of groupID. The assertion that matters is this COUNT, not "the survivor is
// primary" — the latter passes just as well in the broken double-primary state.
func countGroupPrimaries(t *testing.T, store database.Store, groupID string) []string {
	t.Helper()
	members, err := store.GetBooksByVersionGroup(groupID)
	require.NoError(t, err)
	var primaries []string
	for i := range members {
		if effectivePrimary(members[i].IsPrimaryVersion) {
			primaries = append(primaries, members[i].ID)
		}
	}
	return primaries
}

func TestService_MergeBooks_DemotesPreExistingGroupMember(t *testing.T) {
	store := setupTestStore(t)
	groupID := ulid.Make().String()
	yes, no := true, false

	// Pre-existing group from a prior merge: A is its primary, B is not.
	// A is deliberately NOT part of the merge call below — that is the whole
	// case this fix exists for.
	bookA := seedGroupMember(t, store, groupID, "PriorPrimary", "mp3", &yes)
	bookB := seedGroupMember(t, store, groupID, "PriorMember", "mp3", &no)

	// Guard against a vacuous pass: if the seeded group were not visible to
	// GetBooksByVersionGroup, nothing would be demoted and the count assertion
	// below would pass for the wrong reason.
	require.Len(t, countGroupPrimaries(t, store, groupID), 1, "seed: exactly A is primary")
	members, err := store.GetBooksByVersionGroup(groupID)
	require.NoError(t, err)
	require.Len(t, members, 2, "seed: both members must be visible in the group index")

	// A third book with no group of its own. M4B beats MP3 under BookIsBetter,
	// so the NEW book wins the election, not the pre-existing primary.
	bookC := &database.Book{
		ID:       ulid.Make().String(),
		Title:    "NewCandidate",
		Format:   "m4b",
		FilePath: filepath.Join(t.TempDir(), "new.m4b"),
	}
	_, err = store.CreateBook(bookC)
	require.NoError(t, err)

	ms := NewService(store)
	result, err := ms.MergeBooks([]string{bookB.ID, bookC.ID}, "")
	require.NoError(t, err)
	require.Equal(t, groupID, result.VersionGroupID, "merge must reuse B's existing group")
	require.Equal(t, bookC.ID, result.PrimaryID, "m4b candidate wins BookIsBetter")

	// THE assertion: exactly one primary in the group, and it is the winner.
	// B is soft-deleted as a merge loser so it drops out of the live listing;
	// A stays live and must have been demoted.
	primaries := countGroupPrimaries(t, store, groupID)
	require.Len(t, primaries, 1, "version group must have exactly one primary after merge")
	assert.Equal(t, bookC.ID, primaries[0])

	// Assert on the STORED value, not on Go-side truthiness.
	storedA, err := store.GetBookByID(bookA.ID)
	require.NoError(t, err)
	require.NotNil(t, storedA.IsPrimaryVersion, "pre-existing member must store an explicit flag, not nil")
	assert.False(t, *storedA.IsPrimaryVersion, "pre-existing primary must be demoted")

	storedC, err := store.GetBookByID(bookC.ID)
	require.NoError(t, err)
	require.NotNil(t, storedC.IsPrimaryVersion)
	assert.True(t, *storedC.IsPrimaryVersion)

	assert.Empty(t, versionGroupsWithMultiplePrimaries(t, store))
	dbtest.AssertStoreInvariants(t, store)
}

// TestService_MergeBooks_DemotesPreExistingGroupMemberWithNilFlag covers the
// nil case explicitly: a pre-existing member whose is_primary_version is unset
// reads as PRIMARY everywhere, so leaving it nil is the same corruption as
// leaving it true. It must end up storing an explicit false.
func TestService_MergeBooks_DemotesPreExistingGroupMemberWithNilFlag(t *testing.T) {
	store := setupTestStore(t)
	groupID := ulid.Make().String()
	no := false

	bookNil := seedGroupMember(t, store, groupID, "NilFlagMember", "mp3", nil)
	bookB := seedGroupMember(t, store, groupID, "PriorMember", "mp3", &no)

	require.Nil(t, bookNil.IsPrimaryVersion)
	require.Len(t, countGroupPrimaries(t, store, groupID), 1,
		"seed: the nil-flag member already counts as primary")

	bookC := &database.Book{
		ID:       ulid.Make().String(),
		Title:    "NewCandidateNil",
		Format:   "m4b",
		FilePath: filepath.Join(t.TempDir(), "newnil.m4b"),
	}
	_, err := store.CreateBook(bookC)
	require.NoError(t, err)

	ms := NewService(store)
	result, err := ms.MergeBooks([]string{bookB.ID, bookC.ID}, "")
	require.NoError(t, err)
	require.Equal(t, groupID, result.VersionGroupID)

	primaries := countGroupPrimaries(t, store, groupID)
	require.Len(t, primaries, 1, "nil-flag member must not remain effectively primary")
	assert.Equal(t, bookC.ID, primaries[0])

	stored, err := store.GetBookByID(bookNil.ID)
	require.NoError(t, err)
	require.NotNil(t, stored.IsPrimaryVersion,
		"nil is read as primary; the demotion must store an explicit false")
	assert.False(t, *stored.IsPrimaryVersion)

	assert.Empty(t, versionGroupsWithMultiplePrimaries(t, store))
	dbtest.AssertStoreInvariants(t, store)
}

// TestService_MergeBooks_ExplicitPrimaryStillWinsOverPreExisting is the
// known-good-input safeguard: the new demotion must not override the caller's
// explicit primaryID, and must not demote the winner into a zero-primary group
// when the winner is itself a pre-existing member of the reused group.
func TestService_MergeBooks_ExplicitPrimaryStillWinsOverPreExisting(t *testing.T) {
	store := setupTestStore(t)
	groupID := ulid.Make().String()
	yes, no := true, false

	bookA := seedGroupMember(t, store, groupID, "PriorPrimaryExplicit", "mp3", &yes)
	bookB := seedGroupMember(t, store, groupID, "PriorMemberExplicit", "m4b", &no)

	bookC := &database.Book{
		ID:       ulid.Make().String(),
		Title:    "OutsiderExplicit",
		Format:   "m4b",
		FilePath: filepath.Join(t.TempDir(), "outsider.m4b"),
	}
	_, err := store.CreateBook(bookC)
	require.NoError(t, err)

	// B is a pre-existing member AND the elected winner: the demotion loop
	// must skip it via both guards, not blank the group's only primary.
	ms := NewService(store)
	result, err := ms.MergeBooks([]string{bookB.ID, bookC.ID}, bookB.ID)
	require.NoError(t, err)
	require.Equal(t, bookB.ID, result.PrimaryID)
	require.Equal(t, groupID, result.VersionGroupID)

	primaries := countGroupPrimaries(t, store, groupID)
	require.Len(t, primaries, 1, "explicit primary must be the group's only primary")
	assert.Equal(t, bookB.ID, primaries[0])

	storedA, err := store.GetBookByID(bookA.ID)
	require.NoError(t, err)
	require.NotNil(t, storedA.IsPrimaryVersion)
	assert.False(t, *storedA.IsPrimaryVersion)

	assert.Empty(t, versionGroupsWithMultiplePrimaries(t, store))
	dbtest.AssertStoreInvariants(t, store)
}
