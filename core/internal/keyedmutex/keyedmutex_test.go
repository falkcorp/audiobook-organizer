// file: core/internal/keyedmutex/keyedmutex_test.go
// version: 1.0.0
// guid: e1a4bd16-a05e-4c2a-9800-f34ee2e6fbfc
// last-edited: 2026-11-10

package keyedmutex

import (
	"sync"
	"testing"
	"time"
)

// TestKeyedMutex_SerializesPerKey (a test name coined by F3-03): one key's
// holders never overlap, while a held key never blocks another key.
func TestKeyedMutex_SerializesPerKey(t *testing.T) {
	var k Mutex
	var (
		mu     sync.Mutex
		inside = map[string]int{}
		worst  = map[string]int{}
	)
	enter := func(key string) {
		mu.Lock()
		inside[key]++
		worst[key] = max(worst[key], inside[key])
		mu.Unlock()
	}
	leave := func(key string) {
		mu.Lock()
		inside[key]--
		mu.Unlock()
	}
	var wg sync.WaitGroup
	for i := range 64 {
		key := []string{"batcave", "arkham"}[i%2]
		wg.Go(func() {
			unlock := k.Lock(key)
			enter(key)
			time.Sleep(time.Millisecond)
			leave(key)
			unlock()
		})
	}
	wg.Wait()
	for _, key := range []string{"batcave", "arkham"} {
		if worst[key] != 1 {
			t.Errorf("key %s had %d holders at once, want 1", key, worst[key])
		}
	}

	// A held key does not block another one.
	unlock := k.Lock("batcave")
	done := make(chan struct{})
	go func() {
		k.Lock("arkham")()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a held key blocked a different key")
	}
	unlock()
}

func TestLock_SameKeySerializesOtherKeysDoNot(t *testing.T) {
	var k Mutex
	unlockA := k.Lock("a")

	other := make(chan struct{})
	go func() {
		k.Lock("b")()
		close(other)
	}()
	select {
	case <-other:
	case <-time.After(2 * time.Second):
		t.Fatal("a different key was blocked by a held key")
	}

	same := make(chan struct{})
	go func() {
		k.Lock("a")()
		close(same)
	}()
	select {
	case <-same:
		t.Fatal("the same key was acquired while held")
	case <-time.After(50 * time.Millisecond):
	}
	unlockA()
	select {
	case <-same:
	case <-time.After(2 * time.Second):
		t.Fatal("the waiter never got the key after release")
	}
}

func TestLock_EntriesAreDroppedWhenFree(t *testing.T) {
	var k Mutex
	k.Lock("a")()
	k.Lock("b")()
	k.mu.Lock()
	n := len(k.locks)
	k.mu.Unlock()
	if n != 0 {
		t.Errorf("want no entries once every key is released, got %d", n)
	}
}
