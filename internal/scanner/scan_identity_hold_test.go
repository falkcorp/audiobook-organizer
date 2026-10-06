// file: internal/scanner/scan_identity_hold_test.go
// version: 1.1.0
// guid: 98f7a755-16bc-4938-92bb-a032c55d1b10
// last-edited: 2026-10-06

package scanner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// readScanProposal returns bookID's stored scan identity proposal, nil when
// there is none.
func readScanProposal(t *testing.T, st *database.PebbleStore, bookID string) *database.ScanIdentityProposal {
	t.Helper()
	raw, err := st.GetRaw(database.ScanIdentityProposalKey(bookID))
	require.NoError(t, err)
	if len(raw) == 0 {
		return nil
	}
	var p database.ScanIdentityProposal
	require.NoError(t, json.Unmarshal(raw, &p))
	return &p
}

// identityHoldFixture is a pebble store wired as the scanner's store, with a
// RootDir to write fake book files under.
func identityHoldFixture(t *testing.T) (*database.PebbleStore, func(name string) string) {
	t.Helper()
	store, cleanup := setupPebbleStore(t)
	t.Cleanup(cleanup)
	prevStore := database.GetGlobalStore()
	database.SetGlobalStore(store)
	SetStore(store)
	t.Cleanup(func() {
		database.SetGlobalStore(prevStore)
		SetStore(nil)
	})
	prevConfig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = prevConfig })
	root := t.TempDir()
	config.AppConfig.RootDir = root
	write := func(name string) string {
		t.Helper()
		p := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte("content of "+name), 0o644))
		return p
	}
	return store, write
}

func authorNameOf(t *testing.T, st *database.PebbleStore, b *database.Book) string {
	t.Helper()
	if b.AuthorID == nil {
		return ""
	}
	a, err := st.GetAuthorByID(*b.AuthorID)
	require.NoError(t, err)
	if a == nil {
		return ""
	}
	return a.Name
}

// The 2026-10-06 incident: a rescan whose file reads a different title and
// author must keep the existing row's title, author and work -- and so its
// cached candidates, which the store drops on a title or author change --
// record what the file says as a proposal, add no co-author credit, and
// record no history row for a field it did not change.
func TestScanIdentityHold_RescanKeepsIdentityAndCandidates(t *testing.T) {
	st, write := identityHoldFixture(t)
	path := write("Brent Weeks/Shadow's Edge/fragment.mp3")
	require.NoError(t, saveBookToDatabase(context.Background(),
		&Book{FilePath: path, Title: "183 of 301", Author: "Brent Weeks", Format: ".mp3", Duration: 100}))
	before, err := st.GetBookByFilePath(path)
	require.NoError(t, err)
	require.NotNil(t, before)
	require.NotNil(t, before.WorkID)
	require.NoError(t, st.PutMetadataCache(&database.MetadataCandidateCache{BookID: before.ID,
		Candidates: []json.RawMessage{json.RawMessage(`{"title":"Shadow's Edge"}`)}, FetchedAt: time.Now()}))

	// The rescan: the tag/file-name chain now reads another title, and a
	// two-person credit.
	require.NoError(t, saveBookToDatabase(context.Background(),
		&Book{FilePath: path, Title: "of 301", Author: "Colin Baker & Maggie Stables", Format: ".mp3", Duration: 120}))

	after, err := st.GetBookByID(before.ID)
	require.NoError(t, err)
	require.Equal(t, "183 of 301", after.Title, "the rescan rewrote an existing title")
	require.Equal(t, "Brent Weeks", authorNameOf(t, st, after), "the rescan rewrote an existing author")
	require.Equal(t, *before.WorkID, *after.WorkID, "the rescan moved the book to another work")
	require.Equal(t, 120, *after.Duration, "a file-derived field must still land")

	cache, err := st.GetMetadataCache(before.ID)
	require.NoError(t, err)
	require.NotNil(t, cache, "the rescan dropped the book's cached candidates")
	require.Len(t, cache.Candidates, 1)

	credits, err := st.GetBookAuthors(before.ID)
	require.NoError(t, err)
	for _, c := range credits {
		require.Equal(t, *before.AuthorID, c.AuthorID, "the held author's credits gained a co-author from the tag")
	}

	p := readScanProposal(t, st, before.ID)
	require.NotNil(t, p, "the scanned identity was not recorded as a proposal")
	require.Equal(t, database.ScanIdentityChange{From: "183 of 301", To: "of 301"}, p.Fields[database.ScanProposalTitle])
	require.Equal(t, database.ScanIdentityChange{From: "Brent Weeks", To: "Colin Baker & Maggie Stables"},
		p.Fields[database.ScanProposalAuthor])

	history, err := st.GetBookChangeHistory(before.ID, 1<<30)
	require.NoError(t, err)
	var fields []string
	for _, h := range history {
		require.Equal(t, database.ChangeTypeScan, h.ChangeType)
		fields = append(fields, h.Field)
	}
	require.Contains(t, fields, "duration", "the scanner's duration write recorded no history")
	require.NotContains(t, fields, "title")
	require.NotContains(t, fields, database.HistoryFieldAuthor)

	// The file agrees with the row again: the proposal is cleared.
	require.NoError(t, saveBookToDatabase(context.Background(),
		&Book{FilePath: path, Title: "183 of 301", Author: "Brent Weeks", Format: ".mp3", Duration: 120}))
	require.Nil(t, readScanProposal(t, st, before.ID), "a converged proposal was left behind")
}

