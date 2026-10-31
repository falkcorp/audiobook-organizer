// file: internal/database/claim_repair_locks_test.go
// version: 1.0.0
// guid: c1b8f6a3-52e7-4d90-8a14-6e3d97f0b258
// last-edited: 2026-10-30

package database

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func claimStore(t *testing.T) *PebbleStore {
	t.Helper()
	st, err := NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func claimRow(t *testing.T, st *PebbleStore, bookID, field string) *MetadataFieldState {
	t.Helper()
	rows, err := st.GetMetadataFieldStates(bookID)
	require.NoError(t, err)
	for i := range rows {
		if rows[i].Field == field {
			return &rows[i]
		}
	}
	return nil
}

// A repair lock becomes the person's; a lock a revert lifted is taken again as
// the person's; a person's lock or override is left as it is.
func TestClaimRepairLocksLocked(t *testing.T) {
	st := claimStore(t)
	const book = "b-claim"
	fetched := `"Alfred"`
	val := `"Dick Grayson"`
	require.NoError(t, st.UpsertMetadataFieldState(&MetadataFieldState{BookID: book, Field: FieldKeyTitle,
		OverrideLocked: true, LockSource: RepairLockSource("op-1")}))
	require.NoError(t, st.UpsertMetadataFieldState(&MetadataFieldState{BookID: book, Field: FieldKeyNarrator,
		OverrideLocked: false, FetchedValue: &fetched}))
	require.NoError(t, st.UpsertMetadataFieldState(&MetadataFieldState{BookID: book, Field: FieldKeyPublisher,
		OverrideLocked: true, OverrideValue: &val}))

	unlock := LockMetadataState(book)
	err := ClaimRepairLocksLocked(st, book, []string{FieldKeyTitle, FieldKeyNarrator, FieldKeyPublisher, FieldKeyLanguage})
	unlock()
	require.NoError(t, err)

	title := claimRow(t, st, book, FieldKeyTitle)
	require.NotNil(t, title)
	assert.True(t, title.OverrideLocked)
	assert.Empty(t, title.LockSource, "a repair lock is claimed")

	narr := claimRow(t, st, book, FieldKeyNarrator)
	require.NotNil(t, narr)
	assert.True(t, narr.OverrideLocked, "a lifted lock is taken again")
	assert.Empty(t, narr.LockSource)
	require.NotNil(t, narr.FetchedValue)
	assert.Equal(t, fetched, *narr.FetchedValue, "the provider value stays")

	pub := claimRow(t, st, book, FieldKeyPublisher)
	require.NotNil(t, pub)
	require.NotNil(t, pub.OverrideValue)
	assert.Equal(t, val, *pub.OverrideValue, "a person's override is untouched")

	lang := claimRow(t, st, book, FieldKeyLanguage)
	require.NotNil(t, lang, "a lock with no row at all is created")
	assert.True(t, lang.OverrideLocked)
	assert.Empty(t, lang.LockSource)
}
