// file: internal/maintenance/jobs/testdata/oraclefix/fix/fix.go
// version: 1.0.0
// guid: 164bc002-83bc-4523-bed7-b8bfebfc2c9a
// last-edited: 2026-11-19

// Package fix holds the shapes of code the write-set oracle has to follow. Each
// exported function is a root the oracle's tests start a walk from; the store
// calls under it are the writes the walk must find (or, for the Read and
// Shared roots, must not).
package fix

import "github.com/falkcorp/audiobook-organizer/internal/maintenance/jobs/testdata/oraclefix/internal/books/store"

// undoer is an interface whose method is named like no write at all, and whose
// one implementation writes through the store.
type undoer interface{ Undo(id string) error }

type storeUndoer struct{ s *store.Store }

func (u storeUndoer) Undo(id string) error { return u.s.ModifyBook(id) }

// ViaInterface writes a book through an interface call into a wrapper.
func ViaInterface(s *store.Store, id string) error {
	var u undoer = storeUndoer{s}
	return u.Undo(id)
}

// step is a store operation passed around as a value.
type step func(s *store.Store, id string) error

var steps []step

func init() { steps = append(steps, dropAuthor) }

// dropAuthor is registered in init, under another name, and reaches the store
// only when a caller invokes a step.
func dropAuthor(s *store.Store, id string) error { return s.DeleteAuthor(id) }

// ViaFuncValue writes an author through a call to a function value read from a
// package variable.
func ViaFuncValue(s *store.Store, id string) error {
	for _, st := range steps {
		if err := st(s, id); err != nil {
			return err
		}
	}
	return nil
}

type worker struct{ apply step }

func newWorker() *worker { return &worker{apply: renamedModify} }

func renamedModify(s *store.Store, id string) error { return s.ModifyBook(id) }

var theWorker = newWorker()

// ViaStructField writes a book through a function value stored in a struct field
// by a constructor this function never calls.
func ViaStructField(s *store.Store, id string) error { return theWorker.apply(s, id) }

// ViaNonVerbStoreMethod writes a book through a store method whose name starts
// with no write verb.
func ViaNonVerbStoreMethod(s *store.Store, id string) error {
	return s.RevertBookToVersion(id)
}

// runEach is a helper two roots share; its callback is whatever the caller
// passes.
func runEach(s *store.Store, ids []string, fn step) error {
	for _, id := range ids {
		if err := fn(s, id); err != nil {
			return err
		}
	}
	return nil
}

func readOnly(s *store.Store, id string) error { return s.GetBookByID(id) }

// SharedReadOnly passes a reading callback to the shared helper. It writes
// nothing; reporting a write here means the walk treated the helper's callback as
// every callback any caller passes (SharedWriter's dropAuthor).
func SharedReadOnly(s *store.Store, ids []string) error { return runEach(s, ids, readOnly) }

// SharedWriter passes a writing callback to the shared helper.
func SharedWriter(s *store.Store, ids []string) error { return runEach(s, ids, dropAuthor) }

// Read only reads.
func Read(s *store.Store, id string) error { return s.GetBookByID(id) }
