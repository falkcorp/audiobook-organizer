// file: internal/server/metadata_bulk_fetch_title.go
// version: 1.5.0
// guid: a88b51d9-3878-41d2-b50e-c04f1ff6793e
// last-edited: 2026-09-30
//
// The title the bulk metadata fetch searches a book by, for a book whose own
// title is not worth searching.

package server

import (
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/authorname"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// bulkFetchTitleStore is what resolving a stand-in title reads: the full
// book row (BookCore carries no transcription), the book's files and its
// authors (metabatch.SearchQueryReader).
type bulkFetchTitleStore interface {
	GetBookByID(id string) (*database.Book, error)
	metabatch.SearchQueryReader
}

// bulkFetchQuery is the search a bulk-fetch worker runs for one book.
type bulkFetchQuery struct {
	query    metabatch.CandidateSearchQuery
	identity string // per-provider fetch-cache identity for query.Title
	// skipKind / skipStatus are set when query is not Usable: what the
	// book's own title is, and the ledger status its skipped row gets.
	skipKind, skipStatus string
}

// resolveBulkFetchQuery decides what the bulk fetch searches for one book.
//
// A book with a searchable title is searched by it with the identity the
// pre-loop already computed, and costs no store read. Any other book (empty,
// placeholder, chapter number, chapter fragment: metadata.IsUnsearchableTitle)
// used to be left out of the run or skipped outright; it now falls back to
// its transcribed title, then its folder name (metabatch.ResolveCandidateSearchQuery),
// and is skipped only when none is usable. The fallback is a search query
// only: nothing here writes a title onto the book.
//
// It runs inside the worker pool, not the sequential pre-loop, because it
// reads the book and its files: done in the pre-loop that would be a serial
// per-book store walk over every untitled book in the library.
//
// The fetch-cache identity is keyed on the stand-in, exactly as
// metafetch's fetchCacheIdentityForTitle keys the per-book search's rows,
// so the two paths still share rows and neither replays what an earlier
// search of the placeholder cached.
//
// A title whose verdict hangs on the book's folder and duration
// (metadata.NeedsFolderEvidence: "Cobra 100 of 151", "The Sunrise Lands 1",
// "American Gods [64k]") keeps the pre-loop's skip_cached probe and identity:
// it goes to the resolver without a GetBookByID (the resolver reads the
// book's files and, through memo, its folder once per pass), and when it is
// searched by its own title the pre-loop identity stands.
func resolveBulkFetchQuery(store bulkFetchTitleStore, bookID, title, path, author, identity string, full *database.Book, memo *metabatch.FolderMemo) bulkFetchQuery {
	ownTitle := bulkFetchQuery{
		query:    metabatch.CandidateSearchQuery{Title: title, Source: metabatch.SearchQuerySourceTitle, Usable: true},
		identity: identity,
	}
	if !metadata.MayBeUnsearchableTitle(title) {
		if !metadata.NeedsFolderEvidence(title) {
			return ownTitle
		}
		book := full
		if book == nil {
			book = &database.Book{ID: bookID, Title: title, FilePath: path}
		}
		q := metabatch.ResolveCandidateSearchQueryMemo(store, book, memo)
		switch {
		case !q.Usable:
			kind, status := unsearchableQueryKind(q, title)
			return bulkFetchQuery{query: q, skipKind: kind, skipStatus: status}
		case q.Title == title:
			return ownTitle
		default:
			// Rip details cleaned off: the stand-in carries its own identity.
			return bulkFetchQuery{query: q, identity: metafetch.FetchCacheIdentity(q.Title, author, book.ASIN, book.ISBN13, book.ISBN10)}
		}
	}
	book := full
	if book == nil {
		// A read failure falls back to the book as the caller knows it, which
		// can still resolve a folder title from its path.
		if b, err := store.GetBookByID(bookID); err == nil && b != nil {
			book = b
		} else {
			book = &database.Book{ID: bookID, Title: title, FilePath: path}
		}
	}
	q := metabatch.ResolveCandidateSearchQueryMemo(store, book, memo)
	if !q.Usable {
		kind, status := unsearchableQueryKind(q, title)
		return bulkFetchQuery{query: q, skipKind: kind, skipStatus: status}
	}
	return bulkFetchQuery{
		query:    q,
		identity: metafetch.FetchCacheIdentity(q.Title, author, book.ASIN, book.ISBN13, book.ISBN10),
	}
}

// unsearchableQueryKind is unsearchableTitleKind, unless the resolver
// refused the book for a reason its title alone does not show
// (metabatch.SkipKindSiblingPart: one file of several rows in its folder).
func unsearchableQueryKind(q metabatch.CandidateSearchQuery, title string) (kind, status string) {
	if q.SkipKind != "" {
		return q.SkipKind, metafetch.FetchStatusSkippedFragment
	}
	return unsearchableTitleKind(title)
}

// unsearchableTitleKind names what an unsearchable title is, for the skip
// line, and the ledger status its skipped row gets.
func unsearchableTitleKind(title string) (kind, status string) {
	t := strings.TrimSpace(title)
	switch {
	case t == "":
		return "empty title", metafetch.FetchStatusSkippedNoTitle
	case metadata.IsLikelyChapterFragment(t):
		return "chapter fragment", metafetch.FetchStatusSkippedFragment
	case metadata.IsChapterOnlyTitle(t):
		return "chapter number only", metafetch.FetchStatusSkippedFragment
	case authorname.IsPlaceholderTitle(t):
		return "placeholder title", metafetch.FetchStatusSkippedNoTitle
	default:
		// "Book 1", "Vol. 2", or a heading the book's files corroborate
		// ("Book Two" on a multi-file book; metabatch's titleJudge).
		return "section heading only", metafetch.FetchStatusSkippedFragment
	}
}
