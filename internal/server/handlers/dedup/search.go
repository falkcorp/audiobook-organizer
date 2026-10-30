// file: internal/server/handlers/dedup/search.go
// version: 1.2.0
// guid: 5a2e8f31-9c47-4b06-8d1e-6f3a97c2b8d4
// last-edited: 2026-10-30

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
// which populates neither author_name nor the joined `author` object. The
// optional `?` meant that read produced undefined, the nullish-coalescing
// fallback replaced it with a blank, and author search silently matched
// nothing. (Written without a literal empty-string pair on purpose: gofmt's
// doc-comment formatter rewrites two straight quotes into a typographic one,
// which is how a stray non-ASCII byte got into this comment the first time.)
// So this
// resolver implements what the UI intended -- title, author, path -- rather
// than replicating a haystack with two dead entries.
//
// # Cost, and why this primitive
//
// ListBookSearchRows is a store-level projection: one walk of the memdb books
// table (a Pebble scan decoding only the needed fields when memdb is not
// ready) that yields four strings per book -- id, title, path and the author
// name, joined inside the store. The previous shape, GetAllBooksCore(0, 0) plus
// GetAllAuthors, materialised a full BookCore per row (a value of several
// dozen fields, copied twice on the memdb path) to read three strings of it;
// the other alternative on the interface, GetAllBooksFullFrom, lists IDs from
// memdb and then does a Pebble point read PER BOOK. See
// BenchmarkResolveBookIDsMatching for the before/after allocations.
//
// Matching stays here and not in the store on purpose: the substring rule
// (lower-cased contains over title, path and author name) is this handler's
// contract, and pushing it into the store would put search policy in the
// database layer.
//
// # On the concurrency rule
//
// CLAUDE.md requires whole-library loops to be written for multiple cores when
// each item does meaningful work. This loop is deliberately serial: the
// per-item work is three strings.Contains calls, not a DB read, network call,
// hash, or fuzzy comparison. A worker pool over a substring test costs more in
// coordination than it recovers, and the real expense was the bulk read, which
// the projection removes.
func resolveBookIDsMatching(store DedupStore, needle string) (map[string]struct{}, error) {
	needle = strings.ToLower(strings.TrimSpace(needle))
	if needle == "" {
		return nil, nil
	}

	rows, err := store.ListBookSearchRows()
	if err != nil {
		return nil, err
	}

	// A library has far fewer authors than books, so the author test runs once
	// per distinct name and the verdict is reused for every later book by that
	// author.
	authorMatches := make(map[string]bool)
	matched := make(map[string]struct{})
	for _, r := range rows {
		if strings.Contains(strings.ToLower(r.Title), needle) ||
			strings.Contains(strings.ToLower(r.FilePath), needle) {
			matched[r.ID] = struct{}{}
			continue
		}
		if r.AuthorName == "" {
			continue
		}
		hit, seen := authorMatches[r.AuthorName]
		if !seen {
			hit = strings.Contains(strings.ToLower(r.AuthorName), needle)
			authorMatches[r.AuthorName] = hit
		}
		if hit {
			matched[r.ID] = struct{}{}
		}
	}
	return matched, nil
}
