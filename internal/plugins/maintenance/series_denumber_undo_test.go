// file: internal/plugins/maintenance/series_denumber_undo_test.go
// version: 1.0.0
// guid: 7d2b9e40-3a61-4c85-b1f7-e08c4a5d9376
// last-edited: 2026-11-16

package maintenance

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// The op is run for real against Pebble, then its operation is reverted through
// the real revert service. Unlike the MockStore tests, nothing here is written
// by hand: the rows the revert reads are the rows the op journaled, in the order
// it journaled them.

type denumberUndoWorld struct {
	st     *database.PebbleStore
	author int
	// series ids by name, as created.
	series map[string]int
	books  map[string]string // label -> book id
}

func newDenumberUndoWorld(t *testing.T) *denumberUndoWorld {
	t.Helper()
	st := newRelinkTrashedStore(t)
	a, err := st.CreateAuthor("Alfred Pennyworth")
	require.NoError(t, err)
	return &denumberUndoWorld{st: st, author: a.ID, series: map[string]int{}, books: map[string]string{}}
}

func (w *denumberUndoWorld) addSeries(t *testing.T, name string) int {
	t.Helper()
	s, err := w.st.CreateSeries(name, &w.author)
	require.NoError(t, err)
	w.series[name] = s.ID
	return s.ID
}

// addBook creates a book in a series. seq < 0 leaves it without a position.
// group, when set, makes it a version of the other books of that group; only the
// primary one is listed by the listing getters.
func (w *denumberUndoWorld) addBook(t *testing.T, label string, seriesName string, seq int, group string, primary bool) {
	t.Helper()
	sid := w.series[seriesName]
	b := &database.Book{
		Title:            label,
		FilePath:         "/denumber-undo/" + label,
		AuthorID:         &w.author,
		SeriesID:         &sid,
		IsPrimaryVersion: new(primary),
	}
	if seq >= 0 {
		b.SeriesSequence = new(seq)
	}
	if group != "" {
		b.VersionGroupID = new(group)
	}
	created, err := w.st.CreateBook(b)
	require.NoError(t, err)
	w.books[label] = created.ID
}

func (w *denumberUndoWorld) run(t *testing.T, params, opID string) {
	t.Helper()
	p := &Plugin{deps: fakeDeps{store: w.st}}
	require.NoError(t, p.runSeriesDenumber(context.Background(), json.RawMessage(params), &opIDReporter{id: opID}))
}

func (w *denumberUndoWorld) bookState(t *testing.T, label string) (seriesID int, seq string) {
	t.Helper()
	b, err := w.st.GetBookByID(w.books[label])
	require.NoError(t, err)
	require.NotNil(t, b, label)
	require.NotNil(t, b.SeriesID, label)
	seq = "none"
	if b.SeriesSequence != nil {
		seq = strconv.Itoa(*b.SeriesSequence)
	}
	return *b.SeriesID, seq
}

func (w *denumberUndoWorld) seriesByName(t *testing.T, name string) *database.Series {
	t.Helper()
	s, err := w.st.GetSeriesByName(name, &w.author)
	require.NoError(t, err)
	return s
}

