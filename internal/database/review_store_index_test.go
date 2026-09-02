// file: internal/database/review_store_index_test.go
// version: 1.0.0
// guid: 5c1e9a27-7d43-4b8f-a6e2-3f9d0b7c8e15
// last-edited: 2026-09-02

package database

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/cockroachdb/pebble/v2"
)

// reviewStatusIndexRows reads the review_item:status:* index RAW, as
// (status, id) pairs, bypassing the list/count paths. Those paths re-check the
// record's status on every hit, which HIDES a stale row: the invariant below is
// about the index itself, so it has to look at the keys.
func reviewStatusIndexRows(t *testing.T, s *PebbleStore) map[string][]string {
	t.Helper()
	prefix := []byte(reviewItemStatusPfx)
	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: prefixUpperBound(prefix)})
	if err != nil {
		t.Fatalf("index iter: %v", err)
	}
	defer iter.Close()
	byID := map[string][]string{} // id → every status it is indexed under
	for iter.First(); iter.Valid(); iter.Next() {
		rest := string(iter.Key()[len(prefix):])
		sep := strings.LastIndex(rest, ":")
		if sep < 0 {
			t.Fatalf("malformed index key %q", iter.Key())
		}
		byID[rest[sep+1:]] = append(byID[rest[sep+1:]], rest[:sep])
	}
	return byID
}

// assertStatusIndexConsistent is THE invariant: every index row names an item
// whose stored status is that row's status, and every item appears under exactly
// one status.
func assertStatusIndexConsistent(t *testing.T, s *PebbleStore) {
	t.Helper()
	byID := reviewStatusIndexRows(t, s)
	items, err := s.listAllReviewItems()
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	stored := map[string]string{}
	for _, it := range items {
		stored[it.ID] = it.Status
	}
	for id, statuses := range byID {
		want, ok := stored[id]
		if !ok {
			t.Errorf("index names item %s under %v but no such record exists", id, statuses)
			continue
		}
		if len(statuses) != 1 {
			t.Errorf("item %s (stored %q) is indexed under %d statuses: %v", id, want, len(statuses), statuses)
			continue
		}
		if statuses[0] != want {
			t.Errorf("item %s is stored as %q but indexed under %q", id, want, statuses[0])
		}
	}
	for id, want := range stored {
		if _, ok := byID[id]; !ok {
			t.Errorf("item %s (stored %q) has no index row at all", id, want)
		}
	}
}

// 🔴 CONCURRENT DECISIONS MUST LEAVE THE STATUS INDEX EXACT.
//
// N goroutines each decide EVERY item, each with its own status, in the same
// order at the same time. Without reviewMu in SetReviewItemDecision two of them
// read the item as pending, both delete the pending row, and each writes its own
// status row — the record ends up with the last writer's status while the index
// lists the item under two. Verified by removing the lock: this test then fails
// with "indexed under 2 statuses".
func TestSetReviewItemDecision_ConcurrentDecisionsKeepStatusIndexExact(t *testing.T) {
	s := newReviewTestStore(t)

	const items = 24
	ids := make([]string, 0, items)
	for i := range items {
		it, err := s.UpsertReviewItem(mkReviewItem("regroup.multidisc", fmt.Sprintf("dk-%03d", i), "/f", "s", `{}`))
		if err != nil {
			t.Fatalf("upsert %d: %v", i, err)
		}
		ids = append(ids, it.ID)
	}

	// Distinct statuses per goroutine so a double-index is visible as two
	// DIFFERENT rows; the same status from both writers would collide on one key
	// and hide the race.
	statuses := []string{ReviewStatusApproved, ReviewStatusRejected, ReviewStatusApplied, "on-hold", "escalated"}
	const rounds = 5
	for round := range rounds {
		var start, done sync.WaitGroup
		start.Add(1)
		for w, status := range statuses {
			done.Add(1)
			go func(w int, status string) {
				defer done.Done()
				start.Wait()
				for _, id := range ids {
					if _, err := s.SetReviewItemDecision(id, status, ""); err != nil {
						t.Errorf("round %d worker %d: decide %s: %v", round, w, id, err)
					}
				}
			}(w, status)
		}
		start.Done()
		done.Wait()
		assertStatusIndexConsistent(t, s)
		if t.Failed() {
			t.FailNow()
		}
		// Reset to pending through the same writer so the next round races
		// from the same starting state.
		for _, id := range ids {
			if _, err := s.SetReviewItemDecision(id, ReviewStatusPending, ""); err != nil {
				t.Fatalf("reset %s: %v", id, err)
			}
		}
	}
}

