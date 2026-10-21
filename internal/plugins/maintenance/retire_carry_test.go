// file: internal/plugins/maintenance/retire_carry_test.go
// version: 1.2.0
// guid: 3e9a7c15-84d2-4b60-a1f7-0c5d8e2b6f49
// last-edited: 2026-10-21

package maintenance

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// A retire carries what a person attached to the retired book (ratings, a
// place in a static collection or playlist) onto the survivor, journaled and
// undoable (retire_carry.go). The hash-copy class is the vehicle; the carry
// itself lives in retireIntoWith, which every Repairs retire shares. All
// fixtures are synthetic.

func rcFloat(v float64) *float64 { return &v }
func rcStr(v string) *string     { return &v }

func (f *fragFixture) rcRate(t *testing.T, role string, req database.UpdateBookRatingRequest) {
	t.Helper()
	require.NoError(t, f.s.UpdateBookRating(f.ids[role], req))
}

func (f *fragFixture) rcCollection(t *testing.T, name string, ids ...string) string {
	t.Helper()
	c, err := f.s.CreateCollection(&database.Collection{Name: name, Type: database.CollectionTypeStatic, BookIDs: ids})
	require.NoError(t, err)
	return c.ID
}

func (f *fragFixture) rcPlaylist(t *testing.T, name string, ids ...string) string {
	t.Helper()
	p, err := f.s.CreateUserPlaylist(&database.UserPlaylist{Name: name, Type: database.UserPlaylistTypeStatic, BookIDs: ids})
	require.NoError(t, err)
	return p.ID
}

func (f *fragFixture) rcMembers(t *testing.T, colID string) []string {
	t.Helper()
	c, err := f.s.GetCollection(colID)
	require.NoError(t, err)
	require.NotNil(t, c)
	return c.BookIDs
}

func (f *fragFixture) rcPlaylistMembers(t *testing.T, plID string) []string {
	t.Helper()
	p, err := f.s.GetUserPlaylist(plID)
	require.NoError(t, err)
	require.NotNil(t, p)
	return p.BookIDs
}

func (f *fragFixture) rcRetire(t *testing.T, child string) (parent string) {
	t.Helper()
	parent = f.ids["parent"]
	f.plan(t, "op-plan")
	out := f.apply(t, "op-plan", "op-apply", []string{hcRowID(parent, child)}, nil)
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	return parent
}

func TestRetireCarry_RatingsCollectionsAndPlaylistsFollowAndUndo(t *testing.T) {
	t.Parallel()
	f := hcSeed(t)
	child := f.hcChild(t, "c3", "Stray Recording", "hash-p03")
	parent := f.ids["parent"]
	// The survivor has a story rating of its own; the child has overall and
	// notes as well as a (different) story rating.
	f.rcRate(t, "parent", database.UpdateBookRatingRequest{Story: rcFloat(3)})
	f.rcRate(t, "c3", database.UpdateBookRatingRequest{Overall: rcFloat(4.5), Story: rcFloat(1), Notes: rcStr("loved it")})
	// colA: the child sits between two others; colB already lists the parent;
	// plA is a static playlist; a dynamic collection is never touched.
	colA := f.rcCollection(t, "Favourites", "other-1", child, "other-2")
	colB := f.rcCollection(t, "Both", "other-3", child, parent)
	plA := f.rcPlaylist(t, "Road trip", child, "other-4")
	dyn, err := f.s.CreateCollection(&database.Collection{Name: "Query", Type: database.CollectionTypeDynamic, Query: "title:alpha"})
	require.NoError(t, err)

	f.rcRetire(t, child)

	p, err := f.s.GetBookByID(parent)
	require.NoError(t, err)
	require.Equal(t, 3.0, *p.UserRatingStory, "the survivor's own rating is kept")
	require.Equal(t, 4.5, *p.UserRatingOverall, "an empty field takes the retired book's")
	require.Equal(t, "loved it", *p.UserRatingNotes)
	require.Nil(t, p.UserRatingPerformance)
	require.Equal(t, []string{"other-1", parent, "other-2"}, f.rcMembers(t, colA), "the survivor takes the retired book's slot")
	require.Equal(t, []string{"other-3", parent}, f.rcMembers(t, colB), "already a member: just removed")
	require.Equal(t, []string{parent, "other-4"}, f.rcPlaylistMembers(t, plA))
	d, err := f.s.GetCollection(dyn.ID)
	require.NoError(t, err)
	require.Equal(t, dyn.Version, d.Version)

	changes, err := f.s.GetOperationChanges("op-apply")
	require.NoError(t, err)
	kinds := map[string]int{}
	for _, c := range changes {
		if c.ChangeType != undo.ChangeTypeUserContentFollow {
			continue
		}
		require.Empty(t, undo.NotRestorableLabel(c), "every row is restorable: %+v", c)
		k, _, ok := undo.UserContentFromField(c.FieldName)
		require.True(t, ok)
		if undo.IsMarker(k) {
			require.Equal(t, parent, c.BookID, "a marker is journaled on the survivor")
			require.Equal(t, child, c.NewValue)
		} else {
			require.Equal(t, child, c.BookID)
		}
		kinds[k]++
	}
	require.Equal(t, map[string]int{undo.UserContentRating: 1, undo.UserContentCollection: 2, undo.UserContentPlaylist: 1,
		undo.UserContentCollectionTook: 1, undo.UserContentPlaylistTook: 1, undo.UserContentCollectionHeld: 1}, kinds,
		"colA and plA (the survivor took the slot) journal a took marker on the survivor, colB (already listed) a held one")

	rr, err := audiobooks.NewRevertService(f.s).RevertOperation("op-apply")
	require.NoError(t, err)
	require.Zero(t, rr.Failed, "%+v", rr)
	require.Zero(t, rr.NotRestorable, "%+v", rr)

	p, err = f.s.GetBookByID(parent)
	require.NoError(t, err)
	require.Equal(t, 3.0, *p.UserRatingStory)
	require.Nil(t, p.UserRatingOverall, "the survivor's ratings are exactly as before")
	require.Nil(t, p.UserRatingNotes)
	require.Equal(t, []string{"other-1", child, "other-2"}, f.rcMembers(t, colA))
	require.Equal(t, []string{"other-3", child, parent}, f.rcMembers(t, colB), "put back at its old index")
	require.Equal(t, []string{child, "other-4"}, f.rcPlaylistMembers(t, plA))
	require.True(t, f.live(t, "c3"))
}

