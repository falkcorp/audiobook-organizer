// file: internal/versionprimary/lock_groups_sentinel_test.go
// version: 1.1.0
// guid: 6f2b8d14-3a9c-4e57-b0d2-8c1e7a5f3b96
// last-edited: 2026-10-02

package versionprimary

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand"
	"runtime/pprof"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// collidingGroup returns a group id other than g that hashes onto g's stripe.
func collidingGroup(t *testing.T, g string) string {
	t.Helper()
	for i := 0; i < 100000; i++ {
		if c := fmt.Sprintf("collide-%d", i); c != g && groupStripe(c) == groupStripe(g) {
			return c
		}
	}
	t.Fatalf("no group collides with %q", g)
	return ""
}

// requireAllStripesFree fails when any stripe, the sentinel included, is
// still held.
func requireAllStripesFree(t *testing.T) {
	t.Helper()
	for i := range groupLocks {
		if !groupLocks[i].TryLock() {
			t.Fatalf("stripe %d is still held", i)
		}
		groupLocks[i].Unlock()
	}
}

// "" (and an all-whitespace id, as groupOfBook trims) is the no-group
// sentinel: the 65th stripe, past every hashed one, so it always sorts last.
func TestGroupStripe_EmptyIsLastSentinel(t *testing.T) {
	if len(groupLocks) != groupStripes+1 || noGroupStripe != groupStripes {
		t.Fatalf("want %d hashed stripes plus the sentinel at %d; have %d stripes, sentinel %d",
			groupStripes, groupStripes, len(groupLocks), noGroupStripe)
	}
	for _, g := range []string{"", " ", "\t \n"} {
		if got := groupStripe(g); got != noGroupStripe {
			t.Fatalf("groupStripe(%q) = %d, want the sentinel %d", g, got, noGroupStripe)
		}
	}
	for i := 0; i < 5000; i++ {
		g := fmt.Sprintf("vg-%d", i)
		if s := groupStripe(g); s < 0 || s >= noGroupStripe {
			t.Fatalf("groupStripe(%q) = %d, want a hashed stripe below the sentinel %d", g, s, noGroupStripe)
		}
	}
	if groupStripe(" g ") != groupStripe("g") {
		t.Fatal("a padded group id must share the trimmed id's stripe")
	}
}

// LockGroups("", g) takes the sentinel exactly once however many times "" (or
// whitespace) is passed, holds it until the release, and frees everything on
// release. A second Lock on the sentinel would hang the call.
func TestLockGroups_SentinelTakenOnce(t *testing.T) {
	done := make(chan func(), 1)
	go func() { done <- LockGroups("", "g", "", " ", "g") }()
	var unlock func()
	select {
	case unlock = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal(`LockGroups("", "g", "", " ") deadlocked on the sentinel`)
	}
	if groupLocks[noGroupStripe].TryLock() {
		groupLocks[noGroupStripe].Unlock()
		unlock()
		t.Fatal("the no-group sentinel is not held while LockGroups(\"\", ...) is")
	}
	if groupLocks[groupStripe("g")].TryLock() {
		groupLocks[groupStripe("g")].Unlock()
		unlock()
		t.Fatal("g's stripe is not held")
	}
	unlock()
	requireAllStripesFree(t)
}

// LockGroup("") is the sentinel too: it excludes LockGroups("", other).
func TestLockGroup_EmptyExcludesSentinelJoin(t *testing.T) {
	unlock := LockGroup("")
	got := make(chan struct{})
	go func() {
		LockGroups("", "joined")()
		close(got)
	}()
	select {
	case <-got:
		unlock()
		t.Fatal("a join-from-no-group took its locks while the sentinel was held")
	case <-time.After(200 * time.Millisecond):
	}
	unlock()
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("join still blocked after the sentinel was released")
	}
	requireAllStripesFree(t)
}

// flippingStore moves its books between groups on every few reads, as
// concurrent membership writers would, so LockBookGroups' re-read and retry
// path runs under contention.
type flippingStore struct {
	mu     sync.Mutex
	groups []string
	reads  int
}

func (s *flippingStore) GetBookByID(id string) (*database.Book, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	var g string
	if s.reads%3 == 0 {
		g = s.groups[rand.Intn(len(s.groups))]
	} else {
		g = s.groups[len(id)%len(s.groups)]
	}
	return &database.Book{ID: id, VersionGroupID: &g}, nil
}

// Many writers taking overlapping group sets -- two ids that share a stripe,
// the no-group sentinel, a whitespace id, in every argument order, through
// LockGroup, LockGroups and LockBookGroups -- never deadlock, and leave
// every stripe free.
func TestLockGroups_NoDeadlockAcrossOverlappingGroups(t *testing.T) {
	a := "group-a"
	b := collidingGroup(t, a)
	pool := []string{a, b, "group-c", "group-d", "", "  "}
	store := &flippingStore{groups: pool}

	var wg sync.WaitGroup
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			r := rand.New(rand.NewSource(int64(w)))
			for i := 0; i < 300; i++ {
				gids := make([]string, 1+r.Intn(4))
				for j := range gids {
					gids[j] = pool[r.Intn(len(pool))]
				}
				switch i % 3 {
				case 0:
					LockGroups(gids...)()
				case 1:
					LockGroup(gids[0])()
				default:
					unlock, _, err := LockBookGroups(store, []string{"x", "yy", "zzz"}, gids...)
					if err == nil {
						unlock()
					}
				}
			}
		}(w)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		var buf bytes.Buffer
		_ = pprof.Lookup("goroutine").WriteTo(&buf, 2)
		t.Fatalf("group lockers deadlocked:\n%s", buf.String())
	}
	requireAllStripesFree(t)
}

// mapStore is a BookReader over fixed groups ("-" = missing book).
type mapStore map[string]string

func (m mapStore) GetBookByID(id string) (*database.Book, error) {
	g, ok := m[id]
	if !ok || g == "-" {
		return nil, nil
	}
	return &database.Book{ID: id, VersionGroupID: &g}, nil
}

// LockPlannedGroups refuses a book whose group is not the one its caller
// planned from with ErrMembershipChanged and releases every lock; a padded
// planned id matches its trimmed stored one; a missing book is skipped, as
// LockBookGroups skips it (the caller's write reports it).
func TestLockPlannedGroups_RefusesAMovedBook(t *testing.T) {
	store := mapStore{"a": "g", "b": "h", "gone": "-"}
	cases := []struct {
		name    string
		planned map[string]string
		wantErr bool
	}{
		{"as planned", map[string]string{"a": "g", "b": " h "}, false},
		{"moved between plan and lock", map[string]string{"a": "g", "b": "g"}, true},
		{"moved out of no group", map[string]string{"a": ""}, true},
		{"vanished is skipped", map[string]string{"a": "g", "gone": "g"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			unlock, groups, err := LockPlannedGroups(store, []string{"a"}, tc.planned, "dest")
			if tc.wantErr {
				if !errors.Is(err, ErrMembershipChanged) {
					t.Fatalf("err = %v, want ErrMembershipChanged", err)
				}
				requireAllStripesFree(t)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, planB := tc.planned["b"]; groups["a"] != "g" || (planB && groups["b"] != "h") {
				t.Fatalf("groups = %v", groups)
			}
			unlock()
			requireAllStripesFree(t)
		})
	}
}
