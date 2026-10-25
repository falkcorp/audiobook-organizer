// file: internal/server/handlers/export_collections_test.go
// version: 2.0.0
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