func TestRetireCarry_TheSurvivorsRatingIsNeverOverwritten(t *testing.T) {
	t.Parallel()
	f := hcSeed(t)
	child := f.hcChild(t, "c3", "Stray Recording", "hash-p03")
	f.rcRate(t, "parent", database.UpdateBookRatingRequest{Overall: rcFloat(2), Notes: rcStr("meh")})
	f.rcRate(t, "c3", database.UpdateBookRatingRequest{Overall: rcFloat(5), Notes: rcStr("great")})
	parent := f.rcRetire(t, child)

	p, err := f.s.GetBookByID(parent)
	require.NoError(t, err)
	require.Equal(t, 2.0, *p.UserRatingOverall)
	require.Equal(t, "meh", *p.UserRatingNotes)
	changes, err := f.s.GetOperationChanges("op-apply")
	require.NoError(t, err)
	for _, c := range changes {
		require.NotEqual(t, undo.ChangeTypeUserContentFollow, c.ChangeType, "nothing carried, nothing journaled: %+v", c)
	}
}

func TestRetireCarry_UndoNeverOverwritesALaterEdit(t *testing.T) {
	t.Parallel()
	f := hcSeed(t)
	child := f.hcChild(t, "c3", "Stray Recording", "hash-p03")
	f.rcRate(t, "c3", database.UpdateBookRatingRequest{Overall: rcFloat(4)})
	colA := f.rcCollection(t, "Favourites", "other-1", child)
	plA := f.rcPlaylist(t, "Road trip", child)
	parent := f.rcRetire(t, child)

	// After the retire the owner re-rates the survivor, drops it from the
	// collection and clears the playlist.
	f.rcRate(t, "parent", database.UpdateBookRatingRequest{Overall: rcFloat(1)})
	_, err := f.s.RemoveBookFromCollection(colA, parent)
	require.NoError(t, err)
	pl, err := f.s.GetUserPlaylist(plA)
	require.NoError(t, err)
	pl.BookIDs = []string{"other-9"}
	require.NoError(t, f.s.UpdateUserPlaylist(pl))

	rr, err := audiobooks.NewRevertService(f.s).RevertOperation("op-apply")
	require.ErrorContains(t, err, "partially reverted")
	require.NotNil(t, rr)
	require.Equal(t, 3, rr.Failed, "the rating, the collection and the playlist are the owner's now: %+v", rr)

	p, err := f.s.GetBookByID(parent)
	require.NoError(t, err)
	require.Equal(t, 1.0, *p.UserRatingOverall, "the later rating stands")
	require.Equal(t, []string{"other-1"}, f.rcMembers(t, colA))
	require.Equal(t, []string{"other-9"}, f.rcPlaylistMembers(t, plA))
}

func TestRetireCarry_ADeletedListCountsRestored(t *testing.T) {
	t.Parallel()
	f := hcSeed(t)
	child := f.hcChild(t, "c3", "Stray Recording", "hash-p03")
	colA := f.rcCollection(t, "Favourites", child)
	plA := f.rcPlaylist(t, "Road trip", child)
	f.rcRetire(t, child)
	require.NoError(t, f.s.DeleteCollection(colA))
	require.NoError(t, f.s.DeleteUserPlaylist(plA))

	rr, err := audiobooks.NewRevertService(f.s).RevertOperation("op-apply")
	require.NoError(t, err)
	require.Zero(t, rr.Failed, "%+v", rr)
	require.True(t, f.live(t, "c3"))
}
