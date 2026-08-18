// file: internal/database/iface_tags.go
// version: 1.0.0
// guid: 9129bad9-0aa9-4eda-82fb-b945f0393674

package database

// BookTagReader reads tags on books.
type BookTagReader interface {
	GetBookTags(bookID string) ([]string, error)
	GetBookTagsDetailed(bookID string) ([]BookTag, error)
	ListAllTags() ([]TagWithCount, error)
	GetBooksByTag(tag string) ([]string, error)
}

// BookTagWriter adds and removes tags on books.
type BookTagWriter interface {
	// Book tags
	AddBookTag(bookID, tag string) error
	AddBookTagWithSource(bookID, tag, source string) error
	RemoveBookTag(bookID, tag string) error
	RemoveBookTagsByPrefix(bookID, prefix, source string) error
	SetBookTags(bookID string, tags []string) error
}

// AuthorTagReader reads tags on authors.
type AuthorTagReader interface {
	GetAuthorTags(authorID int) ([]string, error)
	GetAuthorTagsDetailed(authorID int) ([]BookTag, error)
	ListAllAuthorTags() ([]TagWithCount, error)
	GetAuthorsByTag(tag string) ([]int, error)
}

// AuthorTagWriter adds and removes tags on authors.
type AuthorTagWriter interface {
	// Author tags
	AddAuthorTag(authorID int, tag string) error
	AddAuthorTagWithSource(authorID int, tag, source string) error
	RemoveAuthorTag(authorID int, tag string) error
	RemoveAuthorTagsByPrefix(authorID int, prefix, source string) error
	SetAuthorTags(authorID int, tags []string) error
}

// SeriesTagReader reads tags on series.
type SeriesTagReader interface {
	GetSeriesTags(seriesID int) ([]string, error)
	GetSeriesTagsDetailed(seriesID int) ([]BookTag, error)
	ListAllSeriesTags() ([]TagWithCount, error)
	GetSeriesByTag(tag string) ([]int, error)
}

// SeriesTagWriter adds and removes tags on series.
type SeriesTagWriter interface {
	// Series tags
	AddSeriesTag(seriesID int, tag string) error
	AddSeriesTagWithSource(seriesID int, tag, source string) error
	RemoveSeriesTag(seriesID int, tag string) error
	RemoveSeriesTagsByPrefix(seriesID int, prefix, source string) error
	SetSeriesTags(seriesID int, tags []string) error
}

// TagStore covers book/author/series tag operations (source-tracked).
// Matches the "Tags" section of the legacy Store interface.
//
// Split into the 6 interfaces above on 2026-08-18. This name is retained
// as their composition so the method set is byte-identical and no consumer moves;
// the type checker proves it, because every implementation -- PebbleStore (496
// methods) and database.MockStore (399) among them -- fails to compile if a method
// is dropped or re-signatured in the regrouping.
//
// Consumers should migrate to whichever pieces they use; this composition is the
// transitional shape, not the destination.
type TagStore interface {
	BookTagReader
	BookTagWriter
	AuthorTagReader
	AuthorTagWriter
	SeriesTagReader
	SeriesTagWriter
}

// UserTagStore covers free-form per-book user tags (the *BookUserTag* variants).
type UserTagStore interface {
	GetBookUserTags(bookID string) ([]string, error)
	SetBookUserTags(bookID string, tags []string) error
	AddBookUserTag(bookID string, tag string) error
	RemoveBookUserTag(bookID string, tag string) error
}
