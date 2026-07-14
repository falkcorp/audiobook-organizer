// file: internal/plugins/maintenance/regroup_apply_test.go
// version: 1.0.0
// guid: a9d3f1c7-6b40-4e28-8f95-2c1e7b0a4d63
// last-edited: 2026-07-14

package maintenance

import (
	"context"
	"encoding/json"
	"testing"

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