// corruptStatusIndex plants exactly the damage the unlocked writer produced:
// a second index row for a live item under a status it does not have, a row for
// an item that no longer exists, and a live item with NO row under its status.
func corruptStatusIndex(t *testing.T, s *PebbleStore) (doubled, ghost, unindexed string) {
	t.Helper()
	mk := func(k string) ReviewItem {
		it, err := s.UpsertReviewItem(mkReviewItem("regroup.multidisc", k, "/f/"+k, "s", `{}`))
		if err != nil {
			t.Fatalf("upsert %s: %v", k, err)
		}
		return it
	}
	a, g, u := mk("doubled"), mk("ghost"), mk("unindexed")
	mk("healthy")

	// a: stored pending, ALSO indexed under approved.
	if err := s.db.Set(reviewItemStatusKey(ReviewStatusApproved, a.ID), nil, pebble.Sync); err != nil {
		t.Fatal(err)
	}
	// g: record deleted out from under its index row.
	if err := s.db.Delete(reviewItemRecKey(g.ID), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	// u: index row deleted out from under a live record.
	if err := s.db.Delete(reviewItemStatusKey(ReviewStatusPending, u.ID), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	return a.ID, g.ID, u.ID
}

func TestRebuildReviewStatusIndex_DryRunCountsAndChangesNothing(t *testing.T) {
	s := newReviewTestStore(t)
	corruptStatusIndex(t, s)
	before := reviewStatusIndexRows(t, s)

	res, err := s.RebuildReviewStatusIndex(false)
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if res.Applied {
		t.Fatal("dry run reported Applied=true")
	}
	if res.StaleIndexEntries != 2 {
		t.Errorf("stale = %d, want 2 (the doubled row and the ghost's row)", res.StaleIndexEntries)
	}
	if res.MissingIndexEntries != 1 {
		t.Errorf("missing = %d, want 1 (the unindexed item)", res.MissingIndexEntries)
	}
	if res.ItemsScanned != 3 {
		t.Errorf("items scanned = %d, want 3 (the ghost's record is gone)", res.ItemsScanned)
	}
	if res.IndexEntriesScanned != 4 {
		t.Errorf("index rows scanned = %d, want 4", res.IndexEntriesScanned)
	}

	after := reviewStatusIndexRows(t, s)
	if fmt.Sprint(before) != fmt.Sprint(after) {
		t.Fatalf("a dry run changed the index:\n before %v\n after  %v", before, after)
	}
}

func TestRebuildReviewStatusIndex_ApplyRepairsAndIsIdempotent(t *testing.T) {
	s := newReviewTestStore(t)
	doubled, ghost, unindexed := corruptStatusIndex(t, s)

	res, err := s.RebuildReviewStatusIndex(true)
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if !res.Applied || res.StaleIndexEntries != 2 || res.MissingIndexEntries != 1 {
		t.Fatalf("apply result = %+v, want applied with 2 stale / 1 missing", res)
	}
	assertStatusIndexConsistent(t, s)

	byID := reviewStatusIndexRows(t, s)
	if got := byID[doubled]; len(got) != 1 || got[0] != ReviewStatusPending {
		t.Errorf("doubled item indexed under %v, want [pending]", got)
	}
	if got, ok := byID[ghost]; ok {
		t.Errorf("ghost item still indexed under %v", got)
	}
	if got := byID[unindexed]; len(got) != 1 || got[0] != ReviewStatusPending {
		t.Errorf("unindexed item indexed under %v, want [pending]", got)
	}
	// The visible read paths agree with the raw index once it is repaired.
	if n, _ := s.CountReviewItems(ReviewStatusPending); n != 3 {
		t.Errorf("pending count = %d, want 3", n)
	}
	if n, _ := s.CountReviewItems(ReviewStatusApproved); n != 0 {
		t.Errorf("approved count = %d, want 0", n)
	}

	// A second pass finds nothing — the repair converged.
	again, err := s.RebuildReviewStatusIndex(true)
	if err != nil {
		t.Fatalf("second rebuild: %v", err)
	}
	if again.StaleIndexEntries != 0 || again.MissingIndexEntries != 0 || again.Applied {
		t.Fatalf("second pass = %+v, want 0 stale / 0 missing / not applied", again)
	}
}
