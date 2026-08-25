// file: internal/scanner/ai_parse_save_target_test.go
// version: 1.0.0
// guid: ade87d70-9dc4-4aee-9538-449a631e678d
// last-edited: 2026-08-24

package scanner

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

func aiSaveStore(t *testing.T) database.Store {
	t.Helper()
	store, cleanup := setupPebbleStore(t)
	t.Cleanup(cleanup)
	orig := database.GetGlobalStore()
	database.SetGlobalStore(store)
	SetStore(store)
	t.Cleanup(func() { database.SetGlobalStore(orig); SetStore(nil) })
	return store
}

func ptrBool(b bool) *bool    { return &b }
func ptrStr(s string) *string { return &s }

// TestSaveAIFieldsWritesToThePrimaryNotTheDemotedSource is the case the queued
// operation created and the unit tests could not see.
//
// The scan's AutoOrganizeFn runs strictly after ProcessBooksParallel returns, so
// by the time a queued batch runs, organize may already have copied the book:
// a NEW row is primary and the source is demoted to IsPrimaryVersion=false, still
// sitting at the path the batch carries. A path-keyed write lands on the demoted
// row, and CreateOrganizedVersion snapshots Title/AuthorID/SeriesID/Narrator at
// creation, so the primary never picks the value up afterwards either. The book
// the UI shows keeps the empty field, permanently, with nothing logged.
func TestSaveAIFieldsWritesToThePrimaryNotTheDemotedSource(t *testing.T) {
	store := aiSaveStore(t)

	const group = "vg-ai-parse"
	source, err := store.CreateBook(&database.Book{
		FilePath:         "/import/staging/book.m4b",
		Title:            "A Book",
		VersionGroupID:   ptrStr(group),
		IsPrimaryVersion: ptrBool(false),
	})
	require.NoError(t, err)

	primary, err := store.CreateBook(&database.Book{
		FilePath:         "/library/Author/A Book/book.m4b",
		Title:            "A Book",
		VersionGroupID:   ptrStr(group),
		IsPrimaryVersion: ptrBool(true),
	})
	require.NoError(t, err)

	// The batch carries the SOURCE path, because that is the path the scan
	// walked and nominated.
	require.NoError(t, saveAIFieldsToPrimary(context.Background(), &Book{
		FilePath: source.FilePath,
		Series:   "The Series",
		Position: 2,
	}))

	gotPrimary, err := store.GetBookByID(primary.ID)
	require.NoError(t, err)
	require.NotNil(t, gotPrimary.SeriesID,
		"the AI series landed on the demoted source; the record the UI shows still has none")
	require.NotNil(t, gotPrimary.SeriesSequence)
	require.Equal(t, 2, *gotPrimary.SeriesSequence)

	gotSource, err := store.GetBookByID(source.ID)
	require.NoError(t, err)
	require.Nil(t, gotSource.SeriesID, "the demoted source was written to as well")
}

// TestSaveAIFieldsWritesToTheRowItselfWhenItIsPrimary is the control. Without
// it, a primaryVersionOf that returned the wrong row for every input would still
// pass the test above.
func TestSaveAIFieldsWritesToTheRowItselfWhenItIsPrimary(t *testing.T) {
	store := aiSaveStore(t)

	row, err := store.CreateBook(&database.Book{
		FilePath:         "/library/Author/Solo/solo.m4b",
		Title:            "Solo",
		IsPrimaryVersion: ptrBool(true),
	})
	require.NoError(t, err)

	require.NoError(t, saveAIFieldsToPrimary(context.Background(), &Book{
		FilePath:  row.FilePath,
		Narrator:  "A Narrator",
		Publisher: "A Publisher",
	}))

	got, err := store.GetBookByID(row.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Narrator)
	require.Equal(t, "A Narrator", *got.Narrator)
	require.NotNil(t, got.Publisher)
	require.Equal(t, "A Publisher", *got.Publisher)
}

