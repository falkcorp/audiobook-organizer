// file: internal/books/identity_store_mark_test.go
// version: 1.0.0
// guid: d26602cb-7784-4b9c-9480-1248bdbd2bce
// last-edited: 2026-11-28

package books

import (
	"errors"
	"testing"
)

type markedStore bool

func (m markedStore) UserDirectoryRequired() bool { return bool(m) }

// Register refuses a store built while the identity flip is on that did not
// mark itself as under core identity; with the flip off, or a marked store, it
// passes.
func TestRefuseUnmarkedStore(t *testing.T) {
	if err := refuseUnmarkedStore(true, markedStore(false)); !errors.Is(err, errStoreNotMarked) {
		t.Errorf("flip on, store unmarked: %v, want errStoreNotMarked", err)
	}
	if err := refuseUnmarkedStore(true, markedStore(true)); err != nil {
		t.Errorf("flip on, store marked: %v", err)
	}
	if err := refuseUnmarkedStore(false, markedStore(false)); err != nil {
		t.Errorf("flip off: %v", err)
	}
}
