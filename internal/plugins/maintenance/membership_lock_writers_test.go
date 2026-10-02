// file: internal/plugins/maintenance/membership_lock_writers_test.go
// version: 1.1.0
// guid: 4b9e1c73-8d2a-4f60-a5e1-3c7f9b2d0e84
// last-edited: 2026-10-02

package maintenance

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// ApplyVersionGroup waits for a concurrent hand-off on the reused group its
// members join and on the no-group sentinel an ungrouped member leaves.
func TestApplyVersionGroup_WaitsForGroupHolder(t *testing.T) {
	for _, held := range []string{"g", ""} {
		t.Run("holding "+held, func(t *testing.T) {
			f := vptest.New(t)
			withLibraryRoot(t, f.Root)
			f.Book(t, vptest.Spec{ID: "inc", Group: "g", Primary: "true"})
			a := f.Book(t, vptest.Spec{ID: "a", Group: "g", Primary: "false"})
			b := f.Book(t, vptest.Spec{ID: "b", Primary: "true"})
			item := versionGroupItem(t, "/lib/vg", []string{a, b})
			vptest.RequireWaitsForHolder(t,
				func() func() { return versionprimary.LockGroup(held) },
				func() error { return ApplyVersionGroup(f.S)(context.Background(), item) },
				func() bool { return f.GroupOf(t, b) == "" })
		})
	}
}

// movesAfterFirstRead returns the row as stored on the first GetBookByID of
// id (the item's planning read), then moves it into group `to`.
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

// A member that changes group between the item's planning read (which chose
// the target and passed the cross-group refusal) and the group locks fails
// the item with ErrMembershipChanged before any link is written.
func TestApplyVersionGroup_RefusesMemberMovedAfterPlanning(t *testing.T) {
	f := vptest.New(t)
	withLibraryRoot(t, f.Root)
	a := f.Book(t, vptest.Spec{ID: "a", Group: "g", Primary: "true"})
	b := f.Book(t, vptest.Spec{ID: "b", Primary: "true"})
	st := &movesAfterFirstRead{PebbleStore: f.S, id: b, to: "other"}
	err := ApplyVersionGroup(st)(context.Background(), versionGroupItem(t, "/lib/vg", []string{a, b}))
	require.ErrorIs(t, err, versionprimary.ErrMembershipChanged)
	require.Equal(t, "other", f.GroupOf(t, b))
	require.Equal(t, "g", f.GroupOf(t, a))
}
