// file: internal/plugins/maintenance/series_denumber_hoist_test.go
// version: 1.0.0
// guid: 8b2e4f61-3c9a-4d07-a5b8-e16f0c9d2a47
// last-edited: 2026-09-13

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// denumberChainFixture is a stateful library for SERIES-MEMBERSHIP-RESIDUAL-LOOPS.
// Book rows really move when UpdateBook is called, and the bulk membership read
// answers from the CURRENT rows, so a stale hoisted map is observable.
//
// The per-series AllVersions getter errors and counts: the op must never call it.
type denumberChainFixture struct {
	mu        sync.Mutex
	store     *database.MockStore
	series    []database.Series
	bookSID   map[string]int   // book ID -> series ID
	history   map[string][]int // book ID -> every series it was written to, in order
	deleted   map[int]bool
	perSeries int
	bulk      int
}

func newDenumberChainFixture(t *testing.T, series []database.Series, books map[string]int, refs map[int]int) *denumberChainFixture {
	t.Helper()
	f := &denumberChainFixture{
		store:   &database.MockStore{},
		series:  series,
		bookSID: books,
		history: map[string][]int{},
		deleted: map[int]bool{},
	}
	s := f.store
	s.GetAllSeriesFunc = func() ([]database.Series, error) { return f.series, nil }
	s.GetAllSeriesBookCountsFunc = func() (map[int]int, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		out := map[int]int{}
		for _, sid := range f.bookSID {
			out[sid]++
		}
		return out, nil
	}
	s.GetAllSeriesBookRefCountsFunc = func() (map[int]int, error) { return refs, nil }
	s.GetBooksBySeriesIDAllVersionsFunc = func(int) ([]database.BookCore, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.perSeries++
		return nil, errors.New("per-series membership read inside the series-denumber loops")
	}
	s.GetBooksBySeriesIDsAllVersionsFunc = func(ids []int) (map[int][]database.BookCore, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.bulk++
		out := map[int][]database.BookCore{}
		for _, id := range ids {
			out[id] = []database.BookCore{}
		}
		bookIDs := make([]string, 0, len(f.bookSID))
		for id := range f.bookSID {
			bookIDs = append(bookIDs, id)
		}
		sort.Strings(bookIDs)
		for _, id := range bookIDs {
			sid := f.bookSID[id]
			if _, want := out[sid]; want {
				out[sid] = append(out[sid], database.BookCore{ID: id})
			}
		}
		return out, nil
	}
	s.GetBookByIDFunc = func(id string) (*database.Book, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		sid, ok := f.bookSID[id]
		if !ok {
			return nil, nil
		}
		return &database.Book{ID: id, SeriesID: &sid}, nil
	}
	s.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.bookSID[id] = *b.SeriesID
		f.history[id] = append(f.history[id], *b.SeriesID)
		return b, nil
	}
	// Case-insensitive, like PebbleStore.GetSeriesByName's name index. This is
	// what lets a target resolve to a numbered series whose own plan sorts later.
	s.GetSeriesByNameFunc = func(name string, _ *int) (*database.Series, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		for i := range f.series {
			if !f.deleted[f.series[i].ID] && strings.EqualFold(strings.TrimSpace(f.series[i].Name), strings.TrimSpace(name)) {
				cp := f.series[i]
				return &cp, nil
			}
		}
		return nil, nil
	}
	s.CreateSeriesFunc = func(name string, _ *int) (*database.Series, error) {
		t.Fatalf("CreateSeries(%q) called: the fixture expects every target to resolve to an existing row", name)
		return nil, nil
	}
	s.DeleteSeriesFunc = func(id int) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.deleted[id] = true
		return nil
	}
	return f
}

func (f *denumberChainFixture) run(t *testing.T, apply bool) error {
	t.Helper()
	raw := json.RawMessage(`{"apply": false}`)
	if apply {
		raw = json.RawMessage(`{"apply": true}`)
	}
	p := &Plugin{deps: fakeDeps{store: f.store}}
	return p.runSeriesDenumber(context.Background(), raw, &fakeReporter{})
}

// chainSeries: "Alpha 05 07" plans onto base "Alpha 05", and "alpha 05" plans
// onto base "alpha". Candidates sort by IntoName byte-wise, so "Alpha 05" runs
// FIRST, and GetSeriesByName("Alpha 05") case-insensitively resolves to series
// 2 ("alpha 05"). Its book moves INTO series 2 before series 2's own plan
// reads series 2.
func chainSeries() []database.Series {
	return []database.Series{
		{ID: 1, Name: "alpha"},
		{ID: 2, Name: "alpha 05"},
		{ID: 3, Name: "Alpha 05 07"},
	}
}

