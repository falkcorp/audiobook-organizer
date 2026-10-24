// file: internal/database/retired_book_files_guards_test.go
// version: 1.1.0
// guid: 4b8d2e60-7a13-4c95-b0e8-2d6f91a3c570
// last-edited: 2026-10-24

package database

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"
)

// A retired version is created only by the retire op, which says which shell
// it was retired out of and when. Either piece missing is refused, and nothing
// is stored.
func TestCreateBookVersion_RetiredNeedsProvenance(t *testing.T) {
	fx := newRetiredFixture(t)
	now := time.Now()
	cases := map[string]BookVersion{
		"no shell":           {BookID: fx.holder.ID, Status: BookVersionStatusRetired, Format: "mp3", RetireOpID: "op", RetiredAt: &now},
		"no retired_at":      {BookID: fx.holder.ID, Status: BookVersionStatusRetired, Format: "mp3", RetireOpID: "op", RetiredFromBookID: fx.shell.ID},
		"neither provenance": {BookID: fx.holder.ID, Status: BookVersionStatusRetired, Format: "mp3"},
	}
	for name, v := range cases {
		t.Run(name, func(t *testing.T) {
			v.ID = "refused-" + fx.holder.ID
			got, err := fx.ps.CreateBookVersion(&v)
			require.ErrorIs(t, err, ErrRetiredVersionMismatch)
			require.Nil(t, got)
			stored, gerr := fx.ps.GetBookVersion(v.ID)
			require.NoError(t, gerr)
			require.Nil(t, stored, "a refused version must not be stored")
		})
	}
}

// A retired version cannot be re-homed onto the shell it was retired out of:
// that book is the one whose files it describes, and holding them again is an
// undo, not a re-home. The refusal leaves the version and its rows where they were.
func TestRehomeRetiredVersion_RefusesTheShell(t *testing.T) {
	fx := newRetiredFixture(t)
	x := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/ret/rehome-shell/01.mp3", FileHash: "rehome-shell-hash"})
	require.NoError(t, fx.retire(t, x))

	err := fx.ps.RehomeRetiredVersion(fx.version.ID, fx.shell.ID)
	require.ErrorIs(t, err, ErrRetiredVersionMismatch)

	v, err := fx.ps.GetBookVersion(fx.version.ID)
	require.NoError(t, err)
	require.Equal(t, fx.holder.ID, v.BookID)
	rows, err := fx.ps.GetRetiredBookFiles(fx.holder.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, fx.holder.ID+":"+x.ID, indexOwner(t, fx.ps, "book_file_hash:rehome-shell-hash"))
}

// roundFixture is one more shell, retired version and file for a repeated
// concurrency round, all sharing the fixture's holder.
func (f *retiredFixture) newRound(t *testing.T, i int) (shell *Book, v *BookVersion, row *BookFile) {
	t.Helper()
	shell, err := f.ps.CreateBook(&Book{Title: fmt.Sprintf("Round Shell %d", i), FilePath: fmt.Sprintf("/lib/ret/round/%d", i)})
	require.NoError(t, err)
	v, err = f.ps.CreateBookVersion(&BookVersion{
		BookID: f.holder.ID, Status: BookVersionStatusRetired, Format: "mp3", Source: "retired_book",
		RetiredFromBookID: shell.ID, RetireOpID: "op-round", RetiredAt: new(time.Now()),
	})
	require.NoError(t, err)
	row = f.create(t, shell.ID, BookFile{FilePath: fmt.Sprintf("/lib/ret/round/%d/01.mp3", i), FileHash: fmt.Sprintf("round-hash-%d", i)})
	return shell, v, row
}

// runTogether starts every fn at the same moment and waits for all of them.
func runTogether(fns ...func()) {
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, fn := range fns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			fn()
		}()
	}
	close(start)
	wg.Wait()
}

