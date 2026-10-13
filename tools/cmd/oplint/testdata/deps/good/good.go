// file: tools/cmd/oplint/testdata/deps/good/good.go
// version: 1.0.0
// guid: 0b8e1d00-26d2-4de5-a270-5b46236b1a1c
// last-edited: 2026-10-13

// Package good is an oplint fixture: narrow interfaces only.
package good

import "github.com/falkcorp/audiobook-organizer/internal/database"

// BookReader is the one method the ops call.
type BookReader interface {
	GetBookByID(id string) (*database.Book, error)
}

// Deps is what this package's ops call.
type Deps struct {
	Books BookReader
}

// NotDeps may hold anything: the rule is about Deps.
type NotDeps struct {
	Store database.Store
}
