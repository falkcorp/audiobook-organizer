// file: internal/merge/refused_before_write_test.go
// version: 1.0.0
// guid: 5c8e2a71-b3d4-4f96-8a0e-7d1f6b9c2e43
// last-edited: 2026-11-17

package merge

import (
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
	"github.com/stretchr/testify/require"
)

// A failure from MergeBooks' read-only phase is wrapped in RefusedBeforeWriteError:
// it is the proof, which dedup's journaled merge relies on, that the merge wrote
// nothing. The wrapper must not hide the underlying error from errors.As.
func TestMergeBooks_ReadOnlyPhaseFailuresAreMarkedRefusedBeforeWrite(t *testing.T) {
	store := setupTestStore(t)
	ghost := seedGuardBook(t, store, "Gotham Ghost", "mp3", false)
	real := seedGuardBook(t, store, "Gotham Real", "mp3", true)
	svc := NewService(store)

	t.Run("missing book", func(t *testing.T) {
		_, err := svc.MergeBooks([]string{real.ID, "01ZZZZZZZZZZZZZZZZZZZZZZZZ"}, "")
		var refused *RefusedBeforeWriteError
		var nf *BookNotFoundError
		require.ErrorAs(t, err, &refused)
		require.ErrorAs(t, err, &nf, "the typed error is still visible through the wrapper")
	})
	t.Run("fileless forced primary", func(t *testing.T) {
		_, err := svc.MergeBooks([]string{ghost.ID, real.ID}, ghost.ID)
		var refused *RefusedBeforeWriteError
		var fe *FilelessPrimaryError
		require.ErrorAs(t, err, &refused)
		require.ErrorAs(t, err, &fe)
	})
	t.Run("store read failure", func(t *testing.T) {
		_, err := NewService(&failingGetStore{Store: store, failID: real.ID}).MergeBooks([]string{ghost.ID, real.ID}, "")
		var refused *RefusedBeforeWriteError
		require.ErrorAs(t, err, &refused)
		require.ErrorContains(t, err, "injected")
	})
	t.Run("soft-deleted participant", func(t *testing.T) {
		a := seedGuardBook(t, store, "Gotham A", "mp3", true)
		b := seedGuardBook(t, store, "Gotham B", "mp3", true)
		_, err := svc.MergeBooks([]string{a.ID, b.ID}, a.ID)
		require.NoError(t, err)
		_, err = svc.MergeBooks([]string{a.ID, b.ID}, b.ID)
		var refused *RefusedBeforeWriteError
		var se *SoftDeletedInputError
		require.ErrorAs(t, err, &refused)
		require.ErrorAs(t, err, &se)
	})
	t.Run("a successful merge returns no error at all", func(t *testing.T) {
		a := seedGuardBook(t, store, "Gotham C", "mp3", true)
		b := seedGuardBook(t, store, "Gotham D", "mp3", true)
		_, err := svc.MergeBooks([]string{a.ID, b.ID}, "")
		require.NoError(t, err)
	})
}

// A failure after the merge began writing is a PartialMergeError and is NOT marked:
// it may have written, so nothing may close a row on the strength of it.
func TestMergeBooks_FailureAfterWritesBeganIsNotMarkedRefusedBeforeWrite(t *testing.T) {
	f := siblingFixture(t)
	k := f.Book(t, vptest.Spec{ID: "k", Group: "H", Primary: "true", State: "imported"})
	f.Book(t, vptest.Spec{ID: "h2", Group: "H", Primary: "false"})
	l := f.Book(t, vptest.Spec{ID: "l", Group: "G", Primary: "true", State: "imported"})
	f.Book(t, vptest.Spec{ID: "ls1", Group: "G", Primary: "false"})
	ls2 := f.Book(t, vptest.Spec{ID: "ls2", Group: "G", Primary: "false"})
	fs := &sjFaultStore{PebbleStore: f.S, failModify: map[string]bool{ls2: true}}

	_, err := NewService(fs).MergeBooks([]string{l, k}, "")
	var partial *PartialMergeError
	require.ErrorAs(t, err, &partial)
	var refused *RefusedBeforeWriteError
	require.False(t, errors.As(err, &refused), "a part-way merge may have written: it must not read as a clean refusal")
}
