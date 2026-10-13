// file: internal/database/ident_state_test.go
// version: 1.3.0
// guid: 325f377a-6217-41c5-9b34-7f92c945f8d9
// last-edited: 2026-10-12

package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"
)

func newIdentStore(t testing.TB) *PebbleStore {
	t.Helper()
	s, err := NewPebbleStoreInMemory("ident_" + ulid.Make().String())
	require.NoError(t, err)
	s.WaitForWarmup()
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func identStrPtr(v string) *string { return &v }
func identBoolPtr(v bool) *bool    { return &v }

func identCands(n int) []json.RawMessage {
	out := make([]json.RawMessage, n)
	for i := range out {
		out[i] = json.RawMessage(fmt.Sprintf(`{"title":"Title %06d","score":0.5}`, i))
	}
	return out
}

// deriveIdentState maps a whole book and cache row through deriveIdent, the
// way the live callers decode them, so the table can be written in rows.
func deriveIdentState(book *Book, cache *MetadataCandidateCache) (IdentState, string) {
	var status *string
	if book != nil {
		status = book.MetadataReviewStatus
	}
	if cache == nil {
		return deriveIdent(status, false, 0, false)
	}
	return deriveIdent(status, true, len(cache.Candidates), cache.Stale)
}

func TestDeriveIdent_Table(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name       string
		book       *Book
		cache      *MetadataCandidateCache
		wantState  IdentState
		wantReason string
	}{
		{"no row", &Book{}, nil, IdentUnasked, ""},
		{"row with no candidates", &Book{}, &MetadataCandidateCache{}, IdentAskedEmpty, ""},
		{"empty answer recorded", &Book{}, &MetadataCandidateCache{LastEmptyFetchAt: &now}, IdentAskedEmpty, ""},
		{"empty answers by source", &Book{}, &MetadataCandidateCache{EmptyAnswers: map[string]time.Time{"audible": now}}, IdentAskedEmpty, ""},
		{"candidates", &Book{}, &MetadataCandidateCache{Candidates: identCands(2)}, IdentHasCandidates, ""},
		{"stale candidates", &Book{}, &MetadataCandidateCache{Candidates: identCands(1), Stale: true}, IdentHasCandidates, IdentReasonStale},
		{"stale row with no candidates is asked_empty", &Book{}, &MetadataCandidateCache{Stale: true}, IdentAskedEmpty, ""},
		{"matched is applied, whatever the row", &Book{MetadataReviewStatus: identStrPtr("matched")}, nil, IdentApplied, ""},
		{"audio_confirmed is applied", &Book{MetadataReviewStatus: identStrPtr("audio_confirmed")}, &MetadataCandidateCache{Candidates: identCands(1), Stale: true}, IdentApplied, ""},
		{"no_match is held", &Book{MetadataReviewStatus: identStrPtr("no_match")}, &MetadataCandidateCache{Candidates: identCands(3)}, IdentHeld, IdentReasonNoMatch},
		{"no_match without a row is held", &Book{MetadataReviewStatus: identStrPtr("no_match")}, nil, IdentHeld, IdentReasonNoMatch},
		{"unknown status reads as unruled", &Book{MetadataReviewStatus: identStrPtr("something_else")}, nil, IdentUnasked, ""},
		{"nil book with candidates", nil, &MetadataCandidateCache{Candidates: identCands(1)}, IdentHasCandidates, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, r := deriveIdentState(tc.book, tc.cache)
			require.Equal(t, tc.wantState, s)
			require.Equal(t, tc.wantReason, r)
		})
	}

	// Scope: a nil primary flag counts as primary (EffectiveIsPrimaryVersion,
	// the Library's own rule); trash and quarantine leave the scope.
	require.True(t, InLibraryDefaultView(&Book{}), "nil IsPrimaryVersion is primary")
	require.True(t, InLibraryDefaultView(&Book{IsPrimaryVersion: identBoolPtr(true)}))
	require.False(t, InLibraryDefaultView(&Book{IsPrimaryVersion: identBoolPtr(false)}))
	require.False(t, InLibraryDefaultView(&Book{MarkedForDeletion: identBoolPtr(true)}))
	require.False(t, InLibraryDefaultView(&Book{QuarantinedAt: &now}))
	require.False(t, InLibraryDefaultView(nil))
}

// identOracle derives every book's state from scratch (book row + cache row)
// and tallies it, independently of the index.
func identOracle(t testing.TB, s *PebbleStore) (all, primary map[IdentState]int64) {
	t.Helper()
	all, primary = map[IdentState]int64{}, map[IdentState]int64{}
	require.NoError(t, forEachBookRow(s.db, func(id string, value []byte) error {
		// The same partial decode and undecodable rule as the index
		// (identBookFromRaw): an undecodable row is no book.
		b, ok := identBookFromRaw(value)
		if !ok {
			return nil
		}
		c, err := s.readIdentCacheView(id)
		require.NoError(t, err)
		st, _ := deriveIdent(b.MetadataReviewStatus, c.present, c.candidates, c.stale)
		all[st]++
		if InLibraryDefaultView(b) {
			primary[st]++
		}
		return nil
	}))
	return all, primary
}

func requireCountsMatchOracle(t testing.TB, s *PebbleStore) {
	t.Helper()
	all, primary := identOracle(t, s)
	c := s.IdentCounts()
	require.True(t, c.Ready)
	for _, st := range AllIdentStates {
		require.Equalf(t, all[st], c.All[st], "all/%s", st)
		require.Equalf(t, primary[st], c.Primary[st], "primary/%s", st)
	}
}

