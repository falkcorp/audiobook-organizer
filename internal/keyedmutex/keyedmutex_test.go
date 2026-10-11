// file: internal/keyedmutex/keyedmutex_test.go
// version: 1.0.0
// guid: 9d1f6b38-27ac-4e5a-b0c4-81e2d7a4f6c3
// last-edited: 2026-10-10

package keyedmutex

import (
	"testing"
	"time"
)

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
