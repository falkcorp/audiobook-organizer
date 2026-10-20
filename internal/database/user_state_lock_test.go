// file: internal/database/user_state_lock_test.go
// version: 1.1.1
// guid: dc3f20c5-76f7-46d2-af45-723d3651b19d
// last-edited: 2026-10-19

package database

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// collidingBooks returns two distinct book ids whose (user, book) keys hash to
// the same user-state stripe, and one that hashes to a different stripe.
func collidingBooks(t *testing.T, user string) (a, sameStripe, otherStripe string) {
	t.Helper()
	a = "book-a"
	sa := userStateStripe(user, a)
	for i := 0; i < 100000 && (sameStripe == "" || otherStripe == ""); i++ {
		id := fmt.Sprintf("book-%d", i)
		if id == a {
			continue
		}
		if userStateStripe(user, id) == sa {
			if sameStripe == "" {
				sameStripe = id
			}
		} else if otherStripe == "" {
			otherStripe = id
		}
	}
	if sameStripe == "" || otherStripe == "" {
		t.Fatal("no colliding / non-colliding book id found")
	}
	return a, sameStripe, otherStripe
}

// waitsWhileHeld fails unless lock() blocks while the caller holds its stripe.
func waitsWhileHeld(t *testing.T, lock func() func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		unlock := lock()
		unlock()
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("lock was taken while another holder had its stripe")
	case <-time.After(100 * time.Millisecond):
	}
}

// Two books on one stripe: the pair lock takes that stripe once (a second
// Lock would self-deadlock) and the stripe is held until the release.
func TestUserStateLockPair_SameStripeTakenOnce(t *testing.T) {
	a, b, _ := collidingBooks(t, "u")
	done := make(chan func(), 1)
	go func() { done <- LockUserBookStatePair("u", a, b) }()
	var unlock func()
	select {
	case unlock = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("LockUserBookStatePair deadlocked on two books sharing a stripe")
	}
	waitsWhileHeld(t, func() func() { return LockUserBookState("u", a) })
	unlock()
	LockUserBookState("u", a)() // released: takes at once
}

// Two books on different stripes: both are held, so a single-book writer on
// either waits, and both are released together.
func TestUserStateLockPair_HoldsBothStripes(t *testing.T) {
	a, _, c := collidingBooks(t, "u")
	unlock := LockUserBookStatePair("u", a, c)
	waitsWhileHeld(t, func() func() { return LockUserBookState("u", a) })
	waitsWhileHeld(t, func() func() { return LockUserBookState("u", c) })
	unlock()
	LockUserBookState("u", a)()
	LockUserBookState("u", c)()
}

// Pair holders that name the same two books in opposite orders, mixed with
// single-book holders on each, never deadlock: the pair lock orders its two
// stripes. Run under -race with a watchdog.
func TestUserStateLockPair_OppositeOrdersNoDeadlock(t *testing.T) {
	a, _, c := collidingBooks(t, "u")
	var wg sync.WaitGroup
	counter := 0 // guarded by the stripes: every goroutine holds a's stripe
	for i := 0; i < 8; i++ {
		wg.Add(4)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				u := LockUserBookStatePair("u", a, c)
				counter++
				u()
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				u := LockUserBookStatePair("u", c, a)
				counter++
				u()
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				u := LockUserBookState("u", a)
				counter++
				u()
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				LockUserBookState("u", c)()
			}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("pair and single user-state locks deadlocked")
	}
	if counter != 8*3*200 {
		t.Fatalf("counter = %d, want %d (a lost increment means two holders of a's stripe overlapped)", counter, 8*3*200)
	}
}

// LockUserBookStates over many keys, with duplicates and keys sharing a
// stripe, takes each distinct stripe once and holds every one.
func TestUserStateLocks_ManyKeysHeldOnceEach(t *testing.T) {
	a, same, other := collidingBooks(t, "u")
	keys := []UserBookKey{{"u", a}, {"u", same}, {"u", other}, {"u", a}, {"v", a}}
	done := make(chan func(), 1)
	go func() { done <- LockUserBookStates(keys...) }()
	var unlock func()
	select {
	case unlock = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("LockUserBookStates deadlocked on duplicate / shared-stripe keys")
	}
	for _, k := range keys {
		waitsWhileHeld(t, func() func() { return LockUserBookState(k.UserID, k.BookID) })
	}
	unlock()
	for _, k := range keys {
		LockUserBookState(k.UserID, k.BookID)()
	}
}