// identPool is the randomized test's set of live book ids.
type identPool struct {
	mu  sync.Mutex
	ids []string
	n   int
}

func (p *identPool) pick(r *rand.Rand) (int, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	i := r.Intn(len(p.ids))
	return i, p.ids[i]
}

func (p *identPool) replace(i int, id string) {
	p.mu.Lock()
	p.ids[i] = id
	p.mu.Unlock()
}

func (p *identPool) nextTitle() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.n++
	return fmt.Sprintf("Title %06d", p.n)
}

func identCreateBook(t testing.TB, s *PebbleStore, pool *identPool) string {
	t.Helper()
	id, err := identCreateBookErr(s, pool)
	require.NoError(t, err)
	return id
}

func identCreateBookErr(s *PebbleStore, pool *identPool) (string, error) {
	title := pool.nextTitle()
	b, err := s.CreateBook(&Book{Title: title, FilePath: "/lib/ident/" + title + ".m4b", Format: "m4b"})
	if err != nil {
		return "", err
	}
	return b.ID, nil
}

// identRandomOp runs one random write of an identification input. It
// returns errors rather than failing, because it runs on worker goroutines.
func identRandomOp(s *PebbleStore, pool *identPool, r *rand.Rand) error {
	slot, id := pool.pick(r)
	modify := func(fn func(*Book)) error {
		_, err := s.ModifyBook(id, func(b *Book) error { fn(b); return nil })
		return err
	}
	switch op := r.Intn(11); op {
	case 0: // put a cache row
		return s.PutMetadataCache(&MetadataCandidateCache{
			BookID: id, FetchedAt: time.Now(), Candidates: identCands(r.Intn(4)), Stale: r.Intn(4) == 0,
		})
	case 1: // delete the cache row
		return s.DeleteMetadataCache(id)
	case 2: // review status
		statuses := []*string{nil, identStrPtr("matched"), identStrPtr("audio_confirmed"), identStrPtr("no_match")}
		st := statuses[r.Intn(len(statuses))]
		return modify(func(b *Book) { b.MetadataReviewStatus = st })
	case 3: // primary flip, including back to nil
		flags := []*bool{nil, identBoolPtr(true), identBoolPtr(false)}
		f := flags[r.Intn(len(flags))]
		return modify(func(b *Book) { b.IsPrimaryVersion = f })
	case 4: // retitle: the Stale stamp
		title := pool.nextTitle()
		return modify(func(b *Book) { b.Title = title })
	case 5: // delete the book, replace it with a new one
		if err := s.DeleteBook(id); err != nil {
			return err
		}
		nid, err := identCreateBookErr(s, pool)
		if err != nil {
			return err
		}
		pool.replace(slot, nid)
		return nil
	case 6: // raw set under the prefix
		data, err := json.Marshal(&MetadataCandidateCache{BookID: id, Candidates: identCands(r.Intn(3))})
		if err != nil {
			return err
		}
		return s.SetRaw(metadataCacheKeyPrefix+id, data)
	case 7: // raw delete, single or batched with another book's row
		if r.Intn(2) == 0 {
			return s.DeleteRaw(metadataCacheKeyPrefix + id)
		}
		_, other := pool.pick(r)
		return s.DeleteRawBatch([]string{metadataCacheKeyPrefix + id, metadataCacheKeyPrefix + other})
	case 8: // trash toggle
		v := r.Intn(2) == 0
		return modify(func(b *Book) { b.MarkedForDeletion = &v })
	case 9: // quarantine toggle
		var at *time.Time
		if r.Intn(2) == 0 {
			now := time.Now()
			at = &now
		}
		return modify(func(b *Book) { b.QuarantinedAt = at })
	default: // an edit that touches no identification input
		g := "Genre 01"
		return modify(func(b *Book) { b.Genre = &g })
	}
}

// TestIdentCounters_EqualRecount_Randomized drives every writer of an
// identification input from several goroutines at once, and after every 100
// operations requires the counters to equal a recount of the membership keys
// (IdentCountersDrift) and both to equal a derivation from scratch.
//
// The acceptance size (5 seeds x 2,000 books x 10 checkpoints) runs without
// -race. Under -race (raceEnabled) it shrinks to 2 seeds x 400 books, still
// 10 checkpoints each: the race detector is there to catch unsynchronized
// access, which the concurrent rounds exercise at any size, and the full
// size cost about 4x its plain time under it.
func TestIdentCounters_EqualRecount_Randomized(t *testing.T) {
	const (
		rounds     = 10
		perRound   = 100
		goroutines = 4
	)
	seeds, books := int64(5), 2000
	if raceEnabled {
		seeds, books = 2, 400
	}
	checkpoints, mismatches := 0, 0
	for seed := int64(1); seed <= seeds; seed++ {
		s := newIdentStore(t)
		require.True(t, s.IdentCounts().Ready, "an empty store is stamped built at open")
		pool := &identPool{}
		for range books {
			pool.ids = append(pool.ids, identCreateBook(t, s, pool))
		}
		for round := range rounds {
			var wg sync.WaitGroup
			for g := range goroutines {
				r := rand.New(rand.NewSource(seed*1000 + int64(round*goroutines+g)))
				wg.Go(func() {
					for range perRound / goroutines {
						if err := identRandomOp(s, pool, r); err != nil {
							t.Error(err)
							return
						}
					}
				})
			}
			wg.Wait()
			if t.Failed() {
				t.FailNow()
			}
			drift, err := s.IdentCountersDrift()
			require.NoError(t, err)
			checkpoints++
			if drift.Total() != 0 {
				mismatches++
			}
			require.Zerof(t, drift.Total(), "seed %d round %d: %v", seed, round, drift.Deltas)
			requireCountsMatchOracle(t, s)
		}
		// The write-through left nothing for a rebuild to fix.
		st, err := s.RebuildIdentIndex(t.Context())
		require.NoError(t, err)
		require.Zero(t, st.RowsFixed, "seed %d", seed)
		require.Zero(t, st.RowsRemoved, "seed %d", seed)
		require.Zero(t, st.KeysRemoved, "seed %d", seed)
		require.Zero(t, st.CounterDrift, "seed %d", seed)
	}
	t.Logf("identification counters: seeds=%d books=%d checkpoints=%d mismatches=%d", seeds, books, checkpoints, mismatches)
}

