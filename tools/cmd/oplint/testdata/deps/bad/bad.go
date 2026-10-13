// file: tools/cmd/oplint/testdata/deps/bad/bad.go
// version: 1.0.0
// guid: 42c09262-b6d5-40b2-8135-d438b6c608ce
// last-edited: 2026-10-13

// Package bad is an oplint fixture: every Deps field below violates R21.
package bad

import (
	"context"

	db "github.com/falkcorp/audiobook-organizer/internal/database"
)

// WideStore embeds the whole store.
type WideStore interface {
	db.Store
	Extra(ctx context.Context) error
}

// WiderStill embeds WideStore, so it embeds the store too.
type WiderStill interface {
	WideStore
}

// Deps is what this package's ops call.
type Deps struct {
	Store  db.Store
	Wide   WideStore
	Wider  WiderStill
	Inline interface {
		db.Store
	}
	OK interface {
		GetBookByID(id string) (*db.Book, error)
	}
}
