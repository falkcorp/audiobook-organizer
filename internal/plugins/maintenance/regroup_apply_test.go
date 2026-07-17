// file: internal/plugins/maintenance/regroup_apply_test.go
// version: 1.1.0
// guid: a9d3f1c7-6b40-4e28-8f95-2c1e7b0a4d63
// last-edited: 2026-07-17

package maintenance

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/dbtest"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	ulid "github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newApplyTestStore(t *testing.T) database.Store {
	t.Helper()
	s, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// minID mirrors pickPrimary — the smallest ID is the survivor CombineBooks keeps.
func minID(ids ...string) string {
	m := ids[0]
	for _, id := range ids[1:] {
		if id < m {
			m = id
		}
	}
	return m
}

func multidiscItem(t *testing.T, folder string, memberIDs []string) database.ReviewItem {
	t.Helper()
	payload, err := json.Marshal(regroupPayload{
		Folder:         folder,
		MemberBookIDs:  memberIDs,
		ProposedAction: "collapse single-file books into one",
		SurvivorTitle:  "Survivor",
		Confidence:     "high",
	})
	require.NoError(t, err)
	return database.ReviewItem{
		ID:        ulid.Make().String(),
		Kind:      "regroup.multidisc",
		FolderRef: folder,
		Payload:   string(payload),
	}
}

func versionGroupItem(t *testing.T, folder string, memberIDs []string) database.ReviewItem {
	t.Helper()
	payload, err := json.Marshal(regroupPayload{
		Folder:         folder,
		MemberBookIDs:  memberIDs,
		ProposedAction: "link editions as a version group",
		SurvivorTitle:  "Survivor",
		Confidence:     "high",
	})
	require.NoError(t, err)
	return database.ReviewItem{
		ID:        ulid.Make().String(),
		Kind:      "regroup.version-group",
		FolderRef: folder,
		Payload:   string(payload),
	}
}

// TestApplyMultidisc_CollapsesAndPreservesData is the core B2 invariant: collapsing
// a multidisc folder must (a) leave exactly ONE book owning all files, (b) preserve
// every file's AcoustIDFingerprint through the move (the memdb-roundtrip wipe class),
// and (c) leave the survivor's author link intact (CombineBooks with a nil override
// must never rewrite the survivor row).
func TestApplyMultidisc_CollapsesAndPreservesData(t *testing.T) {
	store := newApplyTestStore(t)

	// Three single-file books, each with a real fingerprinted BookFile.
	ids := []string{ulid.Make().String(), ulid.Make().String(), ulid.Make().String()}
	fpByPath := map[string][]byte{}
	for i, id := range ids {
		path := "/lib/folder/ch0" + string(rune('1'+i)) + ".mp3"
		_, err := store.CreateBook(&database.Book{ID: id, Title: "Chapter", Format: "mp3", FilePath: path})
		require.NoError(t, err)
		fp := []byte("fp-" + id)
		fpByPath[path] = fp
		require.NoError(t, store.CreateBookFile(&database.BookFile{
			ID: ulid.Make().String(), BookID: id, FilePath: path, Format: "mp3",
			AcoustIDFingerprint: fp,
		}))
	}

	// The survivor (smallest ULID) gets an author link AND a series link, both of
	// which must survive the combine (Author/Series is the plan's named wipe class).
	primaryID := minID(ids...)
	author, err := store.CreateAuthor("Test Author")
	require.NoError(t, err)
	require.NoError(t, store.SetBookAuthors(primaryID, []database.BookAuthor{
		{BookID: primaryID, AuthorID: author.ID, Role: "author", Position: 0},
	}))
	series, err := store.CreateSeries("Test Series", &author.ID)
	require.NoError(t, err)
	primaryBook, err := store.GetBookByID(primaryID)
	require.NoError(t, err)
	primaryBook.SeriesID = &series.ID
	_, err = store.UpdateBook(primaryID, primaryBook)
	require.NoError(t, err)

	apply := ApplyMultidisc(store, merge.NewService(store))
	require.NoError(t, apply(context.Background(), multidiscItem(t, "/lib/folder", ids)))

	// Exactly one book survives, and it is the deterministic primary.
	for _, id := range ids {
		b, _ := store.GetBookByID(id)
		if id == primaryID {
			require.NotNil(t, b, "survivor must remain")
		} else {
			assert.Nil(t, b, "absorbed book %s must be hard-deleted", id)
		}
	}

	// Survivor owns all three files, and EVERY fingerprint survived the move.
	files, err := store.GetBookFiles(primaryID)
	require.NoError(t, err)
	require.Len(t, files, 3, "survivor owns all files")
	for _, f := range files {
		want, ok := fpByPath[f.FilePath]
		require.True(t, ok, "unexpected file path %s", f.FilePath)
		assert.Equal(t, want, f.AcoustIDFingerprint,
			"AcoustIDFingerprint must survive the file move (memdb-wipe guard)")
	}

	// Survivor's author link is intact (nil override never rewrote the row).
	authors, err := store.GetBookAuthors(primaryID)
	require.NoError(t, err)
	require.Len(t, authors, 1)
	assert.Equal(t, author.ID, authors[0].AuthorID)

	// Survivor's SeriesID (a Book-row field) is intact — the other half of the
	// plan's Author/Series wipe class.
	survivorBook, err := store.GetBookByID(primaryID)
	require.NoError(t, err)
	require.NotNil(t, survivorBook.SeriesID, "survivor SeriesID must survive the combine")
	assert.Equal(t, series.ID, *survivorBook.SeriesID)

	dbtest.AssertStoreInvariants(t, store)
}

// TestApplyMultidisc_AlreadyApplied_NoOp verifies a re-approve (or an approve after
// the group was already collapsed) is an idempotent no-op, never an error, when
// fewer than two members still resolve.
func TestApplyMultidisc_AlreadyApplied_NoOp(t *testing.T) {
	store := newApplyTestStore(t)

	// Only one of the two referenced members actually exists.
	survivor := ulid.Make().String()
	_, err := store.CreateBook(&database.Book{ID: survivor, Title: "Only", Format: "mp3", FilePath: "/lib/x/a.mp3"})
	require.NoError(t, err)
	missing := ulid.Make().String()

	apply := ApplyMultidisc(store, merge.NewService(store))
	require.NoError(t, apply(context.Background(), multidiscItem(t, "/lib/x", []string{survivor, missing})),
		"fewer than 2 present members must be a no-op success")

	b, _ := store.GetBookByID(survivor)
	assert.NotNil(t, b, "the lone survivor must be untouched")
}

// TestApplyVersionGroup_LinksAndPreservesData verifies the members end up sharing one
// VersionGroupID while keeping their own files/fingerprints/authors (the re-fetch-
// and-patch UpdateBook must not wipe other fields).
func TestApplyVersionGroup_LinksAndPreservesData(t *testing.T) {
	store := newApplyTestStore(t)

	ids := []string{ulid.Make().String(), ulid.Make().String()}
	author, err := store.CreateAuthor("VG Author")
	require.NoError(t, err)
	for i, id := range ids {
		path := "/lib/vg/ed" + string(rune('1'+i)) + ".mp3"
		_, err := store.CreateBook(&database.Book{ID: id, Title: "Edition", Format: "mp3", FilePath: path})
		require.NoError(t, err)
		require.NoError(t, store.CreateBookFile(&database.BookFile{
			ID: ulid.Make().String(), BookID: id, FilePath: path, Format: "mp3",
			AcoustIDFingerprint: []byte("fp-" + id),
		}))
		require.NoError(t, store.SetBookAuthors(id, []database.BookAuthor{
			{BookID: id, AuthorID: author.ID, Role: "author", Position: 0},
		}))
	}

	apply := ApplyVersionGroup(store)
	require.NoError(t, apply(context.Background(), versionGroupItem(t, "/lib/vg", ids)))

	// Both members now share one non-empty VersionGroupID, stay VISIBLE (not
	// soft-deleted — locked #8), and exactly ONE is primary.
	var vg string
	primaries := 0
	for _, id := range ids {
		b, err := store.GetBookByID(id)
		require.NoError(t, err)
		require.NotNil(t, b)
		require.NotNil(t, b.VersionGroupID, "member %s must have a VersionGroupID", id)
		require.NotEmpty(t, *b.VersionGroupID)
		if vg == "" {
			vg = *b.VersionGroupID
		} else {
			assert.Equal(t, vg, *b.VersionGroupID, "members must share the same group")
		}
		require.NotNil(t, b.IsPrimaryVersion, "member %s must have IsPrimaryVersion set", id)
		if *b.IsPrimaryVersion {
			primaries++
		}
		assert.True(t, b.MarkedForDeletion == nil || !*b.MarkedForDeletion,
			"version-group members must stay visible, never soft-deleted")

		// Files/fingerprints preserved.
		files, err := store.GetBookFiles(id)
		require.NoError(t, err)
		require.Len(t, files, 1)
		assert.Equal(t, []byte("fp-"+id), files[0].AcoustIDFingerprint)

		// Author link preserved through the UpdateBook patch.
		authors, err := store.GetBookAuthors(id)
		require.NoError(t, err)
		require.Len(t, authors, 1)
		assert.Equal(t, author.ID, authors[0].AuthorID)
	}
	assert.Equal(t, 1, primaries, "exactly one member must be the primary version")

	dbtest.AssertStoreInvariants(t, store)
}

// TestApplyVersionGroup_Idempotent verifies a second apply keeps the same group
// (does not mint a new VersionGroupID) and stays a no-op.
func TestApplyVersionGroup_Idempotent(t *testing.T) {
	store := newApplyTestStore(t)

	ids := []string{ulid.Make().String(), ulid.Make().String()}
	for i, id := range ids {
		_, err := store.CreateBook(&database.Book{
			ID: id, Title: "Edition", Format: "mp3",
			FilePath: "/lib/vg2/ed" + string(rune('1'+i)) + ".mp3",
		})
		require.NoError(t, err)
	}

	apply := ApplyVersionGroup(store)
	item := versionGroupItem(t, "/lib/vg2", ids)
	require.NoError(t, apply(context.Background(), item))

	first, err := store.GetBookByID(ids[0])
	require.NoError(t, err)
	require.NotNil(t, first.VersionGroupID)
	vg1 := *first.VersionGroupID

	require.NoError(t, apply(context.Background(), item)) // re-apply
	second, err := store.GetBookByID(ids[0])
	require.NoError(t, err)
	require.NotNil(t, second.VersionGroupID)
	assert.Equal(t, vg1, *second.VersionGroupID, "re-apply must reuse the existing group")
}

func TestPickPrimary_SmallestID(t *testing.T) {
	assert.Equal(t, "01AAAA", pickPrimary([]string{"01ZZZZ", "01AAAA", "01MMMM"}))
	assert.Equal(t, "solo", pickPrimary([]string{"solo"}))
}

// softDeleteBook marks a book soft-deleted the same way merge.Service does:
// re-fetch the full row, set only MarkedForDeletion(+At), write back.
func softDeleteBook(t *testing.T, store database.Store, id string) {
	t.Helper()
	b, err := store.GetBookByID(id)
	require.NoError(t, err)
	require.NotNil(t, b)
	deleted := true
	now := time.Now().UTC()
	b.MarkedForDeletion = &deleted
	b.MarkedForDeletionAt = &now
	_, err = store.UpdateBook(id, b)
	require.NoError(t, err)
}

// putInGroup places an existing book into a version group with the given primary
// flag via the same re-fetch-and-patch pattern the apply path uses.
func putInGroup(t *testing.T, store database.Store, id, groupID string, primary bool) {
	t.Helper()
	b, err := store.GetBookByID(id)
	require.NoError(t, err)
	require.NotNil(t, b)
	b.VersionGroupID = &groupID
	b.IsPrimaryVersion = &primary
	_, err = store.UpdateBook(id, b)
	require.NoError(t, err)
}

// TestApplyVersionGroup_SoftDeletedMembers is BUG 1 for the version-group path:
// a hold created days before apply can reference a member that was merged away
// (soft-deleted) in between. The corpse must be treated exactly like a vanished
// member — never re-linked into the group and never designated primary.
func TestApplyVersionGroup_SoftDeletedMembers(t *testing.T) {
	cases := []struct {
		name      string
		liveCount int // live members created AFTER the corpse (corpse has smallest ULID)
		wantGroup bool
	}{
		// The corpse holds the smallest ULID, so if it leaked through it WOULD be
		// chosen primary — this case proves it is skipped entirely.
		{name: "corpse skipped, two live members grouped", liveCount: 2, wantGroup: true},
		// Only one live member remains → short-circuit no-op, nothing written.
		{name: "corpse leaves fewer than two members — no-op", liveCount: 1, wantGroup: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newApplyTestStore(t)

			corpse := ulid.Make().String() // created first → smallest ULID
			_, err := store.CreateBook(&database.Book{ID: corpse, Title: "Corpse", Format: "mp3", FilePath: "/lib/sd/corpse.mp3"})
			require.NoError(t, err)
			softDeleteBook(t, store, corpse)

			live := make([]string, 0, tc.liveCount)
			for i := 0; i < tc.liveCount; i++ {
				id := ulid.Make().String()
				live = append(live, id)
				_, err := store.CreateBook(&database.Book{
					ID: id, Title: "Edition", Format: "mp3",
					FilePath: "/lib/sd/ed" + string(rune('1'+i)) + ".mp3",
				})
				require.NoError(t, err)
			}

			apply := ApplyVersionGroup(store)
			members := append([]string{corpse}, live...)
			require.NoError(t, apply(context.Background(), versionGroupItem(t, "/lib/sd", members)))

			// The corpse is untouched: still soft-deleted, never linked, never primary.
			cb, err := store.GetBookByID(corpse)
			require.NoError(t, err)
			require.NotNil(t, cb)
			require.NotNil(t, cb.MarkedForDeletion)
			assert.True(t, *cb.MarkedForDeletion, "corpse must stay soft-deleted")
			assert.Nil(t, cb.VersionGroupID, "corpse must NOT be re-linked into the group")
			assert.True(t, cb.IsPrimaryVersion == nil || !*cb.IsPrimaryVersion,
				"corpse must never be designated primary")

			for _, id := range live {
				b, err := store.GetBookByID(id)
				require.NoError(t, err)
				require.NotNil(t, b)
				if tc.wantGroup {
					require.NotNil(t, b.VersionGroupID, "live member %s must be grouped", id)
					require.NotEmpty(t, *b.VersionGroupID)
				} else {
					assert.Nil(t, b.VersionGroupID, "no-op case must not write any member")
				}
			}

			dbtest.AssertStoreInvariants(t, store)
		})
	}
}

