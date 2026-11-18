// file: internal/maintenance/jobs/user_lock_helpers_test.go
// version: 1.0.0
// guid: 064b9a47-a175-4f9a-a2ff-0e1310ea7184
// last-edited: 2026-11-18

package jobs_test

import (
	"fmt"
	"strings"
	"sync"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// userLockFixture wires a MockStore's field-state read to a table: the keys
// listed for a book id are user-locked on it, and a book id in unreadable has
// a lock read that fails. Every book write that reaches the store is captured,
// so a test asserts on what was NOT written as well as what was.
type userLockFixture struct {
	mu         sync.Mutex
	locked     map[string][]string
	unreadable map[string]bool
	writes     map[string]database.Book
}

func newUserLockFixture() *userLockFixture {
	return &userLockFixture{
		locked:     map[string][]string{},
		unreadable: map[string]bool{},
		writes:     map[string]database.Book{},
	}
}

// install points the store's lock read and book write at the fixture.
func (f *userLockFixture) install(st *database.MockStore) {
	st.GetMetadataFieldStatesFunc = func(id string) ([]database.MetadataFieldState, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.unreadable[id] {
			return nil, fmt.Errorf("field states for %s: store closed", id)
		}
		rows := make([]database.MetadataFieldState, 0, len(f.locked[id]))
		for _, k := range f.locked[id] {
			rows = append(rows, database.MetadataFieldState{BookID: id, Field: k, OverrideLocked: true})
		}
		return rows, nil
	}
	st.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.writes[id] = *b
		return b, nil
	}
}

func (f *userLockFixture) wrote(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.writes[id]
	return ok
}

func (f *userLockFixture) written(id string) database.Book {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.writes[id]
}

// logged reports whether any captured report line contains every needle.
func logged(r *noopReporter, needles ...string) bool {
	for _, l := range r.logs {
		ok := true
		for _, n := range needles {
			if !strings.Contains(l, n) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}
