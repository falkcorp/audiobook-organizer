// file: internal/database/ident_state_test.go
// version: 1.0.0
// guid: 325f377a-6217-41c5-9b34-7f92c945f8d9
// last-edited: 2026-10-11

package database

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"sync"
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

func TestDeriveIdentState_Table(t *testing.T) {
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
			s, r := DeriveIdentState(tc.book, tc.cache)
			require.Equal(t, tc.wantState, s)
			require.Equal(t, tc.wantReason, r)
		})
	}

	// Scope: a nil primary flag counts as primary (EffectiveIsPrimaryVersion,
	// the Library's own rule); trash and quarantine leave the scope.
	require.True(t, identInScope(&Book{}), "nil IsPrimaryVersion is primary")
	require.True(t, identInScope(&Book{IsPrimaryVersion: identBoolPtr(true)}))
	require.False(t, identInScope(&Book{IsPrimaryVersion: identBoolPtr(false)}))
	require.False(t, identInScope(&Book{MarkedForDeletion: identBoolPtr(true)}))
	require.False(t, identInScope(&Book{QuarantinedAt: &now}))
	require.False(t, identInScope(nil))
}

// identOracle derives every book's state from scratch (book row + cache row)
// and tallies it, independently of the index.
func identOracle(t testing.TB, s *PebbleStore) (all, primary map[IdentState]int64) {
	t.Helper()
	all, primary = map[IdentState]int64{}, map[IdentState]int64{}
	require.NoError(t, forEachBookRow(s.db, func(id string, value []byte) error {
		var b Book
		require.NoError(t, json.Unmarshal(value, &b))
		c, err := s.getMetadataCacheForIdent(id)
		require.NoError(t, err)
		st, _ := DeriveIdentState(&b, c)
		all[st]++
		if identInScope(&b) {
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
func TestIdentCounters_EqualRecount_Randomized(t *testing.T) {
	const (
		seeds      = 5
		books      = 2000
		rounds     = 10
		perRound   = 100
		goroutines = 4
	)
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

func TestIdentRebuild_IdempotentAndReportsNotReady(t *testing.T) {
	const books = 5000
	s := newIdentStore(t)
	pool := &identPool{}
	r := rand.New(rand.NewSource(7))
	for range books {
		id := identCreateBook(t, s, pool)
		pool.ids = append(pool.ids, id)
		switch r.Intn(5) {
		case 0:
			require.NoError(t, s.PutMetadataCache(&MetadataCandidateCache{BookID: id, Candidates: identCands(2)}))
		case 1:
			require.NoError(t, s.PutMetadataCache(&MetadataCandidateCache{BookID: id}))
		case 2:
			_, err := s.ModifyBook(id, func(b *Book) error { b.MetadataReviewStatus = identStrPtr("matched"); return nil })
			require.NoError(t, err)
		}
	}

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
	require.NoError(t, s.db.Set(identCountKey(identScopeAll, IdentApplied), encodeIdentCounter(1), nil))
	st, err = s.RebuildIdentIndex(t.Context())
	require.NoError(t, err)
	require.Equal(t, 1, st.RowsFixed)
	require.NotZero(t, st.CounterDrift)
	requireCountsMatchOracle(t, s)
	drift, err := s.IdentCountersDrift()
	require.NoError(t, err)
	require.Zero(t, drift.Total())

	// A wipe of the cache keyspace rebuilds the index from what is left.
	_, err = s.WipeByPrefixes([]string{metadataCacheKeyPrefix})
	require.NoError(t, err)
	require.True(t, s.IdentCounts().Ready)
	requireCountsMatchOracle(t, s)

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
