// file: internal/searchcache/changelog_limit_test.go
// version: 1.0.0
// guid: 4b7e1d92-6c3a-4f58-9a20-d8e5b1c7f403
// last-edited: 2026-09-26

package searchcache

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
	"time"
)

// referenceChangedSince is ChangedSince as it was before it took a limit and
// copied the window out of the lock: a linear walk of every live record under
// the mutex. The randomized test below holds the new code to its answers.
func referenceChangedSince(c *ChangeLog, since uint64) (ids []string, current uint64, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	current = c.gen.Load()
	if since >= current {
		return nil, current, true
	}
	if since < c.floor {
		return nil, current, false
	}
	seen := make(map[string]struct{})
	for i := 0; i < c.size; i++ {
		r := c.ring[(c.head+i)%len(c.ring)]
		if r.gen <= since {
			continue
		}
		if _, dup := seen[r.id]; dup {
			continue
		}
		seen[r.id] = struct{}{}
		ids = append(ids, r.id)
	}
	return ids, current, true
}

// With no limit, ChangedSince answers exactly as the old linear walk did, for
// every generation, across ring wrap-around, eviction, multi-ID records and
// RecordAll. With a limit it answers with the first limit IDs of that answer.
func TestChangedSince_MatchesReferenceWalk(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for _, ringSize := range []int{1, 2, 7, 16, 64} {
		cl := NewChangeLog(ringSize)
		for step := range 400 {
			switch r := rng.IntN(100); {
			case r < 2:
				cl.RecordAll()
			case r < 5:
				cl.Record("", "") // no usable IDs: a no-op
			default:
				n := 1 + rng.IntN(3)
				batch := make([]string, n)
				for i := range batch {
					batch[i] = fmt.Sprintf("b%d", rng.IntN(12))
				}
				cl.Record(batch...)
			}
			cur := cl.Generation()
			for since := uint64(0); since <= cur+1; since++ {
				want, wantCur, wantOK := referenceChangedSince(cl, since)
				for _, limit := range []int{0, -1, 1, 2, 3, 5, 100} {
					got, gotCur, gotOK := cl.ChangedSince(since, limit)
					exp := want
					if limit > 0 && len(exp) > limit {
						exp = exp[:limit]
					}
					if gotOK != wantOK || gotCur != wantCur || !slices.Equal(got, exp) {
						t.Fatalf("ring=%d step=%d since=%d limit=%d: got (%v, %d, %v), want (%v, %d, %v)",
							ringSize, step, since, limit, got, gotCur, gotOK, exp, wantCur, wantOK)
					}
				}
			}
		}
	}
}

// A window that wraps past the end of the ring slice (head != 0) comes back
// whole and in record order.
func TestChangedSince_WindowWrapsRing(t *testing.T) {
	cl := NewChangeLog(4)
	for _, id := range []string{"a", "b", "c", "d", "e", "f"} {
		cl.Record(id)
	}
	cl.mu.Lock()
	head := cl.head
	cl.mu.Unlock()
	if head == 0 {
		t.Fatal("test setup: ring did not wrap")
	}
	// Generations 1..6; the ring holds c(3) d(4) e(5) f(6), floor 2.
	ids, cur, ok := cl.ChangedSince(2, 0)
	if !ok || cur != 6 || !slices.Equal(ids, []string{"c", "d", "e", "f"}) {
		t.Fatalf("ChangedSince(2) = %v, %d, %v; want [c d e f], 6, true", ids, cur, ok)
	}
	if ids, _, _ := cl.ChangedSince(4, 0); !slices.Equal(ids, []string{"e", "f"}) {
		t.Fatalf("ChangedSince(4) = %v; want [e f]", ids)
	}
	if ids, _, _ := cl.ChangedSince(2, 3); !slices.Equal(ids, []string{"c", "d", "e"}) {
		t.Fatalf("ChangedSince(2, limit 3) = %v; want [c d e]", ids)
	}
	if _, _, ok := cl.ChangedSince(1, 0); ok {
		t.Fatal("ChangedSince(1) reported ok for an evicted generation")
	}
}

// The limit counts DISTINCT IDs: repeats of one book do not use it up.
func TestChangedSince_LimitCountsDistinctIDs(t *testing.T) {
	cl := NewChangeLog(64)
	for range 10 {
		cl.Record("a")
	}
	cl.Record("b")
	cl.Record("c")
	if ids, _, _ := cl.ChangedSince(0, 2); !slices.Equal(ids, []string{"a", "b"}) {
		t.Fatalf("ChangedSince(0, limit 2) = %v; want [a b]", ids)
	}
	if ids, _, _ := cl.ChangedSince(0, 3); !slices.Equal(ids, []string{"a", "b", "c"}) {
		t.Fatalf("ChangedSince(0, limit 3) = %v; want [a b c]", ids)
	}
}

// Through the cache: exactly MaxPatchChanged changed books still patch; one
// more stops the walk at the limit and is a patch-cap rebuild, counted once.
func TestCache_PatchCapBoundaryWithLimitedChangedSince(t *testing.T) {
	const maxChanged = 8
	changes := NewChangeLog(256)
	c := New(changes, Config{PatchLimit: 1, MaxPatchChanged: maxChanged})
	fe := newFake(map[string]string{"b1": "red"}, "red")
	ctx := context.Background()
	if _, err := c.Lookup(ctx, "k", fe, exactOpts); err != nil {
		t.Fatal(err)
	}

	atCap := make([]string, maxChanged)
	for i := range atCap {
		atCap[i] = fmt.Sprintf("x%d", i)
	}
	// Each written several times, so the ring holds far more records than
	// distinct books: the limit is on books, not records.
	for range 5 {
		changes.Record(atCap...)
	}
	before := c.Stats()
	if _, err := c.Lookup(ctx, "k", fe, exactOpts); err != nil {
		t.Fatal(err)
	}
	after := c.Stats()
	if after.Patches != before.Patches+1 || after.PatchCapRebuilds != before.PatchCapRebuilds {
		t.Fatalf("%d changed books (== MaxPatchChanged): stats before %+v after %+v; want one patch, no cap rebuild",
			maxChanged, before, after)
	}

	overCap := make([]string, maxChanged+1)
	for i := range overCap {
		overCap[i] = fmt.Sprintf("y%d", i)
	}
	for range 5 {
		changes.Record(overCap...)
	}
	if _, err := c.Lookup(ctx, "k", fe, webOpts(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return c.Stats().Building == 0 })
	final := c.Stats()
	if final.PatchCapRebuilds != after.PatchCapRebuilds+1 {
		t.Fatalf("%d changed books (> MaxPatchChanged): PatchCapRebuilds %d -> %d; want +1",
			maxChanged+1, after.PatchCapRebuilds, final.PatchCapRebuilds)
	}
	if final.Patches != after.Patches {
		t.Fatalf("%d changed books (> MaxPatchChanged) patched; want a rebuild", maxChanged+1)
	}
}
