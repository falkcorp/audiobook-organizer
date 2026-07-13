// file: internal/activity/changelog.go
// version: 1.4.0
// guid: 93167949-a587-41e9-8ef9-92d03f86aea6
// last-edited: 2026-07-13

package activity

import (
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// changelogStore is the narrow slice of database.Store this service uses.
type changelogStore interface {
	database.MetadataStore
	database.OperationStore
	database.PathHistoryStore
}

// ChangeLogEntry represents a single entry in a book's changelog timeline.
type ChangeLogEntry struct {
	Timestamp time.Time      `json:"timestamp"`
	Type      string         `json:"type"` // tag_write, rename, metadata_apply, import, transcode
	Summary   string         `json:"summary"`
	Details   map[string]any `json:"details,omitempty"`
}

// ChangelogService merges history data from multiple sources into a unified changelog.
type ChangelogService struct {
	db changelogStore
}

// NewChangelogService creates a new ChangelogService instance.
func NewChangelogService(db changelogStore) *ChangelogService {
	return &ChangelogService{db: db}
}

// MaxChangelogEntries is the maximum number of entries returned by GetBookChangelog.
const MaxChangelogEntries = 50

// GetBookChangelog returns a merged, time-sorted changelog for the given book.
// It pulls data from book_path_history (renames), metadata_change_history
// (metadata applies/tag writes), and operation_changes (imports/transcodes).
func (svc *ChangelogService) GetBookChangelog(bookID string) ([]ChangeLogEntry, error) {
	if svc.db == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	var entries []ChangeLogEntry

	// 1. Path history → rename entries
	pathHistory, err := svc.db.GetBookPathHistory(bookID)
	if err != nil {
		slog.Warn("changelog GetBookPathHistory", "bookID", bookID, "err", err)
	} else {
		for _, ph := range pathHistory {
			entryType, summary := pathChangeEntry(ph)
			entries = append(entries, ChangeLogEntry{
				Timestamp: ph.CreatedAt,
				Type:      entryType,
				Summary:   summary,
				Details: map[string]any{
					"old_path":    ph.OldPath,
					"new_path":    ph.NewPath,
					"change_type": ph.ChangeType,
				},
			})
		}
	}

	// 2. Metadata change history → metadata_apply and tag_write entries
	metaHistory, err := svc.db.GetBookChangeHistory(bookID, 100)
	if err != nil {
		slog.Warn("changelog GetBookChangeHistory", "bookID", bookID, "err", err)
	} else {
		for _, mh := range metaHistory {
			entryType := "metadata_apply"
			summary := fmt.Sprintf("Metadata applied — %s: %s (%s)", mh.Field, DerefStrDisplay(mh.NewValue), mh.Source)

			if mh.ChangeType == "override" || mh.ChangeType == "clear" || mh.ChangeType == "undo" {
				entryType = "tag_write"
				summary = fmt.Sprintf("Tag written — %s set to %s (%s)", mh.Field, DerefStrDisplay(mh.NewValue), mh.ChangeType)
			}

			details := map[string]any{
				"field":       mh.Field,
				"change_type": mh.ChangeType,
				"source":      mh.Source,
			}
			if mh.PreviousValue != nil {
				details["previous_value"] = *mh.PreviousValue
			}
			if mh.NewValue != nil {
				details["new_value"] = *mh.NewValue
			}

			entries = append(entries, ChangeLogEntry{
				Timestamp: mh.ChangedAt,
				Type:      entryType,
				Summary:   summary,
				Details:   details,
			})
		}
	}

	// 3. Operation changes → import and transcode entries
	opChanges, err := svc.db.GetBookChanges(bookID)
	if err != nil {
		slog.Warn("changelog GetBookChanges", "bookID", bookID, "err", err)
	} else {
		for _, oc := range opChanges {
			entryType := "import"
			summary := fmt.Sprintf("Operation change — %s: %s → %s", oc.FieldName, oc.OldValue, oc.NewValue)

			switch oc.ChangeType {
			case "file_move":
				entryType = "rename"
				summary = fmt.Sprintf("File moved — %s → %s", oc.OldValue, oc.NewValue)
			case "organize_rename":
				entryType = "rename"
				summary = fmt.Sprintf("Organized — %s → %s", oc.OldValue, oc.NewValue)
			case "book_create":
				entryType = "import"
				summary = "Organized version created"
			case "tag_write":
				entryType = "tag_write"
				summary = fmt.Sprintf("Tags written — %s: %s → %s", oc.FieldName, oc.OldValue, oc.NewValue)
			case "metadata_update":
				entryType = "metadata_apply"
				summary = fmt.Sprintf("Metadata updated — %s: %s → %s", oc.FieldName, oc.OldValue, oc.NewValue)
			}

			details := map[string]any{
				"operation_id": oc.OperationID,
				"change_type":  oc.ChangeType,
				"field_name":   oc.FieldName,
				"old_value":    oc.OldValue,
				"new_value":    oc.NewValue,
			}
			if oc.RevertedAt != nil {
				details["reverted_at"] = oc.RevertedAt
			}

			entries = append(entries, ChangeLogEntry{
				Timestamp: oc.CreatedAt,
				Type:      entryType,
				Summary:   summary,
				Details:   details,
			})
		}
	}

	// Sort by timestamp descending (newest first)
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Timestamp.After(entries[j].Timestamp)
	})

	// Limit to MaxChangelogEntries
	if len(entries) > MaxChangelogEntries {
		entries = entries[:MaxChangelogEntries]
	}

	return entries, nil
}

// pathChangeEntry maps a BookPathChange to a changelog (type, summary) pair.
//
// The change-log frontend (web/src/components/ChangeLog.tsx) only has icons/labels
// for a fixed set of types — rename (📁), import (📦), metadata_apply, tag_write,
// transcode — so this deliberately emits only "import" or "rename" and encodes the
// specific verb in the summary. A "label — detail" em-dash separator matches the
// sibling summaries ("Metadata applied — …").
//
// The historical bug: this loop hardcoded Type "rename" and "Renamed — <old> → <new>"
// for EVERY record, so an import record (OldPath == "", ChangeType "import" written by
// PebbleStore.CreateBook) rendered as "Renamed — → /newpath" — a phantom rename with
// an empty "from". Import records now render as "Imported — /newpath".
func pathChangeEntry(ph database.BookPathChange) (entryType, summary string) {
	// Import records legitimately have no source path (the file was ingested in
	// place, e.g. by the scanner or iTunes importer). Treat any empty-OldPath row
	// as an import regardless of the stored ChangeType.
	if ph.ChangeType == "import" || ph.OldPath == "" {
		return "import", fmt.Sprintf("Imported — %s", ph.NewPath)
	}

	verb := "Moved"
	switch ph.ChangeType {
	case "organize":
		verb = "Organized"
	case "rename":
		verb = "Renamed"
	case "library_copy":
		verb = "Copied to library"
	case "version_swap":
		verb = "Version swapped"
	case "quarantine":
		verb = "Quarantined"
	case "unquarantine":
		verb = "Restored from quarantine"
	case "itunes_path_repair":
		verb = "Path repaired"
	}
	return "rename", fmt.Sprintf("%s — %s → %s", verb, ph.OldPath, ph.NewPath)
}

// DerefStrDisplay safely dereferences a *string, returning "<nil>" for nil pointers (display-oriented).
func DerefStrDisplay(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}
