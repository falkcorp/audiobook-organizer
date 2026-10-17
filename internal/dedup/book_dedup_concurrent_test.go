// file: internal/dedup/book_dedup_concurrent_test.go
// version: 1.1.1
// guid: 9f2c7b41-6d38-4e05-a1b9-3c7e0d2f5a64
// last-edited: 2026-10-17

package dedup

import (
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// dedupSerializeProbe wraps a real database.Store and counts the maximum number
// of goroutines simultaneously inside the merge read-modify-write. It overrides
// the two methods the merge-family paths drive (GetBookByID / UpdateBook) and
// delegates the rest. A tiny sleep on entry widens
// the interleave window so that, if the two paths did NOT share one lock, their
// bodies would overlap and push maxActive to 2+. Sharing merge's package-level
// lock keeps at most one goroutine inside at a time -> maxActive == 1.
type dedupSerializeProbe struct {
	database.Store
	active    atomic.Int32
	maxActive atomic.Int32
}

func (p *dedupSerializeProbe) enter() {
	n := p.active.Add(1)
	for {
		m := p.maxActive.Load()
		if n <= m || p.maxActive.CompareAndSwap(m, n) {
			break
		}
	}
	time.Sleep(500 * time.Microsecond)
}

func (p *dedupSerializeProbe) leave() { p.active.Add(-1) }

func (p *dedupSerializeProbe) GetBookByID(id string) (*database.Book, error) {
	p.enter()
	defer p.leave()
	return p.Store.GetBookByID(id)
}

func (p *dedupSerializeProbe) UpdateBook(id string, b *database.Book) (*database.Book, error) {
	p.enter()
	defer p.leave()
	return p.Store.UpdateBook(id, b)
}

// ModifyBook is counted like UpdateBook: merge.SoftDeleteBook and the merge
// service's column writes go through it now, and a probe that let them bypass
// the counter would measure nothing.
func (p *dedupSerializeProbe) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	p.enter()
	defer p.leave()
	return p.Store.ModifyBook(id, fn)
}

func newConcurrentTestStore(t *testing.T) database.Store {
	t.Helper()
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "pebble"))
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	orig := database.GetGlobalStore()
	database.SetGlobalStore(store)
	t.Cleanup(func() {
		database.SetGlobalStore(orig)
		_ = store.Close()
	})
	return store
}
