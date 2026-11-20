// file: internal/maintenance/jobs/testdata/oraclefix/internal/books/store/store.go
// version: 1.1.0
// guid: db2d82b3-5d4e-4b22-b482-844ff5219e2b
// last-edited: 2026-11-19

// Package store is a stand-in for internal/books/store for the write-set
// oracle's own tests. Its path contains /internal/books/store, which is how the
// oracle recognises the store: the oracle judges a call into it by the method's
// name and never walks its bodies.
package store

// Store has one write named like a write, two writes that are not, and a read.
type Store struct{}

// ModifyBook writes a book row.
func (*Store) ModifyBook(id string) error { return nil }

// DeleteAuthor writes an author row.
func (*Store) DeleteAuthor(id string) error { return nil }

// RevertBookToVersion writes a book row, and its name starts with no write verb
// the oracle knew before F5-18m.
func (*Store) RevertBookToVersion(id string) error { return nil }

// GetBookByID reads a book row.
func (*Store) GetBookByID(id string) error { return nil }

// UpdateWith is a generic function of the store package: a call to it is a call
// to an instance, which has no package of its own.
func UpdateWith[T any](s *Store, v T) error { return s.ModifyBook("x") }

// LookupBatcave is a plain store-package function a caller can name directly.
func LookupBatcave(s *Store, id string) error { return nil }