// A -race smoke test of a single-row writer racing RetireBookFiles: nothing
// may be lost or resurrected under the shell the row left. PatchBookFileFields
// reads book_file:<shell>:<id>; read before the retire's delete and written
// after, it would put a second, live copy back under the shell. The window is
// too narrow for the scheduler to hit reliably, so this test passes with the row
// stripe removed too (checked); it is here for the race detector and for the
// end-state invariants. The deterministic guard that the stripes are taken is
// TestRetiredWriters_TakeVersionAndRowStripes.
func TestRetireBookFiles_ConcurrentRowWriterNeverResurrectsTheShellCopy(t *testing.T) {
	fx := newRetiredFixture(t)
	const rounds = 12
	for i := range rounds {
		shell, v, row := fx.newRound(t, i)
		title := fmt.Sprintf("patched-%d", i)

		var retireErr, patchErr error
		var patched *BookFile
		runTogether(
			func() {
				retireErr = fx.ps.RetireBookFiles(
					[]BookFileMove{{FileIDs: []string{row.ID}, SourceBookID: shell.ID}}, fx.holder.ID, v.ID)
			},
			func() {
				_, patched, patchErr = fx.ps.PatchBookFileFields(shell.ID, row.ID, BookFileFieldPatch{Title: &title})
			},
		)
		require.NoError(t, retireErr, "round %d", i)
		require.NoError(t, patchErr, "round %d", i)

		_, closer, err := fx.ps.db.Get([]byte("book_file:" + shell.ID + ":" + row.ID))
		if err == nil {
			closer.Close()
			t.Fatalf("round %d: the shell's copy of the row was resurrected", i)
		}
		require.ErrorIs(t, err, pebble.ErrNotFound)

		got, err := fx.ps.GetBookFileByID(fx.holder.ID, row.ID)
		require.NoError(t, err)
		require.NotNil(t, got, "round %d: the row must exist under the holder", i)
		require.True(t, got.Retired)
		require.Equal(t, v.ID, got.VersionID)
		if patched != nil {
			require.Equal(t, title, got.Title, "round %d: a patch that was applied must be on the retired row", i)
		}
	}
}

// A -race smoke test of a caller holding a version loaded before
// RehomeRetiredVersion moved it: afterwards the version names the holder its
// rows are under, whichever got there first. Like the test above it cannot hit
// the narrow window on demand; the stripe guard is
// TestRetiredWriters_TakeVersionAndRowStripes.
func TestRehomeRetiredVersion_ConcurrentStaleUpdateNeverMovesItBack(t *testing.T) {
	fx := newRetiredFixture(t)
	const rounds = 12
	for i := range rounds {
		shell, v, row := fx.newRound(t, i)
		require.NoError(t, fx.ps.RetireBookFiles(
			[]BookFileMove{{FileIDs: []string{row.ID}, SourceBookID: shell.ID}}, fx.holder.ID, v.ID))
		newHolder, err := fx.ps.CreateBook(&Book{Title: fmt.Sprintf("Round Holder %d", i), FilePath: fmt.Sprintf("/lib/ret/roundholder/%d", i)})
		require.NoError(t, err)

		stale, err := fx.ps.GetBookVersion(v.ID)
		require.NoError(t, err)
		stale.MetadataJSON = fmt.Sprintf(`{"round":%d}`, i)

		var rehomeErr, updateErr error
		runTogether(
			func() { rehomeErr = fx.ps.RehomeRetiredVersion(v.ID, newHolder.ID) },
			func() { updateErr = fx.ps.UpdateBookVersion(stale) },
		)
		require.NoError(t, rehomeErr, "round %d", i)
		if updateErr != nil {
			require.True(t, errors.Is(updateErr, ErrRetiredVersionHasFiles), "round %d: unexpected update error: %v", i, updateErr)
		}

		got, err := fx.ps.GetBookVersion(v.ID)
		require.NoError(t, err)
		require.Equal(t, newHolder.ID, got.BookID, "round %d: the stale update moved the version back", i)
		rows, err := fx.ps.GetRetiredBookFiles(newHolder.ID)
		require.NoError(t, err)
		require.Len(t, rows, 1, "round %d", i)
		require.Equal(t, v.ID, rows[0].VersionID)
		old, err := fx.ps.GetRetiredBookFiles(fx.holder.ID)
		require.NoError(t, err)
		for _, r := range old {
			require.NotEqual(t, v.ID, r.VersionID, "round %d: a row is left under the old holder", i)
		}
	}
}

// blocksWhileHeld asserts that fn does not complete while a stripe is held and
// does once it is released. The concurrent-writer tests above only catch a
// missing lock when the scheduler happens to hit the window; this one fails
// every time the stripe is not taken. The wait is a negative assertion, so a
// slow machine can only make it pass, never fail.
func blocksWhileHeld(t *testing.T, name string, unlock func(), fn func() error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		unlock()
		t.Fatalf("%s finished (err=%v) while its stripe was held: it does not take the lock", name, err)
	case <-time.After(150 * time.Millisecond):
	}
	unlock()
	select {
	case err := <-done:
		// fn's result is the second half of the assertion: it must have run to
		// its expected outcome once the stripe was released, not merely returned.
		require.NoError(t, err, "%s after its stripe was released", name)
	case <-time.After(10 * time.Second):
		t.Fatalf("%s did not finish after its stripe was released", name)
	}
}

