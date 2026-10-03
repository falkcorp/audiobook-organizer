// file: internal/database/ulid_order_test.go
// version: 1.0.0
// guid: 31364789-bcf5-4d20-bf45-120283862710
// last-edited: 2026-10-03

package database

import (
	"crypto/rand"
	"sync"
	"testing"
	"time"

	ulid "github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"
)

// Record ids must sort in the order they were handed out, even inside one
// millisecond. Search rank and the paged listings break ties on the id, so an
// unordered pair is a pair of rows whose order changes from run to run: that
// is how TestSearchBooksFiltered_LimitCountsAdmittedHitsOnly failed in CI on
// 2026-10-03 with two books created in the same millisecond. A tight loop puts
// thousands of ids in each millisecond; before the fix about half of all
// consecutive pairs came out reversed.
func TestNewULID_SortsInCreationOrder(t *testing.T) {
	prev, err := newULID()
	require.NoError(t, err)
	for i := 0; i < 100000; i++ {
		id, err := newULID()
		require.NoError(t, err)
		require.Greater(t, id, prev, "id %d does not sort after the one before it", i)
		prev = id
	}
}

// Concurrent callers share the one source: every id is unique and each
// goroutine sees its own ids strictly increasing.
func TestNewULID_ConcurrentCallersStayOrderedAndUnique(t *testing.T) {
	const workers, perWorker = 8, 5000
	out := make([][]string, workers)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ids := make([]string, 0, perWorker)
			for i := 0; i < perWorker; i++ {
				id, err := newULID()
				if err != nil {
					t.Errorf("newULID: %v", err)
					return
				}
				ids = append(ids, id)
			}
			out[w] = ids
		}()
	}
	wg.Wait()
	seen := make(map[string]struct{}, workers*perWorker)
	for w, ids := range out {
		require.Len(t, ids, perWorker)
		for i, id := range ids {
			if i > 0 {
				require.Greater(t, id, ids[i-1], "worker %d id %d", w, i)
			}
			_, dup := seen[id]
			require.False(t, dup, "duplicate id %s", id)
			seen[id] = struct{}{}
		}
	}
}

// A wall clock that stands still or steps backwards must not hand a later
// caller a smaller id.
func TestOrderedULIDSource_ClockStandingStillOrSteppingBack(t *testing.T) {
	base := time.UnixMilli(1_800_000_000_000)
	clock := base
	src := newOrderedULIDSource(func() time.Time { return clock }, rand.Reader)

	prev, err := src.next()
	require.NoError(t, err)
	for i, step := range []time.Duration{0, 0, -5 * time.Second, 0, time.Millisecond, -time.Hour, 0} {
		clock = clock.Add(step)
		id, err := src.next()
		require.NoError(t, err)
		require.Greater(t, id, prev, "step %d (%v)", i, step)
		prev = id
	}
}

// scriptedEntropy returns 0xFF for its first ten bytes and 0x01 after that, so
// the first id of a millisecond gets the largest possible entropy and the
// second one overflows it.
type scriptedEntropy struct{ served int }

func (s *scriptedEntropy) Read(p []byte) (int, error) {
	for i := range p {
		if s.served < 10 {
			p[i] = 0xFF
		} else {
			p[i] = 0x01
		}
		s.served++
	}
	return len(p), nil
}

// When the entropy overflows inside a millisecond the source moves to the next
// millisecond instead of failing the write, and stays there while the clock
// catches up.
func TestOrderedULIDSource_EntropyOverflowBorrowsTheNextMillisecond(t *testing.T) {
	frozen := time.UnixMilli(1_800_000_000_000)
	src := newOrderedULIDSource(func() time.Time { return frozen }, &scriptedEntropy{})

	first, err := src.next()
	require.NoError(t, err)
	second, err := src.next()
	require.NoError(t, err, "an overflow must not surface as a failed create")
	third, err := src.next()
	require.NoError(t, err)
	require.Greater(t, second, first)
	require.Greater(t, third, second)

	parsedFirst, err := ulid.ParseStrict(first)
	require.NoError(t, err)
	parsedSecond, err := ulid.ParseStrict(second)
	require.NoError(t, err)
	parsedThird, err := ulid.ParseStrict(third)
	require.NoError(t, err)
	require.Equal(t, parsedFirst.Time()+1, parsedSecond.Time())
	require.Equal(t, parsedSecond.Time(), parsedThird.Time(), "the clock is still behind: no going back")
}

// A session id is the session cookie's value. Ordered ids are the previous id
// plus a step of at most 32 bits inside a millisecond, which would let the
// holder of one session id search for its neighbours, so session ids come from
// newUnlinkedULID: two in the same millisecond share nothing. With ordered ids
// the top 48 entropy bits of same-millisecond neighbours are equal (barring a
// carry); with independent ids they are equal once in 2^48.
func TestNewUnlinkedULID_SameMillisecondIDsShareNoEntropy(t *testing.T) {
	sameMS := 0
	prev := ulid.ULID{}
	for i := 0; i < 1_000_000 && sameMS < 1000; i++ {
		s, err := newUnlinkedULID()
		require.NoError(t, err)
		id, err := ulid.ParseStrict(s)
		require.NoError(t, err)
		if i > 0 && id.Time() == prev.Time() {
			sameMS++
			require.NotEqual(t, prev.Entropy()[:6], id.Entropy()[:6], "ids %s and %s are linked", prev, id)
		}
		prev = id
	}
	require.Equal(t, 1000, sameMS, "the loop never produced same-millisecond pairs, so it checked nothing")
}

// CreateSession must use the unlinked generator.
func TestCreateSession_IDsAreNotLinkedToEachOther(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	// The ordered source is idle for the whole loop, so if CreateSession drew
	// from it, any two sessions in one millisecond would share their top
	// entropy bits. Sandwich each session between two ordered ids instead of
	// relying on two session writes landing in one millisecond.
	for i := 0; i < 20; i++ {
		before, err := newULID()
		require.NoError(t, err)
		sess, err := store.CreateSession("user-1", "192.0.2.1", "test", time.Hour)
		require.NoError(t, err)
		b, err := ulid.ParseStrict(before)
		require.NoError(t, err)
		s, err := ulid.ParseStrict(sess.ID)
		require.NoError(t, err)
		if b.Time() == s.Time() {
			require.NotEqual(t, b.Entropy()[:6], s.Entropy()[:6], "session id %s continues the ordered sequence", s)
		}
	}
}
