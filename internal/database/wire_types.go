// file: internal/database/wire_types.go
// version: 1.0.0
// guid: 6f2c8d15-3a70-4b94-8e1d-c5a09b7d2f36
// last-edited: 2026-10-18

package database

// Wire shapes that the generated TypeScript for BookCore reaches
// (web/src/types/generated/database.ts, from tygo.yaml). tygo generates only
// the files listed in include_files, and store.go holds the Store interfaces,
// which cannot be expressed in TypeScript, so the two struct types BookCore
// refers to from store.go live here instead. Keep this file to wire structs.

import "time"

// BookAuthor represents the many-to-many relationship between books and authors
type BookAuthor struct {
	BookID   string `json:"book_id"`
	AuthorID int    `json:"author_id"`
	Role     string `json:"role"`     // author, co-author, editor
	Position int    `json:"position"` // 0 = primary
}

// MetadataProvenanceEntry represents the source breakdown for a metadata field.
type MetadataProvenanceEntry struct {
	FileValue      any  `json:"file_value,omitempty"`
	FetchedValue   any  `json:"fetched_value,omitempty"`
	StoredValue    any  `json:"stored_value,omitempty"`
	OverrideValue  any  `json:"override_value,omitempty"`
	OverrideLocked bool `json:"override_locked"`
	// LockSource names a repair's lock ("repair:<op id>"); empty for a
	// person's lock (MetadataFieldState.LockSource).
	LockSource      string     `json:"lock_source,omitempty"`
	EffectiveValue  any        `json:"effective_value,omitempty"`
	EffectiveSource string     `json:"effective_source,omitempty"`
	ComparisonValue any        `json:"comparison_value,omitempty"`
	UpdatedAt       *time.Time `json:"updated_at,omitempty"`
}
