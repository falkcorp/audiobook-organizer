// file: internal/diagnostics/service.go
// version: 1.7.0
// guid: d1a9n0st-1cs0-s3rv-1c3z-1pexp0rt001
// last-edited: 2026-08-22

package diagnostics

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
)

// Service generates diagnostic ZIP exports for troubleshooting and AI analysis.
type Service struct {
	db            Store
	aiPipeline    interface{} // *ai.Pipeline or nil
	itunesXMLPath string
}

// NewService creates a new diagnostics Service.
func NewService(db Store, aiPipeline interface{}, itunesXMLPath string) *Service {
	return &Service{
		db:            db,
		aiPipeline:    aiPipeline,
		itunesXMLPath: itunesXMLPath,
	}
}

// SlimBook is a reduced representation of a book for diagnostics export.
type SlimBook struct {
	ID                   string  `json:"id"`
	Title                string  `json:"title"`
	AuthorID             *int    `json:"author_id,omitempty"`
	Narrator             *string `json:"narrator,omitempty"`
	SeriesID             *int    `json:"series_id,omitempty"`
	Format               string  `json:"format,omitempty"`
	Duration             *int    `json:"duration,omitempty"`
	FilePath             string  `json:"file_path"`
	FileSize             *int64  `json:"file_size,omitempty"`
	VersionGroupID       *string `json:"version_group_id,omitempty"`
	IsPrimaryVersion     *bool   `json:"is_primary_version,omitempty"`
	WorkID               *string `json:"work_id,omitempty"`
	ITunesPersistentID   *string `json:"itunes_persistent_id,omitempty"`
	AudiobookReleaseYear *int    `json:"audiobook_release_year,omitempty"`
	Publisher            *string `json:"publisher,omitempty"`
	LibraryState         *string `json:"library_state,omitempty"`
	MarkedForDeletion    *bool   `json:"marked_for_deletion,omitempty"`
}

// ToSlimBook converts a database.BookCore to a SlimBook.
func ToSlimBook(b database.BookCore) SlimBook {
	return SlimBook{
		ID:                   b.ID,
		Title:                b.Title,
		AuthorID:             b.AuthorID,
		Narrator:             b.Narrator,
		SeriesID:             b.SeriesID,
		Format:               b.Format,
		Duration:             b.Duration,
		FilePath:             b.FilePath,
		FileSize:             b.FileSize,
		VersionGroupID:       b.VersionGroupID,
		IsPrimaryVersion:     b.IsPrimaryVersion,
		WorkID:               b.WorkID,
		ITunesPersistentID:   b.ITunesPersistentID,
		AudiobookReleaseYear: b.AudiobookReleaseYear,
		Publisher:            b.Publisher,
		LibraryState:         b.LibraryState,
		MarkedForDeletion:    b.MarkedForDeletion,
	}
}

// systemInfo is the structure written to system_info.json.
type systemInfo struct {
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	GoVersion   string `json:"go_version"`
	BookCount   int    `json:"book_count"`
	AuthorCount int    `json:"author_count"`
	SeriesCount int    `json:"series_count"`
	Category    string `json:"category"`
	Description string `json:"description"`
}

// authorWithCounts extends Author with book/file counts.
type authorWithCounts struct {
	database.Author
	BookCount int `json:"book_count"`
	FileCount int `json:"file_count"`
}

// seriesWithCounts extends Series with book/file counts.
type seriesWithCounts struct {
	database.Series
	BookCount int `json:"book_count"`
	FileCount int `json:"file_count"`
}

// versionGroup represents a group of books sharing a version group ID.
type versionGroup struct {
	GroupID string     `json:"group_id"`
	Books   []SlimBook `json:"books"`
}

// ITunesAlbumSummary summarizes tracks grouped by album from iTunes XML.
type ITunesAlbumSummary struct {
	Album      string `json:"album"`
	TrackCount int    `json:"track_count"`
	Artist     string `json:"artist,omitempty"`
}

// missingFieldEntry records a book with missing metadata fields.
type missingFieldEntry struct {
	BookID        string   `json:"book_id"`
	Title         string   `json:"title"`
	MissingFields []string `json:"missing_fields"`
}

// ProgressFunc receives one call per export phase, in the shape the operations
// registry's Reporter.UpdateProgress takes. A nil ProgressFunc is allowed.
type ProgressFunc func(current, total int, message string)

