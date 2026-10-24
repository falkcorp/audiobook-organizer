// file: internal/plugins/maintenance/rewrite_path_prefix_book_unit_test.go
// version: 1.0.0
// guid: 3d1f6a52-8c7e-4b19-a4d0-6e2b9f50c7a3
// last-edited: 2026-10-24

package maintenance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// bookConcurrencyStore counts, per book, how many writes are in flight at once.
// Each write sleeps briefly so two workers that were given the same book would
// overlap; the fake's own mutex is taken AFTER the sleep, so it cannot hide the
// overlap by serialising the calls first.
type bookConcurrencyStore struct {
	*rewriteFakeStore

	trackMu  sync.Mutex
	inFlight map[string]int
	maxSeen  map[string]int
}

func (b *bookConcurrencyStore) enter(bookID string) func() {
	b.trackMu.Lock()
	b.inFlight[bookID]++
	b.maxSeen[bookID] = max(b.maxSeen[bookID], b.inFlight[bookID])
	b.trackMu.Unlock()
	time.Sleep(5 * time.Millisecond)
	return func() {
		b.trackMu.Lock()
		b.inFlight[bookID]--
		b.trackMu.Unlock()
	}
}

func (b *bookConcurrencyStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	defer b.enter(id)()
	return b.rewriteFakeStore.ModifyBook(id, fn)
}

func (b *bookConcurrencyStore) ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error) {
	defer b.enter(bookID)()
	return b.rewriteFakeStore.ModifyBookFile(bookID, fileID, fn)
}

// A holder's live unit and its retired unit share a BookID, and every write of
// either takes that book's stripe, so the write phase must hand both to ONE
// worker (live first). Planning them as independent items let a second worker
// land on the same book and interleave its aggregate recompute with the first.
func TestRewritePathPrefix_ALiveAndItsRetiredUnitNeverWriteTheSameBookConcurrently(t *testing.T) {
	root := t.TempDir()
	oldDir := filepath.Join(root, "old")
	newDir := filepath.Join(root, "new")

	const books = 24 // as many holders as the pool has workers, so every unit can start at once
	base := seedRewrite(t, oldDir)
	base.books, base.fileCores = nil, nil
	base.fullBooks, base.fullFiles = map[string]*database.Book{}, map[string]*database.BookFile{}
	base.atPath = map[string][]string{}
	for i := range books {
		id := fmt.Sprintf("b%02d", i)
		dir := filepath.Join(oldDir, id)
		makeTree(t, filepath.Join(newDir, id))
		require.NoError(t, os.WriteFile(filepath.Join(newDir, id, "02.mp3"), []byte("x"), 0o644))
		base.books = append(base.books, database.BookCore{ID: id, FilePath: dir})
		base.fullBooks[id] = &database.Book{ID: id, FilePath: dir}
		live := filepath.Join(dir, "01.mp3")
		base.fileCores = append(base.fileCores, database.BookFileCore{ID: id + "-live", BookID: id, FilePath: live})
		base.fullFiles[id+"-live"] = &database.BookFile{ID: id + "-live", BookID: id, FilePath: live}
		addRetiredRow(base, id, id+"-retired", filepath.Join(dir, "02.mp3"))
		base.atPath[dir] = []string{id}
	}
	store := &bookConcurrencyStore{rewriteFakeStore: base, inFlight: map[string]int{}, maxSeen: map[string]int{}}

	plan, err := planRewritePathPrefix(context.Background(), store, nil,
		rewriteParams(oldDir, newDir, false), &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, books, plan.BooksRewritten)
	require.Equal(t, books, plan.RetiredFilesRewritten, "every retired unit was written too")

	for id, n := range store.maxSeen {
		require.Equal(t, 1, n, "book %s: its live and retired units wrote concurrently", id)
	}
	require.Len(t, store.maxSeen, books)
}