// A new import still takes the file's values, and records no proposal.
func TestScanIdentityHold_NewImportTakesTagValues(t *testing.T) {
	st, write := identityHoldFixture(t)
	path := write("Tasha Suri/The Jasmine Throne/book.m4b")
	require.NoError(t, saveBookToDatabase(context.Background(),
		&Book{FilePath: path, Title: "The Jasmine Throne", Author: "Tasha Suri", Series: "Burning Kingdoms", Position: 1,
			Format: ".m4b", Duration: 100}))
	b, err := st.GetBookByFilePath(path)
	require.NoError(t, err)
	require.NotNil(t, b)
	require.Equal(t, "The Jasmine Throne", b.Title)
	require.Equal(t, "Tasha Suri", authorNameOf(t, st, b))
	require.NotNil(t, b.SeriesSequence)
	require.Equal(t, 1, *b.SeriesSequence)
	require.Nil(t, readScanProposal(t, st, b.ID))
}

// The inline AI phase re-saves a NEW import through saveBookToDatabase, which
// then finds the row the main pass created. That row is this scan's new
// import: it takes the AI title.
func TestScanIdentityHold_AIResaveOfNewImportTakesAITitle(t *testing.T) {
	st, write := identityHoldFixture(t)
	path := write("Unknown/74.m4b")
	b := &Book{FilePath: path, Title: "74", Author: "Tasha Suri", Format: ".m4b", Duration: 100}
	require.NoError(t, saveBookToDatabase(context.Background(), b))
	require.NotEmpty(t, b.createdRowID)

	b.Title = "The Jasmine Throne"
	require.NoError(t, saveBookToDatabase(context.Background(), b))
	got, err := st.GetBookByID(b.createdRowID)
	require.NoError(t, err)
	require.Equal(t, "The Jasmine Throne", got.Title)
	require.Nil(t, readScanProposal(t, st, got.ID))
}

