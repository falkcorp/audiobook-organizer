// file: internal/dedup/series_prune_core_test.go
// version: 1.1.0
// guid: e4a47712-ccaf-464e-9213-29a55a6dbe52
// last-edited: 2026-10-11

package dedup

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// pruneFake is a small in-memory library for the prune cores.
type pruneFake struct {
	series   map[int]database.Series
	books    map[string]*database.Book
	core     map[int]int
	coreErr  map[int]error
	hydrate  map[string]error
	modify   map[string]error
	deleteEr map[int]error
	deleted  []int
}

func newPruneFake() *pruneFake {
	return &pruneFake{series: map[int]database.Series{}, books: map[string]*database.Book{}, core: map[int]int{},
		coreErr: map[int]error{}, hydrate: map[string]error{}, modify: map[string]error{}, deleteEr: map[int]error{}}
}

func (f *pruneFake) GetAllSeries() ([]database.Series, error) {
	out := make([]database.Series, 0, len(f.series))
	for id := 1; id <= 100; id++ {
		if s, ok := f.series[id]; ok {
			out = append(out, s)
		}
	}
	return out, nil
}

func (f *pruneFake) GetBooksBySeriesIDCore(id int) ([]database.BookCore, error) {
	if err := f.coreErr[id]; err != nil {
		return nil, err
	}
	return make([]database.BookCore, f.core[id]), nil
}

func (f *pruneFake) DeleteSeries(id int) error {
	if err := f.deleteEr[id]; err != nil {
		return err
	}
	delete(f.series, id)
	f.deleted = append(f.deleted, id)
	return nil
}

func (f *pruneFake) GetBookByID(id string) (*database.Book, error) {
	if err := f.hydrate[id]; err != nil {
		return nil, err
	}
	b, ok := f.books[id]
	if !ok {
		return nil, nil
	}
	c := *b
	return &c, nil
}

func (f *pruneFake) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	if err := f.modify[id]; err != nil {
		return nil, err
	}
	b, ok := f.books[id]
	if !ok {
		return nil, nil
	}
	c := *b
	if err := fn(&c); err != nil {
		if errors.Is(err, database.ErrSkipBookWrite) {
			return b, nil
		}
		return nil, err
	}
	f.books[id] = &c
	return &c, nil
}

func (f *pruneFake) addSeries(id int, name string, author int, held bool) database.Series {
	s := database.Series{ID: id, Name: name}
	if author != 0 {
		a := author
		s.AuthorID = &a
	}
	if held {
		s.HeldBy = "test-holder"
	}
	f.series[id] = s
	return s
}

func (f *pruneFake) addBook(id string, series int) database.BookCore {
	sid := series
	f.books[id] = &database.Book{ID: id, SeriesID: &sid}
	return database.BookCore{ID: id, SeriesID: &sid}
}

type changeLog struct{ rows []string }

func (c *changeLog) record(ch *database.OperationChange) error {
	c.rows = append(c.rows, fmt.Sprintf("%s|%s|%s|%s|%s", ch.ChangeType, ch.BookID, ch.FieldName, ch.OldValue, ch.NewValue))
	return nil
}

func TestGroupDuplicateSeries_NormalisesAndSkipsHeld(t *testing.T) {
	f := newPruneFake()
	f.addSeries(1, "Series One", 7, false)
	f.addSeries(2, "  series one ", 7, false)
	f.addSeries(3, "Series One", 8, false) // another author: no group
	f.addSeries(4, "Series Two", 0, true)  // held: in no group
	f.addSeries(5, "series two", 0, false)
	f.addSeries(6, "SERIES TWO", 0, false)
	all, _ := f.GetAllSeries()

	groups, held := GroupDuplicateSeries(all)
	assert.Equal(t, 1, held)
	require.Len(t, groups, 2)
	assert.Equal(t, SeriesGroupKey{Name: "series one", AuthorID: 7}, groups[0].Key)
	assert.Equal(t, []int{1, 2}, seriesIDs(groups[0].Members))
	assert.Equal(t, SeriesGroupKey{Name: "series two", AuthorID: 0}, groups[1].Key)
	assert.Equal(t, []int{5, 6}, seriesIDs(groups[1].Members), "the held row's twins still group")
}

func seriesIDs(ss []database.Series) []int {
	out := make([]int, len(ss))
	for i, s := range ss {
		out[i] = s.ID
	}
	return out
}

func TestVoteSeriesCanonical(t *testing.T) {
	f := newPruneFake()
	group := []database.Series{f.addSeries(5, "A", 0, false), f.addSeries(3, "a", 0, false), f.addSeries(9, "a ", 0, false)}
	f.core[5], f.core[3], f.core[9] = 2, 2, 1
	vote, err := VoteSeriesCanonical(f, group)
	require.NoError(t, err)
	assert.Equal(t, 1, vote.CanonicalIdx, "most books, then the lowest id")
	assert.Equal(t, []int{2, 2, 1}, vote.CoreCounts)

	f.coreErr[9] = errors.New("read failed")
	_, err = VoteSeriesCanonical(f, group)
	require.Error(t, err, "a failed count disqualifies the group")
	assert.Contains(t, err.Error(), "REFUSING to merge this group")
}