// wipeIdentRows removes every ident: row and the marker, leaving the store as
// a build that predates the index would.
func wipeIdentRows(t testing.TB, s *PebbleStore) {
	t.Helper()
	b := s.db.NewBatch()
	defer b.Close()
	require.NoError(t, b.DeleteRange([]byte("ident:"), []byte("ident;"), nil))
	require.NoError(t, b.Commit(nil))
}

// seedIdentBooksMixed writes n synthetic book rows straight into Pebble, one
// batch per 1,000, in a random mix of identification inputs: a third with
// candidates, a fifth with an empty cache row, some applied or held, some
// non-primary, trashed or quarantined. It writes no ident: row and leaves
// memdb alone, so it is for database-level tests of the index only; it
// replaces 5,000 synced CreateBook commits (most of this test's time under
// -race) with five batches.
func seedIdentBooksMixed(t testing.TB, s *PebbleStore, n int, r *rand.Rand) []string {
	t.Helper()
	ids := make([]string, 0, n)
	f, tr := false, true
	for start := 0; start < n; start += 1000 {
		batch := s.db.NewBatch()
		for i := start; i < min(start+1000, n); i++ {
			id := fmt.Sprintf("mixed%06d", i)
			ids = append(ids, id)
			b := &Book{ID: id, Title: fmt.Sprintf("Title %06d", i), FilePath: "/lib/mixed/" + id + ".m4b", Format: "m4b"}
			switch r.Intn(10) {
			case 0:
				b.MetadataReviewStatus = identStrPtr("matched")
			case 1:
				b.MetadataReviewStatus = identStrPtr("no_match")
			case 2:
				b.IsPrimaryVersion = &f
			case 3:
				b.IsPrimaryVersion = &tr
			case 4:
				b.MarkedForDeletion = &tr
			case 5:
				now := time.Now()
				b.QuarantinedAt = &now
			}
			row, err := json.Marshal(b)
			require.NoError(t, err)
			require.NoError(t, batch.Set([]byte(bookRowPrefix+id), row, nil))
			var cache *MetadataCandidateCache
			switch r.Intn(5) {
			case 0, 1:
				cache = &MetadataCandidateCache{BookID: id, Candidates: identCands(2), Stale: r.Intn(3) == 0}
			case 2:
				cache = &MetadataCandidateCache{BookID: id}
			}
			if cache != nil {
				cr, err := json.Marshal(cache)
				require.NoError(t, err)
				require.NoError(t, batch.Set(metadataCacheKey(id), cr, nil))
			}
		}
		require.NoError(t, batch.Commit(nil))
		require.NoError(t, batch.Close())
	}
	return ids
}

func TestIdentRebuild_IdempotentAndReportsNotReady(t *testing.T) {
	// 5,000 books is the acceptance size (the logged rebuild duration);
	// under -race it is 1,500, which still spans several seed batches and
	// rebuild worker rounds.
	books := 5000
	if raceEnabled {
		books = 1500
	}
	s := newIdentStore(t)
	pool := &identPool{ids: seedIdentBooksMixed(t, s, books, rand.New(rand.NewSource(7)))}

	// A boot that finds no marker: the index is not ready, its reads refuse.
	wipeIdentRows(t, s)
	s.ident.ready.Store(false)
	require.NoError(t, s.loadIdentIndexState())
	require.False(t, s.IdentCounts().Ready)
	_, err := s.IdentBookIDs([]IdentState{IdentUnasked})
	require.ErrorIs(t, err, ErrIdentIndexBuilding)
	_, err = s.IdentCountersDrift()
	require.ErrorIs(t, err, ErrIdentIndexBuilding)

	began := time.Now()
	require.NoError(t, s.EnsureIdentIndex(t.Context()))
	t.Logf("identification rebuild from scratch: books=%d duration=%s", books, time.Since(began).Round(time.Millisecond))
	require.True(t, s.IdentCounts().Ready)
	requireCountsMatchOracle(t, s)
	ids, err := s.IdentBookIDs([]IdentState{IdentUnasked, IdentAskedEmpty, IdentHasCandidates, IdentApplied, IdentHeld})
	require.NoError(t, err)
	require.Len(t, ids, books)

	// Idempotent: a second run finds nothing to fix and stays ready.
	st, err := s.RebuildIdentIndex(t.Context())
	require.NoError(t, err)
	require.False(t, st.FromScratch)
	require.Zero(t, st.RowsFixed)
	require.Zero(t, st.CounterDrift)
	requireCountsMatchOracle(t, s)

	// A reconcile over a built index repairs what a build without the index
	// wrote: here a cache row set behind its back, and a counter knocked off.
	victim := pool.ids[0]
	require.NoError(t, s.db.Set(metadataCacheKey(victim), []byte(`{"book_id":"x","candidates":[{"t":1}]}`), nil))
	s.ident.published[identScopeAll][identStateIndex(IdentApplied)].Add(1)
	st, err = s.RebuildIdentIndex(t.Context())
	require.NoError(t, err)
	require.Equal(t, 1, st.RowsFixed)
	require.NotZero(t, st.CounterDrift)
	requireCountsMatchOracle(t, s)
	drift, err := s.IdentCountersDrift()
	require.NoError(t, err)
	require.Zero(t, drift.Total())

	// Reset leaves an empty, built index.
	require.NoError(t, s.Reset())
	c := s.IdentCounts()
	require.True(t, c.Ready)
	for _, st := range AllIdentStates {
		require.Zero(t, c.All[st])
	}
}