// Two numbered series fold into ONE base the run creates. After the revert both
// numbered series are back under their own ids with their books (and the
// alternate version) at their old, position-less state, and the base is gone.
func TestSeriesDenumber_ApplyThenRevertRestoresEverythingUnderTheSameIDs(t *testing.T) {
	w := newDenumberUndoWorld(t)
	id5 := w.addSeries(t, "Court of Owls 05")
	id7 := w.addSeries(t, "Court of Owls Book 7")
	idEmpty := w.addSeries(t, "Dark Knight 03")
	// The alternate rip shares the primary's version group.
	w.addBook(t, "owls-5-primary", "Court of Owls 05", -1, "vg-owls-5", true)
	w.addBook(t, "owls-5-alternate", "Court of Owls 05", -1, "vg-owls-5", false)
	w.addBook(t, "owls-7", "Court of Owls Book 7", -1, "", true)

	w.run(t, `{"apply": true}`, "op-denumber-e2e")

	base := w.seriesByName(t, "Court of Owls")
	require.NotNil(t, base, "the base series was not created")
	for label, want := range map[string]string{"owls-5-primary": "5", "owls-5-alternate": "5", "owls-7": "7"} {
		sid, seq := w.bookState(t, label)
		require.Equal(t, base.ID, sid, label)
		require.Equal(t, want, seq, label)
	}
	for _, id := range []int{id5, id7} {
		gone, err := w.st.GetSeriesByID(id)
		require.NoError(t, err)
		require.Nil(t, gone, "the emptied numbered series %d should be deleted", id)
	}
	kept, err := w.st.GetSeriesByID(idEmpty)
	require.NoError(t, err)
	require.NotNil(t, kept, "an empty numbered series is not this op's to delete")

	res, err := audiobooks.NewRevertService(w.st).RevertOperation("op-denumber-e2e")
	require.NoError(t, err)
	require.Zero(t, res.Failed, "%+v", res)
	require.Zero(t, res.NotRestorable, "%+v", res)

	for label, want := range map[string]int{"owls-5-primary": id5, "owls-5-alternate": id5, "owls-7": id7} {
		sid, seq := w.bookState(t, label)
		require.Equal(t, want, sid, "%s is back in its own numbered series", label)
		require.Equal(t, "none", seq, "%s has no position again", label)
	}
	for id, name := range map[int]string{id5: "Court of Owls 05", id7: "Court of Owls Book 7"} {
		back, err := w.st.GetSeriesByID(id)
		require.NoError(t, err)
		require.NotNil(t, back, "series %d not restored under its own id", id)
		require.Equal(t, name, back.Name)
		require.NotNil(t, back.AuthorID)
		require.Equal(t, w.author, *back.AuthorID)
	}
	require.Nil(t, w.seriesByName(t, "Court of Owls"), "the base series made for the move should be gone")
}

// A book already in the base keeps its place through apply and revert.
func TestSeriesDenumber_ApplyThenRevertLeavesTheBaseAndItsBooksAlone(t *testing.T) {
	w := newDenumberUndoWorld(t)
	w.addSeries(t, "Court of Owls")
	id5 := w.addSeries(t, "Court of Owls 05")
	w.addBook(t, "talon-1", "Court of Owls", 1, "", true)
	w.addBook(t, "owls-5", "Court of Owls 05", -1, "", true)

	w.run(t, `{"apply": true}`, "op-denumber-existing-base")
	sid, seq := w.bookState(t, "owls-5")
	require.Equal(t, w.series["Court of Owls"], sid)
	require.Equal(t, "5", seq)

	res, err := audiobooks.NewRevertService(w.st).RevertOperation("op-denumber-existing-base")
	require.NoError(t, err)
	require.Zero(t, res.Failed, "%+v", res)

	sid, seq = w.bookState(t, "owls-5")
	require.Equal(t, id5, sid)
	require.Equal(t, "none", seq)
	sid, seq = w.bookState(t, "talon-1")
	require.Equal(t, w.series["Court of Owls"], sid)
	require.Equal(t, "1", seq)
	require.NotNil(t, w.seriesByName(t, "Court of Owls"), "a base that existed before the run is not removed")
}

// A canary apply is reverted as completely as a full one.
func TestSeriesDenumber_ACanaryApplyIsReverted(t *testing.T) {
	w := newDenumberUndoWorld(t)
	id5 := w.addSeries(t, "Court of Owls 05")
	w.addSeries(t, "Dark Knight 03")
	w.addBook(t, "owls-5", "Court of Owls 05", -1, "", true)
	w.addBook(t, "dark-3", "Dark Knight 03", -1, "", true)

	w.run(t, `{"apply": true, "baseName": "Court of Owls"}`, "op-denumber-canary")
	sid, _ := w.bookState(t, "dark-3")
	require.Equal(t, w.series["Dark Knight 03"], sid, "the canary left the other plan alone")

	res, err := audiobooks.NewRevertService(w.st).RevertOperation("op-denumber-canary")
	require.NoError(t, err)
	require.Zero(t, res.Failed, "%+v", res)
	sid, seq := w.bookState(t, "owls-5")
	require.Equal(t, id5, sid)
	require.Equal(t, "none", seq)
}
