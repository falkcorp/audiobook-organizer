// file: internal/server/handlers/dedup/search.go
// version: 1.1.0
// guid: 5a2e8f31-9c47-4b06-8d1e-6f3a97c2b8d4
// last-edited: 2026-09-01

package deduphandler

import (
	"strings"
)

// resolveBookIDsMatching returns the set of book IDs whose title, author name,
// or file path contains needle (case-insensitive). It is the "joined book"
// half of the Dupes search union: the candidate rows themselves carry only
// layer/band/entity-IDs, so the book-derived fields the panel searches have to
// be resolved to IDs BEFORE the candidate scan runs. The resulting set is
// handed to database.CandidateFilter.SearchEntityIDs, which applies it at scan
// level so `total` counts every match in the library rather than the ones that
// happened to survive on the current page.
//
// An empty or whitespace-only needle returns a nil set: the caller treats that
// as "no search", not as "search that matched nothing".
//
// # Which fields, and why not simply the client's list
//
// The panel's old in-browser filter built its haystack from ten fields, two of
// which -- book_a.author_name and book_b.author_name -- were always undefined.
// The TypeScript Book interface declares author_name?: string and OTHER
// endpoints do populate it (the audiobooks service, and enrichedBookResponse in
// server.go), but this one returns the bare database.Book from GetBookByID,
// which populates neither author_name nor the joined `author` object. The optional `?` meant that read produced undefined,
// `?? ''` swallowed it, and author search silently matched nothing. So this
// resolver implements what the UI intended -- title, author, path -- rather
// than replicating a haystack with two dead entries.
//
// # Cost, and why this primitive
//
// GetAllBooksCore(0, 0) is one in-memory walk of the memdb books table (it
// routes to memdb on PebbleStore and falls back to a Pebble scan only when
// memdb is unavailable). It is not free -- it materializes a Book per row
// before narrowing to Core -- but the alternatives are worse:
// GetAllBooksFullFrom's memdb path lists IDs and then does a Pebble point read
// PER BOOK, which trades transient allocation for tens of thousands of reads.
// A dedicated store-level projection that matched during the walk and returned
// only IDs would beat both; that is a store-surface change, filed as follow-up
// rather than smuggled into this handler.
//
// # On the concurrency rule
//
// CLAUDE.md requires whole-library loops to be written for multiple cores when
// each item does meaningful work. This loop is deliberately serial: the
// per-item work is three strings.Contains calls, not a DB read, network call,
// hash, or fuzzy comparison. A worker pool over a substring test costs more in
// coordination than it recovers, and the real expense here is the bulk read
// above, not the matching.
func resolveBookIDsMatching(store DedupStore, needle string) (map[string]struct{}, error) {
	needle = strings.ToLower(strings.TrimSpace(needle))
	if needle == "" {
		return nil, nil
	}

	// Author names are stored on the author row, not the book, so they are
	// pulled once into an ID->name map rather than looked up per book.
	authorNames := map[int]string{}
	authors, err := store.GetAllAuthors()
	if err != nil {
		return nil, err
	}
	for _, a := range authors {
		authorNames[a.ID] = strings.ToLower(a.Name)
	}

	books, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		return nil, err
	}

	matched := make(map[string]struct{})
	for _, b := range books {
		if strings.Contains(strings.ToLower(b.Title), needle) ||
			strings.Contains(strings.ToLower(b.FilePath), needle) {
			matched[b.ID] = struct{}{}
			continue
		}
		if b.AuthorID != nil {
			if name, ok := authorNames[*b.AuthorID]; ok && strings.Contains(name, needle) {
				matched[b.ID] = struct{}{}
			}
		}
	}
	return matched, nil
}
