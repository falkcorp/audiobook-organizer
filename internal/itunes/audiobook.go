// file: internal/itunes/audiobook.go
// version: 0.1.0
// guid: 5d2a9c64-81e3-47b0-bf15-e0c73a46d982
// last-edited: 2026-11-21

package itunes

import "time"

// Author represents an audiobook author
type Author struct {
	ID   int    `json:"id" db:"id"`
	Name string `json:"name" db:"name"`
}

// Series represents an audiobook series
type Series struct {
	ID       int     `json:"id" db:"id"`
	Name     string  `json:"name" db:"name"`
	AuthorID *int    `json:"author_id" db:"author_id"`
	Author   *Author `json:"author,omitempty"`
}

// Audiobook represents an audiobook with all its metadata
type Audiobook struct {
	ID                   int     `json:"id" db:"id"`
	Title                string  `json:"title" db:"title"`
	AuthorID             *int    `json:"author_id" db:"author_id"`
	SeriesID             *int    `json:"series_id" db:"series_id"`
	SeriesSequence       *int    `json:"series_sequence" db:"series_sequence"`
	FilePath             string  `json:"file_path" db:"file_path"`
	OriginalFilename     *string `json:"original_filename" db:"original_filename"`
	Format               string  `json:"format" db:"format"`
	Duration             *int    `json:"duration" db:"duration"`
	Narrator             *string `json:"narrator" db:"narrator"`
	Edition              *string `json:"edition" db:"edition"`
	Description          *string `json:"description" db:"description"`
	Language             *string `json:"language" db:"language"`
	Publisher            *string `json:"publisher" db:"publisher"`
	PrintYear            *int    `json:"print_year" db:"print_year"`
	AudiobookReleaseYear *int    `json:"audiobook_release_year" db:"audiobook_release_year"`
	ISBN10               *string `json:"isbn10" db:"isbn10"`
	ISBN13               *string `json:"isbn13" db:"isbn13"`

	// iTunes import fields (preserved from iTunes library)
	ITunesPersistentID *string    `json:"itunes_persistent_id" db:"itunes_persistent_id"` // iTunes unique identifier
	ITunesDateAdded    *time.Time `json:"itunes_date_added" db:"itunes_date_added"`       // When added to iTunes
	ITunesPlayCount    *int       `json:"itunes_play_count" db:"itunes_play_count"`       // Number of plays in iTunes
	ITunesLastPlayed   *time.Time `json:"itunes_last_played" db:"itunes_last_played"`     // Last played in iTunes
	ITunesRating       *int       `json:"itunes_rating" db:"itunes_rating"`               // Rating (0-100 scale)
	ITunesBookmark     *int64     `json:"itunes_bookmark" db:"itunes_bookmark"`           // Playback position in ms
	ITunesImportSource *string    `json:"itunes_import_source" db:"itunes_import_source"` // Path to iTunes Library.xml

	// Media info fields (parsed from file metadata)
	Bitrate    *int    `json:"bitrate_kbps" db:"bitrate_kbps"`     // Bitrate in kbps
	Codec      *string `json:"codec" db:"codec"`                   // e.g., 'AAC', 'MP3', 'FLAC'
	SampleRate *int    `json:"sample_rate_hz" db:"sample_rate_hz"` // Sample rate in Hz
	Channels   *int    `json:"channels" db:"channels"`             // Number of audio channels
	BitDepth   *int    `json:"bit_depth" db:"bit_depth"`           // Bit depth (for lossless formats)
	Quality    *string `json:"quality" db:"quality"`               // Human-readable quality string

	// Version management
	IsPrimaryVersion *bool   `json:"is_primary_version" db:"is_primary_version"` // Mark preferred version
	VersionGroupID   *string `json:"version_group_id" db:"version_group_id"`     // Links versions together
	VersionNotes     *string `json:"version_notes" db:"version_notes"`           // e.g., 'Remastered 2020'

	// Related objects (populated via joins)
	Author *Author `json:"author,omitempty"`
	Series *Series `json:"series,omitempty"`
}