// TestApplyMultidisc_SoftDeletedMembers is BUG 1 for the multidisc path: a
// soft-deleted member must never be fed to CombineBooks — its files must not be
// moved onto a survivor, and it counts as absent for the <2-members short-circuit.
func TestApplyMultidisc_SoftDeletedMembers(t *testing.T) {
	cases := []struct {
		name        string
		liveCount   int
		wantCombine bool
	}{
		{name: "corpse skipped, two live members combined", liveCount: 2, wantCombine: true},
		{name: "corpse leaves fewer than two members — no-op", liveCount: 1, wantCombine: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newApplyTestStore(t)

			corpse := ulid.Make().String() // smallest ULID — would be picked survivor if it leaked
			_, err := store.CreateBook(&database.Book{ID: corpse, Title: "Corpse", Format: "mp3", FilePath: "/lib/md/corpse.mp3"})
			require.NoError(t, err)
			require.NoError(t, store.CreateBookFile(&database.BookFile{
				ID: ulid.Make().String(), BookID: corpse, FilePath: "/lib/md/corpse.mp3", Format: "mp3",
				AcoustIDFingerprint: []byte("fp-corpse"),
			}))
			softDeleteBook(t, store, corpse)

			live := make([]string, 0, tc.liveCount)
			for i := 0; i < tc.liveCount; i++ {
				id := ulid.Make().String()
				live = append(live, id)
				path := "/lib/md/ch" + string(rune('1'+i)) + ".mp3"
				_, err := store.CreateBook(&database.Book{ID: id, Title: "Chapter", Format: "mp3", FilePath: path})
				require.NoError(t, err)
				require.NoError(t, store.CreateBookFile(&database.BookFile{
					ID: ulid.Make().String(), BookID: id, FilePath: path, Format: "mp3",
					AcoustIDFingerprint: []byte("fp-" + id),
				}))
			}

			apply := ApplyMultidisc(store, merge.NewService(store))
			members := append([]string{corpse}, live...)
			require.NoError(t, apply(context.Background(), multidiscItem(t, "/lib/md", members)))

			// The corpse row still exists (soft-deleted, NOT hard-deleted by the
			// combine) and still owns its own file.
			cb, err := store.GetBookByID(corpse)
			require.NoError(t, err)
			require.NotNil(t, cb, "corpse must not be absorbed/hard-deleted")
			require.NotNil(t, cb.MarkedForDeletion)
			assert.True(t, *cb.MarkedForDeletion)
			corpseFiles, err := store.GetBookFiles(corpse)
			require.NoError(t, err)
			assert.Len(t, corpseFiles, 1, "corpse's files must not be moved onto a survivor")

			if tc.wantCombine {
				survivor := minID(live...)
				files, err := store.GetBookFiles(survivor)
				require.NoError(t, err)
				assert.Len(t, files, tc.liveCount, "survivor owns only the LIVE members' files")
				for _, id := range live {
					b, _ := store.GetBookByID(id)
					if id == survivor {
						assert.NotNil(t, b)
					} else {
						assert.Nil(t, b, "absorbed live member %s must be hard-deleted", id)
					}
				}
			} else {
				b, err := store.GetBookByID(live[0])
				require.NoError(t, err)
				require.NotNil(t, b, "no-op case must leave the lone live member untouched")
			}

			dbtest.AssertStoreInvariants(t, store)
		})
	}
}

