// file: internal/database/change_observer.go
// version: 1.1.0
// guid: 53e053ef-f45a-4f3a-9ddd-ae18779c3d50
// last-edited: 2026-10-13

package database

// ChangeObserver is told about every PebbleStore write that can change a
// search result. It is the ONE mechanism by which the server learns that a
// book's search document or a cached search result may be stale: the server's
// observer (internal/server, search_observer.go) feeds the Bleve index queue
// and the search result cache from it. (Until 07-S3 a second mechanism, the
// server's indexedStore decorator, intercepted a handful of book-write methods
// as well; it only saw writes CALLED through it, so the store's own internal
// writes -- aggregate recomputes after a book-file edit, the tag keyspace,
// author and series renames -- reached the book row or its search document
// without passing it. The decorator is gone.)
//
// The hooks sit at the memdb write-through choke points (memdb_sync.go), which
// every book-row writer already calls after its Pebble commit, so a new book
// writer is covered without anyone remembering to add a call. A new writer that
// does NOT go through those choke points must call notifyBooksChanged or
// notifyBooksNeedReindex itself, or its writes are invisible to search.
//
// Implementations must be cheap and must not call back into the store
// synchronously: several hooks fire while a store lock is held (the name-index
// locks for renames).
type ChangeObserver interface {
	// BooksChanged reports book rows written, created or deleted.
	BooksChanged(ids ...string)
	// BooksNeedReindex reports books whose search document changed without a
	// book-row write through the choke points above: tag writes.
	BooksNeedReindex(ids ...string)
	// BookCreditsChanged reports books whose author or narrator JUNCTION rows
	// were rewritten. The search document is built from the book row (AuthorID,
	// Narrator) and never reads the junction, so this can change a cached search
	// result (a filter on credits) but cannot change the document: observers
	// invalidate cached results and do NOT schedule a re-index. A credit edit
	// that also writes the book row reports that through BooksChanged.
	BookCreditsChanged(ids ...string)
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
	// ClearChangeObserver removes o if it is still the installed observer
	// (compared by identity), so a server shutting down never removes the
	// observer a newer server installed on the same store. o must be a
	// comparable value (a pointer).
	ClearChangeObserver(o ChangeObserver)
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

// ClearChangeObserver removes o only if it is the installed observer.
func (p *PebbleStore) ClearChangeObserver(o ChangeObserver) {
	if b := p.changeObs.Load(); b != nil && b.o == o {
		p.changeObs.CompareAndSwap(b, nil)
	}
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

func (p *PebbleStore) notifyBookCreditsChanged(ids ...string) {
	if o := p.observer(); o != nil {
		o.BookCreditsChanged(ids...)
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
