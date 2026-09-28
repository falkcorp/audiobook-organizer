// file: internal/database/book_change_observer_test.go
// version: 1.0.0
// guid: 9f2b6d41-3c85-4e17-b0a9-5d7c1e4a8b36
// last-edited: 2026-09-27

package database

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// Every committed book write — create, update (UpdateBook and ModifyBook both
// funnel through updateBookLockedMode), delete — reaches the observer, which
// is what lets the Library patch rows in place instead of reloading.
func TestBookChangeObserver_SeesCreateUpdateModifyDelete(t *testing.T) {
	s := setupTestPebbleStore(t)
	var mu sync.Mutex
	var seen []string
	SetBookChangeObserver(func(kind BookChangeKind, id string) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, string(kind)+":"+id)
	})
	t.Cleanup(func() { SetBookChangeObserver(nil) })

	b, err := s.CreateBook(&Book{Title: "Observed", FilePath: "/tmp/observed.m4b", Format: "m4b"})
	require.NoError(t, err)
	b.Title = "Observed 2"
	_, err = s.UpdateBook(b.ID, b)
	require.NoError(t, err)
	_, err = s.ModifyBook(b.ID, func(row *Book) error {
		row.Title = "Observed 3"
		return nil
	})
	require.NoError(t, err)
	require.NoError(t, s.DeleteBook(b.ID))

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{
		"created:" + b.ID, "updated:" + b.ID, "updated:" + b.ID, "deleted:" + b.ID,
	}, seen)
}
