// file: internal/itunes/service/types.go
// version: 1.3.0
// guid: 43dcecba-4cba-4139-bd4c-5047a9a1f0c0

package itunesservice

import "github.com/falkcorp/audiobook-organizer/internal/database"

// BookOrganizer is the narrow interface the import pipeline needs from
// internal/organizer. Injected via Deps.OrganizerFactory so this
// package never imports internal/config.
type BookOrganizer interface {
	OrganizeBook(book *database.Book) (newPath, sidecar string, err error)
	// OrganizeBookDirectory organizes a multi-file (merged) book by moving
	// each of its per-track segment paths into the book's target directory.
	// Returns the new target directory and a map of old segment path ->
	// new segment path so callers can update per-file records.
	OrganizeBookDirectory(book *database.Book, segmentPaths []string) (targetDir string, pathMap map[string]string, err error)
}

// ValidateRequest is the wire type for POST /itunes/validate.
type ValidateRequest struct {
	LibraryPath  string        `json:"library_path" binding:"required"`
	PathMappings []PathMapping `json:"path_mappings,omitempty"`
}

// ValidateResponse summarises validation results.
type ValidateResponse struct {
	TotalTracks     int      `json:"total_tracks"`
	AudiobookTracks int      `json:"audiobook_tracks"`
	AudiobookCount  int      `json:"audiobook_count"`
	FilesFound      int      `json:"files_found"`
	FilesMissing    int      `json:"files_missing"`
	MissingPaths    []string `json:"missing_paths,omitempty"`
	PathPrefixes    []string `json:"path_prefixes,omitempty"`
	DuplicateCount  int      `json:"duplicate_count"`
	EstimatedTime   string   `json:"estimated_import_time"`
}

// TestMappingRequest is the wire type for POST /itunes/test-mapping.
type TestMappingRequest struct {
	LibraryPath string `json:"library_path" binding:"required"`
	From        string `json:"from" binding:"required"`
	To          string `json:"to" binding:"required"`
}

// TestMappingResponse is returned by POST /itunes/test-mapping.
type TestMappingResponse struct {
	Tested   int               `json:"tested"`
	Found    int               `json:"found"`
	Examples []TestMappingItem `json:"examples"`
}

// TestMappingItem is a single found-file example inside TestMappingResponse.
type TestMappingItem struct {
	Title string `json:"title"`
	Path  string `json:"path"`
}

// ImportRequest is the wire type for POST /itunes/import.
type ImportRequest struct {
	LibraryPath      string        `json:"library_path" binding:"required"`
	ImportMode       string        `json:"import_mode" binding:"required,oneof=organized import organize"`
	PreserveLocation bool          `json:"preserve_location"`
	ImportPlaylists  bool          `json:"import_playlists"`
	SkipDuplicates   bool          `json:"skip_duplicates"`
	FetchMetadata    bool          `json:"fetch_metadata"`
	PathMappings     []PathMapping `json:"path_mappings,omitempty"`
}

// ImportResponse acknowledges a queued import.
type ImportResponse struct {
	OperationID string `json:"operation_id"`
	Status      string `json:"status"`
	Message     string `json:"message"`
}

// WriteBackRequest is the wire type for POST /itunes/write-back.
type WriteBackRequest struct {
	LibraryPath  string        `json:"library_path"`
	AudiobookIDs []string      `json:"audiobook_ids"`
	PathMappings []PathMapping `json:"path_mappings,omitempty"`
}

// WriteBackResponse reports the result of a write-back.
type WriteBackResponse struct {
	Success      bool   `json:"success"`
	UpdatedCount int    `json:"updated_count"`
	Message      string `json:"message"`
}

// BookMapping is a single book-to-iTunes-path mapping (used in preview).
type BookMapping struct {
	BookID             string `json:"book_id"`
	Title              string `json:"title"`
	Author             string `json:"author"`
	ITunesPersistentID string `json:"itunes_persistent_id"`
	LocalPath          string `json:"local_path"`
	ITunesPath         string `json:"itunes_path,omitempty"`
	PathDiffers        bool   `json:"path_differs,omitempty"`
}

// WriteBackPreviewRequest is the wire type for POST /itunes/write-back-preview.
type WriteBackPreviewRequest struct {
	LibraryPath string   `json:"library_path" binding:"required"`
	BookIDs     []string `json:"book_ids,omitempty"`
}

// WriteBackPreviewResponse is returned by POST /itunes/write-back-preview.
type WriteBackPreviewResponse struct {
	Items []BookMapping `json:"items"`
	Total int           `json:"total"`
}

// ImportStatusResponse is returned by GET /itunes/import/:id.
type ImportStatusResponse struct {
	OperationID string   `json:"operation_id"`
	Status      string   `json:"status"`
	Progress    int      `json:"progress"`
	Message     string   `json:"message"`
	TotalBooks  int      `json:"total_books"`
	Processed   int      `json:"processed"`
	Imported    int      `json:"imported"`
	Skipped     int      `json:"skipped"`
	Failed      int      `json:"failed"`
	Errors      []string `json:"errors,omitempty"`
}

// ImportStatusSnapshot is an exported counter snapshot returned by
// Importer.GetStatus and Importer.GetStatusBulk. The unexported
// itunesImportStatus remains internal; callers use this type.
type ImportStatusSnapshot struct {
	Total     int
	Processed int
	Imported  int
	Skipped   int
	Linked    int
	Failed    int
	Errors    []string
}

// SyncRequest is the wire type for POST /itunes/sync.
type SyncRequest struct {
	LibraryPath  string        `json:"library_path,omitempty"`
	PathMappings []PathMapping `json:"path_mappings,omitempty"`
	Force        bool          `json:"force,omitempty"`
}

// SyncResponse acknowledges a queued sync.
type SyncResponse struct {
	OperationID string `json:"operation_id"`
	Message     string `json:"message"`
}