// GenerateExport creates a temporary ZIP file containing diagnostic data.
// Category must be one of: "deduplication", "error_analysis", "metadata_quality", "general".
//
// REPORTING PROGRESS IS NOT COSMETIC HERE. This runs as the diagnostics.export
// operation, and the registry watchdog cancels any op that reports nothing for
// ProgressTimeout — which that def does not set, so it gets the 5-minute default
// while declaring a 30-minute Timeout. Before this took a ProgressFunc the whole
// export was one silent call, so every export slower than five minutes was
// killed, and because there was no ctx to observe the cancel it went right on
// building a ZIP nothing would ever serve.
//
// ctx is checked between phases. It cannot interrupt a phase already in flight
// (the write helpers take no ctx), which bounds the cancellation granularity at
// one file rather than the whole export.
func (ds *Service) GenerateExport(ctx context.Context, category, description string, onProgress ProgressFunc) (string, error) {
	if onProgress == nil {
		onProgress = func(int, int, string) {}
	}

	tmpFile, err := os.CreateTemp("", "diagnostics-*.zip")
	if err != nil {
		return "", err
	}
	zipPath := tmpFile.Name()
	zw := zip.NewWriter(tmpFile)

	// Every failure below discards the partial ZIP. This was nine identical
	// four-line blocks; a phase added without one would have leaked a temp file.
	fail := func(err error) (string, error) {
		zw.Close()
		tmpFile.Close()
		os.Remove(zipPath)
		return "", err
	}

	// Declared before the phase list so the closures capture the variable, and
	// assigned by the collect step below — that way the phase count, and so the
	// progress total, is known before any work starts.
	var allBooks []database.BookCore

	type phase struct {
		name string
		run  func() error
	}
	phases := []phase{
		{"system_info.json", func() error { return ds.writeSystemInfo(zw, category, description) }},
		{"books.json", func() error { return ds.writeBooks(zw, allBooks) }},
		{"authors.json", func() error { return ds.writeAuthors(zw) }},
		{"series.json", func() error { return ds.writeSeries(zw) }},
		{"batch.jsonl", func() error { return ds.writeBatchJSONL(zw, category, description, allBooks) }},
	}
	if category == "error_analysis" || category == "general" {
		phases = append(phases,
			phase{"logs", func() error { return ds.writeLogs(zw) }},
			phase{"operations.json", func() error { return ds.writeOperations(zw) }},
		)
	}
	if category == "deduplication" || category == "general" {
		phases = append(phases,
			phase{"version_groups.json", func() error { return ds.writeVersionGroups(zw, allBooks) }},
			phase{"itunes_albums.json", func() error { return ds.writeITunesAlbums(zw) }},
		)
	}
	if category == "metadata_quality" || category == "general" {
		phases = append(phases,
			phase{"missing_fields.json", func() error { return ds.writeMissingFields(zw, allBooks) }},
		)
	}

	// +1 for the book collect, which is a reported step of its own: it walks the
	// whole library and is typically the longest single stretch of the export.
	total := len(phases) + 1

	onProgress(0, total, "Collecting books")
	if allBooks, err = ds.CollectAllBooks(); err != nil {
		return fail(err)
	}

	for i, ph := range phases {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		onProgress(i+1, total, "Writing "+ph.name)
		if err := ph.run(); err != nil {
			return fail(fmt.Errorf("write %s: %w", ph.name, err))
		}
	}

	if err := zw.Close(); err != nil {
		tmpFile.Close()
		os.Remove(zipPath)
		return "", err
	}
	if err := tmpFile.Close(); err != nil {
		os.Remove(zipPath)
		return "", err
	}

	onProgress(total, total, "Export complete")
	return zipPath, nil
}

// CollectAllBooks paginates through GetAllBooksCore until no more results.
func (ds *Service) CollectAllBooks() ([]database.BookCore, error) {
	const pageSize = 10000
	var allBooks []database.BookCore
	offset := 0
	for {
		books, err := ds.db.GetAllBooksCore(pageSize, offset)
		if err != nil {
			return nil, err
		}
		if len(books) == 0 {
			break
		}
		allBooks = append(allBooks, books...)
		offset += pageSize
	}
	return allBooks, nil
}

// WriteJSON is a helper that marshals data and writes it into the ZIP.
func WriteJSON(zw *zip.Writer, filename string, data interface{}) error {
	w, err := zw.Create(filename)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(data)
}

