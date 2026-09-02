// file: internal/organizer/landing_triple_test.go
// version: 1.0.0
// guid: 3f2b7c1e-9d4a-4e8b-b5c6-7a1d2e3f4a5b
// last-edited: 2026-09-02

package organizer

import "github.com/falkcorp/audiobook-organizer/internal/database"

// organizeDirTriple is the (dir, map, err) shape OrganizeBookDirectory had
// until 2026-09-02, kept for the in-package tests that were written against
// it. Production callers get the *Landing so they can roll back Created; the
// tests here assert on the landed map and directory, which are the same
// values, so keeping their call sites byte-identical costs nothing and keeps
// the diff that changed the exported shape reviewable.
func organizeDirTriple(o *Organizer, book *database.Book, files []database.BookFile) (string, map[string]string, error) {
	landing, err := o.OrganizeBookDirectory(book, files)
	if err != nil {
		return "", nil, err
	}
	return landing.Path, landing.Files, nil
}