// TestSaveAIFieldsNeverOverwritesAnExistingValue pins the "fill empty only"
// contract at the WRITE, not just in runAIBatchPhase's in-memory merge. The
// queued batch carries a snapshot taken during the scan; anything that changed
// the row since -- a metadata fetch, a manual edit -- must win.
func TestSaveAIFieldsNeverOverwritesAnExistingValue(t *testing.T) {
	store := aiSaveStore(t)

	row, err := store.CreateBook(&database.Book{
		FilePath: "/library/Author/Known/known.m4b",
		Title:    "The Real Title",
		Narrator: ptrStr("The Real Narrator"),
	})
	require.NoError(t, err)

	require.NoError(t, saveAIFieldsToPrimary(context.Background(), &Book{
		FilePath: row.FilePath,
		Title:    "AI Guessed Title",
		Narrator: "AI Guessed Narrator",
	}))

	got, err := store.GetBookByID(row.ID)
	require.NoError(t, err)
	require.Equal(t, "The Real Title", got.Title)
	require.Equal(t, "The Real Narrator", *got.Narrator)
}

// TestSaveAIFieldsToleratesAMissingRow: the row can legitimately be gone by the
// time a queued batch runs (deleted, or merged away by dedup). Failing the
// operation for that would fail whole batches over one absent book.
func TestSaveAIFieldsToleratesAMissingRow(t *testing.T) {
	aiSaveStore(t)

	require.NoError(t, saveAIFieldsToPrimary(context.Background(), &Book{
		FilePath: "/nowhere/gone.m4b",
		Title:    "Whatever",
	}))
}

// fakeGroupLookup returns a fixed member list in a fixed order, which is the
// point: the Pebble-backed test above passed against a primaryVersionOf that
// ignored IsPrimaryVersion entirely and returned the group's FIRST member. It
// only passed because the store happened to return the primary first. Ordering
// luck is not a contract, so this pins the selection directly.
type fakeGroupLookup struct {
	scanBookLookup
	members []database.Book
}

func (f fakeGroupLookup) GetBooksByVersionGroup(string) ([]database.Book, error) {
	return f.members, nil
}

func TestPrimaryVersionOfSelectsByFlagNotByPosition(t *testing.T) {
	const group = "vg"
	// The primary is deliberately LAST.
	members := []database.Book{
		{ID: "decoy-1", VersionGroupID: ptrStr(group), IsPrimaryVersion: ptrBool(false)},
		{ID: "decoy-2", VersionGroupID: ptrStr(group)}, // nil flag, not false
		{ID: "the-primary", VersionGroupID: ptrStr(group), IsPrimaryVersion: ptrBool(true)},
	}
	row := &database.Book{ID: "decoy-1", VersionGroupID: ptrStr(group), IsPrimaryVersion: ptrBool(false)}

	got := primaryVersionOf(fakeGroupLookup{members: members}, row)

	require.NotNil(t, got, "no primary found in a group that has one")
	require.Equal(t, "the-primary", got.ID,
		"selected by position instead of IsPrimaryVersion")
}

// TestPrimaryVersionOfFailsOpen: a group with no primary, an unreadable group,
// or no group at all must return nil so the caller writes to the row it already
// has. Returning some other member would move the write to an arbitrary row.
func TestPrimaryVersionOfFailsOpen(t *testing.T) {
	const group = "vg"
	noPrimary := []database.Book{
		{ID: "a", VersionGroupID: ptrStr(group), IsPrimaryVersion: ptrBool(false)},
		{ID: "b", VersionGroupID: ptrStr(group)},
	}

	require.Nil(t, primaryVersionOf(
		fakeGroupLookup{members: noPrimary},
		&database.Book{ID: "a", VersionGroupID: ptrStr(group), IsPrimaryVersion: ptrBool(false)}),
		"a group with no primary must leave the write where it was")

	require.Nil(t, primaryVersionOf(
		fakeGroupLookup{members: noPrimary},
		&database.Book{ID: "solo"}),
		"a book in no version group must not be redirected")

	// A row that is already primary short-circuits BEFORE the group is read.
	// The members list here deliberately contains a primary, and a stale one:
	// GetBooksByVersionGroup is a separate read from the GetBookByFilePath the
	// caller already did, so redirecting to it would swap the caller's fresh row
	// for a second copy and write that instead.
	staleGroup := []database.Book{
		{ID: "a", VersionGroupID: ptrStr(group), IsPrimaryVersion: ptrBool(true), Title: "stale copy"},
	}
	require.Nil(t, primaryVersionOf(
		fakeGroupLookup{members: staleGroup},
		&database.Book{ID: "a", VersionGroupID: ptrStr(group), IsPrimaryVersion: ptrBool(true), Title: "fresh"}),
		"a row that is already primary must not be redirected to a re-read copy of itself")
}
