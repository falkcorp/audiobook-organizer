// file: internal/plugins/maintenance/itunes_clone_rollback_failures_test.go
// version: 1.1.0
// guid: b07ff01f-6cf6-482c-a79d-2b4cbad437be
// last-edited: 2026-10-05

package maintenance

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// icStepFailStore fails one step of a version clone's rollback.
type icStepFailStore struct {
	*database.PebbleStore
	step              string
	sourceID, cloneID string
}

func (s *icStepFailStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	if s.step == "restore_source" && id == s.sourceID {
		return nil, fmt.Errorf("injected ModifyBook failure for %s", id)
	}
	return s.PebbleStore.ModifyBook(id, fn)
}

func (s *icStepFailStore) ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error) {
	if s.step == "release_pid" && bookID == s.cloneID {
		return nil, fmt.Errorf("injected ModifyBookFile failure for %s", fileID)
	}
	return s.PebbleStore.ModifyBookFile(bookID, fileID, fn)
}

func (s *icStepFailStore) DeleteBookFilesByIDs(ids []string) error {
	if s.step == "delete_rows" {
		return fmt.Errorf("injected DeleteBookFilesByIDs failure")
	}
	return s.PebbleStore.DeleteBookFilesByIDs(ids)
}

func (s *icStepFailStore) SetBookAuthors(bookID string, authors []database.BookAuthor) error {
	if s.step == "clear_authors" && bookID == s.cloneID {
		return fmt.Errorf("injected SetBookAuthors failure for %s", bookID)
	}
	return s.PebbleStore.SetBookAuthors(bookID, authors)
}

func (s *icStepFailStore) DeleteBook(id string) error {
	if s.step == "delete_book" && id == s.cloneID {
		return fmt.Errorf("injected DeleteBook failure for %s", id)
	}
	return s.PebbleStore.DeleteBook(id)
}

// #3769 review SF4: a version rollback that fails at any step leaves the
// clone's users' state on a book ABS lists -- the source when it is restored
// as a listed book, else the clone it is moved back to -- never on a hidden
// book while a listed one is live. A healthy retry then finishes the
// rollback with the state on the source. hiddenSource makes the source's
// pre-clone state one ABS does not list, so a failure after the carry has to
// move the state back to the clone. minted gives both books an ABS sync id
// before the rollback, as a client that listed them would: every sync id
// must still resolve after the failure and after the retry (#3770 review B1:
// a carry back recorded source -> clone, the retry clone -> source, and the
// cycle broke both ids for good).
func TestITunesClone_RollbackFailureKeepsStateOnListedBook(t *testing.T) {
	steps := []string{"restore_source", "release_pid", "delete_rows", "clear_authors", "delete_book"}
	for _, minted := range []bool{false, true} {
		for _, hiddenSource := range []bool{false, true} {
			for _, step := range steps {
				t.Run(fmt.Sprintf("%s/hidden_source=%v/minted=%v", step, hiddenSource, minted), func(t *testing.T) {
					rollbackFailureCase(t, step, hiddenSource, minted)
				})
			}
		}
	}
}

// icRequireSyncResolves asserts each book's ABS sync id (when it has one)
// resolves, to wantBook when set.
func icRequireSyncResolves(t *testing.T, s *database.PebbleStore, books []string, wantBook string) {
	t.Helper()
	ids := database.AsSyncIdentityStore(s)
	for _, b := range books {
		sid, has, err := ids.GetSyncIDForBook(b)
		require.NoError(t, err)
		if !has {
			continue
		}
		item, err := ids.ResolveSyncItem(sid)
		require.NoError(t, err, "sync id of %s must resolve", b)
		require.NotNil(t, item)
		if wantBook != "" {
			require.Equal(t, wantBook, item.CurrentBookID, "sync id of %s", b)
		}
	}
}

func rollbackFailureCase(t *testing.T, step string, hiddenSource, minted bool) {
	f := newICFixture(t)
	cloneID, user := applyCloneWithProgress(t, f)
	if minted {
		for _, id := range []string{"T", cloneID} {
			_, err := database.AsSyncIdentityStore(f.s).MintOrGetSyncID(id)
			require.NoError(t, err)
		}
	}
	runner := &icRunner{store: icStore{OpsStore: f.s}}
	if hiddenSource {
		rec, err := runner.loadRecord("vg-t")
		require.NoError(t, err)
		rec.SourcePriorState = "imported"
		require.NoError(t, runner.saveRecord(rec))
	}

	fs := &icStepFailStore{PebbleStore: f.s, step: step, sourceID: "T", cloneID: cloneID}
	p := &Plugin{deps: icTestDeps{fakeDeps: fakeDeps{store: fs}, s: f.s, root: f.root}}
	rep, err := p.itunesCloneIntoLibrary(t.Context(), icParams{Rollback: true, GroupIDs: []string{"vg-t"}}, f.root, &opIDReporter{id: "op-ic-test"})
	require.NoError(t, err)
	g := icGroup(t, rep, "vg-t")
	require.Equal(t, icOutcomeFailed, g.Outcome)

	holder := ""
	for _, id := range []string{"T", cloneID} {
		st, err := f.s.GetUserBookState(user.ID, id)
		require.NoError(t, err)
		if st != nil && st.ProgressPct == 55 {
			require.Empty(t, holder, "the state is on one book")
			holder = id
		}
	}
	require.NotEmpty(t, holder, "the user's state is kept (%s)", g.Error)
	b, err := f.s.GetBookByID(holder)
	require.NoError(t, err)
	require.True(t, database.ABSLibraryFilter().Matches(b),
		"the state is on %s, which ABS must list (%s)", holder, g.Error)
	pos, err := f.s.ListUserPositionsForBook(user.ID, holder)
	require.NoError(t, err)
	require.NotEmpty(t, pos, "the position is with the state")
	rec, err := runner.loadRecord("vg-t")
	require.NoError(t, err)
	require.NotNil(t, rec, "the record is kept for a retry")
	icRequireSyncResolves(t, f.s, []string{"T", cloneID}, "")
	if holder == cloneID {
		// Moved back to the live clone: both stay live, so each
		// id resolves to its own book.
		icRequireSyncResolves(t, f.s, []string{cloneID}, cloneID)
		icRequireSyncResolves(t, f.s, []string{"T"}, "T")
	}

	g = icGroup(t, f.run(t, icParams{Rollback: true, GroupIDs: []string{"vg-t"}}), "vg-t")
	require.Equal(t, icOutcomeRolled, g.Outcome, g.Error)
	gone, err := f.s.GetBookByID(cloneID)
	require.NoError(t, err)
	require.True(t, gone == nil || gone.IsSoftDeleted(), "the retry removes the clone")
	st, err := f.s.GetUserBookState(user.ID, "T")
	require.NoError(t, err)
	require.NotNil(t, st)
	require.Equal(t, 55, st.ProgressPct, "the retry leaves the state on the source")
	icRequireSyncResolves(t, f.s, []string{"T", cloneID}, "T")
}
