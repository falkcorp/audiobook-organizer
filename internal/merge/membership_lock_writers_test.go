// file: internal/merge/membership_lock_writers_test.go
// version: 1.0.0
// guid: 2c7e9a41-6b3d-4f18-8e05-a9d4b1c6f372
// last-edited: 2026-10-02

package merge

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

func holdGroup(gid string) func() func() {
	return func() func() { return versionprimary.LockGroup(gid) }
}

// Each merge-package membership writer waits for a concurrent hand-off
// (versionprimary.LockGroup) on every group it moves a book out of or into;
// "" is the no-group sentinel, held by a reader relying on a book staying
// ungrouped.
func TestMergeMembershipWriters_WaitForGroupHolder(t *testing.T) {
	cases := []struct {
		name string
		held string
		run  func(t *testing.T, f *vptest.Fixture) (write func() error, unchanged func() bool)
	}{
		// RestoreFromTrash: a restore whose apply moves the row from g into
		// h (a batch restore that also sets version_group_id).
		{"trash restore leaves g", "g", restoreIntoH},
		{"trash restore joins h", "h", restoreIntoH},
		// MergeBooksWithOptions: an ungrouped participant joins the grouped
		// one's group.
		{"merge joins g", "g", mergeIntoG},
		{"merge leaves no group", "", mergeIntoG},
		// applyUndo: the absorbed shell is restored into its old group.
		{"combine undo restores into g-a", "g-a", undoIntoGA},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := combineUndoFixture(t)
			write, unchanged := tc.run(t, f)
			vptest.RequireWaitsForHolder(t, holdGroup(tc.held), write, unchanged)
		})
	}
}

func restoreIntoH(t *testing.T, f *vptest.Fixture) (func() error, func() bool) {
	f.Book(t, vptest.Spec{ID: "inc", Group: "g", Primary: "true"})
	r := f.Book(t, vptest.Spec{ID: "r", Group: "g", Primary: "false"})
	f.SoftDelete(t, r)
	write := func() error {
		_, err := RestoreFromTrash(f.S, r, func(b *database.Book) {
			h := "h"
			b.VersionGroupID = &h
		}, func(_, _ *database.Book) {}, "h")
		return err
	}
	return write, func() bool { return f.GroupOf(t, r) == "g" }
}

func mergeIntoG(t *testing.T, f *vptest.Fixture) (func() error, func() bool) {
	x := f.Book(t, vptest.Spec{ID: "x", Group: "g", Primary: "true"})
	y := f.Book(t, vptest.Spec{ID: "y", Primary: "nil"})
	write := func() error {
		_, err := NewService(f.S).MergeBooksWithOptions([]string{x, y}, x, MergeOptions{})
		return err
	}
	return write, func() bool { return f.GroupOf(t, y) == "" }
}

func undoIntoGA(t *testing.T, f *vptest.Fixture) (func() error, func() bool) {
	s := f.Book(t, vptest.Spec{ID: "s"})
	a := f.Book(t, vptest.Spec{ID: "a", Group: "g-a", Primary: "true"})
	f.Book(t, vptest.Spec{ID: "sib", Group: "g-a", Primary: "false"})
	svc := NewService(f.S)
	res, err := svc.CombineBooks([]string{s, a}, s, nil)
	require.NoError(t, err)
	write := func() error {
		_, err := svc.UndoCombine(res.JournalID)
		return err
	}
	unchanged := func() bool {
		b, err := f.S.GetBookByID(a)
		return err == nil && b != nil && b.IsSoftDeleted()
	}
	return write, unchanged
}

// movesAfterFirstRead returns the row as stored on the first GetBookByID of
// id (the caller's planning read), then moves it into group `to` -- as a
// concurrent writer would between the plan and the lock.
type movesAfterFirstRead struct {
	*database.PebbleStore
	id, to string
	moved  bool
}

func (s *movesAfterFirstRead) GetBookByID(id string) (*database.Book, error) {
	b, err := s.PebbleStore.GetBookByID(id)
	if id == s.id && !s.moved {
		s.moved = true
		to := s.to
		if _, merr := s.PebbleStore.ModifyBook(id, func(r *database.Book) error {
			r.VersionGroupID = &to
			return nil
		}); merr != nil {
			return nil, merr
		}
	}
	return b, err
}

// A live participant that changes group between the merge's planning read
// and its group locks is refused with ErrMembershipChanged before anything
// is written: the merge would otherwise move it out of a group it never
// locked and hand off the group it planned from instead.
func TestMergeBooks_RefusesParticipantMovedAfterPlanning(t *testing.T) {
	f := combineUndoFixture(t)
	x := f.Book(t, vptest.Spec{ID: "x", Group: "g", Primary: "true"})
	y := f.Book(t, vptest.Spec{ID: "y", Primary: "nil"})
	st := &movesAfterFirstRead{PebbleStore: f.S, id: y, to: "h"}
	_, err := NewService(st).MergeBooksWithOptions([]string{x, y}, x, MergeOptions{})
	require.ErrorIs(t, err, versionprimary.ErrMembershipChanged)
	require.Equal(t, "h", f.GroupOf(t, y), "the moved participant stays where the other writer put it")
	b, err := f.S.GetBookByID(y)
	require.NoError(t, err)
	require.False(t, b.IsSoftDeleted(), "nothing written for a refused merge")
}
