// file: internal/audiobooks/revert_version_join_test.go
// version: 1.0.1
// guid: 7d3f9a15-4b8c-4e62-a0d7-c52e18b6f904
// last-edited: 2026-11-09

package audiobooks

import (
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

func joinRow(t *testing.T, bookID, gid string) *database.OperationChange {
	t.Helper()
	yes := true
	now, err := undo.VersionGroupState{Group: &gid, Primary: &yes}.Encode()
	require.NoError(t, err)
	old, err := undo.VersionGroupState{}.Encode()
	require.NoError(t, err)
	return &database.OperationChange{ID: "vj-" + bookID, OperationID: "op-join", BookID: bookID,
		ChangeType: undo.ChangeTypeRepairVersionGroupJoin, FieldName: "version_group_id", OldValue: old, NewValue: now}
}

// The revert puts the group and flag back exactly: a group-less book with no
// primary flag ends with neither, not with "" and false.
func TestRevertRepairVersionGroupJoin_RestoresTheExactState(t *testing.T) {
	s := newRevertPebble(t)
	rs := NewRevertService(s)
	gid, yes := "vg-gotham", true
	bk, err := s.CreateBook(&database.Book{Title: "Year One", FilePath: "/x/y1", VersionGroupID: &gid, IsPrimaryVersion: &yes})
	require.NoError(t, err)
	c := joinRow(t, bk.ID, gid)
	require.NoError(t, rs.revertRepairVersionGroupJoin(c))
	got, err := s.GetBookByID(bk.ID)
	require.NoError(t, err)
	require.Nil(t, got.VersionGroupID)
	require.Nil(t, got.IsPrimaryVersion)
	// Reverting again finds the old state already there.
	require.ErrorIs(t, rs.revertRepairVersionGroupJoin(c), undo.ErrAlreadyRestored)
}

func TestRevertRepairVersionGroupJoin_RefusesAChangedBook(t *testing.T) {
	gid, yes, no := "vg-gotham", true, false
	for _, tc := range []struct {
		name string
		prep func(t *testing.T, s *database.PebbleStore, id string)
	}{
		{"another live member joined", func(t *testing.T, s *database.PebbleStore, _ string) {
			_, err := s.CreateBook(&database.Book{Title: "Hush", FilePath: "/x/hush", VersionGroupID: &gid, IsPrimaryVersion: &no})
			require.NoError(t, err)
		}},
		{"book was demoted", func(t *testing.T, s *database.PebbleStore, id string) {
			_, err := s.ModifyBook(id, func(b *database.Book) error { b.IsPrimaryVersion = &no; return nil })
			require.NoError(t, err)
		}},
		{"book moved to another group", func(t *testing.T, s *database.PebbleStore, id string) {
			other := "vg-elsewhere"
			_, err := s.ModifyBook(id, func(b *database.Book) error { b.VersionGroupID = &other; return nil })
			require.NoError(t, err)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newRevertPebble(t)
			rs := NewRevertService(s)
			bk, err := s.CreateBook(&database.Book{Title: "Year One", FilePath: "/x/y1", VersionGroupID: &gid, IsPrimaryVersion: &yes})
			require.NoError(t, err)
			tc.prep(t, s, bk.ID)
			before, err := s.GetBookByID(bk.ID)
			require.NoError(t, err)
			err = rs.revertRepairVersionGroupJoin(joinRow(t, bk.ID, gid))
			require.Equal(t, undo.ReasonChangedSince, undo.RefusalReason(err), "%v", err)
			after, err := s.GetBookByID(bk.ID)
			require.NoError(t, err)
			require.Equal(t, before.VersionGroupID, after.VersionGroupID, "a refused row writes nothing")
			require.Equal(t, before.IsPrimaryVersion, after.IsPrimaryVersion)
		})
	}
}

func TestRevertRepairVersionGroupJoin_AHiddenMemberDoesNotBlock(t *testing.T) {
	s := newRevertPebble(t)
	rs := NewRevertService(s)
	gid, yes, no := "vg-gotham", true, false
	bk, err := s.CreateBook(&database.Book{Title: "Year One", FilePath: "/x/y1", VersionGroupID: &gid, IsPrimaryVersion: &yes})
	require.NoError(t, err)
	hidden, err := s.CreateBook(&database.Book{Title: "Hush", FilePath: "/x/hush", VersionGroupID: &gid, IsPrimaryVersion: &no, MarkedForDeletion: &yes})
	require.NoError(t, err)
	require.True(t, hidden.IsSoftDeleted())
	require.NoError(t, rs.revertRepairVersionGroupJoin(joinRow(t, bk.ID, gid)))
}

func TestRevertRepairVersionGroupJoin_RefusesAMalformedRow(t *testing.T) {
	s := newRevertPebble(t)
	rs := NewRevertService(s)
	bk, err := s.CreateBook(&database.Book{Title: "Year One", FilePath: "/x/y1"})
	require.NoError(t, err)
	c := joinRow(t, bk.ID, "vg-gotham")
	c.NewValue = "not json"
	require.Equal(t, undo.ReasonOldValueUnparsable, undo.RefusalReason(rs.revertRepairVersionGroupJoin(c)))
}
