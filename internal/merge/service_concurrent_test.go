// file: internal/merge/service_concurrent_test.go
// version: 1.1.0
// guid: 5c8a1f42-9d6b-4e73-8a10-2b4c6d9e0f13
// last-edited: 2026-07-13

package merge

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	ulid "github.com/oklog/ulid/v2"
)

// serializeProbe wraps a real database.Store and counts the maximum number of
// goroutines simultaneously executing inside MergeBooks' read-modify-write. It
// overrides only the two store methods MergeBooks drives repeatedly
// (GetBookByID / UpdateBook) and delegates everything else to the embedded
// store. A tiny sleep on entry widens the interleave window so that, WITHOUT
// the merge.Service mutex, two concurrent MergeBooks calls reliably overlap and
// push maxActive to 2+. WITH the mutex, MergeBooks bodies are fully serialized,
// so at most one goroutine is ever inside these methods at a time → maxActive
// stays 1. (A real PebbleStore is internally synchronized, so `-race` does NOT
// fire here; maxActive is the load-bearing serialization signal.)
type serializeProbe struct {
	database.Store
	active    int32
	maxActive int32
}

func (p *serializeProbe) enter() {
	n := atomic.AddInt32(&p.active, 1)
	for {
		m := atomic.LoadInt32(&p.maxActive)
		if n <= m || atomic.CompareAndSwapInt32(&p.maxActive, m, n) {
			break
		}
	}
	time.Sleep(500 * time.Microsecond)
}

func (p *serializeProbe) leave() { atomic.AddInt32(&p.active, -1) }

func (p *serializeProbe) GetBookByID(id string) (*database.Book, error) {
	p.enter()
	defer p.leave()
	return p.Store.GetBookByID(id)
}

func (p *serializeProbe) UpdateBook(id string, b *database.Book) (*database.Book, error) {
	p.enter()
	defer p.leave()
	return p.Store.UpdateBook(id, b)
}

// TestMergeBooks_ConcurrentSamePair_Serializes is the load-bearing regression
// test for the data-corruption fix. N goroutines merge the SAME pair {A, B} at
// once through one shared Service. With the mutex inside MergeBooks the calls
// serialize (maxActive == 1) and the version group stays consistent: A and B
// share ONE version group, the m4b winner is alive and primary, and no book is
// left both primary and soft-deleted. Without the mutex, interleaved writes can
// strand A and B in different ulids or leave a book both-primary-and-deleted —
// verified by reverting the mutex and observing maxActive >= 2 plus the group
// assertions failing.
func TestMergeBooks_ConcurrentSamePair_Serializes(t *testing.T) {
	real := setupTestStore(t)
	probe := &serializeProbe{Store: real}

	// B is m4b so BookIsBetter deterministically picks it as the winner,
	// regardless of goroutine scheduling.
	aID := ulid.Make().String()
	bID := ulid.Make().String()
	if _, err := real.CreateBook(&database.Book{ID: aID, Title: "Dup A", Format: "mp3", FilePath: "/tmp/a.mp3"}); err != nil {
		t.Fatalf("CreateBook A: %v", err)
	}
	if _, err := real.CreateBook(&database.Book{ID: bID, Title: "Dup B", Format: "m4b", FilePath: "/tmp/b.m4b"}); err != nil {
		t.Fatalf("CreateBook B: %v", err)
	}

	ms := NewService(probe)

	const goroutines = 16
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			// primaryID="" so every caller auto-picks via BookIsBetter — the
			// exact shape the concurrent dedup ops use.
			_, _ = ms.MergeBooks([]string{aID, bID}, "")
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&probe.maxActive); got != 1 {
		t.Fatalf("merges were NOT serialized: maxActive=%d, want 1 (two MergeBooks read-modify-writes overlapped)", got)
	}

	a, err := real.GetBookByID(aID)
	if err != nil || a == nil {
		t.Fatalf("GetBookByID A: %v", err)
	}
	b, err := real.GetBookByID(bID)
	if err != nil || b == nil {
		t.Fatalf("GetBookByID B: %v", err)
	}

	// Version group consistency: both books share ONE non-empty group.
	if a.VersionGroupID == nil || b.VersionGroupID == nil {
		t.Fatalf("version group not set on both sides: a=%v b=%v", a.VersionGroupID, b.VersionGroupID)
	}
	if *a.VersionGroupID == "" || *a.VersionGroupID != *b.VersionGroupID {
		t.Fatalf("version group inconsistent (books stranded across ulids): a=%q b=%q", *a.VersionGroupID, *b.VersionGroupID)
	}

	// No book may be simultaneously primary AND soft-deleted (the corruption).
	for _, bk := range []*database.Book{a, b} {
		primary := bk.IsPrimaryVersion != nil && *bk.IsPrimaryVersion
		deleted := bk.MarkedForDeletion != nil && *bk.MarkedForDeletion
		if primary && deleted {
			t.Fatalf("book %s is BOTH primary and soft-deleted — corrupt version group", bk.ID)
		}
	}

	// The deterministic winner (B, m4b) is alive and primary; the loser (A) is
	// soft-deleted. Exactly one live primary.
	if b.MarkedForDeletion != nil && *b.MarkedForDeletion {
		t.Fatalf("winner B was soft-deleted — an entire version group would vanish")
	}
	if b.IsPrimaryVersion == nil || !*b.IsPrimaryVersion {
		t.Fatalf("winner B is not marked primary")
	}
	if a.MarkedForDeletion == nil || !*a.MarkedForDeletion {
		t.Fatalf("loser A was not soft-deleted")
	}
}