// TestRunSeriesDenumber_HoistedMembershipFollowsBooksIntoALaterSource pins the
// apply-loop Move. Removing members.Move in runSeriesDenumber makes it fail:
// series 2's plan then reads a map that never learned y1 arrived, moves only
// x1, and deletes series 2 with y1 still pointing at it.
//
// Parity: the expected end state is what the old live per-series read
// produced. It re-read series 2 when its plan ran, saw both x1 and y1, and
// moved both to series 1.
func TestRunSeriesDenumber_HoistedMembershipFollowsBooksIntoALaterSource(t *testing.T) {
	f := newDenumberChainFixture(t, chainSeries(),
		map[string]int{"x1": 2, "y1": 3},
		map[int]int{2: 1, 3: 1})

	if err := f.run(t, true); err != nil {
		t.Fatalf("runSeriesDenumber: %v", err)
	}

	// Guard the fixture: without the chain this test proves nothing.
	if h := f.history["y1"]; len(h) == 0 || h[0] != 2 {
		t.Fatalf("fixture did not chain: y1 was written to %v, want series 2 first", h)
	}

	for id, sid := range f.bookSID {
		if f.deleted[sid] {
			t.Errorf("book %s still points at series %d, which the run deleted: the hoisted "+
				"membership map went stale after an earlier plan moved it in", id, sid)
		}
		if sid != 1 {
			t.Errorf("book %s ended in series %d, want 1 (what a live per-series read produced)", id, sid)
		}
	}
	if !f.deleted[2] || !f.deleted[3] {
		t.Errorf("deleted = %v, want series 2 and 3 deleted (both emptied)", f.deleted)
	}
	if f.perSeries != 0 {
		t.Errorf("per-series AllVersions getter called %d times; membership must be hoisted", f.perSeries)
	}
	if f.bulk != 1 {
		t.Errorf("bulk membership read %d times, want exactly 1 per run", f.bulk)
	}
}

// TestRunSeriesDenumber_DryRunReadsMembershipOnce: the dry-run preview also
// reads the hoisted map, once, and still reports the guard's held-back plans.
func TestRunSeriesDenumber_DryRunReadsMembershipOnce(t *testing.T) {
	// Series 3 is referenced by one trashed row the membership read cannot see,
	// so the preview must count it as held back by the guard.
	f := newDenumberChainFixture(t, chainSeries(),
		map[string]int{"x1": 2},
		map[int]int{2: 1, 3: 1})

	rep := &fakeReporter{}
	p := &Plugin{deps: fakeDeps{store: f.store}}
	if err := p.runSeriesDenumber(context.Background(), json.RawMessage(`{"apply": false}`), rep); err != nil {
		t.Fatalf("runSeriesDenumber: %v", err)
	}
	rep.mu.Lock()
	logged := strings.Join(rep.logs, "\n")
	rep.mu.Unlock()

	if f.perSeries != 0 || f.bulk != 1 {
		t.Errorf("dry run: per-series=%d bulk=%d, want 0 and 1", f.perSeries, f.bulk)
	}
	if !strings.Contains(logged, "1 would be merged but NOT deleted") {
		t.Errorf("dry-run summary %q does not report the one guard-held plan", logged)
	}
	if len(f.history) != 0 || len(f.deleted) != 0 {
		t.Errorf("dry run wrote: history=%v deleted=%v", f.history, f.deleted)
	}
}

// TestRunSeriesDenumber_FailsClosedWithoutMembership: a failed bulk read aborts
// the run, dry or apply, before any write. The old per-plan read counted the
// failure and carried on.
func TestRunSeriesDenumber_FailsClosedWithoutMembership(t *testing.T) {
	for _, apply := range []bool{false, true} {
		f := newDenumberChainFixture(t, chainSeries(),
			map[string]int{"x1": 2, "y1": 3},
			map[int]int{2: 1, 3: 1})
		f.store.GetBooksBySeriesIDsAllVersionsFunc = func([]int) (map[int][]database.BookCore, error) {
			return nil, errors.New("scan failed")
		}
		err := f.run(t, apply)
		if err == nil {
			t.Fatalf("apply=%v: run succeeded without series membership; it must fail closed", apply)
		}
		if !strings.Contains(err.Error(), "series membership") {
			t.Errorf("apply=%v: error %q does not name the membership read", apply, err)
		}
		if len(f.history) != 0 || len(f.deleted) != 0 {
			t.Errorf("apply=%v: wrote before failing: history=%v deleted=%v", apply, f.history, f.deleted)
		}
	}
}
