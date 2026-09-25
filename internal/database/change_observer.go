// file: internal/database/change_observer.go
// version: 1.0.0
// guid: 53e053ef-f45a-4f3a-9ddd-ae18779c3d50
// last-edited: 2026-09-25

package database

// ChangeObserver is told about every PebbleStore write that can change a
// search result. It exists because the server's indexedStore decorator only
// sees the book writes that are CALLED through it: the store's own internal
// writes (aggregate recomputes after a book-file edit, the tag keyspace,
// author and series renames) reach the book row or its search document
// without passing the decorator at all.
//
// The hooks sit at the memdb write-through choke points (memdb_sync.go), which
// every book-row writer already calls after its Pebble commit, so a new book
// writer is covered without anyone remembering to add a call.
//
// Implementations must be cheap and must not call back into the store
// synchronously: several hooks fire while a store lock is held (the name-index
// locks for renames).
type ChangeObserver interface {
	// BooksChanged reports book rows (or their author/narrator junctions)
	// written, created or deleted.
	BooksChanged(ids ...string)
	// BooksNeedReindex reports books whose search document changed through a
	// path the indexedStore decorator never sees, so it did not schedule a
	// re-index: tag writes and aggregate recomputes.
	BooksNeedReindex(ids ...string)
	// AuthorRenamed reports an author whose name changed or was deleted.
	// Every book whose AuthorID is this author carries the old name in its
	// search document.
	AuthorRenamed(authorID int)
	// SeriesRenamed is AuthorRenamed for series.
	SeriesRenamed(seriesID int)
}

// ChangeObservable is implemented by stores that report writes to a
// ChangeObserver. Look it up with AsCapability so decorators are seen through.
type ChangeObservable interface {
	SetChangeObserver(o ChangeObserver)
}

type changeObserverBox struct{ o ChangeObserver }

// SetChangeObserver installs o (nil removes it). Safe to call concurrently
// with writes.
func (p *PebbleStore) SetChangeObserver(o ChangeObserver) {
	if o == nil {
		p.changeObs.Store(nil)
		return
	}
	p.changeObs.Store(&changeObserverBox{o: o})
}

func (p *PebbleStore) observer() ChangeObserver {
	if b := p.changeObs.Load(); b != nil {
		return b.o
	}
	return nil
}

func (p *PebbleStore) notifyBooksChanged(ids ...string) {
	if o := p.observer(); o != nil {
		o.BooksChanged(ids...)
	}
}

func (p *PebbleStore) notifyBooksNeedReindex(ids ...string) {
	if o := p.observer(); o != nil {
		o.BooksNeedReindex(ids...)
	}
}

func (p *PebbleStore) notifyAuthorRenamed(id int) {
	if o := p.observer(); o != nil {
		o.AuthorRenamed(id)
	}
}

func (p *PebbleStore) notifySeriesRenamed(id int) {
	if o := p.observer(); o != nil {
		o.SeriesRenamed(id)
	}
}

var _ ChangeObservable = (*PebbleStore)(nil)
