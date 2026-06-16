// file: internal/metafetch/helpers.go
// version: 1.3.0
// guid: 9a0b1c2d-3e4f-5a6b-7c8d-9e0f1a2b3c4d

package metafetch

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func stringPtr(s string) *string {
	return &s
}

func intPtrHelper(i int) *int {
	return &i
}

func stripChapterFromTitle(title string) string {
	cleaned := title

	// Strip leading track/disc number prefixes from filenames
	// e.g. "01 - Title", "01. Title", "1 - Title", "123 - Title"
	trackNumPrefix := regexp.MustCompile(`^\d{1,3}\s*[-–.]\s*`)
	cleaned = trackNumPrefix.ReplaceAllString(cleaned, "")
	// e.g. "01 Title" (bare number prefix followed by non-numeric text)
	bareNumPrefix := regexp.MustCompile(`^\d{1,3}\s+`)
	if stripped := strings.TrimSpace(bareNumPrefix.ReplaceAllString(cleaned, "")); stripped != "" {
		cleaned = stripped
	}
	// e.g. "Track 01 - Title", "Track01 - Title"
	trackWordPrefix := regexp.MustCompile(`(?i)^[Tt]rack\s*\d+\s*[-–.]\s*`)
	cleaned = trackWordPrefix.ReplaceAllString(cleaned, "")
	// e.g. "Disc 1 - Title", "Disc01 - Title"
	discWordPrefix := regexp.MustCompile(`(?i)^[Dd]is[ck]\s*\d+\s*[-–.]\s*`)
	cleaned = discWordPrefix.ReplaceAllString(cleaned, "")

	// Strip leading bracketed series info like "[The Expanse 9.0]" or "[Series Name]"
	bracketPrefix := regexp.MustCompile(`^\[.*?\]\s*[-–]?\s*`)
	cleaned = bracketPrefix.ReplaceAllString(cleaned, "")

	// Strip trailing bracketed info like "Title [Unabridged]"
	bracketSuffix := regexp.MustCompile(`\s*\[.*?\]\s*$`)
	cleaned = bracketSuffix.ReplaceAllString(cleaned, "")

	// Common patterns for chapters/books/parts/volumes
	patterns := []string{
		`(?i)[,:\s]*-?\s*(?:Book|Chapter|Part|Volume|Vol\.?|Pt\.?)\s*\d+[\.\d]*\s*$`,
		`(?i)\s*\((?:Book|Chapter|Part|Volume)\s*\d+[\.\d]*\)`,
		`(?i)\s*#\d+[\.\d]*\s*$`,
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		cleaned = re.ReplaceAllString(cleaned, "")
	}

	// Strip audiobook qualifiers like "(Unabridged)", "(Abridged)", etc.
	qualifiers := regexp.MustCompile(`(?i)\s*\((un)?abridged\)`)
	cleaned = qualifiers.ReplaceAllString(cleaned, "")

	// Strip leading/trailing " - " artifacts from removals
	cleaned = strings.TrimLeft(cleaned, " -–")
	cleaned = strings.TrimRight(cleaned, " -–")
	cleaned = strings.TrimSpace(cleaned)

	// If stripping removed everything, return the original title
	if cleaned == "" {
		return strings.TrimSpace(title)
	}
	return cleaned
}

// stripSubtitle removes subtitle portions from a title, e.g.
// "Title: A Subtitle" -> "Title", "Title - A Subtitle" -> "Title".
// Returns the original title if no subtitle separator is found.
func stripSubtitle(title string) string {
	// Try colon separator first: "Title: Subtitle"
	if idx := strings.Index(title, ": "); idx > 0 {
		return strings.TrimSpace(title[:idx])
	}
	// Try dash separator: "Title - Subtitle"
	if idx := strings.Index(title, " - "); idx > 0 {
		return strings.TrimSpace(title[:idx])
	}
	// Try em-dash: "Title — Subtitle"
	if idx := strings.Index(title, " — "); idx > 0 {
		return strings.TrimSpace(title[:idx])
	}
	return title
}