// TestIdentRebuild_UndecodableBookRowsDoNotBlock: more undecodable book rows
// than the rebuild has workers (the shape that could fill its channels) are
// skipped and counted, and the rebuild still finishes promptly and serves.
func TestIdentRebuild_UndecodableBookRowsDoNotBlock(t *testing.T) {
	s := newIdentStore(t)
	pool := &identPool{}
	var good []string
	for range 50 {
		good = append(good, identCreateBook(t, s, pool))
	}
	bad := identRebuildWorkers()*4 + 20
	b := s.db.NewBatch()
	for i := range bad {
		require.NoError(t, b.Set([]byte(fmt.Sprintf("book:zzbad%04d", i)), []byte(`{"title": not json`), nil))
	}
	require.NoError(t, b.Commit(nil))
	require.NoError(t, b.Close())
	// One of them carries a stale ident row, which the rebuild must remove.
	require.NoError(t, s.db.Set(identStateKey("zzbad0000"), []byte(`{"s":"unasked","p":true}`), nil))
	require.NoError(t, s.db.Set(identByKey(IdentUnasked, "zzbad0000"), []byte{'1'}, nil))

	wipeIdentRows(t, s)
	s.ident.ready.Store(false)
	type result struct {
		st  IdentRebuildStats
		err error
	}
	done := make(chan result, 1)
	go func() {
		st, err := s.RebuildIdentIndex(t.Context())
		done <- result{st, err}
	}()
	select {
	case r := <-done:
		require.NoError(t, r.err)
		require.Equal(t, bad, r.st.Undecodable)
		require.Equal(t, len(good)+bad, r.st.Books)
	case <-time.After(30 * time.Second):
		t.Fatal("rebuild did not finish")
	}
	c := s.IdentCounts()
	require.True(t, c.Ready)
	require.Equal(t, int64(len(good)), c.All[IdentUnasked], "only the decodable books are indexed")
	drift, err := s.IdentCountersDrift()
	require.NoError(t, err)
	require.Zero(t, drift.Total())

	// Again with the stale row present on a built index: it is removed.
	require.NoError(t, s.db.Set(identStateKey("zzbad0001"), []byte(`{"s":"unasked","p":true}`), nil))
	require.NoError(t, s.db.Set(identByKey(IdentUnasked, "zzbad0001"), []byte{'1'}, nil))
	_, err = s.RebuildIdentIndex(t.Context())
	require.NoError(t, err)
	require.Equal(t, int64(len(good)), s.IdentCounts().All[IdentUnasked])
	_, ok, err := s.readIdentRow("zzbad0001")
	require.NoError(t, err)
	require.False(t, ok)
}

// TestIdentRebuild_CancelledReturns: a cancelled rebuild returns its
// context's error and leaves the index not ready, rather than blocking.
func TestIdentRebuild_CancelledReturns(t *testing.T) {
	s := newIdentStore(t)
	pool := &identPool{}
	for range 200 {
		identCreateBook(t, s, pool)
	}
	wipeIdentRows(t, s)
	s.ident.ready.Store(false)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := s.RebuildIdentIndex(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, s.IdentCounts().Ready)
	_, err = s.RebuildIdentIndex(t.Context())
	require.NoError(t, err)
	require.True(t, s.IdentCounts().Ready)
}

func TestIdentBookIDs_Membership(t *testing.T) {
	s := newIdentStore(t)
	pool := &identPool{}
	a := identCreateBook(t, s, pool)
	b := identCreateBook(t, s, pool)
	c := identCreateBook(t, s, pool)
	require.NoError(t, s.PutMetadataCache(&MetadataCandidateCache{BookID: b, Candidates: identCands(1)}))
	_, err := s.ModifyBook(c, func(bk *Book) error { bk.MetadataReviewStatus = identStrPtr("no_match"); return nil })
	require.NoError(t, err)

	got, err := s.IdentBookIDs([]IdentState{IdentUnasked, IdentAskedEmpty})
	require.NoError(t, err)
	require.Equal(t, map[string]struct{}{a: {}}, got)
	got, err = s.IdentBookIDs([]IdentState{IdentHeld})
	require.NoError(t, err)
	require.Equal(t, map[string]struct{}{c: {}}, got)
	_, err = s.IdentBookIDs([]IdentState{"bogus"})
	require.Error(t, err)

	c2 := s.IdentCounts()
	require.Equal(t, int64(1), IdentMissing(c2.Primary))
	require.Equal(t, int64(1), c2.Primary[IdentHasCandidates])
	require.Equal(t, int64(1), c2.Primary[IdentHeld])
}