// A gap is not a rewrite: an existing row with no author, series or position
// takes the file's, with a history row; its title is held.
func TestScanIdentityHold_FillsEmptyIdentityWithHistory(t *testing.T) {
	st, write := identityHoldFixture(t)
	path := write("x/book.m4b")
	row, err := st.CreateBook(&database.Book{Title: "Zero History", FilePath: path, Format: "m4b"})
	require.NoError(t, err)

	require.NoError(t, saveBookToDatabase(context.Background(),
		&Book{FilePath: path, Title: "10 Zero History", Author: "William Gibson", Series: "Blue Ant", Position: 3,
			Format: ".m4b", Duration: 100}))
	got, err := st.GetBookByID(row.ID)
	require.NoError(t, err)
	require.Equal(t, "Zero History", got.Title)
	require.Equal(t, "William Gibson", authorNameOf(t, st, got))
	require.NotNil(t, got.SeriesID)
	require.NotNil(t, got.SeriesSequence)
	require.Equal(t, 3, *got.SeriesSequence)

	history, err := st.GetBookChangeHistory(row.ID, 1<<30)
	require.NoError(t, err)
	byField := map[string]database.MetadataChangeRecord{}
	for _, h := range history {
		byField[h.Field] = h
	}
	a, ok := byField[database.HistoryFieldAuthor]
	require.True(t, ok, "the author fill recorded no history")
	require.Equal(t, database.ChangeTypeScan, a.ChangeType)
	require.Equal(t, `"William Gibson"`, *a.NewValue)

	p := readScanProposal(t, st, row.ID)
	require.NotNil(t, p)
	require.Equal(t, "10 Zero History", p.Fields[database.ScanProposalTitle].To)
	_, hasAuthor := p.Fields[database.ScanProposalAuthor]
	require.False(t, hasAuthor, "a filled author is not a proposal")
}

// A user-locked field is never proposed.
func TestScanIdentityChanges_SkipsUserLocked(t *testing.T) {
	w := &database.Book{Title: "Stored"}
	held := map[string]bool{database.FieldKeyTitle: true}
	got := scanIdentityChanges(w, scannedIdentity{Title: "Scanned"}, held, map[string]bool{database.FieldKeyTitle: true})
	require.Empty(t, got)
	got = scanIdentityChanges(w, scannedIdentity{Title: "Scanned"}, held, nil)
	require.Equal(t, "Scanned", got[database.ScanProposalTitle].To)
	got = scanIdentityChanges(w, scannedIdentity{Title: " stored "}, held, nil)
	require.Empty(t, got, "a case/space-only difference is not a proposal")
}

// "183 of 301" is a position out of a count: the file-name chain must not
// strip its number and leave "of 301" (the 299 Shadow's Edge regressions).
func TestExtractInfoFromPath_CountedPartKeepsItsNumber(t *testing.T) {
	for _, p := range []string{
		"/lib/Brent Weeks/Shadow's Edge/183 of 301.mp3",
		"/lib/Brent Weeks/Shadow's Edge/Shadow's Edge - 183 of 301.mp3",
	} {
		b := Book{FilePath: p, Author: "Brent Weeks"}
		extractInfoFromPath(&b)
		require.NotEqual(t, "of 301", b.Title, p)
		require.NotEmpty(t, b.Title, p)
		t.Logf("%s -> %q", p, b.Title)
	}
	// A track number before a title is still stripped.
	b := Book{FilePath: "/lib/William Gibson/Zero History/10 Zero History.mp3", Author: "William Gibson"}
	extractInfoFromPath(&b)
	require.Equal(t, "Zero History", b.Title)
}

// A second save of the same Book in one scan (the inline AI phase re-saves
// it, by then holding the row's own values) must not erase the proposal the
// first save recorded.
func TestScanIdentityHold_ResaveKeepsFirstSaveProposal(t *testing.T) {
	st, write := identityHoldFixture(t)
	path := write("Tasha Suri/74/74.mp3")
	require.NoError(t, saveBookToDatabase(context.Background(),
		&Book{FilePath: path, Title: "74", Author: "Tasha Suri", Format: ".mp3", Duration: 100}))
	row, err := st.GetBookByFilePath(path)
	require.NoError(t, err)
	require.NotNil(t, row)

	rescan := &Book{FilePath: path, Title: "The Jasmine Throne", Author: "Tasha Suri", Format: ".mp3", Duration: 100}
	require.NoError(t, saveBookToDatabase(context.Background(), rescan))
	require.NotNil(t, readScanProposal(t, st, row.ID), "the first save recorded no proposal")
	require.Equal(t, "74", rescan.Title, "the hold put the row's title on the Book")

	// The AI-phase re-save of the same Book.
	require.NoError(t, saveBookToDatabase(context.Background(), rescan))

	p := readScanProposal(t, st, row.ID)
	require.NotNil(t, p, "the re-save deleted the first save's proposal")
	require.Equal(t, database.ScanIdentityChange{From: "74", To: "The Jasmine Throne"}, p.Fields[database.ScanProposalTitle])
	after, err := st.GetBookByID(row.ID)
	require.NoError(t, err)
	require.Equal(t, "74", after.Title)
}