// WriteRaw writes raw bytes into the ZIP.
func WriteRaw(zw *zip.Writer, filename string, data []byte) error {
	w, err := zw.Create(filename)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

func (ds *Service) writeSystemInfo(zw *zip.Writer, category, description string) error {
	bookCount, _ := ds.db.CountPrimaryBooks()
	authorCount, _ := ds.db.CountAuthors()
	seriesCount, _ := ds.db.CountSeries()

	info := systemInfo{
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		GoVersion:   runtime.Version(),
		BookCount:   bookCount,
		AuthorCount: authorCount,
		SeriesCount: seriesCount,
		Category:    category,
		Description: description,
	}
	return WriteJSON(zw, "system_info.json", info)
}

func (ds *Service) writeBooks(zw *zip.Writer, allBooks []database.BookCore) error {
	slim := make([]SlimBook, len(allBooks))
	for i, b := range allBooks {
		slim[i] = ToSlimBook(b)
	}
	return WriteJSON(zw, "books.json", slim)
}

func (ds *Service) writeAuthors(zw *zip.Writer) error {
	authors, err := ds.db.GetAllAuthors()
	if err != nil {
		return err
	}

	bookCounts, err := ds.db.GetAllAuthorBookCounts()
	if err != nil {
		bookCounts = map[int]int{}
	}

	fileCounts, err := ds.db.GetAllAuthorFileCounts()
	if err != nil {
		fileCounts = map[int]int{}
	}

	result := make([]authorWithCounts, len(authors))
	for i, a := range authors {
		result[i] = authorWithCounts{
			Author:    a,
			BookCount: bookCounts[a.ID],
			FileCount: fileCounts[a.ID],
		}
	}
	return WriteJSON(zw, "authors.json", result)
}

func (ds *Service) writeSeries(zw *zip.Writer) error {
	series, err := ds.db.GetAllSeries()
	if err != nil {
		return err
	}

	bookCounts, err := ds.db.GetAllSeriesBookCounts()
	if err != nil {
		bookCounts = map[int]int{}
	}

	fileCounts, err := ds.db.GetAllSeriesFileCounts()
	if err != nil {
		fileCounts = map[int]int{}
	}

	result := make([]seriesWithCounts, len(series))
	for i, s := range series {
		result[i] = seriesWithCounts{
			Series:    s,
			BookCount: bookCounts[s.ID],
			FileCount: fileCounts[s.ID],
		}
	}
	return WriteJSON(zw, "series.json", result)
}

// writeBatchJSONL writes a batch.jsonl file based on current category and books.
func (ds *Service) writeBatchJSONL(zw *zip.Writer, category, description string, allBooks []database.BookCore) error {
	slimBooks := make([]SlimBook, len(allBooks))
	for i, b := range allBooks {
		slimBooks[i] = ToSlimBook(b)
	}

	data, err := BuildBatchJSONL(category, description, slimBooks, nil, nil, nil)
	if err != nil {
		return err
	}
	return WriteRaw(zw, "batch.jsonl", data)
}

func (ds *Service) writeLogs(zw *zip.Writer) error {
	logs, err := ds.db.GetSystemActivityLogs("", 10000)
	if err != nil {
		logs = []database.SystemActivityLog{}
	}

	// Filter to last 24 hours
	cutoff := time.Now().Add(-24 * time.Hour)
	filtered := make([]database.SystemActivityLog, 0, len(logs))
	for _, log := range logs {
		if log.CreatedAt.After(cutoff) {
			filtered = append(filtered, log)
		}
	}

	return WriteJSON(zw, "logs.json", filtered)
}

func (ds *Service) writeOperations(zw *zip.Writer) error {
	ops, err := ds.db.GetRecentOperations(100)
	if err != nil {
		ops = []database.Operation{}
	}
	return WriteJSON(zw, "operations.json", ops)
}

func (ds *Service) writeVersionGroups(zw *zip.Writer, allBooks []database.BookCore) error {
	groups := map[string][]SlimBook{}
	for _, b := range allBooks {
		if b.VersionGroupID != nil && *b.VersionGroupID != "" {
			groups[*b.VersionGroupID] = append(groups[*b.VersionGroupID], ToSlimBook(b))
		}
	}

	result := make([]versionGroup, 0, len(groups))
	for gid, books := range groups {
		result = append(result, versionGroup{GroupID: gid, Books: books})
	}
	return WriteJSON(zw, "version_groups.json", result)
}

func (ds *Service) writeITunesAlbums(zw *zip.Writer) error {
	if ds.itunesXMLPath == "" {
		return WriteJSON(zw, "itunes_albums.json", []ITunesAlbumSummary{})
	}

	lib, err := itunes.ParseLibrary(ds.itunesXMLPath)
	if err != nil {
		// If parsing fails, write empty array
		return WriteJSON(zw, "itunes_albums.json", []ITunesAlbumSummary{})
	}

	albumMap := map[string]*ITunesAlbumSummary{}
	for _, track := range lib.Tracks {
		album := track.Album
		if album == "" {
			album = "(no album)"
		}
		if existing, ok := albumMap[album]; ok {
			existing.TrackCount++
		} else {
			albumMap[album] = &ITunesAlbumSummary{
				Album:      album,
				TrackCount: 1,
				Artist:     track.Artist,
			}
		}
	}

	result := make([]ITunesAlbumSummary, 0, len(albumMap))
	for _, summary := range albumMap {
		result = append(result, *summary)
	}
	return WriteJSON(zw, "itunes_albums.json", result)
}

func (ds *Service) writeMissingFields(zw *zip.Writer, allBooks []database.BookCore) error {
	var missing []missingFieldEntry
	for _, b := range allBooks {
		var fields []string
		if b.Title == "" {
			fields = append(fields, "title")
		}
		if b.AuthorID == nil {
			fields = append(fields, "author")
		}
		if b.SeriesID == nil {
			fields = append(fields, "series")
		}
		if len(fields) > 0 {
			missing = append(missing, missingFieldEntry{
				BookID:        b.ID,
				Title:         b.Title,
				MissingFields: fields,
			})
		}
	}
	if missing == nil {
		missing = []missingFieldEntry{}
	}
	return WriteJSON(zw, "missing_fields.json", missing)
}
