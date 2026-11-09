// file: internal/books/store/export_change_type_class_test.go
// version: 1.0.1
// guid: 0e5a8d73-6b21-4f9c-8d14-b73c2a9e60f5
// last-edited: 2026-11-09

package store

import "maps"

// ExportUndoClassified and ExportLiteralClassified give the external test
// package (which may import internal/undo without a cycle) the two tables.
func ExportUndoClassified() map[string]bool { return maps.Clone(undoChangeTypeHoldsUserData) }

// ExportLiteralClassified: see ExportUndoClassified.
func ExportLiteralClassified() map[string]bool { return maps.Clone(libraryDataLiteralChangeTypes) }
