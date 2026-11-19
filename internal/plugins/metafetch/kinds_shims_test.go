// file: internal/plugins/metafetch/kinds_shims_test.go
// version: 1.0.0
// guid: 4f2d12cc-19fc-44bb-b9c7-08749d2575f7
// last-edited: 2026-11-19

// Test-only shim: applyOrigin is the HEAD-named entry the older tests call; the
// core goes through applyOriginVia.

package metafetch

import database "github.com/falkcorp/audiobook-organizer/internal/books/store"

// applyOrigin classifies a book's apply origin: "manual" when the book carries
// any manual metadata-field override (recorded with source "manual" in the
// metadata-field-state store — i.e. a non-nil OverrideValue), else "auto".
// A field-state read error is treated as "auto" (fail-open, never fatal).
func (p *Plugin) applyOrigin(book *database.Book) string {
	return applyOriginVia(p.store, book)
}
