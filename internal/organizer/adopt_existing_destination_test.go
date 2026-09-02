// file: internal/organizer/adopt_existing_destination_test.go
// version: 1.0.0
// guid: 5c1d8e2a-7f94-4b36-a0d5-e83b6f2c9a17
// last-edited: 2026-09-02

package organizer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// adoptExistingDestination is the one adoption test shared by the pre-copy
// check and the race-recovery branch. Each case below pins one row of its
// decision table directly, so a regression in either caller's wiring and a
// regression in the table itself fail different tests.
func TestAdoptExistingDestination_DecisionTable(t *testing.T) {
	book := &database.Book{ID: "b", Title: "T"}
	dir := t.TempDir()
	src := filepath.Join(dir, "src.m4b")
	require.NoError(t, os.WriteFile(src, []byte("payload-bytes"), 0644))

	stat := func(p string) os.FileInfo {
		fi, err := os.Stat(p)
		require.NoError(t, err)
		return fi
	}

	t.Run("same inode (hardlink re-organize) is adopted", func(t *testing.T) {
		dst := filepath.Join(dir, "linked.m4b")
		require.NoError(t, os.Link(src, dst))
		assert.True(t, adoptExistingDestination(book, src, dst, stat(dst)))
	})

	t.Run("different inode, identical bytes (interrupted copy) is adopted", func(t *testing.T) {
		dst := filepath.Join(dir, "copied.m4b")
		require.NoError(t, os.WriteFile(dst, []byte("payload-bytes"), 0644))
		require.False(t, os.SameFile(stat(src), stat(dst)))
		assert.True(t, adoptExistingDestination(book, src, dst, stat(dst)))
	})

	t.Run("same size, different bytes is NOT adopted", func(t *testing.T) {
		dst := filepath.Join(dir, "other.m4b")
		require.NoError(t, os.WriteFile(dst, []byte("another-book!"), 0644))
		require.Equal(t, stat(src).Size(), stat(dst).Size(), "fixture must be size-equal or the size pre-filter decides, not the hash")
		assert.False(t, adoptExistingDestination(book, src, dst, stat(dst)))
	})

	t.Run("different size is NOT adopted", func(t *testing.T) {
		dst := filepath.Join(dir, "short.m4b")
		require.NoError(t, os.WriteFile(dst, []byte("x"), 0644))
		assert.False(t, adoptExistingDestination(book, src, dst, stat(dst)))
	})

	t.Run("source unreadable is NOT adopted (fails closed)", func(t *testing.T) {
		dst := filepath.Join(dir, "orphan.m4b")
		require.NoError(t, os.WriteFile(dst, []byte("payload-bytes"), 0644))
		assert.False(t, adoptExistingDestination(book, filepath.Join(dir, "missing.m4b"), dst, stat(dst)))
	})
}