// TestApplyVersionGroup_DemotesStaleExistingPrimary is BUG 2: when the target
// group is reused, books already in the group but NOT in the hold must not keep
// a stale IsPrimaryVersion=true alongside the newly chosen primary. Also covers
// idempotent re-apply of the demotion path.
func TestApplyVersionGroup_DemotesStaleExistingPrimary(t *testing.T) {
	store := newApplyTestStore(t)
	groupID := ulid.Make().String()

	// X: existing group member + current primary, NOT in the hold.
	xID := ulid.Make().String()
	_, err := store.CreateBook(&database.Book{ID: xID, Title: "Existing Primary", Format: "mp3", FilePath: "/lib/dp/x.mp3"})
	require.NoError(t, err)
	putInGroup(t, store, xID, groupID, true)

	// A: already in the group (non-primary), in the hold. B: ungrouped, in the hold.
	aID := ulid.Make().String()
	_, err = store.CreateBook(&database.Book{ID: aID, Title: "Edition A", Format: "mp3", FilePath: "/lib/dp/a.mp3"})
	require.NoError(t, err)
	putInGroup(t, store, aID, groupID, false)
	bID := ulid.Make().String()
	_, err = store.CreateBook(&database.Book{ID: bID, Title: "Edition B", Format: "mp3", FilePath: "/lib/dp/b.mp3"})
	require.NoError(t, err)

	apply := ApplyVersionGroup(store)
	item := versionGroupItem(t, "/lib/dp", []string{aID, bID})
	require.NoError(t, apply(context.Background(), item))

	assertSinglePrimary := func() {
		members, err := store.GetBooksByVersionGroup(groupID)
		require.NoError(t, err)
		require.Len(t, members, 3, "X, A, B must all be in the reused group")
		wantPrimary := minID(aID, bID)
		primaries := 0
		for _, m := range members {
			if m.IsPrimaryVersion != nil && *m.IsPrimaryVersion {
				primaries++
				assert.Equal(t, wantPrimary, m.ID, "the only primary must be the hold's chosen primary")
			}
		}
		assert.Equal(t, 1, primaries, "group must have exactly ONE primary after apply")
	}
	assertSinglePrimary()

	// X was demoted but otherwise untouched (still visible, still in the group).
	x, err := store.GetBookByID(xID)
	require.NoError(t, err)
	require.NotNil(t, x)
	require.NotNil(t, x.IsPrimaryVersion)
	assert.False(t, *x.IsPrimaryVersion, "stale existing primary must be demoted")
	require.NotNil(t, x.VersionGroupID)
	assert.Equal(t, groupID, *x.VersionGroupID)
	assert.Equal(t, "Existing Primary", x.Title, "demotion must patch ONLY IsPrimaryVersion")
	assert.True(t, x.MarkedForDeletion == nil || !*x.MarkedForDeletion)

	// Idempotent re-apply: same single primary, no flapping.
	require.NoError(t, apply(context.Background(), item))
	assertSinglePrimary()

	dbtest.AssertStoreInvariants(t, store)
}

