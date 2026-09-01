// file: internal/metafetch/helpers.go
// version: 1.5.0
// guid: 9a0b1c2d-3e4f-5a6b-7c8d-9e0f1a2b3c4d
// last-edited: 2026-09-01

package metafetch

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/metastate"

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

// These four helpers are *Service methods so they use mfs.db rather than
// the package global (SERVER-GLOBAL-STORE-AUDIT phase 4).
func (mfs *Service) loadLegacyMetadataState(bookID string) (map[string]metadataFieldState, error) {
	state := map[string]metadataFieldState{}
	if mfs == nil || mfs.db == nil {
		return state, fmt.Errorf("database not initialized")
	}

	pref, err := mfs.db.GetUserPreference(metastate.Key(bookID))
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
			FetchedValue:   metastate.Decode(entry.FetchedValue),
			OverrideValue:  metastate.Decode(entry.OverrideValue),
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
		fetched, err := metastate.Encode(entry.FetchedValue)
		if err != nil {
			return fmt.Errorf("failed to encode fetched metadata for %s: %w", field, err)
		}
		override, err := metastate.Encode(entry.OverrideValue)
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

// dedupeBookFilesByPath collapses book_file rows that share the same cleaned
// absolute path, keeping the best-evidenced row per path.
//
// This is DUPROW-1, the fix for the 2026-08-21 prod incident: book
// 01KZR9GEH5ZQW9CV1EN130Y7C0 held 42 book_file rows for 21 distinct paths.
// Every apply-pipeline load site fed the raw, duplicated row list straight
// into its downstream logic, so every file's tags were written twice
// ("wrote metadata back to" logged twice per path), the pathOrganizer
// computed 42 target paths for 21 files ("file naming pattern does not
// distinguish... files=42"), and the second rename pass for a given path
// failed with "stat rename source ...: no such file or directory" because
// the first pass had already moved the file out from under it. Calling this
// once, immediately after every GetBookFiles load in the apply pipeline,
// makes it impossible for one path to be processed twice in a single run.
//
// The keeper preference order below is copied by hand from the repo's
// existing, data-loss-reviewed ranking in
// internal/plugins/maintenance/dedupe_book_file_rows.go's rankKeeper: a
// fingerprint costs a full-file decode and cannot be guessed back, so it
// outranks everything; then a known duration; then a file hash; then the
// lexicographically smallest ID, so a dry run and the run that follows it
// pick the same survivor. rankKeeper is unexported in another package and
// cannot be imported here — if either order changes, the other must change
// with it.
func dedupeBookFilesByPath(bookID string, files []database.BookFile) []database.BookFile {
	if len(files) < 2 {
		return files
	}
	// better reports whether cand should replace cur as the keeper for a path.
	// SAME ORDER as maintenance.rankKeeper (see doc comment above).
	better := func(cand, cur database.BookFile) bool {
		cf, uf := len(cand.AcoustIDFingerprint) > 0, len(cur.AcoustIDFingerprint) > 0
		if cf != uf {
			return cf
		}
		cd, ud := cand.Duration > 0, cur.Duration > 0
		if cd != ud {
			return cd
		}
		ch, uh := strings.TrimSpace(cand.FileHash) != "", strings.TrimSpace(cur.FileHash) != ""
		if ch != uh {
			return ch
		}
		return cand.ID < cur.ID
	}

	idx := make(map[string]int, len(files))
	out := make([]database.BookFile, 0, len(files))
	for _, f := range files {
		key := strings.TrimSpace(f.FilePath)
		if key == "" {
			// A pathless row is not a duplicate of anything; pass it through
			// so this helper never changes which rows exist, only how many
			// twins of a real path do. Downstream (internal/organizer/pipeline.go)
			// already drops empty-path rows.
			out = append(out, f)
			continue
		}
		// Byte-exact comparison after Clean/TrimSpace — do NOT lowercase. The
		// library lives on a case-sensitive NAS mount, so two case-different
		// paths are two real files, not duplicates.
		key = filepath.Clean(key)
		if at, seen := idx[key]; seen {
			if better(f, out[at]) {
				out[at] = f
			}
			continue
		}
		idx[key] = len(out)
		out = append(out, f)
	}

	if len(out) != len(files) {
		slog.Warn("duplicate book_file rows collapsed",
			"book_id", bookID, "rows", len(files), "distinct", len(out),
			"collapsed", len(files)-len(out))
	}
	return out
}
