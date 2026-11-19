// file: internal/maintenance/jobs/testdata/oraclefix/fix/fix.go
// version: 1.2.0
// guid: 164bc002-83bc-4523-bed7-b8bfebfc2c9a
// last-edited: 2026-11-19

// Package fix holds the shapes of code the write-set oracle has to follow. Each
// exported function is a root the oracle's tests start a walk from; the store
// calls under it are the writes the walk must find (or, for the Read and
// Shared roots, must not).
package fix

import (
	"slices"

	"github.com/falkcorp/audiobook-organizer/internal/maintenance/jobs/testdata/oraclefix/internal/books/store"
)

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

// runEachOrDefault is a helper whose callback is the caller's, or, when the
// caller passes none, one read from a package variable. The value mixes a
// parameter with a field load, so the call graph (which cannot tell the two
// apart, and is context-insensitive) is not asked about it.
func runEachOrDefault(s *store.Store, ids []string, fn step) error {
	if fn == nil {
		fn = steps[0]
	}
	for _, id := range ids {
		if err := fn(s, id); err != nil {
			return err
		}
	}
	return nil
}

// MixedReadOnly passes a reading callback to a helper that falls back to a
// package variable's step. It writes nothing: the callback is this caller's.
func MixedReadOnly(s *store.Store, ids []string) error { return runEachOrDefault(s, ids, readOnly) }

// orphanWriter is a module interface nothing implements: no call graph has a
// callee for it, so the call is judged by the interface method's own name.
type orphanWriter interface{ SaveBook(id string) error }

// ViaOrphanInterface writes a book through an interface with no implementation.
func ViaOrphanInterface(w orphanWriter, id string) error { return w.SaveBook(id) }

// Each is a generic function of this module; its instance and the closure passed
// to it have no package of their own, and belong to this one.
func Each[T any](xs []T, f func(T) error) error {
	for _, x := range xs {
		if err := f(x); err != nil {
			return err
		}
	}
	return nil
}

// ViaGeneric writes a book from a closure handed to an instance of a generic of
// this module.
func ViaGeneric(s *store.Store, ids []string) error {
	return Each(ids, func(id string) error { return s.ModifyBook(id) })
}

// UsesStdGeneric calls an instance of a standard library generic, which is not
// module code and is not followed.
func UsesStdGeneric(ids []string) []string {
	slices.Sort(ids)
	return ids
}

// progressReporter is progress reporting: its calls, and what implements them,
// are not books writes (the oracle's oracleNotAWrite names the Reporter types).
type progressReporter interface{ Report(id string) error }

type storeReporter struct{ s *store.Store }

func (r storeReporter) Report(id string) error { return r.s.ModifyBook(id) }

// ReportsProgress calls a progress reporter whose implementation happens to
// write through the store. Reporting progress is not a books write.
func ReportsProgress(s *store.Store, id string) error {
	var r progressReporter = storeReporter{s}
	return r.Report(id)
}

// runEachConverted is runEach with the callback converted before it is called.
func runEachConverted(s *store.Store, ids []string, fn func(*store.Store, string) error) error {
	for _, id := range ids {
		if err := step(fn)(s, id); err != nil {
			return err
		}
	}
	return nil
}

// ConvertedReadOnly passes a reading callback to a helper that converts it: the
// callback is still this caller's.
func ConvertedReadOnly(s *store.Store, ids []string) error {
	return runEachConverted(s, ids, readOnly)
}

// ConvertedWriter is ConvertedReadOnly's sensitivity check: the same helper with
// a writing callback.
func ConvertedWriter(s *store.Store, ids []string) error {
	return runEachConverted(s, ids, dropAuthor)
}

// ViaEitherField calls a function value that is one of two package-level
// values, chosen at run time: the graph resolves both.
func ViaEitherField(s *store.Store, id string, first bool) error {
	fn := steps[0]
	if !first {
		fn = theWorker.apply
	}
	return fn(s, id)
}
