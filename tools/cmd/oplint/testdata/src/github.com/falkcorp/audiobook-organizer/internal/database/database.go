// file: tools/cmd/oplint/testdata/src/github.com/falkcorp/audiobook-organizer/internal/database/database.go
// version: 1.0.0
// guid: 2d631d45-bd9f-40bb-a2b2-b34bcecbfb09
// last-edited: 2026-10-16

// Package database is a minimal stub of internal/database for the oplint
// analysistest fixtures.
package database

type Book struct{ ID, Title string }

// BookReader is a narrow read interface.
type BookReader interface {
	GetBook(id string) (*Book, error)
}

// BookWriter is a narrow write interface.
type BookWriter interface {
	UpdateBook(id string, b *Book) error
	DeleteBook(id string) error
}

// Store is the wide interface.
type Store interface {
	BookReader
	BookWriter
	SetSetting(key, value string) error
}

// PebbleStore is the concrete store.
type PebbleStore struct{}

func (s *PebbleStore) GetBook(id string) (*Book, error)    { return nil, nil }
func (s *PebbleStore) UpdateBook(id string, b *Book) error { return nil }
func (s *PebbleStore) DeleteBook(id string) error          { return nil }
func (s *PebbleStore) SetSetting(key, value string) error  { return nil }
