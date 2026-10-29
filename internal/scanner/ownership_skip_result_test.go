// file: internal/scanner/ownership_skip_result_test.go
// version: 1.0.0
// guid: 7b3e5a1c-92d4-4f68-a0b7-c15e8d2f4963
// last-edited: 2026-10-29

package scanner

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// requireSaved asserts a saveBookToDatabase call saved the book (no error, no
// skip record). It returns the check to apply to the call's two results, so a
// multi-line call reads as requireSaved(t)(saveBookToDatabase(...)).
func requireSaved(t *testing.T) func(*ownershipSkip, error) {
	t.Helper()
	return func(skip *ownershipSkip, err error) {
		t.Helper()
		require.NoError(t, err)
		require.Nil(t, skip)
	}
}

// An ownership skip hands its record (owners, reason, the files to append) back
// beside errFileOwnedByOtherBook. The error is only the signal: it carries none
// of that data, so the file list never travels through an error value (CodeQL
// go/path-injection alerts #2022 and #1651).
func TestSaveBookToDatabase_SkipReturnsRecordBesideBareError(t *testing.T) {
	f := newOwnershipFixture(t)
	useScannerStore(t, f.store)

	staged := append(append([]string{}, f.chapters...), f.loose...)
	skip, err := saveBookToDatabase(context.Background(), &Book{
		FilePath: f.chapters[0], SegmentFiles: staged, Title: "Eldest", Author: "Christopher Paolini", Format: ".mp3",
	})

	require.Equal(t, errFileOwnedByOtherBook, err, "the error must be the bare sentinel, wrapping nothing")
	require.NotNil(t, skip)
	require.Equal(t, f.parent.ID, skip.AppendTo)
	require.ElementsMatch(t, f.loose, skip.Unowned)
	require.Equal(t, []string{f.parent.ID}, skip.Owners)
	require.NotEmpty(t, skip.Reason)
	require.NotContains(t, err.Error(), f.parent.ID)

	// A skip with no append (a chapter alone) still returns its record.
	skip, err = saveBookToDatabase(context.Background(), &Book{
		FilePath: f.chapters[1], Title: "98", Author: "Eldest", Format: ".mp3", BookOrganizerID: f.parent.ID,
	})
	require.ErrorIs(t, err, errFileOwnedByOtherBook)
	require.NotNil(t, skip)
	require.Empty(t, skip.AppendTo)
	require.Equal(t, []string{f.parent.ID}, skip.Owners)

	// A normal save returns no record.
	skip, err = saveBookToDatabase(context.Background(), &Book{FilePath: f.loose[0], Title: "New", Author: "Someone", Format: ".mp3"})
	require.NoError(t, err)
	require.Nil(t, skip)
}