func TestIdentDirty_CompareAndDelete(t *testing.T) {
	s := newIdentStore(t)
	e1, err := s.MarkIdentDirty("book-a")
	require.NoError(t, err)
	page, err := s.IdentDirtyPage("", 256)
	require.NoError(t, err)
	require.Equal(t, []IdentDirtyEntry{{BookID: "book-a", Epoch: e1}}, page)

	// Re-marked between the drain's read and its clear: the clear with the
	// old epoch must leave the newer mark.
	e2, err := s.MarkIdentDirty("book-a")
	require.NoError(t, err)
	require.Greater(t, e2, e1)
	cleared, err := s.ClearIdentDirty("book-a", e1)
	require.NoError(t, err)
	require.False(t, cleared, "a mark re-set after the read survives")
	page, err = s.IdentDirtyPage("", 256)
	require.NoError(t, err)
	require.Equal(t, []IdentDirtyEntry{{BookID: "book-a", Epoch: e2}}, page)

	cleared, err = s.ClearIdentDirty("book-a", e2)
	require.NoError(t, err)
	require.True(t, cleared)
	page, err = s.IdentDirtyPage("", 256)
	require.NoError(t, err)
	require.Empty(t, page)

	cleared, err = s.ClearIdentDirty("book-a", e2)
	require.NoError(t, err)
	require.False(t, cleared, "clearing an absent mark is a no-op")

	_, err = s.MarkIdentDirty("")
	require.Error(t, err)
}

func TestIdentDirty_PageOrderAndLimit(t *testing.T) {
	s := newIdentStore(t)
	ids := []string{"book-c", "book-a", "book-e", "book-b", "book-d"}
	epochs := map[string]uint64{}
	for _, id := range ids {
		e, err := s.MarkIdentDirty(id)
		require.NoError(t, err)
		epochs[id] = e
	}
	page, err := s.IdentDirtyPage("", 2)
	require.NoError(t, err)
	require.Equal(t, []IdentDirtyEntry{{"book-a", epochs["book-a"]}, {"book-b", epochs["book-b"]}}, page)
	page, err = s.IdentDirtyPage("book-b", 2)
	require.NoError(t, err)
	require.Equal(t, []IdentDirtyEntry{{"book-c", epochs["book-c"]}, {"book-d", epochs["book-d"]}}, page)
	page, err = s.IdentDirtyPage("book-d", 2)
	require.NoError(t, err)
	require.Equal(t, []IdentDirtyEntry{{"book-e", epochs["book-e"]}}, page)
	page, err = s.IdentDirtyPage("book-e", 2)
	require.NoError(t, err)
	require.Empty(t, page)
	// A cursor that is not itself marked resumes at its successor.
	page, err = s.IdentDirtyPage("book-bb", 10)
	require.NoError(t, err)
	require.Len(t, page, 3)
	require.Equal(t, "book-c", page[0].BookID)
	_, err = s.IdentDirtyPage("", 0)
	require.Error(t, err)
}

// BenchmarkIdentCount_40k measures the O(1) count over a 40,000-primary-book
// index: IdentCounts is a read of the in-memory counter mirror.
func BenchmarkIdentCount_40k(b *testing.B) {
	s := newIdentStore(b)
	seedIdentBooksFast(b, s, 40000)
	b.ResetTimer()
	var missing int64
	for b.Loop() {
		missing = IdentMissing(s.IdentCounts().Primary)
	}
	b.StopTimer()
	b.ReportMetric(float64(missing), "missing")
	b.Logf("ident:missing primary count over 40000 books = %d, %s per count", missing, time.Duration(b.Elapsed().Nanoseconds()/int64(max(b.N, 1))))
}

// seedIdentBooksFast writes n synthetic book rows straight into Pebble (one
// batch per 1,000) with every third holding candidates, then builds the index
// from them. It bypasses CreateBook's per-book synced commit; memdb is not
// populated, so it is for database-level measurements only.
func seedIdentBooksFast(t testing.TB, s *PebbleStore, n int) {
	t.Helper()
	for start := 0; start < n; start += 1000 {
		batch := s.db.NewBatch()
		for i := start; i < min(start+1000, n); i++ {
			id := fmt.Sprintf("bench%06d", i)
			row, err := json.Marshal(&Book{ID: id, Title: fmt.Sprintf("Title %06d", i), FilePath: "/lib/bench/" + id, Format: "m4b"})
			require.NoError(t, err)
			require.NoError(t, batch.Set([]byte(bookRowPrefix+id), row, nil))
			if i%3 == 0 {
				cr, err := json.Marshal(&MetadataCandidateCache{BookID: id, Candidates: identCands(1)})
				require.NoError(t, err)
				require.NoError(t, batch.Set(metadataCacheKey(id), cr, nil))
			}
		}
		require.NoError(t, batch.Commit(nil))
		batch.Close()
	}
	_, err := s.RebuildIdentIndex(t.Context())
	require.NoError(t, err)
}

// identTotal is the number of books the all-scope counters hold.
func identTotal(c IdentCounts) int64 {
	var n int64
	for _, s := range AllIdentStates {
		n += c.All[s]
	}
	return n
}

func requireNoIdentDrift(t testing.TB, s *PebbleStore) {
	t.Helper()
	d, err := s.IdentCountersDrift()
	require.NoError(t, err)
	require.Zero(t, d.Total(), "%v", d.Deltas)
}

