// file: internal/openlibrary/types.go
// version: 1.0.1
// guid: a1b2c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d
// last-edited: 2026-09-02

package openlibrary

import "time"

// OLEdition represents an Open Library edition record from the data dump.
type OLEdition struct {
	Key         string   `json:"key"`
	Title       string   `json:"title"`
	ISBN10      []string `json:"isbn_10,omitempty"`
	ISBN13      []string `json:"isbn_13,omitempty"`
	Authors     []OLRef  `json:"authors,omitempty"`
	Publishers  []string `json:"publishers,omitempty"`
	PublishDate string   `json:"publish_date,omitempty"`
	Covers      []int    `json:"covers,omitempty"`
	Languages   []OLRef  `json:"languages,omitempty"`
	Works       []OLRef  `json:"works,omitempty"`
	Subjects    []string `json:"subjects,omitempty"`
	Description any      `json:"description,omitempty"` // can be string or {type, value}
}

// OLWork represents an Open Library work record from the data dump.
type OLWork struct {
	Key         string   `json:"key"`
	Title       string   `json:"title"`
	Authors     []OLRef  `json:"authors,omitempty"`
	Subjects    []string `json:"subjects,omitempty"`
	Description any      `json:"description,omitempty"`
	Covers      []int    `json:"covers,omitempty"`
}

// OLAuthor represents an Open Library author record from the data dump.
type OLAuthor struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	BirthDate string `json:"birth_date,omitempty"`
	DeathDate string `json:"death_date,omitempty"`
	Bio       any    `json:"bio,omitempty"`
	RemoteIDs any    `json:"remote_ids,omitempty"`
}

// OLRef is a reference to another Open Library entity (e.g., {key: "/authors/OL123A"}).
type OLRef struct {
	Key string `json:"key"`
}

// DumpTypeStatus tracks the status of a single dump type (editions, authors, works).
type DumpTypeStatus struct {
	Filename         string    `json:"filename,omitempty"`
	Date             string    `json:"date,omitempty"`
	DownloadProgress float64   `json:"download_progress"` // 0.0 to 1.0
	ImportProgress   float64   `json:"import_progress"`   // 0.0 to 1.0
	RecordCount      int64     `json:"record_count"`
	LinesProcessed   int64     `json:"lines_processed"`     // for resume support
	FileSize         int64     `json:"file_size,omitempty"` // dump file size at import time
	LastUpdated      time.Time `json:"last_updated"`
}

// DumpStatus holds the overall status of all dump types.
type DumpStatus struct {
	Editions DumpTypeStatus `json:"editions"`
	Authors  DumpTypeStatus `json:"authors"`
	Works    DumpTypeStatus `json:"works"`
}

// DescriptionText extracts a plain string from an OL description field,
// which may be a string or a {"type": "/type/text", "value": "..."} object.
func DescriptionText(desc any) string {
	if desc == nil {
		return ""
	}
	if s, ok := desc.(string); ok {
		return s
	}
	if m, ok := desc.(map[string]any); ok {
		if v, ok := m["value"].(string); ok {
			return v
		}
	}
	return ""
}