// A row that keeps series X with no position must not take the position the
// file gives for series Y ("Y #3" is not "X #3"); the file's series and
// position are proposed together. The same series' position is still a gap
// fill.
func TestScanIdentityHold_PositionFromOtherSeriesIsNotAGapFill(t *testing.T) {
	st, write := identityHoldFixture(t)
	path := write("Author A/Book/book.mp3")
	require.NoError(t, saveBookToDatabase(context.Background(),
		&Book{FilePath: path, Title: "Book", Author: "Author A", Series: "Series X", Format: ".mp3", Duration: 100}))
	row, err := st.GetBookByFilePath(path)
	require.NoError(t, err)
	require.NotNil(t, row)
	require.NotNil(t, row.SeriesID)
	require.True(t, row.SeriesSequence == nil || *row.SeriesSequence == 0, "fixture row must have no position")

	require.NoError(t, saveBookToDatabase(context.Background(),
		&Book{FilePath: path, Title: "Book", Author: "Author A", Series: "Series Y", Position: 3, Format: ".mp3", Duration: 100}))
	after, err := st.GetBookByID(row.ID)
	require.NoError(t, err)
	require.Equal(t, *row.SeriesID, *after.SeriesID, "the rescan moved the book to another series")
	require.True(t, after.SeriesSequence == nil || *after.SeriesSequence == 0,
		"series Y's position 3 filled series X's empty position: %v", after.SeriesSequence)
	p := readScanProposal(t, st, row.ID)
	require.NotNil(t, p)
	require.Equal(t, database.ScanIdentityChange{From: "Series X", To: "Series Y"}, p.Fields[database.ScanProposalSeries])
	require.Equal(t, "3", p.Fields[database.ScanProposalSeriesPosition].To)

	// The row's own series with a position: a gap, filled.
	require.NoError(t, saveBookToDatabase(context.Background(),
		&Book{FilePath: path, Title: "Book", Author: "Author A", Series: "Series X", Position: 3, Format: ".mp3", Duration: 100}))
	after, err = st.GetBookByID(row.ID)
	require.NoError(t, err)
	require.NotNil(t, after.SeriesSequence)
	require.Equal(t, 3, *after.SeriesSequence, "the row's own series position was not filled")
}

// The write-time half (identityMergeLocks) holds a position from another
// series too: the raced-row and late hash-duplicate paths reach the merge
// without holdIdentityForExisting.
func TestIdentityMergeLocks_HoldsPositionFromOtherSeries(t *testing.T) {
	x := 7
	cur := &database.Book{Title: "Book", SeriesID: &x}
	same := identityGuard{seriesID: &x, seriesName: "Series X", scanned: scannedIdentity{Series: "series x", Position: 3}}
	_, held := identityMergeLocks(nil, cur, same)
	require.False(t, held[database.FieldKeySeriesPosition], "the row's own series position must stay a gap")

	other := same
	other.scanned.Series = "Series Y"
	_, held = identityMergeLocks(nil, cur, other)
	require.True(t, held[database.FieldKeySeriesPosition])

	y := 8
	moved := same
	moved.seriesID = &y // the row's series changed after the guard read it
	_, held = identityMergeLocks(nil, cur, moved)
	require.True(t, held[database.FieldKeySeriesPosition])
}