// TestIdentWrites_UndecodableBookRowDoesNotFailCacheWrites: a book row that
// will not decode (or whose signature sidecar will not) must not fail a
// metadata_cache write, as it did not before the index existed. The
// undecodable book is in no state; the one with a broken sidecar is indexed
// from its row.
func TestIdentWrites_UndecodableBookRowDoesNotFailCacheWrites(t *testing.T) {
	s := newIdentStore(t)
	pool := &identPool{}
	bad := identCreateBook(t, s, pool)
	sig := identCreateBook(t, s, pool)
	require.NoError(t, s.db.Set([]byte(bookRowPrefix+bad), []byte(`{"title": not json`), nil))
	require.NoError(t, s.db.Set(bookSigKey(sig), []byte(`{not json`), nil))
	_, err := s.GetBookByID(sig)
	require.Error(t, err, "the sidecar really is broken for a full read")

	data, err := json.Marshal(&MetadataCandidateCache{BookID: bad, Candidates: identCands(1)})
	require.NoError(t, err)
	for _, id := range []string{bad, sig} {
		require.NoError(t, s.PutMetadataCache(&MetadataCandidateCache{BookID: id, Candidates: identCands(1)}), id)
	}
	_, ok, err := s.readIdentRow(bad)
	require.NoError(t, err)
	require.False(t, ok, "an undecodable book is in no state")
	r, ok, err := s.readIdentRow(sig)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, IdentHasCandidates, r.State)

	require.NoError(t, s.DeleteMetadataCache(bad))
	require.NoError(t, s.SetRaw(metadataCacheKeyPrefix+bad, data))
	require.NoError(t, s.DeleteRaw(metadataCacheKeyPrefix+bad))
	require.NoError(t, s.SetRaw(metadataCacheKeyPrefix+bad, data))
	require.NoError(t, s.DeleteRawBatch([]string{metadataCacheKeyPrefix + bad, metadataCacheKeyPrefix + sig}))
	require.Equal(t, int64(1), identTotal(s.IdentCounts()), "only the decodable book is counted")
	requireNoIdentDrift(t, s)
	requireCountsMatchOracle(t, s)
	// The rebuild agrees with the write-through about both rows.
	st, err := s.RebuildIdentIndex(t.Context())
	require.NoError(t, err)
	require.Zero(t, st.RowsFixed)
	require.Equal(t, 1, st.Undecodable)
}

// TestIdentTxn_FailedCommitMovesNoCounter: a write whose commit fails leaves
// the counters (and the rows) as they were; the next write moves them.
func TestIdentTxn_FailedCommitMovesNoCounter(t *testing.T) {
	s := newIdentStore(t)
	pool := &identPool{}
	id := identCreateBook(t, s, pool)
	before := s.IdentCounts()
	require.Equal(t, int64(1), before.Primary[IdentUnasked])

	boom := errors.New("injected commit failure")
	fault := func(p *PebbleStore) error {
		if p == s {
			return boom
		}
		return nil
	}
	identCommitFault.Store(&fault)
	err := s.PutMetadataCache(&MetadataCandidateCache{BookID: id, Candidates: identCands(1)})
	_, merr := s.ModifyBook(id, func(b *Book) error { b.MetadataReviewStatus = identStrPtr("matched"); return nil })
	derr := s.DeleteBook(id)
	identCommitFault.Store(nil)
	require.ErrorIs(t, err, boom)
	require.ErrorIs(t, merr, boom)
	require.ErrorIs(t, derr, boom)
	require.Equal(t, before, s.IdentCounts(), "no failed commit moved a counter")
	requireNoIdentDrift(t, s)

	require.NoError(t, s.PutMetadataCache(&MetadataCandidateCache{BookID: id, Candidates: identCands(1)}))
	after := s.IdentCounts()
	require.Zero(t, after.Primary[IdentUnasked])
	require.Equal(t, int64(1), after.Primary[IdentHasCandidates])
	requireNoIdentDrift(t, s)
}