func TestMergeSeriesGroup_RepointsThenDeletes(t *testing.T) {
	f := newPruneFake()
	keep := f.addSeries(1, "Series One", 0, false)
	away := f.addSeries(2, "series one", 0, false)
	members := database.SeriesBooksMap{2: {f.addBook("b1", 2), f.addBook("b2", 2)}}
	var log changeLog

	res := MergeSeriesGroup(f, keep, []database.Series{away}, map[int]int{2: 2}, members, RecordAfterWrite(log.record))
	assert.Empty(t, res.Errors)
	assert.Equal(t, 2, res.Repointed)
	assert.Equal(t, 1, res.Merged)
	assert.Equal(t, []int{2}, f.deleted)
	for _, id := range []string{"b1", "b2"} {
		assert.Equal(t, 1, *f.books[id].SeriesID)
	}
	assert.Empty(t, members[2], "Move keeps the hoisted membership current")
	assert.Len(t, members[1], 2)
	assert.Equal(t, []string{
		"series_merge|b1|series_id|2 (series one)|1 (Series One)",
		"series_merge|b2|series_id|2 (series one)|1 (Series One)",
		"series_delete||series|2: series one|merged into 1: Series One",
	}, log.rows)
}

// The reference guard: a trashed book the membership never lists still
// names the series, so the row is kept; the visible books still move.
func TestMergeSeriesGroup_RefusesDeleteWhileStillReferenced(t *testing.T) {
	f := newPruneFake()
	keep := f.addSeries(1, "Series One", 0, false)
	away := f.addSeries(2, "series one", 0, false)
	members := database.SeriesBooksMap{2: {f.addBook("b1", 2)}}
	var log changeLog

	res := MergeSeriesGroup(f, keep, []database.Series{away}, map[int]int{2: 2}, members, RecordAfterWrite(log.record))
	assert.Empty(t, f.deleted)
	assert.Equal(t, 1, res.Repointed)
	assert.Zero(t, res.Merged)
	require.Len(t, res.Errors, 1)
	assert.Contains(t, res.Errors[0], "1 book(s) still reference it after reassigning 1 to 1")
}

func TestMergeSeriesGroup_RefusesDeleteAfterAFailedRepoint(t *testing.T) {
	f := newPruneFake()
	keep := f.addSeries(1, "Series One", 0, false)
	away := f.addSeries(2, "series one", 0, false)
	members := database.SeriesBooksMap{2: {f.addBook("b1", 2), f.addBook("b2", 2), {ID: "ghost"}}}
	f.modify["b2"] = errors.New("write failed")
	var log changeLog

	res := MergeSeriesGroup(f, keep, []database.Series{away}, map[int]int{2: 3}, members, RecordAfterWrite(log.record))
	assert.Empty(t, f.deleted)
	assert.Equal(t, 1, res.Repointed)
	require.Len(t, res.WriteErrs, 1)
	joined := strings.Join(res.Errors, "\n")
	assert.Contains(t, joined, "failed to reassign book b2: write failed")
	assert.Contains(t, joined, "book ghost is listed under series 2 but does not resolve")
	assert.Contains(t, joined, "2 of 3 books could not be repointed to 1; REFUSING")
}

// A record failure after the write is collected; the write it describes stands and the
// step carries on.
func TestMergeSeriesGroup_RecorderFailureIsCollected(t *testing.T) {
	f := newPruneFake()
	keep := f.addSeries(1, "Series One", 0, false)
	away := f.addSeries(2, "series one", 0, false)
	members := database.SeriesBooksMap{2: {f.addBook("b1", 2)}}
	failing := func(*database.OperationChange) error { return errors.New("journal down") }

	res := MergeSeriesGroup(f, keep, []database.Series{away}, map[int]int{2: 1}, members, RecordAfterWrite(failing))
	assert.Equal(t, 1, res.Merged)
	assert.Len(t, res.Unrecorded, 2)
	assert.Empty(t, res.Errors)
}

