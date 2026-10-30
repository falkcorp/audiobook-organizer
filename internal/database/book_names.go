// file: internal/database/book_names.go
// version: 1.0.0
// guid: 996172b2-1c89-4a73-8197-648d60c62312
// last-edited: 2026-10-30

package database

// BookNameReader is the two reads ResolveBookNames needs. It is its own small
// interface so a handler package that holds only a narrow store can resolve a
// book's display names without importing the server package.
type BookNameReader interface {
	GetAuthorByID(id int) (*Author, error)
	GetSeriesByID(id int) (*Series, error)
}

// ResolveBookNames returns the display author and series name of a book: the
// preloaded Author / Series object when the book carries one, else a lookup by
// AuthorID / SeriesID. A nil reader, a missing id, a lookup error or a missing
// row all yield "" -- the same fail-open the audiobooks responses have always
// had, so an endpoint that adds author_name through this function agrees with
// the endpoints that already send it.
//
// It is the single implementation behind server.enrichBookForResponse and the
// dedup candidate endpoints; a copy would let the two drift on what "the
// author of a book" means.
func ResolveBookNames(r BookNameReader, book *Book) (authorName, seriesName string) {
	if book == nil {
		return "", ""
	}
	if book.Author != nil {
		authorName = book.Author.Name
	} else if book.AuthorID != nil && r != nil {
		if author, err := r.GetAuthorByID(*book.AuthorID); err == nil && author != nil {
			authorName = author.Name
		}
	}
	if book.Series != nil {
		seriesName = book.Series.Name
	} else if book.SeriesID != nil && r != nil {
		if series, err := r.GetSeriesByID(*book.SeriesID); err == nil && series != nil {
			seriesName = series.Name
		}
	}
	return authorName, seriesName
}