// TestIdentCounters_SameBookConcurrentWriters hammers ONE book from 8
// goroutines with every writer (cache put/delete, review status, primary,
// trash, quarantine, retitle, raw set/delete, delete-and-recreate): the
// book's cache stripe serializes its transitions, so the in-memory counters
// still equal a recount of the membership keys.
func TestIdentCounters_SameBookConcurrentWriters(t *testing.T) {
	s := newIdentStore(t)
	pool := &identPool{}
	pool.ids = []string{identCreateBook(t, s, pool)}
	const goroutines = 8
	perG := 150
	if raceEnabled {
		perG = 40
	}
	var wg sync.WaitGroup
	for g := range goroutines {
		r := rand.New(rand.NewSource(int64(100 + g)))
		wg.Go(func() {
			for range perG {
				if err := identRandomOp(s, pool, r); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()
	requireNoIdentDrift(t, s)
	requireCountsMatchOracle(t, s)
}

// TestIdentCounters_LoadedOnReopen: the every-boot path. Counters are not
// stored; a reopened store recounts them from the membership keys and serves
// them at once, equal to what the previous process had.
func TestIdentCounters_LoadedOnReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := NewPebbleStore(dir)
	require.NoError(t, err)
	s.WaitForWarmup()
	pool := &identPool{}
	for i := range 6 {
		id := identCreateBook(t, s, pool)
		switch i % 3 {
		case 1:
			require.NoError(t, s.PutMetadataCache(&MetadataCandidateCache{BookID: id, Candidates: identCands(1)}))
		case 2:
			require.NoError(t, s.PutMetadataCache(&MetadataCandidateCache{BookID: id}))
		}
	}
	f := false
	_, err = s.ModifyBook(pool.ids0(t, s), func(b *Book) error { b.IsPrimaryVersion = &f; return nil })
	require.NoError(t, err)
	want := s.IdentCounts()
	require.True(t, want.Ready)
	require.NotZero(t, want.All[IdentHasCandidates])
	require.NoError(t, s.Close())

	s2, err := NewPebbleStore(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s2.Close() })
	got := s2.IdentCounts()
	require.True(t, got.Ready, "a built index is served at open")
	require.Equal(t, want, got)
	requireNoIdentDrift(t, s2)
}

// ids0 returns the first book id of the store (key order), for tests that
// need any one book.
func (p *identPool) ids0(t testing.TB, s *PebbleStore) string {
	t.Helper()
	var first string
	require.NoError(t, forEachBookRow(s.db, func(id string, _ []byte) error {
		first = id
		return errStopScan
	}))
	return first
}

// TestIdentCreateBook_CallerSuppliedIDWithExistingCacheRow: a book created
// under an ID whose metadata_cache row already exists starts in the state
// that row gives it, not unasked.
func TestIdentCreateBook_CallerSuppliedIDWithExistingCacheRow(t *testing.T) {
	s := newIdentStore(t)
	const id = "01SYNTHCALLERSUPPLIED0001"
	require.NoError(t, s.PutMetadataCache(&MetadataCandidateCache{BookID: id, Candidates: identCands(2)}))
	_, err := s.CreateBook(&Book{ID: id, Title: "Title 000001", FilePath: "/lib/ident/caller.m4b", Format: "m4b"})
	require.NoError(t, err)
	r, ok, err := s.readIdentRow(id)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, IdentHasCandidates, r.State)
	require.Equal(t, int64(1), s.IdentCounts().Primary[IdentHasCandidates])
	requireNoIdentDrift(t, s)
}

// TestIdentRawBatch_StripesLockedOnceInOrder: a raw batch over two books on
// the SAME cache stripe takes it once (not twice, which would self-deadlock),
// and two batches naming two books on different stripes in opposite orders
// cannot deadlock (stripes are taken in ascending order).
func TestIdentRawBatch_StripesLockedOnceInOrder(t *testing.T) {
	s := newIdentStore(t)
	byStripe := map[int]string{}
	var same [2]string
	var diff [2]string
	for i := 0; same[1] == "" || diff[1] == ""; i++ {
		id := fmt.Sprintf("stripe%05d", i)
		st := s.metadataCacheStripe(id)
		if prev, ok := byStripe[st]; ok && same[1] == "" {
			same = [2]string{prev, id}
		}
		byStripe[st] = id
		if diff[0] == "" {
			diff[0] = id
		} else if diff[1] == "" && s.metadataCacheStripe(diff[0]) != st {
			diff[1] = id
		}
	}
	key := func(id string) string { return metadataCacheKeyPrefix + id }
	done := make(chan struct{})
	go func() {
		defer close(done)
		require.NoError(t, s.DeleteRawBatch([]string{key(same[0]), key(same[1])}))
		var wg sync.WaitGroup
		for g := range 2 {
			wg.Go(func() {
				a, b := diff[0], diff[1]
				if g == 1 {
					a, b = b, a
				}
				for range 300 {
					if err := s.DeleteRawBatch([]string{key(a), key(b)}); err != nil {
						t.Error(err)
						return
					}
				}
			})
		}
		wg.Wait()
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("raw batches deadlocked on the cache stripes")
	}
}

// TestIdentRebuild_RemovesOrphansAndBadKeys: pass 2 of the rebuild. A state
// row whose book is gone and membership keys that disagree with their state
// row are found by VerifyIdentIndex and removed by RebuildIdentIndex over a
// ready index, and the counters end equal to the recount.
func TestIdentRebuild_RemovesOrphansAndBadKeys(t *testing.T) {
	s := newIdentStore(t)
	pool := &identPool{}
	keep := identCreateBook(t, s, pool)
	// An orphan state row and its key (book never existed), a stray key in a
	// state the book is not in, and a key with the wrong scope byte.
	require.NoError(t, s.db.Set(identStateKey("gone0001"), []byte(`{"s":"unasked","p":true}`), nil))
	require.NoError(t, s.db.Set(identByKey(IdentUnasked, "gone0001"), []byte{'1'}, nil))
	require.NoError(t, s.db.Set(identByKey(IdentHeld, keep), []byte{'1'}, nil))
	require.NoError(t, s.db.Set(identByKey(IdentUnasked, keep), []byte{'0'}, nil))

	rep, err := s.VerifyIdentIndex(t.Context())
	require.NoError(t, err)
	require.Equal(t, 1, rep.OrphanRows)
	// The orphan's key agrees with its (orphan) state row; it goes with the
	// row. The stray state key and the wrong scope byte are bad.
	require.Equal(t, 2, rep.BadKeys)
	require.Equal(t, 1, rep.RowsWrong, "the kept book's key carries the wrong scope byte")
	require.NotZero(t, rep.CounterDrift)
	require.False(t, rep.Clean())

	st, err := s.RebuildIdentIndex(t.Context())
	require.NoError(t, err)
	require.False(t, st.FromScratch)
	require.Equal(t, 1, st.RowsRemoved)
	require.NotZero(t, st.KeysRemoved+st.RowsFixed)
	_, ok, err := s.readIdentRow("gone0001")
	require.NoError(t, err)
	require.False(t, ok)
	requireNoIdentDrift(t, s)
	requireCountsMatchOracle(t, s)
	rep, err = s.VerifyIdentIndex(t.Context())
	require.NoError(t, err)
	require.True(t, rep.Clean(), "%+v", rep)
}

// TestIdentRebuild_SupersededByReset: a rebuild overtaken by a Reset does not
// overwrite the Reset's empty, ready index with counters built over rows the
// Reset removed.
func TestIdentRebuild_SupersededByReset(t *testing.T) {
	s := newIdentStore(t)
	identCreateBook(t, s, &identPool{})
	gen := s.ident.gen.Load()
	require.NoError(t, s.Reset())
	_, err := s.identFinishRebuild(gen)
	require.ErrorIs(t, err, errIdentRebuildSuperseded)
	c := s.IdentCounts()
	require.True(t, c.Ready, "the Reset's own empty index stays in place")
	for _, st := range AllIdentStates {
		require.Zero(t, c.All[st])
	}
	require.True(t, s.IdentIndexReady())
}

// TestIdentRebuild_InvalidateDoesNotBumpResetGeneration: invalidateIdentIndex
// is the from-scratch rebuild's own step, taken under rebuildMu before it reads
// the generation, so it must not move the generation a Reset uses to
// supersede a running rebuild.
func TestIdentRebuild_InvalidateDoesNotBumpResetGeneration(t *testing.T) {
	s := newIdentStore(t)
	gen := s.ident.gen.Load()
	require.NoError(t, s.invalidateIdentIndex())
	require.Equal(t, gen, s.ident.gen.Load())
	require.False(t, s.IdentIndexReady())
	_, err := s.RebuildIdentIndex(t.Context())
	require.NoError(t, err)
	require.True(t, s.IdentIndexReady())
}

// TestSuperviseIdentIndex_RetriesFailedBuild: the boot build is retried
// after a failure.
func TestSuperviseIdentIndex_RetriesFailedBuild(t *testing.T) {
	s := newIdentStore(t)
	pool := &identPool{}
	for range 20 {
		pool.ids = append(pool.ids, identCreateBook(t, s, pool))
	}
	oldDelays := identRetryDelays
	identRetryDelays = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	var fails atomic.Int32
	fault := func(p *PebbleStore) error {
		if p == s && fails.Add(-1) >= 0 {
			return errors.New("injected commit failure")
		}
		return nil
	}
	identCommitFault.Store(&fault)

	wipeIdentRows(t, s)
	s.ident.ready.Store(false)
	fails.Store(2) // the first two builds fail on their first write
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { s.SuperviseIdentIndex(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		<-done
		identCommitFault.Store(nil)
		identRetryDelays = oldDelays
	})
	require.Eventually(t, s.IdentIndexReady, 10*time.Second, 5*time.Millisecond)
	requireCountsMatchOracle(t, s)
}

// TestIdentCounters_RecountWhileWriting runs the whole-index operations --
// a reconcile over the ready index (its final recount replaces the counters)
// and the drift check -- WHILE writers commit. identMu (read for writers,
// write for those) is what keeps a commit and its counter move on one side
// of every recount: without it a commit counted by the recount could have its
// move added on top afterwards, and the counters would stay off once the
// writers stop.
func TestIdentCounters_RecountWhileWriting(t *testing.T) {
	s := newIdentStore(t)
	pool := &identPool{}
	books, perG := 300, 150
	if raceEnabled {
		books, perG = 120, 60
	}
	for range books {
		pool.ids = append(pool.ids, identCreateBook(t, s, pool))
	}
	stop := make(chan struct{})
	var bg sync.WaitGroup
	bg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := s.RebuildIdentIndex(t.Context()); err != nil {
				t.Error(err)
				return
			}
			if _, err := s.IdentCountersDrift(); err != nil {
				t.Error(err)
				return
			}
		}
	})
	var wg sync.WaitGroup
	for g := range 6 {
		r := rand.New(rand.NewSource(int64(500 + g)))
		wg.Go(func() {
			for range perG {
				if err := identRandomOp(s, pool, r); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()
	close(stop)
	bg.Wait()
	requireNoIdentDrift(t, s)
	requireCountsMatchOracle(t, s)
}

// TestIdentCounters_RecountWaitsForPendingCounterMove pins the lock
// protocol deterministically: a write that has committed but not yet moved
// its counters holds identMu for read, so the drift check (identMu for
// write) waits for it instead of seeing the commit without its move.
func TestIdentCounters_RecountWaitsForPendingCounterMove(t *testing.T) {
	s := newIdentStore(t)
	id := identCreateBook(t, s, &identPool{})
	committed, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	hook := func(p *PebbleStore) {
		if p == s {
			once.Do(func() { close(committed); <-release })
		}
	}
	identBeforeCountMove.Store(&hook)
	t.Cleanup(func() { identBeforeCountMove.Store(nil) })

	errc := make(chan error, 1)
	go func() {
		errc <- s.PutMetadataCache(&MetadataCandidateCache{BookID: id, Candidates: identCands(1)})
	}()
	<-committed
	driftc := make(chan IdentDrift, 1)
	go func() {
		d, err := s.IdentCountersDrift()
		if err != nil {
			t.Error(err)
		}
		driftc <- d
	}()
	select {
	case d := <-driftc:
		close(release)
		t.Fatalf("the drift check ran while a committed write's counter move was pending: %v", d.Deltas)
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-errc)
	require.Zero(t, (<-driftc).Total())
	requireCountsMatchOracle(t, s)
}
