// file: internal/merge/service_b3_realstore_test.go
// version: 1.0.0
// guid: 6a2f9e14-3c7b-4d8a-9e10-b8f2a5c6d7e1
// last-edited: 2026-07-18

package merge

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/dbtest"
	ulid "github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// b3FakeEnqueuer is a minimal WriteBackEnqueuer that just records every PID
// it was asked to remove, so tests can assert the ITL-cleanup enqueue call
// (MergeBooks step 3 in the doc comment) actually fires with the right PIDs.
type b3FakeEnqueuer struct {
	removed []string
}

func (f *b3FakeEnqueuer) EnqueueRemove(pid string) { f.removed = append(f.removed, pid) }

// TestB3_MergeBooks_ITunesPIDCollectionAndITLEnqueue drives the full loser
// cleanup sequence on a real store: the loser's iTunes PID is collected
// BEFORE reassignment, external IDs move to the winner, and the collected
// PID is enqueued for ITL removal via the batcher. A non-itunes / tombstoned
// mapping on the loser must NOT be enqueued.
func TestB3_MergeBooks_ITunesPIDCollectionAndITLEnqueue(t *testing.T) {
	store := setupTestStore(t)

	loser := &database.Book{ID: ulid.Make().String(), Title: "Loser", Format: "mp3", FilePath: "/tmp/b3loser.mp3"}
	winner := &database.Book{ID: ulid.Make().String(), Title: "Winner", Format: "m4b", FilePath: "/tmp/b3winner.m4b"}
	_, err := store.CreateBook(loser)
	require.NoError(t, err)
	_, err = store.CreateBook(winner)
	require.NoError(t, err)

	require.NoError(t, store.CreateExternalIDMapping(&database.ExternalIDMapping{
		Source: "itunes", ExternalID: "B3-PID-LIVE", BookID: loser.ID,
	}))
	require.NoError(t, store.CreateExternalIDMapping(&database.ExternalIDMapping{
		Source: "audible", ExternalID: "B3-ASIN-1", BookID: loser.ID,
	}))
	require.NoError(t, store.CreateExternalIDMapping(&database.ExternalIDMapping{
		Source: "itunes", ExternalID: "B3-PID-TOMBSTONED", BookID: loser.ID, Tombstoned: true,
	}))

	enq := &b3FakeEnqueuer{}
	ms := NewService(store)
	ms.SetWriteBackBatcher(enq)

	result, err := ms.MergeBooks([]string{loser.ID, winner.ID}, winner.ID)
	require.NoError(t, err)
	assert.Equal(t, winner.ID, result.PrimaryID)

	// Only the live itunes PID is enqueued for ITL removal — not the audible
	// ASIN, and not the tombstoned itunes mapping.
	assert.Equal(t, []string{"B3-PID-LIVE"}, enq.removed)

	// External IDs (both itunes and audible) are reassigned to the winner.
	mappings, err := store.GetExternalIDsForBook(winner.ID)
	require.NoError(t, err)
	got := map[string]bool{}
	for _, m := range mappings {
		got[m.ExternalID] = true
	}
	assert.True(t, got["B3-PID-LIVE"], "live itunes PID reassigned to winner")
	assert.True(t, got["B3-ASIN-1"], "audible ASIN reassigned to winner")

	dbtest.AssertStoreInvariants(t, store)
}

// TestB3_MergeBooks_NoWriteBackBatcher_SkipsEnqueueSilently verifies the
// documented best-effort behavior: a nil writeBackBatcher (e.g. iTunes
// write-back disabled) must not panic and must not block the merge.
func TestB3_MergeBooks_NoWriteBackBatcher_SkipsEnqueueSilently(t *testing.T) {
	store := setupTestStore(t)

	loser := &database.Book{ID: ulid.Make().String(), Title: "Loser", Format: "mp3", FilePath: "/tmp/b3nobatch-loser.mp3"}
	winner := &database.Book{ID: ulid.Make().String(), Title: "Winner", Format: "m4b", FilePath: "/tmp/b3nobatch-winner.m4b"}
	_, err := store.CreateBook(loser)
	require.NoError(t, err)
	_, err = store.CreateBook(winner)
	require.NoError(t, err)
	require.NoError(t, store.CreateExternalIDMapping(&database.ExternalIDMapping{
		Source: "itunes", ExternalID: "B3-PID-NOBATCH", BookID: loser.ID,
	}))

	ms := NewService(store) // writeBackBatcher left nil
	result, err := ms.MergeBooks([]string{loser.ID, winner.ID}, winner.ID)
	require.NoError(t, err)
	assert.Equal(t, winner.ID, result.PrimaryID)
}

