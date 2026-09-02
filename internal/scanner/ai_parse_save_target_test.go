// file: internal/scanner/ai_parse_save_target_test.go
// version: 1.1.2
// guid: ade87d70-9dc4-4aee-9538-449a631e678d
// last-edited: 2026-09-02

package scanner

import (
	"context"
	"errors"
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

// saveAI drops the stamp-path return so the assertions below stay about the
// row that got written, which is what these tests are for.
func saveAI(id string, b *Book) error {
	_, err := saveAIFieldsToPrimary(context.Background(), id, b)
	return err
}

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
		VersionGroupID:   new(group),
		IsPrimaryVersion: new(false),
	})
	require.NoError(t, err)

	primary, err := store.CreateBook(&database.Book{
		FilePath:         "/library/Author/A Book/book.m4b",
		Title:            "A Book",
		VersionGroupID:   new(group),
		IsPrimaryVersion: new(true),
	})
	require.NoError(t, err)

	// The batch carries the SOURCE path, because that is the path the scan
	// walked and nominated.
	require.NoError(t, saveAI("", &Book{
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
		IsPrimaryVersion: new(true),
	})
	require.NoError(t, err)

	require.NoError(t, saveAI("", &Book{
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
		Narrator: new("The Real Narrator"),
	})
	require.NoError(t, err)

	require.NoError(t, saveAI("", &Book{
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

	require.NoError(t, saveAI("", &Book{
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
	err     error
}

func (f fakeGroupLookup) GetBooksByVersionGroup(string) ([]database.Book, error) {
	return f.members, f.err
}

func TestPrimaryVersionOfSelectsByFlagNotByPosition(t *testing.T) {
	const group = "vg"
	// The primary is deliberately LAST.
	members := []database.Book{
		{ID: "decoy-1", VersionGroupID: new(group), IsPrimaryVersion: new(false)},
		{ID: "decoy-2", VersionGroupID: new(group)}, // nil flag, not false
		{ID: "the-primary", VersionGroupID: new(group), IsPrimaryVersion: new(true)},
	}
	row := &database.Book{ID: "decoy-1", VersionGroupID: new(group), IsPrimaryVersion: new(false)}

	got, err := primaryVersionOf(fakeGroupLookup{members: members}, row)

	require.NoError(t, err)
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
		{ID: "a", VersionGroupID: new(group), IsPrimaryVersion: new(false)},
		{ID: "b", VersionGroupID: new(group)},
	}

	got, err := primaryVersionOf(
		fakeGroupLookup{members: noPrimary},
		&database.Book{ID: "a", VersionGroupID: new(group), IsPrimaryVersion: new(false)})
	require.NoError(t, err)
	require.Nil(t, got, "a group with no primary must leave the write where it was")

	got, err = primaryVersionOf(
		fakeGroupLookup{members: noPrimary},
		&database.Book{ID: "solo"})
	require.NoError(t, err)
	require.Nil(t, got, "a book in no version group must not be redirected")

	// A row that is already primary short-circuits BEFORE the group is read.
	// The members list here deliberately contains a primary, and a stale one:
	// GetBooksByVersionGroup is a separate read from the GetBookByFilePath the
	// caller already did, so redirecting to it would swap the caller's fresh row
	// for a second copy and write that instead.
	staleGroup := []database.Book{
		{ID: "a", VersionGroupID: new(group), IsPrimaryVersion: new(true), Title: "stale copy"},
	}
	got, err = primaryVersionOf(
		fakeGroupLookup{members: staleGroup},
		&database.Book{ID: "a", VersionGroupID: new(group), IsPrimaryVersion: new(true), Title: "fresh"})
	require.NoError(t, err)
	require.Nil(t, got, "a row that is already primary must not be redirected to a re-read copy of itself")
}

// TestPrimaryVersionOfSurfacesAReadFailure: failing open here is right --
// skipping the write loses the update too -- but it must not be SILENT. The row
// the caller holds in that case is the demoted organized_source, so a swallowed
// error writes the AI fields somewhere the UI never shows them and reports
// success, which is exactly the bug primaryVersionOf exists to prevent.
func TestPrimaryVersionOfSurfacesAReadFailure(t *testing.T) {
	boom := errors.New("pebble is unhappy")
	got, err := primaryVersionOf(
		fakeGroupLookup{err: boom},
		&database.Book{ID: "a", VersionGroupID: new("vg"), IsPrimaryVersion: new(false)})

	require.ErrorIs(t, err, boom, "a group read failure must be reported, not swallowed")
	require.Nil(t, got, "and it must still fail OPEN, leaving the caller its own row")
}

// TestSaveAIFieldsFollowsTheRowIDAcrossAnInPlaceRename is the defect that made
// the queued path silently lossy in the normal prod configuration.
//
// OrganizeOneBook sends every book under RootDir -- i.e. every book in an
// ordinary library scan -- through ReOrganizeInPlace, which is a true
// safeRename, and then rewrites the row's FilePath. Organize runs AFTER
// ProcessBooksParallel returns, so by the time the queued batch ran, the path in
// its params named nothing: GetBookByFilePath returned no row, the saver
// returned a bare nil, and the parse was discarded with no error and no log
// line. Carrying the row ID is what survives the rename.
func TestSaveAIFieldsFollowsTheRowIDAcrossAnInPlaceRename(t *testing.T) {
	store := aiSaveStore(t)

	const scannedPath = "/library/incoming/some file.m4b"
	row, err := store.CreateBook(&database.Book{FilePath: scannedPath})
	require.NoError(t, err)

	// Organize renames the file and rewrites the row's path. The batch is still
	// carrying the path it was queued with.
	const organizedPath = "/library/Author Name/A Title/A Title.m4b"
	row.FilePath = organizedPath
	_, err = store.UpdateBook(row.ID, row)
	require.NoError(t, err)

	stamped, err := saveAIFieldsToPrimary(context.Background(), row.ID, &Book{
		FilePath: scannedPath, // the dead path
		Title:    "A Title",
		Narrator: "A Narrator",
	})
	require.NoError(t, err)

	got, err := store.GetBookByID(row.ID)
	require.NoError(t, err)
	require.Equal(t, "A Title", got.Title,
		"the parse was discarded: the ID did not survive the rename")
	require.NotNil(t, got.Narrator)
	require.Equal(t, "A Narrator", *got.Narrator)

	// And the stamp must name the row's CURRENT path, not the params one --
	// stamping the dead path writes nothing and the book is re-read forever.
	require.Equal(t, organizedPath, stamped)
}

// TestSaveAIFieldsReportsTheStampPathForAnUnchangedRow: a book the LLM had
// nothing to say about has still been ATTEMPTED, and must be stamped. Leaving
// it unstamped re-reads AND re-queues the same unparseable filename on every
// scan, which is how an LLM budget gets burned by a scan feedback loop.
func TestSaveAIFieldsReportsTheStampPathForAnUnchangedRow(t *testing.T) {
	store := aiSaveStore(t)
	row, err := store.CreateBook(&database.Book{FilePath: "/lib/x.m4b", Title: "Already Set"})
	require.NoError(t, err)

	stamped, err := saveAIFieldsToPrimary(context.Background(), row.ID, &Book{FilePath: "/lib/x.m4b"})
	require.NoError(t, err)
	require.Equal(t, "/lib/x.m4b", stamped,
		"an attempted-but-unchanged book must still report a path to stamp")
}