func TestDeleteOrphanSeries_Guards(t *testing.T) {
	f := newPruneFake()
	var log changeLog

	ref := f.addSeries(1, "Referenced", 0, false)
	res := DeleteOrphanSeries(f, ref, 1, RecordAfterWrite(log.record))
	assert.True(t, res.Referenced)
	assert.False(t, res.Deleted)

	held := f.addSeries(2, "Held", 0, true)
	res = DeleteOrphanSeries(f, held, 0, RecordAfterWrite(log.record))
	assert.True(t, res.Held)
	assert.False(t, res.Deleted)

	failing := f.addSeries(3, "Failing", 0, false)
	f.deleteEr[3] = errors.New("store down")
	res = DeleteOrphanSeries(f, failing, 0, RecordAfterWrite(log.record))
	assert.False(t, res.Deleted)
	require.Error(t, res.Err)
	assert.Equal(t, "failed to delete orphan series 3: store down", res.Err.Error())

	empty := f.addSeries(4, "Empty", 0, false)
	res = DeleteOrphanSeries(f, empty, 0, RecordAfterWrite(log.record))
	assert.True(t, res.Deleted)
	assert.Equal(t, []int{4}, f.deleted, "only the unreferenced, unheld series is deleted")
	assert.Equal(t, []string{"series_delete||orphan_series|4: Empty|deleted (0 book references, any state)"}, log.rows)
}

// Every series is in at most one candidate; a group member listing no books
// is never also an orphan candidate, and a held empty series is one.
func TestPlanSeriesPrune_EverySeriesInOneCandidate(t *testing.T) {
	f := newPruneFake()
	f.addSeries(1, "Series One", 0, false)
	f.addSeries(2, "series one", 0, false)
	f.addSeries(3, "Series Two", 0, false)
	f.addSeries(4, "Series Three", 0, true)
	f.addSeries(5, "Series Four", 0, false)
	all, _ := f.GetAllSeries()
	listed := map[int]int{3: 1}

	cands := PlanSeriesPrune(all, listed)
	seen := map[int]int{}
	var kinds []string
	for _, c := range cands {
		kinds = append(kinds, c.Kind)
		for _, s := range c.Members {
			seen[s.ID]++
		}
	}
	assert.Equal(t, []string{SeriesPruneDuplicate, SeriesPruneOrphan, SeriesPruneOrphan}, kinds)
	assert.Equal(t, map[int]int{1: 1, 2: 1, 4: 1, 5: 1}, seen, "series 3 lists a book: no candidate")
}

// The ledger decides the order of each row and its write. A journal-first
// ledger sees the row before the store changes; a write it refuses (it could
// not journal) is never made, counts as a failed repoint and keeps the series
// row; and the delete goes through it too.
func TestMergeSeriesGroup_LedgerOrdersEachRowBeforeItsWrite(t *testing.T) {
	f := newPruneFake()
	keep := f.addSeries(1, "Series One", 0, false)
	away := f.addSeries(2, "series one", 0, false)
	members := database.SeriesBooksMap{2: {f.addBook("b1", 2), f.addBook("b2", 2)}}
	var events []string
	journalFirst := func(c *database.OperationChange, write func() error) (error, error) {
		if c.BookID == "b2" {
			events = append(events, "refuse b2")
			return errors.New("journal down; not written"), nil
		}
		state := "b1 on 2"
		if *f.books["b1"].SeriesID == 1 {
			state = "b1 on 1"
		}
		events = append(events, c.ChangeType+" "+c.BookID+" journaled while "+state)
		return write(), nil
	}

	res := MergeSeriesGroup(f, keep, []database.Series{away}, map[int]int{2: 2}, members, journalFirst)
	assert.Equal(t, []string{"series_merge b1 journaled while b1 on 2", "refuse b2"}, events,
		"each row is handed over before its write; the delete is never reached")
	assert.Equal(t, 1, res.Repointed)
	assert.Equal(t, 2, *f.books["b2"].SeriesID, "a write the ledger refused is not made")
	assert.Empty(t, f.deleted, "a refused repoint keeps the series row")
	require.Len(t, res.WriteErrs, 1)
	assert.Empty(t, res.Unrecorded, "a refused write is a write error, not an unrecorded write")

	orphan := f.addSeries(3, "Empty", 0, false)
	o := DeleteOrphanSeries(f, orphan, 0, func(c *database.OperationChange, write func() error) (error, error) {
		assert.Empty(t, f.deleted, "the orphan row is handed over before the delete")
		return write(), nil
	})
	assert.True(t, o.Deleted)
	assert.Equal(t, []int{3}, f.deleted)
}

// RecordAfterWrite records nothing for a write that failed.
func TestRecordAfterWrite_FailedWriteRecordsNothing(t *testing.T) {
	var log changeLog
	ledger := RecordAfterWrite(log.record)
	werr, rerr := ledger(&database.OperationChange{ChangeType: "x"}, func() error { return errors.New("store down") })
	require.Error(t, werr)
	require.NoError(t, rerr)
	assert.Empty(t, log.rows)
	werr, rerr = ledger(&database.OperationChange{ChangeType: "x"}, func() error { return nil })
	require.NoError(t, werr)
	require.NoError(t, rerr)
	assert.Len(t, log.rows, 1)
}