// TestB3_MergeBooks_DuplicatePrimaryInBookIDs_NoDoublePrimary is a
// regression lock for the class of bug PR #2007 fixed at the caller layer
// (applyBookMergeReroute failing to exclude keepID from the loser set
// before F6/T10). Even if a malformed bookIDs list includes the primary
// twice, MergeBooks itself must still produce exactly one live primary and
// must not soft-delete the winner.
func TestB3_MergeBooks_DuplicatePrimaryInBookIDs_NoDoublePrimary(t *testing.T) {
	store := setupTestStore(t)

	loser := &database.Book{ID: ulid.Make().String(), Title: "Loser", Format: "mp3", FilePath: "/tmp/b3dup-loser.mp3"}
	winner := &database.Book{ID: ulid.Make().String(), Title: "Winner", Format: "m4b", FilePath: "/tmp/b3dup-winner.m4b"}
	_, err := store.CreateBook(loser)
	require.NoError(t, err)
	_, err = store.CreateBook(winner)
	require.NoError(t, err)

	ms := NewService(store)
	// winner.ID appears twice — as itself and (erroneously) as if it were
	// also a loser to merge into itself.
	result, err := ms.MergeBooks([]string{loser.ID, winner.ID, winner.ID}, winner.ID)
	require.NoError(t, err)
	assert.Equal(t, winner.ID, result.PrimaryID)

	w, err := store.GetBookByID(winner.ID)
	require.NoError(t, err)
	require.NotNil(t, w.IsPrimaryVersion)
	assert.True(t, *w.IsPrimaryVersion, "winner must still be primary")
	if w.MarkedForDeletion != nil {
		assert.False(t, *w.MarkedForDeletion, "winner must NOT be soft-deleted despite appearing twice in bookIDs")
	}

	l, err := store.GetBookByID(loser.ID)
	require.NoError(t, err)
	require.NotNil(t, l.MarkedForDeletion)
	assert.True(t, *l.MarkedForDeletion, "the real loser is still soft-deleted")

	dbtest.AssertStoreInvariants(t, store)
}

// TestB3_MergeBooks_ThreeBooks_ExactlyOnePrimary locks the version-group
// integrity invariant for the N>2 case: exactly one book ends up primary,
// the rest are all soft-deleted losers sharing the same version group.
func TestB3_MergeBooks_ThreeBooks_ExactlyOnePrimary(t *testing.T) {
	store := setupTestStore(t)

	var ids []string
	for i, format := range []string{"mp3", "mp3", "m4b"} {
		b := &database.Book{
			ID:       ulid.Make().String(),
			Title:    "Three-way dup",
			Format:   format,
			FilePath: "/tmp/b3three-" + ulid.Make().String() + "." + format,
		}
		_, err := store.CreateBook(b)
		require.NoError(t, err)
		ids = append(ids, b.ID)
		_ = i
	}

	ms := NewService(store)
	result, err := ms.MergeBooks(ids, "")
	require.NoError(t, err)

	primaryCount := 0
	var groupIDs []string
	for _, id := range ids {
		b, err := store.GetBookByID(id)
		require.NoError(t, err)
		require.NotNil(t, b.IsPrimaryVersion)
		if *b.IsPrimaryVersion {
			primaryCount++
			if b.MarkedForDeletion != nil {
				assert.False(t, *b.MarkedForDeletion, "primary must not be soft-deleted")
			}
		} else {
			require.NotNil(t, b.MarkedForDeletion)
			assert.True(t, *b.MarkedForDeletion, "non-primary must be soft-deleted")
		}
		require.NotNil(t, b.VersionGroupID)
		groupIDs = append(groupIDs, *b.VersionGroupID)
	}
	assert.Equal(t, 1, primaryCount, "exactly one book must be primary")
	assert.Equal(t, result.PrimaryID, ids[2], "the m4b should auto-win")
	for _, g := range groupIDs {
		assert.Equal(t, groupIDs[0], g, "all books must share one version group")
	}

	dbtest.AssertStoreInvariants(t, store)
}

// ---------- CombineBooks validation branches ----------

