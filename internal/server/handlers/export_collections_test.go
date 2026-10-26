// file: internal/server/handlers/export_collections_test.go
// version: 2.1.0
// guid: 5b2e8d17-3c64-4f90-a1d8-7e0c4b9a6f23
// last-edited: 2026-10-25

package handlers

// SetCollectionLogger replaces the logger the scheduled refresh's warnings go
// to. It exists for the external handlers_test package only (a test seam, so a
// warning can be asserted); production uses collectionLog.
func SetCollectionLogger(h *CollectionHandler, l collectionWarner) {
	if l != nil {
		h.log = l
	}
}

// SetCollectionEvalLimit replaces the process-wide bound on concurrent
// collection evaluations with one of n slots and returns a function that puts
// the real one back. A test seam for the external handlers_test package; it must
// not be used by a parallel test, because the bound is process-wide.
func SetCollectionEvalLimit(n int) (restore func()) {
	old := collectionEvalSlots
	collectionEvalSlots = make(chan struct{}, n)
	return func() { collectionEvalSlots = old }
}
