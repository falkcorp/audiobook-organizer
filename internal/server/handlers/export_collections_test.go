// file: internal/server/handlers/export_collections_test.go
// version: 1.0.0
// guid: 5b2e8d17-3c64-4f90-a1d8-7e0c4b9a6f23
// last-edited: 2026-10-18

package handlers

// WithCollectionLogger replaces the logger the owner-fallback warning goes to.
// It exists for the external handlers_test package only (a test seam, so the
// once-per-collection warning can be asserted); production uses collectionLog.
func WithCollectionLogger(l collectionWarner) CollectionOption {
	return func(h *CollectionHandler) {
		if l != nil {
			h.log = l
		}
	}
}