func TestB3_CombineBooks_PrimaryNotFound(t *testing.T) {
	store := setupTestStore(t)
	a := &database.Book{ID: ulid.Make().String(), Title: "A", Format: "mp3", FilePath: "/tmp/b3pnf-a.mp3"}
	_, err := store.CreateBook(a)
	require.NoError(t, err)

	ms := NewService(store)
	_, err = ms.CombineBooks([]string{a.ID, "b3-ghost-primary"}, "b3-ghost-primary", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestB3_CombineBooks_DuplicateBookID(t *testing.T) {
	store := setupTestStore(t)
	a := &database.Book{ID: ulid.Make().String(), Title: "A", Format: "mp3", FilePath: "/tmp/b3dupid-a.mp3"}
	b := &database.Book{ID: ulid.Make().String(), Title: "B", Format: "mp3", FilePath: "/tmp/b3dupid-b.mp3"}
	_, err := store.CreateBook(a)
	require.NoError(t, err)
	_, err = store.CreateBook(b)
	require.NoError(t, err)

	ms := NewService(store)
	_, err = ms.CombineBooks([]string{a.ID, b.ID, a.ID}, a.ID, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate book id")
}

func TestB3_CombineBooks_BookIDNotFound(t *testing.T) {
	store := setupTestStore(t)
	a := &database.Book{ID: ulid.Make().String(), Title: "A", Format: "mp3", FilePath: "/tmp/b3idnf-a.mp3"}
	_, err := store.CreateBook(a)
	require.NoError(t, err)

	ms := NewService(store)
	_, err = ms.CombineBooks([]string{a.ID, "b3-ghost-member"}, a.ID, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "b3-ghost-member")
	assert.Contains(t, err.Error(), "not found")
}

// ---------- CombineBooks override: nil-override wipe-safety ----------

// TestB3_CombineBooks_NilOverride_DoesNotWipeSurvivorMetadata is the
// explicit nil-override wipe-safety regression test: a combine performed
// with override == nil must leave the survivor's existing Title/Author/
// Narrator completely untouched. CombineBooks must never fall back to a
// blanket UpdateBook(survivor) that would zero out fields the caller didn't
// intend to change.
func TestB3_CombineBooks_NilOverride_DoesNotWipeSurvivorMetadata(t *testing.T) {
	store := setupTestStore(t)

	author, err := store.CreateAuthor("B3 Original Author")
	require.NoError(t, err)

	narrator := "B3 Original Narrator"
	survivor := &database.Book{
		ID:       ulid.Make().String(),
		Title:    "B3 Original Title",
		Format:   "mp3",
		FilePath: "/tmp/b3nilov-survivor.mp3",
		AuthorID: &author.ID,
		Narrator: &narrator,
	}
	loser := &database.Book{ID: ulid.Make().String(), Title: "Loser", Format: "mp3", FilePath: "/tmp/b3nilov-loser.mp3"}
	_, err = store.CreateBook(survivor)
	require.NoError(t, err)
	_, err = store.CreateBook(loser)
	require.NoError(t, err)

	ms := NewService(store)
	_, err = ms.CombineBooks([]string{survivor.ID, loser.ID}, survivor.ID, nil)
	require.NoError(t, err)

	fresh, err := store.GetBookByID(survivor.ID)
	require.NoError(t, err)
	assert.Equal(t, "B3 Original Title", fresh.Title, "nil override must not touch Title")
	require.NotNil(t, fresh.Narrator)
	assert.Equal(t, "B3 Original Narrator", *fresh.Narrator, "nil override must not touch Narrator")
	require.NotNil(t, fresh.AuthorID)
	assert.Equal(t, author.ID, *fresh.AuthorID, "nil override must not touch AuthorID")

	dbtest.AssertStoreInvariants(t, store)
}

// TestB3_CombineBooks_EmptyOverride_DoesNotWipeSurvivorMetadata covers the
// sibling case: override is a non-nil *CombineOverride with every field left
// as the zero value. The "any non-empty field" guard must treat this
// identically to nil — no UpdateBook call, no field wiped.
func TestB3_CombineBooks_EmptyOverride_DoesNotWipeSurvivorMetadata(t *testing.T) {
	store := setupTestStore(t)

	survivor := &database.Book{ID: ulid.Make().String(), Title: "B3 Keep Me", Format: "mp3", FilePath: "/tmp/b3emptyov-survivor.mp3"}
	loser := &database.Book{ID: ulid.Make().String(), Title: "Loser", Format: "mp3", FilePath: "/tmp/b3emptyov-loser.mp3"}
	_, err := store.CreateBook(survivor)
	require.NoError(t, err)
	_, err = store.CreateBook(loser)
	require.NoError(t, err)

	ms := NewService(store)
	_, err = ms.CombineBooks([]string{survivor.ID, loser.ID}, survivor.ID, &CombineOverride{})
	require.NoError(t, err)

	fresh, err := store.GetBookByID(survivor.ID)
	require.NoError(t, err)
	assert.Equal(t, "B3 Keep Me", fresh.Title)
}

func TestB3_CombineBooks_OverrideTitleOnly(t *testing.T) {
	store := setupTestStore(t)

	narrator := "Keep This Narrator"
	survivor := &database.Book{ID: ulid.Make().String(), Title: "Old Title", Format: "mp3", FilePath: "/tmp/b3title-survivor.mp3", Narrator: &narrator}
	loser := &database.Book{ID: ulid.Make().String(), Title: "Loser", Format: "mp3", FilePath: "/tmp/b3title-loser.mp3"}
	_, err := store.CreateBook(survivor)
	require.NoError(t, err)
	_, err = store.CreateBook(loser)
	require.NoError(t, err)

	ms := NewService(store)
	_, err = ms.CombineBooks([]string{survivor.ID, loser.ID}, survivor.ID, &CombineOverride{Title: "New Title"})
	require.NoError(t, err)

	fresh, err := store.GetBookByID(survivor.ID)
	require.NoError(t, err)
	assert.Equal(t, "New Title", fresh.Title)
	require.NotNil(t, fresh.Narrator)
	assert.Equal(t, "Keep This Narrator", *fresh.Narrator, "omitted override field must not be wiped")
}

func TestB3_CombineBooks_OverrideNarratorOnly(t *testing.T) {
	store := setupTestStore(t)

	survivor := &database.Book{ID: ulid.Make().String(), Title: "Keep This Title", Format: "mp3", FilePath: "/tmp/b3narr-survivor.mp3"}
	loser := &database.Book{ID: ulid.Make().String(), Title: "Loser", Format: "mp3", FilePath: "/tmp/b3narr-loser.mp3"}
	_, err := store.CreateBook(survivor)
	require.NoError(t, err)
	_, err = store.CreateBook(loser)
	require.NoError(t, err)

	ms := NewService(store)
	_, err = ms.CombineBooks([]string{survivor.ID, loser.ID}, survivor.ID, &CombineOverride{Narrator: "New Narrator"})
	require.NoError(t, err)

	fresh, err := store.GetBookByID(survivor.ID)
	require.NoError(t, err)
	assert.Equal(t, "Keep This Title", fresh.Title, "omitted override field must not be wiped")
	require.NotNil(t, fresh.Narrator)
	assert.Equal(t, "New Narrator", *fresh.Narrator)
}

func TestB3_CombineBooks_OverrideAuthorNew(t *testing.T) {
	store := setupTestStore(t)

	survivor := &database.Book{ID: ulid.Make().String(), Title: "T", Format: "mp3", FilePath: "/tmp/b3authnew-survivor.mp3"}
	loser := &database.Book{ID: ulid.Make().String(), Title: "Loser", Format: "mp3", FilePath: "/tmp/b3authnew-loser.mp3"}
	_, err := store.CreateBook(survivor)
	require.NoError(t, err)
	_, err = store.CreateBook(loser)
	require.NoError(t, err)

	ms := NewService(store)
	_, err = ms.CombineBooks([]string{survivor.ID, loser.ID}, survivor.ID, &CombineOverride{Author: "Brand New Author"})
	require.NoError(t, err)

	author, err := store.GetAuthorByName("Brand New Author")
	require.NoError(t, err)
	require.NotNil(t, author, "author should have been created")

	fresh, err := store.GetBookByID(survivor.ID)
	require.NoError(t, err)
	require.NotNil(t, fresh.AuthorID)
	assert.Equal(t, author.ID, *fresh.AuthorID)
}

func TestB3_CombineBooks_OverrideAuthorExisting(t *testing.T) {
	store := setupTestStore(t)

	existing, err := store.CreateAuthor("Already Here")
	require.NoError(t, err)

	survivor := &database.Book{ID: ulid.Make().String(), Title: "T", Format: "mp3", FilePath: "/tmp/b3authex-survivor.mp3"}
	loser := &database.Book{ID: ulid.Make().String(), Title: "Loser", Format: "mp3", FilePath: "/tmp/b3authex-loser.mp3"}
	_, err = store.CreateBook(survivor)
	require.NoError(t, err)
	_, err = store.CreateBook(loser)
	require.NoError(t, err)

	ms := NewService(store)
	_, err = ms.CombineBooks([]string{survivor.ID, loser.ID}, survivor.ID, &CombineOverride{Author: "Already Here"})
	require.NoError(t, err)

	fresh, err := store.GetBookByID(survivor.ID)
	require.NoError(t, err)
	require.NotNil(t, fresh.AuthorID)
	assert.Equal(t, existing.ID, *fresh.AuthorID, "must reuse the existing author, not create a duplicate")
}

// ---------- ensureOwnFile / attachVirtualFile (unexported helpers) ----------

func TestB3_EnsureOwnFile_NoFilePath_ReturnsZero(t *testing.T) {
	store := setupTestStore(t)
	ms := NewService(store)

	b := &database.Book{ID: ulid.Make().String(), Title: "No path"}
	assert.Equal(t, 0, ms.ensureOwnFile(b))
}

func TestB3_EnsureOwnFile_AlreadyMaterialized_ReturnsZero(t *testing.T) {
	store := setupTestStore(t)
	ms := NewService(store)

	b := &database.Book{ID: ulid.Make().String(), Title: "Has file", FilePath: "/tmp/b3already.mp3"}
	_, err := store.CreateBook(b)
	require.NoError(t, err)
	require.NoError(t, store.CreateBookFile(&database.BookFile{
		ID: ulid.Make().String(), BookID: b.ID, FilePath: b.FilePath, Format: "mp3",
	}))

	assert.Equal(t, 0, ms.ensureOwnFile(b), "book already has a BookFile row; nothing to materialize")
}

// TestB3_AttachVirtualFile_ReattachExistingOwnedByOtherBook exercises the
// "#1549 reattach-safe" branch documented on attachVirtualFile: a BookFile
// row already exists at the target path but is owned by a DIFFERENT book
// (a stray/orphaned row from an earlier partial operation). attachVirtualFile
// must MOVE that row onto the target book rather than creating a duplicate.
func TestB3_AttachVirtualFile_ReattachExistingOwnedByOtherBook(t *testing.T) {
	store := setupTestStore(t)
	ms := NewService(store)

	strayOwner := &database.Book{ID: ulid.Make().String(), Title: "Stray owner", FilePath: "/tmp/b3stray-owner.mp3"}
	target := &database.Book{ID: ulid.Make().String(), Title: "Target", FilePath: "/tmp/b3stray-shared.mp3"}
	_, err := store.CreateBook(strayOwner)
	require.NoError(t, err)
	_, err = store.CreateBook(target)
	require.NoError(t, err)

	strayFile := &database.BookFile{ID: ulid.Make().String(), BookID: strayOwner.ID, FilePath: target.FilePath, Format: "mp3"}
	require.NoError(t, store.CreateBookFile(strayFile))

	n := ms.attachVirtualFile(target, target.ID)
	assert.Equal(t, 1, n)

	files, err := store.GetBookFiles(target.ID)
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, strayFile.ID, files[0].ID, "the existing row was moved, not duplicated")

	strayFiles, err := store.GetBookFiles(strayOwner.ID)
	require.NoError(t, err)
	assert.Empty(t, strayFiles, "the stray owner no longer owns the file")
}

// TestB3_AttachVirtualFile_SetsDurationFromBook covers the create-new-file
// path where the book being materialized has a Duration set — it must
// carry over onto the new BookFile row.
func TestB3_AttachVirtualFile_SetsDurationFromBook(t *testing.T) {
	store := setupTestStore(t)
	ms := NewService(store)

	dur := 4242
	b := &database.Book{ID: ulid.Make().String(), Title: "Has duration", FilePath: "/tmp/b3duration.mp3", Duration: &dur}
	_, err := store.CreateBook(b)
	require.NoError(t, err)

	n := ms.attachVirtualFile(b, b.ID)
	assert.Equal(t, 1, n)

	files, err := store.GetBookFiles(b.ID)
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, dur, files[0].Duration)
}
