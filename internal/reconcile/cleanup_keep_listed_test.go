// file: internal/reconcile/cleanup_keep_listed_test.go
// version: 1.0.0
// guid: 1dc7f095-72fa-4bc6-ae78-4d04a33e1aac
// last-edited: 2026-10-05

package reconcile

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// stateDupFixture: an original outside the library, a kept library copy in
// keepState, and a fileless duplicate that is the group's primary and holds
// a reader's progress (35%).
func stateDupFixture(t *testing.T, keepState string) (f *vptest.Fixture, keep, dup string, u *database.User) {
	t.Helper()
	f = vptest.New(t)
	f.Book(t, vptest.Spec{ID: "a-orig", Group: "g", Primary: "false", Outside: true})
	keep = f.Book(t, vptest.Spec{ID: "b-keep", Group: "g", Primary: "false", State: keepState})
	dup = f.Book(t, vptest.Spec{ID: "c-dup", Group: "g", Primary: "true", NoFile: true})
	_, err := f.S.ModifyBook(dup, func(b *database.Book) error {
		b.FilePath = filepath.Join(f.Root, "Author", "dup.m4b")
		return nil
	})
	require.NoError(t, err)
	u, err = f.S.CreateUser("reader", "reader@example.com", "argon2id", "x", []string{"user"}, "active")
	require.NoError(t, err)
	require.NoError(t, f.S.SetUserBookState(&database.UserBookState{UserID: u.ID, BookID: dup,
		Status: database.UserBookStatusInProgress, ProgressPct: 35}))
	require.NoError(t, f.S.SetUserPosition(u.ID, dup, "seg", 120))
	return f, keep, dup, u
}

// #3769 review SF3: the kept copy (lowest ULID under the root) is not a book
// ABS lists -- here not organized, so the hand-off cannot crown it either.
// The duplicate holding the reader's state is the primary ABS shows; it is
// kept with its state, not drained onto the hidden copy, and counted.
func TestCleanupDuplicateVersionGroups_KeepNotListedKeepsStatefulDup(t *testing.T) {
	f, keep, dup, u := stateDupFixture(t, "imported")

	for _, dry := range []bool{true, false} {
		res, err := CleanupDuplicateVersionGroups(f.S, f.Root, dry)
		require.NoError(t, err)
		require.Equal(t, 1, res.SkippedKeepNotListed, "dry=%v", dry)
		require.Zero(t, res.DuplicatesRemoved, "dry=%v", dry)
		require.Zero(t, res.StateCarried, "dry=%v", dry)
	}
	b, err := f.S.GetBookByID(dup)
	require.NoError(t, err)
	require.NotNil(t, b, "the duplicate ABS lists is kept")
	st, err := f.S.GetUserBookState(u.ID, dup)
	require.NoError(t, err)
	require.Equal(t, 35, st.ProgressPct, "the reader's state stays on the listed copy")
	kst, err := f.S.GetUserBookState(u.ID, keep)
	require.NoError(t, err)
	require.Nil(t, kst, "nothing moved onto the hidden copy")
}

// #3769 review NIT b: one undecodable user row no longer aborts the whole
// cleanup. Each duplicate is kept and counted as a state-check error, and
// the run still finishes (the group's primary hand-off included).
func TestCleanupDuplicateVersionGroups_UndecodableUserFailsPerDuplicate(t *testing.T) {
	f := vptest.New(t)
	f.Book(t, vptest.Spec{ID: "a-orig", Group: "g", Primary: "true", Outside: true})
	f.Book(t, vptest.Spec{ID: "b-keep", Group: "g", Primary: "false"})
	dup := f.Book(t, vptest.Spec{ID: "c-dup", Group: "g", Primary: "false", NoFile: true})
	_, err := f.S.ModifyBook(dup, func(b *database.Book) error {
		b.FilePath = filepath.Join(f.Root, "Author", "dup.m4b")
		return nil
	})
	require.NoError(t, err)
	require.NoError(t, f.S.SetRaw("u:broken", []byte("{not json")))

	res, err := CleanupDuplicateVersionGroups(f.S, f.Root, false)
	require.NoError(t, err, "the run is not aborted")
	require.Equal(t, 1, res.StateCheckErrors)
	require.Zero(t, res.DuplicatesRemoved)
	b, err := f.S.GetBookByID(dup)
	require.NoError(t, err)
	require.NotNil(t, b, "the duplicate is kept")
	f.RequireSinglePrimary(t, "g", "")
}

// lateWriteStore writes a reader's position onto book `on` right after the
// cleanup's first probe read it, as a client listening to the duplicate
// between the probe and the delete would.
type lateWriteStore struct {
	*database.PebbleStore
	on, user string
	once     sync.Once
}

func (s *lateWriteStore) ListUserPositionsForBook(userID, bookID string) ([]database.UserPosition, error) {
	pos, err := s.PebbleStore.ListUserPositionsForBook(userID, bookID)
	if bookID == s.on && userID == s.user {
		s.once.Do(func() { err = s.PebbleStore.SetUserPosition(userID, bookID, "seg", 77) })
	}
	return pos, err
}

// #3769 review NIT a: state that lands on a duplicate after the probe said it
// had none is caught by the re-check made under the merge lock right before
// the delete. The duplicate, its file and the new position are kept.
func TestCleanupDuplicateVersionGroups_StateAfterProbeRefusesDelete(t *testing.T) {
	f := vptest.New(t)
	f.Book(t, vptest.Spec{ID: "a-orig", Group: "g", Primary: "true", Outside: true})
	f.Book(t, vptest.Spec{ID: "b-keep", Group: "g", Primary: "false"})
	dup := f.Book(t, vptest.Spec{ID: "c-dup", Group: "g", Primary: "false", NoFile: true})
	dupFile := filepath.Join(f.Root, "Author", "dup.m4b")
	require.NoError(t, os.MkdirAll(filepath.Dir(dupFile), 0o755))
	require.NoError(t, os.WriteFile(dupFile, []byte("audio"), 0o644))
	_, err := f.S.ModifyBook(dup, func(b *database.Book) error {
		b.FilePath = dupFile
		return nil
	})
	require.NoError(t, err)
	u, err := f.S.CreateUser("reader", "reader@example.com", "argon2id", "x", []string{"user"}, "active")
	require.NoError(t, err)

	s := &lateWriteStore{PebbleStore: f.S, on: dup, user: u.ID}
	res, err := CleanupDuplicateVersionGroups(s, f.Root, false)
	require.NoError(t, err)
	require.Equal(t, 1, res.StateReappeared)
	require.Zero(t, res.DuplicatesRemoved)
	require.Zero(t, res.FilesDeleted)
	require.FileExists(t, dupFile, "the file is not removed before the re-check")
	b, err := f.S.GetBookByID(dup)
	require.NoError(t, err)
	require.NotNil(t, b)
	pos, err := f.S.ListUserPositionsForBook(u.ID, dup)
	require.NoError(t, err)
	require.Len(t, pos, 1)
	require.Equal(t, 77.0, pos[0].PositionSeconds)
}