// Every retired-row writer and the version writers take the stripes the plan
// names, from read to commit: the version stripe for RetireBookFiles,
// UnretireBookFiles, RehomeRetiredVersion, UpdateBookVersion and
// DeleteBookVersion, and each row's stripe for the three row movers.
func TestRetiredWriters_TakeVersionAndRowStripes(t *testing.T) {
	fx := newRetiredFixture(t)
	i := 0
	round := func() (*Book, *BookVersion, *BookFile) {
		i++
		return fx.newRound(t, i)
	}
	retireOne := func(shell *Book, v *BookVersion, row *BookFile) {
		require.NoError(t, fx.ps.RetireBookFiles(
			[]BookFileMove{{FileIDs: []string{row.ID}, SourceBookID: shell.ID}}, fx.holder.ID, v.ID))
	}

	t.Run("RetireBookFiles waits for the row stripe", func(t *testing.T) {
		shell, v, row := round()
		unlock := fx.ps.lockBookFile(row.ID)
		blocksWhileHeld(t, "RetireBookFiles", unlock, func() error {
			return fx.ps.RetireBookFiles([]BookFileMove{{FileIDs: []string{row.ID}, SourceBookID: shell.ID}}, fx.holder.ID, v.ID)
		})
	})
	t.Run("RetireBookFiles waits for the version stripe", func(t *testing.T) {
		shell, v, row := round()
		unlock := fx.ps.lockBookVersion(v.ID)
		blocksWhileHeld(t, "RetireBookFiles", unlock, func() error {
			return fx.ps.RetireBookFiles([]BookFileMove{{FileIDs: []string{row.ID}, SourceBookID: shell.ID}}, fx.holder.ID, v.ID)
		})
	})
	t.Run("UnretireBookFiles waits for the row and version stripes", func(t *testing.T) {
		shell, v, row := round()
		retireOne(shell, v, row)
		unretire := func() error {
			return fx.ps.UnretireBookFiles([]BookFileMove{{FileIDs: []string{row.ID}, SourceBookID: fx.holder.ID}}, shell.ID, v.ID, nil)
		}
		blocksWhileHeld(t, "UnretireBookFiles (row stripe)", fx.ps.lockBookFile(row.ID), func() error { return unretire() })
		// The first call ran to completion once released; put the row back.
		retireOne(shell, v, row)
		blocksWhileHeld(t, "UnretireBookFiles (version stripe)", fx.ps.lockBookVersion(v.ID), func() error { return unretire() })
	})
	t.Run("RehomeRetiredVersion waits for the row and version stripes", func(t *testing.T) {
		shell, v, row := round()
		retireOne(shell, v, row)
		h1, err := fx.ps.CreateBook(&Book{Title: "Stripe Holder 1", FilePath: "/lib/ret/stripe/1"})
		require.NoError(t, err)
		h2, err := fx.ps.CreateBook(&Book{Title: "Stripe Holder 2", FilePath: "/lib/ret/stripe/2"})
		require.NoError(t, err)
		blocksWhileHeld(t, "RehomeRetiredVersion (row stripe)", fx.ps.lockBookFile(row.ID), func() error {
			return fx.ps.RehomeRetiredVersion(v.ID, h1.ID)
		})
		blocksWhileHeld(t, "RehomeRetiredVersion (version stripe)", fx.ps.lockBookVersion(v.ID), func() error {
			return fx.ps.RehomeRetiredVersion(v.ID, h2.ID)
		})
	})
	t.Run("UpdateBookVersion and DeleteBookVersion wait for the version stripe", func(t *testing.T) {
		shell, v, row := round()
		retireOne(shell, v, row)
		cur, err := fx.ps.GetBookVersion(v.ID)
		require.NoError(t, err)
		blocksWhileHeld(t, "UpdateBookVersion", fx.ps.lockBookVersion(v.ID), func() error {
			return fx.ps.UpdateBookVersion(cur)
		})
		blocksWhileHeld(t, "DeleteBookVersion", fx.ps.lockBookVersion(v.ID), func() error {
			err := fx.ps.DeleteBookVersion(v.ID)
			if !errors.Is(err, ErrRetiredVersionHasFiles) {
				return fmt.Errorf("want ErrRetiredVersionHasFiles while rows are held, got %v", err)
			}
			return nil
		})
	})
}
