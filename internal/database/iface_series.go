// file: internal/database/iface_series.go
// version: 1.2.0
// guid: 459a6734-95fb-437c-bb97-6baecc64aba4
// last-edited: 2026-09-12

package database

// SeriesReader is the read-only series slice.
type SeriesReader interface {
	GetAllSeries() ([]Series, error)
	GetSeriesByID(id int) (*Series, error)
	GetSeriesByName(name string, authorID *int) (*Series, error)
	GetAllSeriesBookCounts() (map[int]int, error)
	GetAllSeriesFileCounts() (map[int]int, error)
	// GetSeriesByIDs returns a map from seriesID → *Series for the given IDs.
	// Missing IDs are absent from the map. Returns empty map (not nil) for empty input.
	GetSeriesByIDs(ids []int) (map[int]*Series, error)
}

// SeriesWriter is the write-only series slice.
type SeriesWriter interface {
	CreateSeries(name string, authorID *int) (*Series, error)
	DeleteSeries(id int) error
	UpdateSeriesName(id int, name string) error
	// RenameSeriesIf renames only if the series is still named expectCurrent
	// and newName is free under its author, checked and written atomically;
	// see PebbleStore.RenameSeriesIf.
	RenameSeriesIf(id int, expectCurrent, newName string) error
}

// SeriesStore combines both halves.
type SeriesStore interface {
	SeriesReader
	SeriesWriter
}