// TestApplyVersionGroup_RefusesCrossGroupMerge is BUG 3: when hold members sit in
// TWO different non-empty version groups, silently merging them can strand the
// losing group (or leave it primary-less). The apply must refuse with an error —
// the review item goes to "failed" with a reason — and mutate NOTHING.
func TestApplyVersionGroup_RefusesCrossGroupMerge(t *testing.T) {
	store := newApplyTestStore(t)
	g1 := ulid.Make().String()
	g2 := ulid.Make().String()

	aID := ulid.Make().String()
	_, err := store.CreateBook(&database.Book{ID: aID, Title: "In G1", Format: "mp3", FilePath: "/lib/xg/a.mp3"})
	require.NoError(t, err)
	putInGroup(t, store, aID, g1, true)
	bID := ulid.Make().String()
	_, err = store.CreateBook(&database.Book{ID: bID, Title: "In G2", Format: "mp3", FilePath: "/lib/xg/b.mp3"})
	require.NoError(t, err)
	putInGroup(t, store, bID, g2, true)

	apply := ApplyVersionGroup(store)
	err = apply(context.Background(), versionGroupItem(t, "/lib/xg", []string{aID, bID}))
	require.Error(t, err, "members in two different groups must be refused, not merged")
	assert.Contains(t, err.Error(), "version group", "error must explain the refusal")

	// Nothing was mutated: both members keep their original group + primary flag.
	for id, wantGroup := range map[string]string{aID: g1, bID: g2} {
		b, gerr := store.GetBookByID(id)
		require.NoError(t, gerr)
		require.NotNil(t, b)
		require.NotNil(t, b.VersionGroupID)
		assert.Equal(t, wantGroup, *b.VersionGroupID, "member %s must keep its original group", id)
		require.NotNil(t, b.IsPrimaryVersion)
		assert.True(t, *b.IsPrimaryVersion, "member %s must keep its primary flag", id)
	}

	dbtest.AssertStoreInvariants(t, store)
}

// TestApplyMultidisc_BadPayload surfaces a decode error rather than silently
// swallowing it (so the item is NOT marked "applied").
func TestApplyMultidisc_BadPayload(t *testing.T) {
	store := newApplyTestStore(t)
	apply := ApplyMultidisc(store, merge.NewService(store))
	err := apply(context.Background(), database.ReviewItem{
		ID: ulid.Make().String(), Kind: "regroup.multidisc", Payload: "{not json",
	})
	require.Error(t, err)
}
