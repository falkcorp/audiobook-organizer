// file: internal/diagnostics/store.go
// version: 1.1.0
// guid: 2a8c4f61-5b07-4d39-8e15-9c0b6a3e7d21
// last-edited: 2026-08-19

package diagnostics

import "github.com/falkcorp/audiobook-organizer/internal/database"

// The store surface this package needs, measured with an empty-interface
// compiler probe under -gcflags=-e: 12 methods, no forwarding constraints. It
// was database.Store -- 398 methods -- until 2026-08-19.
//
// Grouped by the three things this service counts, so each group reads as the
// question it answers rather than as an alphabetised list.

type libraryCounter interface {
	CountPrimaryBooks() (int, error)
	CountAuthors() (int, error)
	CountSeries() (int, error)
}

type authorAggregateReader interface {
	GetAllAuthors() ([]database.Author, error)
	GetAllAuthorBookCounts() (map[int]int, error)
	GetAllAuthorFileCounts() (map[int]int, error)
}

type seriesAggregateReader interface {
	GetAllSeries() ([]database.Series, error)
	GetAllSeriesBookCounts() (map[int]int, error)
	GetAllSeriesFileCounts() (map[int]int, error)
}

// Store is the diagnostics consumer slice. Exported so a caller that constructs
// a Service (internal/server/handlers) can name it instead of database.Store.
type Store interface {
	libraryCounter
	authorAggregateReader
	seriesAggregateReader

	GetAllBooksCore(limit, offset int) ([]database.BookCore, error)
	GetRecentOperations(limit int) ([]database.Operation, error)
	GetSystemActivityLogs(source string, limit int) ([]database.SystemActivityLog, error)
}