// isProtectedPath returns true if the file path is within import or iTunes paths.
// Method on *Service so it uses mfs.db rather than database.GetGlobalStore
// (SERVER-GLOBAL-STORE-AUDIT phase 4).
func (mfs *Service) isProtectedPath(filePath string) bool {
	absPath, _ := filepath.Abs(filePath)

	// Check import paths
	if mfs != nil && mfs.db != nil {
		importPaths, err := mfs.db.GetAllImportPaths()
		if err == nil {
			for _, ip := range importPaths {
				ipAbs, _ := filepath.Abs(ip.Path)
				if strings.HasPrefix(absPath, ipAbs+"/") || absPath == ipAbs {
					return true
				}
			}
		}
	}

	// Check iTunes library paths
	if config.AppConfig.ITunes.LibraryReadPath != "" {
		itunesDir := filepath.Dir(config.AppConfig.ITunes.LibraryReadPath)
		itunesAbs, _ := filepath.Abs(itunesDir)
		if strings.HasPrefix(absPath, itunesAbs+"/") || absPath == itunesAbs {
			return true
		}
	}
	if config.AppConfig.ITunes.LibraryWritePath != "" {
		itunesDir := filepath.Dir(config.AppConfig.ITunes.LibraryWritePath)
		itunesAbs, _ := filepath.Abs(itunesDir)
		if strings.HasPrefix(absPath, itunesAbs+"/") || absPath == itunesAbs {
			return true
		}
	}

	// Also check if path contains "iTunes Media" as a safety net
	if strings.Contains(absPath, "iTunes Media") || strings.Contains(absPath, "iTunes%20Media") {
		return true
	}

	// Hard-block .failed/ quarantine folder.
	if strings.Contains(filepath.ToSlash(absPath), "/.failed/") {
		return true
	}

	return false
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// MetadataFieldState represents the state of a single metadata field.
type MetadataFieldState struct {
	FetchedValue   any       `json:"fetched_value,omitempty"`
	OverrideValue  any       `json:"override_value,omitempty"`
	OverrideLocked bool      `json:"override_locked"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// metadataFieldState is an alias for backward compatibility (deprecated).
type metadataFieldState = MetadataFieldState

func metadataStateKey(bookID string) string {
	return fmt.Sprintf("metadata_state_%s", bookID)
}

func decodeMetadataValue(raw *string) any {
	if raw == nil || *raw == "" {
		return nil
	}
	var value any
	if err := json.Unmarshal([]byte(*raw), &value); err != nil {
		return *raw
	}
	return value
}

func encodeMetadataValue(value any) (*string, error) {
	if value == nil {
		return nil, nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	encoded := string(data)
	return &encoded, nil
}

// These four helpers are *Service methods so they use mfs.db rather than
// the package global (SERVER-GLOBAL-STORE-AUDIT phase 4).
func (mfs *Service) loadLegacyMetadataState(bookID string) (map[string]metadataFieldState, error) {
	state := map[string]metadataFieldState{}
	if mfs == nil || mfs.db == nil {
		return state, fmt.Errorf("database not initialized")
	}

	pref, err := mfs.db.GetUserPreference(metadataStateKey(bookID))
	if err != nil {
		return state, err
	}
	if pref == nil || pref.Value == nil || *pref.Value == "" {
		return state, nil
	}

	if err := json.Unmarshal([]byte(*pref.Value), &state); err != nil {
		return state, fmt.Errorf("failed to parse metadata state: %w", err)
	}
	return state, nil
}

func (mfs *Service) loadMetadataState(bookID string) (map[string]metadataFieldState, error) {
	state := map[string]metadataFieldState{}
	if mfs == nil || mfs.db == nil {
		return state, fmt.Errorf("database not initialized")
	}

	stored, err := mfs.db.GetMetadataFieldStates(bookID)
	if err != nil {
		return state, err
	}
	for _, entry := range stored {
		state[entry.Field] = metadataFieldState{
			FetchedValue:   decodeMetadataValue(entry.FetchedValue),
			OverrideValue:  decodeMetadataValue(entry.OverrideValue),
			OverrideLocked: entry.OverrideLocked,
			UpdatedAt:      entry.UpdatedAt,
		}
	}
	if len(state) > 0 {
		return state, nil
	}

	legacy, err := mfs.loadLegacyMetadataState(bookID)
	if err != nil {
		return state, err
	}
	if len(legacy) == 0 {
		return state, nil
	}

	if err := mfs.saveMetadataState(bookID, legacy); err != nil {
				slog.Warn("failed to migrate legacy metadata state for", "id", bookID, "error", err)
	}
	return legacy, nil
}

func (mfs *Service) saveMetadataState(bookID string, state map[string]metadataFieldState) error {
	if mfs == nil || mfs.db == nil {
		return fmt.Errorf("database not initialized")
	}

	existing, err := mfs.db.GetMetadataFieldStates(bookID)
	if err != nil {
		return err
	}
	existingFields := map[string]struct{}{}
	for _, entry := range existing {
		existingFields[entry.Field] = struct{}{}
	}

	now := time.Now()
	for field, entry := range state {
		fetched, err := encodeMetadataValue(entry.FetchedValue)
		if err != nil {
			return fmt.Errorf("failed to encode fetched metadata for %s: %w", field, err)
		}
		override, err := encodeMetadataValue(entry.OverrideValue)
		if err != nil {
			return fmt.Errorf("failed to encode override metadata for %s: %w", field, err)
		}
		if entry.UpdatedAt.IsZero() {
			entry.UpdatedAt = now
		}

		dbState := database.MetadataFieldState{
			BookID:         bookID,
			Field:          field,
			FetchedValue:   fetched,
			OverrideValue:  override,
			OverrideLocked: entry.OverrideLocked,
			UpdatedAt:      entry.UpdatedAt,
		}

		if err := mfs.db.UpsertMetadataFieldState(&dbState); err != nil {
			return fmt.Errorf("failed to persist metadata state for %s: %w", field, err)
		}
		delete(existingFields, field)
	}

	for field := range existingFields {
		if err := mfs.db.DeleteMetadataFieldState(bookID, field); err != nil {
			return fmt.Errorf("failed to clean up metadata state for %s: %w", field, err)
		}
	}

	return nil
}

func (mfs *Service) updateFetchedMetadataState(bookID string, values map[string]any) error {
	state, err := mfs.loadMetadataState(bookID)
	if err != nil {
		return err
	}
	if state == nil {
		state = map[string]metadataFieldState{}
	}
	for field, value := range values {
		entry := state[field]
		entry.FetchedValue = value
		entry.UpdatedAt = time.Now()
		state[field] = entry
	}
	return mfs.saveMetadataState(bookID, state)
}

func stringVal(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

func intVal(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}
