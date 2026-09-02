// file: internal/merge/service_guards_test.go
// version: 1.0.0
// guid: 7b2e9d4c-1a5f-4e83-9c6b-2d8f0a3e5b17
// last-edited: 2026-09-02

package merge

import (
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests pin the three chokepoint guards added after the 2026-09-02
// dedup bug hunt, against a real PebbleStore so the soft-delete flag, the
// book_file rows and the book:hash: index behave as they do in prod:
//
//	F1  the survivor election is file-aware (a book with book_file rows beats
//	    one without, before BookIsBetter's format rule), and a forced
//	    file-less primary is refused when another participant has files;
//	F2  re-running a merge on a pair whose loser is already soft-deleted into
//	    the winner's group is a no-op, and the loser can never come back as
//	    the primary;
//	F4  a soft-deleted row outside the resolved group is refused as either
//	    role, and MergeBooks fails when a loser cannot be soft-deleted.

func seedGuardBook(t *testing.T, store database.Store, title, format string, withFile bool) *database.Book {
	t.Helper()
	b := &database.Book{ID: ulid.Make().String(), Title: title, Format: format, FilePath: "/lib/" + title + "." + format}
	_, err := store.CreateBook(b)
	require.NoError(t, err)
	if withFile {
		require.NoError(t, store.CreateBookFile(&database.BookFile{
			ID: ulid.Make().String(), BookID: b.ID, FilePath: b.FilePath, Format: format,
		}))
	}
	return b
}

func softDeleted(t *testing.T, store database.Store, id string) bool {
	t.Helper()
	b, err := store.GetBookByID(id)
	require.NoError(t, err)
	require.NotNil(t, b, "book %s must still exist (no hard delete)", id)
	return b.MarkedForDeletion != nil && *b.MarkedForDeletion
}

// F1: an m4b with NO book_file rows loses to an mp3 that has one, even though
// BookIsBetter prefers m4b on format. Before the file tier the m4b won and the
// only row that could reach audio went onto the purge clock.
func TestMergeBooks_Election_FileBearingBeatsFileless(t *testing.T) {
	store := setupTestStore(t)
	ghostM4B := seedGuardBook(t, store, "Election Ghost", "m4b", false)
	realMP3 := seedGuardBook(t, store, "Election Real", "mp3", true)

	res, err := NewService(store).MergeBooks([]string{ghostM4B.ID, realMP3.ID}, "")
	require.NoError(t, err)
	assert.Equal(t, realMP3.ID, res.PrimaryID, "the book with a file row must survive")
	assert.True(t, softDeleted(t, store, ghostM4B.ID))
	assert.False(t, softDeleted(t, store, realMP3.ID))
}

// F1: within the file-bearing tier BookIsBetter still decides (m4b > mp3),
// so the tier does not flip the existing format preference when both sides
// can reach audio.
func TestMergeBooks_Election_WithinTierBookIsBetterDecides(t *testing.T) {
	store := setupTestStore(t)
	mp3 := seedGuardBook(t, store, "Tier MP3", "mp3", true)
	m4b := seedGuardBook(t, store, "Tier M4B", "m4b", true)

	res, err := NewService(store).MergeBooks([]string{mp3.ID, m4b.ID}, "")
	require.NoError(t, err)
	assert.Equal(t, m4b.ID, res.PrimaryID)
}

// F1: an explicit primary with no file rows is refused (typed error naming
// the file-bearing books) rather than silently overridden or honored.
func TestMergeBooks_ExplicitFilelessPrimary_Refused(t *testing.T) {
	store := setupTestStore(t)
	ghost := seedGuardBook(t, store, "Forced Ghost", "m4b", false)
	real := seedGuardBook(t, store, "Forced Real", "mp3", true)

	res, err := NewService(store).MergeBooks([]string{ghost.ID, real.ID}, ghost.ID)
	require.Error(t, err)
	assert.Nil(t, res)
	var fe *FilelessPrimaryError
	require.True(t, errors.As(err, &fe), "want FilelessPrimaryError, got %T: %v", err, err)
	assert.Equal(t, ghost.ID, fe.PrimaryID)
	assert.Equal(t, []string{real.ID}, fe.FileBearing)

	// Nothing was written: neither row is grouped or soft-deleted.
	for _, id := range []string{ghost.ID, real.ID} {
		b, err := store.GetBookByID(id)
		require.NoError(t, err)
		assert.False(t, softDeleted(t, store, id))
		assert.True(t, b.VersionGroupID == nil || *b.VersionGroupID == "", "refused merge must not touch %s", id)
	}
}

// F1: when NO participant has file rows the merge is still allowed and an
// explicit primary is honored — there is no audio to strand, and refusing
// would make the file-less ghost class impossible to tidy.
func TestMergeBooks_AllFileless_ExplicitPrimaryHonored(t *testing.T) {
	store := setupTestStore(t)
	a := seedGuardBook(t, store, "Ghost A", "mp3", false)
	b := seedGuardBook(t, store, "Ghost B", "m4b", false)

	res, err := NewService(store).MergeBooks([]string{a.ID, b.ID}, a.ID)
	require.NoError(t, err)
	assert.Equal(t, a.ID, res.PrimaryID)
	assert.True(t, softDeleted(t, store, b.ID))
}

// F2: after a manual "keep A", replaying the same pair — in either order and
// with no primary forced — is a no-op that keeps A primary and B soft-deleted.
// This is the shape the FullScan exact-hash pass produces when the book:hash:
// index still points at the soft-deleted loser.
func TestMergeBooks_ReplayOnMergedPair_IsNoop(t *testing.T) {
	store := setupTestStore(t)
	a := seedGuardBook(t, store, "Replay A", "mp3", true)
	b := seedGuardBook(t, store, "Replay B", "m4b", true) // BookIsBetter would pick B on format

	svc := NewService(store)
	first, err := svc.MergeBooks([]string{a.ID, b.ID}, a.ID)
	require.NoError(t, err)
	require.Equal(t, a.ID, first.PrimaryID)

	for _, order := range [][]string{{a.ID, b.ID}, {b.ID, a.ID}} {
		res, err := svc.MergeBooks(order, "")
		require.NoError(t, err, "replay %v", order)
		assert.Equal(t, a.ID, res.PrimaryID, "the soft-deleted loser must never be elected on replay")
		assert.False(t, softDeleted(t, store, a.ID), "the manual keep must survive the replay")
		assert.True(t, softDeleted(t, store, b.ID))
		got, err := store.GetBookByID(a.ID)
		require.NoError(t, err)
		require.NotNil(t, got.IsPrimaryVersion)
		assert.True(t, *got.IsPrimaryVersion)
	}
}

// F2/F4: forcing the soft-deleted loser back as primary is refused with a
// typed error, and the refusal writes nothing.
func TestMergeBooks_SoftDeletedForcedPrimary_Refused(t *testing.T) {
	store := setupTestStore(t)
	a := seedGuardBook(t, store, "Reverse A", "mp3", true)
	b := seedGuardBook(t, store, "Reverse B", "mp3", true)

	svc := NewService(store)
	_, err := svc.MergeBooks([]string{a.ID, b.ID}, a.ID)
	require.NoError(t, err)

	res, err := svc.MergeBooks([]string{a.ID, b.ID}, b.ID)
	require.Error(t, err)
	assert.Nil(t, res)
	var se *SoftDeletedInputError
	require.True(t, errors.As(err, &se), "want SoftDeletedInputError, got %T: %v", err, err)
	assert.Equal(t, b.ID, se.BookID)
	assert.True(t, se.AsPrimary)
	assert.False(t, softDeleted(t, store, a.ID), "the live winner must not have been soft-deleted")
}

// F4: a soft-deleted row that is NOT a member of the group this merge
// resolves to is a stale pair, not a replay, and is refused as a loser.
// Merging it would drag the live book into whichever group already consumed
// the deleted row and re-route its external IDs a second time.
func TestMergeBooks_SoftDeletedLoserOutsideGroup_Refused(t *testing.T) {
	store := setupTestStore(t)
	a := seedGuardBook(t, store, "Stale A", "mp3", true)
	b := seedGuardBook(t, store, "Stale B", "mp3", true)
	c := seedGuardBook(t, store, "Stale C", "mp3", true)

	svc := NewService(store)
	_, err := svc.MergeBooks([]string{a.ID, b.ID}, a.ID) // b is now soft-deleted in a's group
	require.NoError(t, err)

	for _, primary := range []string{"", c.ID} {
		res, err := svc.MergeBooks([]string{c.ID, b.ID}, primary)
		require.Error(t, err, "primary=%q", primary)
		assert.Nil(t, res)
		var se *SoftDeletedInputError
		require.True(t, errors.As(err, &se), "want SoftDeletedInputError, got %T: %v", err, err)
		assert.Equal(t, b.ID, se.BookID)
		assert.False(t, se.AsPrimary)
	}
	got, err := store.GetBookByID(c.ID)
	require.NoError(t, err)
	assert.True(t, got.VersionGroupID == nil || *got.VersionGroupID == "", "c must not have been pulled into a's group")

	// A directly soft-deleted row (no group at all) is refused the same way.
	d := seedGuardBook(t, store, "Stale D", "mp3", true)
	require.NoError(t, SoftDeleteBook(store, d.ID))
	_, err = svc.MergeBooks([]string{c.ID, d.ID}, "")
	var se *SoftDeletedInputError
	require.True(t, errors.As(err, &se), "got %T: %v", err, err)
	assert.Equal(t, d.ID, se.BookID)
}

// ElectPrimary never returns a soft-deleted index, and returns -1 when every
// book is soft-deleted.
func TestElectPrimary_SkipsSoftDeleted(t *testing.T) {
	yes := true
	deleted := &database.Book{ID: "del", Format: "m4b", MarkedForDeletion: &yes}
	live := &database.Book{ID: "live", Format: "mp3"}
	files := map[string][]database.BookFile{
		"del":  {{ID: "f1", BookID: "del"}},
		"live": nil,
	}
	// The deleted book is "better" on every axis (m4b, has a file) and still
	// must not be elected.
	assert.Equal(t, 1, ElectPrimary([]*database.Book{deleted, live}, files))
	assert.Equal(t, 0, ElectPrimary([]*database.Book{live, deleted}, files))
	assert.Equal(t, -1, ElectPrimary([]*database.Book{deleted}, files))
}