// TestMergeFamily_CombineAndMerge_ShareOneLock proves CombineBooks serializes on
// the SAME lock as MergeBooks (the package-level mergeSerializeMu). Each
// goroutine operates on its OWN disjoint pair, so a maxActive>1 can only mean two
// read-modify-writes overlapped — i.e. the lock was NOT shared — not a data
// conflict. Even goroutines Combine, odd goroutines Merge; all go through one
// shared Service+probe. WITH the shared lock maxActive stays 1. Discriminates:
// reverting CombineBooks' mergeSerializeMu.Lock() lets a Combine overlap a Merge
// and maxActive reaches 2 (verified during development).
func TestMergeFamily_CombineAndMerge_ShareOneLock(t *testing.T) {
	real := setupTestStore(t)
	probe := &serializeProbe{Store: real}
	ms := NewService(probe)

	const goroutines = 16
	type pair struct{ a, b string }
	pairs := make([]pair, goroutines)
	for i := range pairs {
		a := ulid.Make().String()
		b := ulid.Make().String()
		// Unique file paths so combine's per-book file materialization never
		// collides across goroutines.
		if _, err := real.CreateBook(&database.Book{ID: a, Title: "A", Format: "mp3", FilePath: "/tmp/" + a + ".mp3"}); err != nil {
			t.Fatalf("CreateBook A: %v", err)
		}
		if _, err := real.CreateBook(&database.Book{ID: b, Title: "B", Format: "m4b", FilePath: "/tmp/" + b + ".m4b"}); err != nil {
			t.Fatalf("CreateBook B: %v", err)
		}
		pairs[i] = pair{a, b}
	}

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		i := i
		go func() {
			defer wg.Done()
			p := pairs[i]
			if i%2 == 0 {
				// Combine b into a (a survives, b absorbed + hard-deleted).
				_, _ = ms.CombineBooks([]string{p.a, p.b}, p.a, nil)
			} else {
				// Merge auto-picks the m4b winner (b); a is soft-deleted.
				_, _ = ms.MergeBooks([]string{p.a, p.b}, "")
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&probe.maxActive); got != 1 {
		t.Fatalf("Combine and Merge did NOT share one lock: maxActive=%d, want 1 "+
			"(a CombineBooks read-modify-write overlapped a MergeBooks)", got)
	}

	// Spot-check consistency of one pair of each kind.
	// pair[0] was combined: survivor a alive, absorbed b hard-deleted (gone).
	if a0, err := real.GetBookByID(pairs[0].a); err != nil || a0 == nil {
		t.Fatalf("combine survivor A missing: %v", err)
	}
	if b0, _ := real.GetBookByID(pairs[0].b); b0 != nil {
		t.Fatalf("combine absorbed B was not deleted")
	}
	// pair[1] was merged: both books share one non-empty version group.
	a1, _ := real.GetBookByID(pairs[1].a)
	b1, _ := real.GetBookByID(pairs[1].b)
	if a1 == nil || b1 == nil || a1.VersionGroupID == nil || b1.VersionGroupID == nil ||
		*a1.VersionGroupID == "" || *a1.VersionGroupID != *b1.VersionGroupID {
		t.Fatalf("merged pair not in one consistent version group: a=%+v b=%+v", a1, b1)
	}
}
