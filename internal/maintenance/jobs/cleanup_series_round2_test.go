// file: internal/maintenance/jobs/cleanup_series_round2_test.go
// version: 1.1.0
// guid: 5c8e2b19-7d34-4a60-9f13-0b6a4e7d2c85
// last-edited: 2026-11-18

package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// Round 2 of the cleanup-series conditional-delete review (#4709):
//
//   - M-1: a series phase 1 keeps must not be counted again by phase 2.
//   - m-1: a store that has the delete but cannot count must not get its books
//     unlinked first.
//   - m-2: every outcome in both phases, and every key of the Done line.
//   - n-1, n-2: every candidate lands in exactly one bucket.

// csSyncBuffer is a log sink the Done-line capture can read while other tests'
// goroutines might still be writing the default logger.
type csSyncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *csSyncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *csSyncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// csCaptureDone points the default slog logger at a buffer for the test and
// returns a func that decodes the job's "Done ..." line from it.
func csCaptureDone(t *testing.T) func() map[string]any {
	t.Helper()
	sink := &csSyncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(sink, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return func() map[string]any {
		t.Helper()
		for _, line := range strings.Split(sink.String(), "\n") {
			if !strings.Contains(line, `"msg":"Done single_found`) {
				continue
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(line), &m); err != nil {
				t.Fatalf("decode Done line %q: %v", line, err)
			}
			return m
		}
		t.Fatalf("the run logged no Done line; got %q", sink.String())
		return nil
	}
}

func csDoneInt(t *testing.T, done map[string]any, key string) int {
	t.Helper()
	v, ok := done[key].(float64)
	if !ok {
		t.Fatalf("Done line has no numeric %q: %v", key, done)
	}
	return int(v)
}

func csDoneBool(t *testing.T, done map[string]any, key string) bool {
	t.Helper()
	v, ok := done[key].(bool)
	if !ok {
		t.Fatalf("Done line has no boolean %q: %v", key, done)
	}
	return v
}

// csHushStore: "Batman: Hush" (id 7) holds ONE book, so phase 1 collapses it;
// "batman hush" (id 8) holds four. They normalize to the same name, so before
// the fix a series phase 1 kept was grouped with 8 in phase 2.
func csHushStore() *csCondStore {
	return newCsCondStore(
		[]database.Series{{ID: 7, Name: "Batman: Hush"}, {ID: 8, Name: "batman hush"}},
		database.Book{ID: "hush-1", SeriesID: intp(7)},
		database.Book{ID: "hush-2", SeriesID: intp(8)},
		database.Book{ID: "hush-3", SeriesID: intp(8)},
		database.Book{ID: "hush-4", SeriesID: intp(8)},
		database.Book{ID: "hush-5", SeriesID: intp(8)},
	)
}

// csOwlsTripleStore: three same-name series, 7 and 9 each one book at sequence 2
// (phase 1 leaves them alone), 8 the keeper with two books. Phase 2 merges 7 and
// 9 into 8.
func csOwlsTripleStore() *csCondStore {
	return newCsCondStore(
		[]database.Series{{ID: 7, Name: "Court of Owls"}, {ID: 8, Name: "court of owls"}, {ID: 9, Name: "COURT OF OWLS"}},
		database.Book{ID: "court-of-owls-1", SeriesID: intp(7), SeriesSequence: intp(2)},
		database.Book{ID: "night-of-owls-1", SeriesID: intp(8)},
		database.Book{ID: "night-of-owls-2", SeriesID: intp(8)},
		database.Book{ID: "court-of-owls-3", SeriesID: intp(9), SeriesSequence: intp(2)},
	)
}

// The Done line must add up: every phase-1 candidate is in one bucket, and every
// merged-from series of a phase-2 group is in one bucket.
func csAssertBucketsAddUp(t *testing.T, done map[string]any) {
	t.Helper()
	g := func(k string) int { return csDoneInt(t, done, k) }
	p1 := g("singleApplied") + g("singleRefusedLive") + g("singleKeptChanged") + g("singleAlreadyGone") +
		g("singleDeleteFailed") + g("singleUnlinkFailed")
	if p1 != g("singleFound") {
		t.Errorf("phase 1 buckets sum to %d, singleFound = %d: %v", p1, g("singleFound"), done)
	}
	p2 := g("dupSeriesDeleted") + g("dupRefused") + g("dupRefusedLive") + g("dupKeptChanged") +
		g("dupAlreadyGone") + g("dupDeleteFailed") + g("dupNotReached")
	if p2 != g("dupMergeFromSeries") {
		t.Errorf("phase 2 buckets sum to %d, dupMergeFromSeries = %d: %v", p2, g("dupMergeFromSeries"), done)
	}
}

// --- M-1, m-2, n-1, n-2: every outcome, in both phases, adds up ---------------

func TestCleanupSeriesRun_DoneLineBuckets(t *testing.T) {
	type want struct {
		ints       map[string]int
		stopped1   bool
		stopped2   bool
		inWarn     []string
		notInWarn  []string
		keepSeries []int // series that must still exist
	}
	const guardLine = "Reference guard kept"
	cases := []struct {
		name  string
		build func() *csCondStore
		want  want
	}{
		{
			name:  "phase 1 deletes (control)",
			build: csHushStore,
			want: want{
				ints:      map[string]int{"singleFound": 1, "singleApplied": 1, "dupFound": 0, "dupRefused": 0},
				notInWarn: []string{guardLine},
			},
		},
		{
			// M-1. The kept series is also in a duplicate group. It must be counted
			// once, as live references, and phase 2 must not refuse it again under
			// the reference guard's cause.
			name: "phase 1 keeps a series a book was linked into, inside a duplicate group",
			build: func() *csCondStore {
				s := csHushStore()
				s.afterMembership = func(s *csCondStore) { s.linkBook("hush-late", 7) }
				return s
			},
			want: want{
				ints:       map[string]int{"singleFound": 1, "singleRefusedLive": 1, "dupFound": 0, "dupRefused": 0, "dupMergeFromSeries": 0},
				inWarn:     []string{"found live references in 1 single-book series and 0 merged-from series"},
				notInWarn:  []string{guardLine},
				keepSeries: []int{7},
			},
		},
		{
			name: "phase 1 keeps a series renamed since the read, inside a duplicate group",
			build: func() *csCondStore {
				s := csHushStore()
				s.afterMembership = func(s *csCondStore) { s.series[7] = database.Series{ID: 7, Name: "Batman: Hush Returns"} }
				return s
			},
			want: want{
				ints:       map[string]int{"singleFound": 1, "singleKeptChanged": 1, "dupFound": 0, "dupRefused": 0},
				inWarn:     []string{"kept 1 single-book series and 0 merged-from series whose row was renamed"},
				notInWarn:  []string{guardLine},
				keepSeries: []int{7},
			},
		},
		{
			name: "phase 1 meets a series someone else already deleted, inside a duplicate group",
			build: func() *csCondStore {
				s := csHushStore()
				s.afterMembership = func(s *csCondStore) { delete(s.series, 7) }
				return s
			},
			want: want{
				ints:      map[string]int{"singleFound": 1, "singleAlreadyGone": 1, "dupFound": 0, "dupRefused": 0},
				inWarn:    []string{"1 single-book series and 0 merged-from series were already deleted"},
				notInWarn: []string{guardLine},
			},
		},
		{
			// A store that cannot answer: counted, and phase 1 stops, so phase 2 does
			// not run and cannot count the series again.
			name: "phase 1 delete the store cannot answer, inside a duplicate group",
			build: func() *csCondStore {
				s := csHushStore()
				s.deleteIfErr = errors.New("memdb is missing rows")
				return s
			},
			want: want{
				ints:       map[string]int{"singleFound": 1, "singleDeleteFailed": 1, "dupFound": 0, "dupRefused": 0},
				stopped1:   true,
				inWarn:     []string{"could not verify 1 single-book series and 0 merged-from series", "stopped in phase 1"},
				notInWarn:  []string{guardLine},
				keepSeries: []int{7},
			},
		},
		{
			// n-1. The unlink fails, the delete is never attempted. Its own bucket,
			// and (as before) the series stays grouped: phase 2 then fails to
			// re-home the same book and leaves the merged-from series unreached.
			name: "phase 1 unlink fails",
			build: func() *csCondStore {
				s := csHushStore()
				s.modifyErr = errors.New("simulated write failure")
				return s
			},
			want: want{
				ints:       map[string]int{"singleFound": 1, "singleUnlinkFailed": 1, "singleDeleteFailed": 0, "dupMergeFromSeries": 1, "dupNotReached": 1},
				inWarn:     []string{"1 single-book series could not be unlinked", "1 merged-from series were not processed"},
				notInWarn:  []string{"could not verify"},
				keepSeries: []int{7},
			},
		},
		{
			name:  "phase 2 deletes (control)",
			build: dupSeriesStore,
			want: want{
				ints: map[string]int{"dupFound": 1, "dupApplied": 1, "dupSeriesDeleted": 1, "dupMergeFromSeries": 1, "singleFound": 0},
			},
		},
		{
			name: "phase 2 keeps a series a book was linked into",
			build: func() *csCondStore {
				s := dupSeriesStore()
				s.afterMembership = func(s *csCondStore) { s.linkBook("court-of-owls-2", 7) }
				return s
			},
			want: want{
				ints:       map[string]int{"dupRefusedLive": 1, "dupRefused": 0, "dupMergeFromSeries": 1, "dupApplied": 0},
				inWarn:     []string{"found live references in 0 single-book series and 1 merged-from series"},
				notInWarn:  []string{guardLine},
				keepSeries: []int{7},
			},
		},
		{
			// m-2: the Changed outcome at the run level, in phase 2.
			name: "phase 2 keeps a series renamed since the read",
			build: func() *csCondStore {
				s := dupSeriesStore()
				s.afterMembership = func(s *csCondStore) { s.series[7] = database.Series{ID: 7, Name: "Night of Owls"} }
				return s
			},
			want: want{
				ints:       map[string]int{"dupKeptChanged": 1, "dupMergeFromSeries": 1, "dupApplied": 0},
				inWarn:     []string{"kept 0 single-book series and 1 merged-from series whose row was renamed"},
				keepSeries: []int{7},
			},
		},
		{
			// m-2: the Gone outcome at the run level, in phase 2.
			name: "phase 2 meets a series someone else already deleted",
			build: func() *csCondStore {
				s := dupSeriesStore()
				s.afterMembership = func(s *csCondStore) { delete(s.series, 7) }
				return s
			},
			want: want{
				ints:   map[string]int{"dupAlreadyGone": 1, "dupMergeFromSeries": 1, "dupApplied": 0},
				inWarn: []string{"0 single-book series and 1 merged-from series were already deleted"},
			},
		},
		{
			name: "phase 2 delete the store cannot answer",
			build: func() *csCondStore {
				s := dupSeriesStore()
				s.deleteIfErr = errors.New("memdb is missing rows")
				return s
			},
			want: want{
				ints:       map[string]int{"dupDeleteFailed": 1, "dupMergeFromSeries": 1, "dupNotReached": 0},
				stopped2:   true,
				inWarn:     []string{"could not verify 0 single-book series and 1 merged-from series", "stopped in phase 2"},
				keepSeries: []int{7},
			},
		},
		{
			// n-2: the series after the one the store could not answer for.
			name: "phase 2 delete the store cannot answer, with another series behind it",
			build: func() *csCondStore {
				s := csOwlsTripleStore()
				s.deleteIfErr = errors.New("memdb is missing rows")
				return s
			},
			want: want{
				ints:       map[string]int{"dupDeleteFailed": 1, "dupNotReached": 1, "dupMergeFromSeries": 2},
				stopped2:   true,
				inWarn:     []string{"1 merged-from series were not processed"},
				keepSeries: []int{7, 9},
			},
		},
		{
			// n-2: a book write fails, so neither merged-from series is reached.
			name: "phase 2 book write fails",
			build: func() *csCondStore {
				s := csOwlsTripleStore()
				s.modifyErr = errors.New("simulated write failure")
				return s
			},
			want: want{
				ints:       map[string]int{"dupNotReached": 2, "dupMergeFromSeries": 2, "dupSeriesDeleted": 0},
				inWarn:     []string{"2 merged-from series were not processed"},
				keepSeries: []int{7, 9},
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			done := csCaptureDone(t)
			store := c.build()
			rep := &csPhase1Reporter{}

			err := (&cleanupSeriesJob{}).Run(context.Background(), store, rep, false)
			// A stop fails the op, wrapping the sentinel; every other run succeeds.
			stopped := c.want.stopped1 || c.want.stopped2
			if stopped != errors.Is(err, ErrCleanupSeriesStopped) {
				t.Fatalf("Run error = %v, want errors.Is(ErrCleanupSeriesStopped) = %v", err, stopped)
			}
			if !stopped && err != nil {
				t.Fatalf("Run: %v", err)
			}

			d := done()
			for k, v := range c.want.ints {
				if got := csDoneInt(t, d, k); got != v {
					t.Errorf("Done %s = %d, want %d: %v", k, got, v, d)
				}
			}
			if got := csDoneBool(t, d, "stoppedPhase1"); got != c.want.stopped1 {
				t.Errorf("Done stoppedPhase1 = %v, want %v", got, c.want.stopped1)
			}
			if got := csDoneBool(t, d, "stoppedPhase2"); got != c.want.stopped2 {
				t.Errorf("Done stoppedPhase2 = %v, want %v", got, c.want.stopped2)
			}
			csAssertBucketsAddUp(t, d)
			for _, part := range c.want.inWarn {
				if !csLogContains(rep, part) {
					t.Errorf("no log line contains %q: %v", part, rep.logs)
				}
			}
			for _, part := range c.want.notInWarn {
				if csLogContains(rep, part) {
					t.Errorf("a log line contains %q, which names a cause that is not the real one: %v", part, rep.logs)
				}
			}
			for _, id := range c.want.keepSeries {
				if !store.hasSeries(id) {
					t.Errorf("series %d was deleted", id)
				}
			}
		})
	}
}

// --- m-1: a store that has the delete but cannot count --------------------------

func TestCleanupSeriesRun_RefusesALiveRunWhenTheStoreCannotCountReferences(t *testing.T) {
	store := csHushStore()
	store.notReady = errors.New("the memdb is not serving")
	rep := &csPhase1Reporter{}

	err := (&cleanupSeriesJob{}).Run(context.Background(), store, rep, false)

	if err == nil || !strings.Contains(err.Error(), "cannot count references to a series right now") {
		t.Fatalf("err = %v, want a refusal naming the unready store", err)
	}
	if !errors.Is(err, store.notReady) {
		t.Fatalf("the refusal must wrap the store's own reason: %v", err)
	}
	if store.membershipReads != 0 {
		t.Fatalf("the run read membership %d times before refusing; it must refuse first", store.membershipReads)
	}
	if got := store.books["hush-1"].SeriesID; got == nil || *got != 7 {
		t.Fatalf("hush-1 was unlinked from series 7: %v", got)
	}
	if len(store.conditionalCalls) != 0 {
		t.Fatalf("the run tried deletes %v on a store that said it could not count", store.conditionalCalls)
	}
}

func TestCleanupSeriesRun_ADryRunDoesNotProbeTheStore(t *testing.T) {
	store := csHushStore()
	store.notReady = errors.New("the memdb is not serving")
	rep := &csPhase1Reporter{}

	if err := (&cleanupSeriesJob{}).Run(context.Background(), store, rep, true); err != nil {
		t.Fatalf("a dry run writes nothing, so a store that cannot count must not stop it: %v", err)
	}
}

// csDeleterWithoutReadiness has the conditional delete but cannot say whether it
// can count.
type csDeleterWithoutReadiness struct {
	*database.MockStore
}

func (csDeleterWithoutReadiness) GetAllSeriesBookRefCounts() (map[int]int, error) {
	return map[int]int{}, nil
}

func (csDeleterWithoutReadiness) DeleteSeriesIfUnreferenced(int, string, *int) (bool, error) {
	return true, nil
}

func TestCleanupSeriesRun_RefusesALiveRunWhenTheStoreCannotSayItIsReady(t *testing.T) {
	reads := 0
	mock := &database.MockStore{}
	mock.GetAllSeriesFunc = func() ([]database.Series, error) {
		reads++
		return nil, nil
	}

	err := (&cleanupSeriesJob{}).Run(context.Background(), csDeleterWithoutReadiness{mock}, &csPhase1Reporter{}, false)

	if err == nil || !strings.Contains(err.Error(), "database.SeriesDeleteReadiness") {
		t.Fatalf("err = %v, want a refusal naming the missing capability", err)
	}
	if reads != 0 {
		t.Fatalf("the run read the library %d times before refusing", reads)
	}
}

// A store that degrades AFTER the probe: the first delete cannot be answered, and
// phase 1 must stop there. Before this, it went on to unlink the books of every
// remaining candidate and fail every delete.
func TestCleanupSeriesRun_Phase1StopsAtTheFirstDeleteTheStoreCannotAnswer(t *testing.T) {
	store := newCsCondStore(
		[]database.Series{{ID: 7, Name: "Year One"}, {ID: 8, Name: "Zero Year"}, {ID: 9, Name: "Endgame"}},
		database.Book{ID: "year-one-1", SeriesID: intp(7)},
		database.Book{ID: "zero-year-1", SeriesID: intp(8)},
		database.Book{ID: "endgame-1", SeriesID: intp(9)},
	)
	cause := errors.New("memdb is missing rows")
	store.deleteIfErr = cause
	rep := &csPhase1Reporter{}
	done := csCaptureDone(t)

	err := (&cleanupSeriesJob{}).Run(context.Background(), store, rep, false)
	csRequireStopError(t, err, cause, "phase 1")
	// The counts are logged before the error returns, so a failed op still says
	// how far it got.
	if d := done(); csDoneInt(t, d, "singleFound") != 1 || !csDoneBool(t, d, "stoppedPhase1") {
		t.Fatalf("Done line must be logged before the stop error returns: %v", d)
	}

	if len(store.conditionalCalls) != 1 {
		t.Fatalf("conditional deletes = %v, want exactly the first", store.conditionalCalls)
	}
	for _, id := range []string{"zero-year-1", "endgame-1"} {
		if got := store.books[id].SeriesID; got == nil {
			t.Errorf("book %s was unlinked after the store had already refused a delete", id)
		}
	}
	if !csLogContains(rep, "stopped in phase 1") {
		t.Fatalf("the stop must be in the warn line: %v", rep.logs)
	}
}

// Phase 2 re-homes a group's books before it deletes, so it stops the same way.
func TestCleanupSeriesRun_Phase2StopsAtTheFirstDeleteTheStoreCannotAnswer(t *testing.T) {
	store := newCsCondStore(
		[]database.Series{
			{ID: 7, Name: "Court of Owls"}, {ID: 8, Name: "court of owls"},
			{ID: 9, Name: "Night of Owls"}, {ID: 10, Name: "night of owls"},
		},
		database.Book{ID: "court-of-owls-1", SeriesID: intp(7), SeriesSequence: intp(2)},
		database.Book{ID: "court-of-owls-2", SeriesID: intp(8)},
		database.Book{ID: "court-of-owls-3", SeriesID: intp(8)},
		database.Book{ID: "night-of-owls-1", SeriesID: intp(9), SeriesSequence: intp(2)},
		database.Book{ID: "night-of-owls-2", SeriesID: intp(10)},
		database.Book{ID: "night-of-owls-3", SeriesID: intp(10)},
	)
	cause := errors.New("memdb is missing rows")
	store.deleteIfErr = cause
	rep := &csPhase1Reporter{}
	done := csCaptureDone(t)

	err := (&cleanupSeriesJob{}).Run(context.Background(), store, rep, false)
	csRequireStopError(t, err, cause, "phase 2")
	if d := done(); csDoneInt(t, d, "dupFound") != 1 || !csDoneBool(t, d, "stoppedPhase2") {
		t.Fatalf("Done line must be logged before the stop error returns: %v", d)
	}

	if len(store.conditionalCalls) != 1 {
		t.Fatalf("conditional deletes = %v, want only the first group's", store.conditionalCalls)
	}
	rehomed := 0
	for _, id := range []string{"court-of-owls-1", "night-of-owls-1"} {
		if got := store.books[id].SeriesID; got != nil && (*got == 8 || *got == 10) {
			rehomed++
		}
	}
	if rehomed != 1 {
		t.Fatalf("%d groups re-homed their books, want exactly the one the store failed in", rehomed)
	}
	if !csLogContains(rep, "stopped in phase 2") {
		t.Fatalf("the stop must be in the warn line: %v", rep.logs)
	}
}

// Phase 1 stopped because the store cannot count; phase 2 would re-home an
// unrelated group's books and then meet the same refusal, so it does not run.
func TestCleanupSeriesRun_Phase2DoesNotRunAfterPhase1Stopped(t *testing.T) {
	store := newCsCondStore(
		[]database.Series{
			{ID: 7, Name: "Year One"},
			{ID: 8, Name: "Court of Owls"}, {ID: 9, Name: "court of owls"},
		},
		database.Book{ID: "year-one-1", SeriesID: intp(7)},
		database.Book{ID: "court-of-owls-1", SeriesID: intp(8), SeriesSequence: intp(2)},
		database.Book{ID: "night-of-owls-1", SeriesID: intp(9)},
		database.Book{ID: "night-of-owls-2", SeriesID: intp(9)},
	)
	cause := errors.New("memdb is missing rows")
	store.deleteIfErr = cause
	rep := &csPhase1Reporter{}

	err := (&cleanupSeriesJob{}).Run(context.Background(), store, rep, false)
	csRequireStopError(t, err, cause, "phase 1")

	if len(store.conditionalCalls) != 1 || store.conditionalCalls[0] != 7 {
		t.Fatalf("conditional deletes = %v, want only phase 1's [7]", store.conditionalCalls)
	}
	if got := store.books["court-of-owls-1"].SeriesID; got == nil || *got != 8 {
		t.Fatalf("phase 2 re-homed a book after phase 1 had stopped: %v", got)
	}
}

// csRequireStopError checks the error a stopped run returns: the sentinel, the
// phase it stopped in, and the store's own error, all reachable.
func csRequireStopError(t *testing.T, err, cause error, phase string) {
	t.Helper()
	if !errors.Is(err, ErrCleanupSeriesStopped) {
		t.Fatalf("Run error = %v, want it to wrap ErrCleanupSeriesStopped", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("Run error = %v, want it to wrap the store's error %v", err, cause)
	}
	if !strings.Contains(err.Error(), phase) {
		t.Fatalf("Run error = %q, want it to name %s", err, phase)
	}
}
