// file: internal/audiobooks/author_series.go
// version: 1.6.0
// guid: f6a7b8c9-d0e1-2f3a-4b5c-6d7e8f9a0b1c
// last-edited: 2026-08-18

package audiobooks

import (
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// authorReadModel is the author half of what AuthorSeriesService reads: the
// authors themselves plus the aggregates the list endpoint enriches them with.
type authorReadModel interface {
	GetAllAuthors() ([]database.Author, error)
	GetAuthorByID(id int) (*database.Author, error)
	GetAllAuthorAliases() ([]database.AuthorAlias, error)
	GetAllAuthorBookCounts() (map[int]int, error)
	GetAllAuthorFileCounts() (map[int]int, error)
}

// seriesReadModel is the series half, in the same shape.
type seriesReadModel interface {
	GetAllSeries() ([]database.Series, error)
	GetSeriesByID(id int) (*database.Series, error)
	GetAllSeriesBookCounts() (map[int]int, error)
	GetAllSeriesFileCounts() (map[int]int, error)
}

// authorSeriesStore is the narrow slice of database.Store this service uses.
//
// Measured with an empty-interface compiler probe: 9 methods. It previously
// embedded database.AuthorReader and database.SeriesReader wholesale. Listing
// the 9 flat would itself trip the width linter, so they are grouped by the two
// entities the service actually reads.
type authorSeriesStore interface {
	authorReadModel
	seriesReadModel
}

type AuthorSeriesService struct {
	db authorSeriesStore
}

func NewAuthorSeriesService(db authorSeriesStore) *AuthorSeriesService {
	return &AuthorSeriesService{db: db}
}

// AuthorWithCount enriches an Author with book count, file count, and aliases.
type AuthorWithCount struct {
	ID        int                    `json:"id"`
	Name      string                 `json:"name"`
	BookCount int                    `json:"book_count"`
	FileCount int                    `json:"file_count"`
	Aliases   []database.AuthorAlias `json:"aliases"`
}

type AuthorListResponse struct {
	Items []database.Author `json:"items"`
	Count int               `json:"count"`
}

type AuthorWithCountListResponse struct {
	Items []AuthorWithCount `json:"items"`
	Count int               `json:"count"`
}

// SeriesWithCount extends Series with book count, file count, and author name.
type SeriesWithCount struct {
	database.Series
	BookCount  int    `json:"book_count"`
	FileCount  int    `json:"file_count"`
	AuthorName string `json:"author_name,omitempty"`
}

type SeriesListResponse struct {
	Items []database.Series `json:"items"`
	Count int               `json:"count"`
}

type SeriesWithCountsResponse struct {
	Items []SeriesWithCount `json:"items"`
	Count int               `json:"count"`
}

func (as *AuthorSeriesService) ListAuthors() (*AuthorListResponse, error) {
	authors, err := as.db.GetAllAuthors()
	if err != nil {
		return nil, err
	}
	if authors == nil {
		authors = []database.Author{}
	}
	return &AuthorListResponse{
		Items: authors,
		Count: len(authors),
	}, nil
}

// ListAuthorsWithCounts returns all authors enriched with book counts and aliases.
func (as *AuthorSeriesService) ListAuthorsWithCounts() (*AuthorWithCountListResponse, error) {
	authors, err := as.db.GetAllAuthors()
	if err != nil {
		return nil, err
	}
	if authors == nil {
		authors = []database.Author{}
	}

	bookCounts, err := as.db.GetAllAuthorBookCounts()
	if err != nil {
		return nil, err
	}

	fileCounts, _ := as.db.GetAllAuthorFileCounts()
	if fileCounts == nil {
		fileCounts = map[int]int{}
	}

	allAliases, err := as.db.GetAllAuthorAliases()
	if err != nil {
		return nil, err
	}

	aliasesByAuthor := make(map[int][]database.AuthorAlias)
	for _, alias := range allAliases {
		aliasesByAuthor[alias.AuthorID] = append(aliasesByAuthor[alias.AuthorID], alias)
	}

	items := make([]AuthorWithCount, len(authors))
	for i, a := range authors {
		aliases := aliasesByAuthor[a.ID]
		if aliases == nil {
			aliases = []database.AuthorAlias{}
		}
		items[i] = AuthorWithCount{
			ID:        a.ID,
			Name:      a.Name,
			BookCount: bookCounts[a.ID],
			FileCount: fileCounts[a.ID],
			Aliases:   aliases,
		}
	}

	return &AuthorWithCountListResponse{
		Items: items,
		Count: len(items),
	}, nil
}

func (as *AuthorSeriesService) ListSeries() (*SeriesListResponse, error) {
	series, err := as.db.GetAllSeries()
	if err != nil {
		return nil, err
	}
	if series == nil {
		series = []database.Series{}
	}
	return &SeriesListResponse{
		Items: series,
		Count: len(series),
	}, nil
}

// ListSeriesWithCounts returns all series enriched with book counts and author names.
func (as *AuthorSeriesService) ListSeriesWithCounts() (*SeriesWithCountsResponse, error) {
	series, err := as.db.GetAllSeries()
	if err != nil {
		return nil, err
	}
	if series == nil {
		series = []database.Series{}
	}

	counts, err := as.db.GetAllSeriesBookCounts()
	if err != nil {
		return nil, err
	}

	fileCounts, _ := as.db.GetAllSeriesFileCounts()
	if fileCounts == nil {
		fileCounts = map[int]int{}
	}

	authors, _ := as.db.GetAllAuthors()
	authorMap := make(map[int]string, len(authors))
	for _, a := range authors {
		authorMap[a.ID] = a.Name
	}

	items := make([]SeriesWithCount, 0, len(series))
	for _, s := range series {
		swc := SeriesWithCount{
			Series:    s,
			BookCount: counts[s.ID],
			FileCount: fileCounts[s.ID],
		}
		if s.AuthorID != nil {
			swc.AuthorName = authorMap[*s.AuthorID]
		}
		items = append(items, swc)
	}

	return &SeriesWithCountsResponse{
		Items: items,
		Count: len(items),
	}, nil
}
