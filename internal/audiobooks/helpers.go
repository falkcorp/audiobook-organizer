// file: internal/audiobooks/helpers.go
// version: 1.2.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234560010
// last-edited: 2026-07-01
//
// Private utilities needed by the audiobooks service package. These mirror
// equivalent helpers from internal/server/ but are standalone so that the
// audiobooks package does not import internal/server (which would cycle).

package audiobooks

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// --- basic pointer helpers --------------------------------------------------

// stringPtr returns a pointer to s.
func stringPtr(s string) *string { return &s }

func boolPtr(b bool) *bool { return &b }

func ptrStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// --- metadata field state ---------------------------------------------------

// metadataFieldState represents the persisted state of one metadata field.
type metadataFieldState struct {
	FetchedValue   any       `json:"fetched_value,omitempty"`
	OverrideValue  any       `json:"override_value,omitempty"`
	OverrideLocked bool      `json:"override_locked"`
	UpdatedAt      time.Time `json:"updated_at"`
}

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

func decodeRawValue(raw json.RawMessage) any {
	if raw == nil {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return string(raw)
	}
	return value
}

// loadLegacyMetadataState / loadMetadataState / saveMetadataState are now
// methods on *AudiobookService so they read/write through svc.store
// rather than the package-level GetGlobalStore (SERVER-GLOBAL-STORE-AUDIT
// phase 6). Nil-safe: a zero-value AudiobookService falls back to
// "database not initialized" same as the old GetGlobalStore == nil path.

func (svc *AudiobookService) loadLegacyMetadataState(bookID string) (map[string]metadataFieldState, error) {
	state := map[string]metadataFieldState{}
	if svc == nil || svc.store == nil {
		return state, nil
	}
	pref, err := svc.store.GetUserPreference(metadataStateKey(bookID))
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

func (svc *AudiobookService) loadMetadataState(bookID string) (map[string]metadataFieldState, error) {
	state := map[string]metadataFieldState{}
	if svc == nil || svc.store == nil {
		return state, fmt.Errorf("database not initialized")
	}
	stored, err := svc.store.GetMetadataFieldStates(bookID)
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
	legacy, err := svc.loadLegacyMetadataState(bookID)
	if err != nil {
		return state, err
	}
	if len(legacy) == 0 {
		return state, nil
	}
	if err := svc.saveMetadataState(bookID, legacy); err != nil {
		slog.Warn("failed to migrate legacy metadata state for", "bookID", bookID, "err", err)
	}
	return legacy, nil
}

func (svc *AudiobookService) saveMetadataState(bookID string, state map[string]metadataFieldState) error {
	if svc == nil || svc.store == nil {
		return fmt.Errorf("database not initialized")
	}
	existing, err := svc.store.GetMetadataFieldStates(bookID)
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
		if err := svc.store.UpsertMetadataFieldState(&dbState); err != nil {
			return fmt.Errorf("failed to persist metadata state for %s: %w", field, err)
		}
		delete(existingFields, field)
	}
	for field := range existingFields {
		if err := svc.store.DeleteMetadataFieldState(bookID, field); err != nil {
			return fmt.Errorf("failed to clean up metadata state for %s: %w", field, err)
		}
	}
	return nil
}

// --- metadata state change recorder ----------------------------------------

// metadataStateStore is the narrow interface needed for recording metadata changes.
type metadataStateStore interface {
	database.MetadataStore
	database.UserPreferenceStore
}

// metadataStateSvc records metadata change history. It is an internal helper
// used only by AudiobookService.UpdateAudiobook.
type metadataStateSvc struct {
	db metadataStateStore
}

func newMetadataStateSvc(db metadataStateStore) *metadataStateSvc {
	return &metadataStateSvc{db: db}
}

func (mss *metadataStateSvc) recordChange(bookID, field, changeType, source string, previousValue, newValue any) {
	if mss.db == nil {
		return
	}
	prev, _ := encodeMetadataValue(previousValue)
	next, _ := encodeMetadataValue(newValue)
	record := &database.MetadataChangeRecord{
		BookID:        bookID,
		Field:         field,
		PreviousValue: prev,
		NewValue:      next,
		ChangeType:    changeType,
		Source:        source,
		ChangedAt:     time.Now(),
	}
	if err := mss.db.RecordMetadataChange(record); err != nil {
		slog.Warn("failed to record metadata change for /", "bookID", bookID, "field", field, "err", err)
	}
}

// --- path helpers -----------------------------------------------------------

// importPathLister is the narrow slice isProtectedPath needs from any
// store: just GetAllImportPaths. Both database.Store and the audiobook
// service's narrower audiobookStore satisfy it.
type importPathLister interface {
	GetAllImportPaths() ([]database.ImportPath, error)
}

// importPathCacheTTLForHelper controls how long cachedImportPathsForHelper
// reuses a previously fetched import-path list before re-querying the
// store. Import paths change extremely infrequently (an admin action via
// the settings UI), so a short TTL-only cache is sufficient here — no
// invalidation hook is wired into the import-path mutation endpoints
// (MAYDEPLOY-H7). Not a const so tests can shrink it.
var importPathCacheTTLForHelper = 5 * time.Second

var (
	importPathCacheForHelperMu sync.Mutex
	importPathCacheForHelper   []database.ImportPath
	importPathCacheForHelperAt time.Time
)

// cachedImportPathsForHelper returns store.GetAllImportPaths(), reusing the
// previous result if it was fetched within importPathCacheTTLForHelper.
func cachedImportPathsForHelper(store importPathLister) ([]database.ImportPath, error) {
	importPathCacheForHelperMu.Lock()
	defer importPathCacheForHelperMu.Unlock()
	if time.Since(importPathCacheForHelperAt) < importPathCacheTTLForHelper {
		return importPathCacheForHelper, nil
	}
	paths, err := store.GetAllImportPaths()
	if err != nil {
		return nil, err
	}
	importPathCacheForHelper = paths
	importPathCacheForHelperAt = time.Now()
	return paths, nil
}

// isProtectedPath returns true if filePath is under a configured import
// path, an iTunes library path, or another protected location. Takes an
// explicit importPathLister so callers thread their own database
// reference rather than reaching for the package global
// (SERVER-GLOBAL-STORE-AUDIT phase 6). Pass nil to skip the import-path
// check; the iTunes / .failed checks still apply.
func isProtectedPath(store importPathLister, filePath string) bool {
	absPath, _ := filepath.Abs(filePath)

	if store != nil {
		importPaths, err := cachedImportPathsForHelper(store)
		if err == nil {
			for _, ip := range importPaths {
				ipAbs, _ := filepath.Abs(ip.Path)
				if strings.HasPrefix(absPath, ipAbs+"/") || absPath == ipAbs {
					return true
				}
			}
		}
	}

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

	if strings.Contains(absPath, "iTunes Media") || strings.Contains(absPath, "iTunes%20Media") {
		return true
	}

	if strings.Contains(filepath.ToSlash(absPath), "/.failed/") {
		return true
	}

	return false
}

// resolveAuthorAndSeriesNames returns the author name and series name
// for book, falling back to a database lookup when the join is not
// pre-loaded. Takes an explicit store (SERVER-GLOBAL-STORE-AUDIT phase 6).
// Nil store skips the lookups; inline Book.Author / Book.Series still
// resolve.
func resolveAuthorAndSeriesNames(store authorSeriesStore, book *database.Book) (string, string) {
	authorName := ""
	if book.Author != nil {
		authorName = book.Author.Name
	} else if book.AuthorID != nil && store != nil {
		if author, err := store.GetAuthorByID(*book.AuthorID); err == nil && author != nil {
			authorName = author.Name
		}
	}
	seriesName := ""
	if book.Series != nil {
		seriesName = book.Series.Name
	} else if book.SeriesID != nil && store != nil {
		if series, err := store.GetSeriesByID(*book.SeriesID); err == nil && series != nil {
			seriesName = series.Name
		}
	}
	return authorName, seriesName
}

// --- external ID store helper -----------------------------------------------

// ExternalIDStore defines the external-ID mapping operations used by
// AudiobookService.DeleteAudiobook.
type ExternalIDStore interface {
	CreateExternalIDMapping(mapping *database.ExternalIDMapping) error
	GetBookByExternalID(source, externalID string) (string, error)
	GetExternalIDsForBook(bookID string) ([]database.ExternalIDMapping, error)
	IsExternalIDTombstoned(source, externalID string) (bool, error)
	TombstoneExternalID(source, externalID string) error
	ReassignExternalIDs(oldBookID, newBookID string) error
	BulkCreateExternalIDMappings(mappings []database.ExternalIDMapping) error
}

// asExternalIDStore type-asserts s to ExternalIDStore, returning nil on failure.
func asExternalIDStore(s any) ExternalIDStore {
	if s == nil {
		return nil
	}
	if eid, ok := s.(ExternalIDStore); ok {
		return eid
	}
	return nil
}

// --- value helpers ----------------------------------------------------------

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

func nonEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// buildMetadataProvenance constructs a per-field provenance map for the
// audiobook metadata panel, combining file-extracted, fetched, stored, and
// override values with their effective resolution.
func buildMetadataProvenance(book *database.Book, state map[string]metadataFieldState, meta metadata.Metadata, authorName, seriesName string, comparisonValues map[string]any) map[string]database.MetadataProvenanceEntry {
	if state == nil {
		state = map[string]metadataFieldState{}
	}

	provenance := map[string]database.MetadataProvenanceEntry{}

	addEntry := func(field string, fileValue any, storedValue any) {
		entryState := state[field]
		effectiveSource := ""
		var effectiveValue any
		switch {
		case entryState.OverrideValue != nil:
			effectiveSource = "override"
			effectiveValue = entryState.OverrideValue
		case storedValue != nil:
			effectiveSource = "stored"
			effectiveValue = storedValue
		case entryState.FetchedValue != nil:
			effectiveSource = "fetched"
			effectiveValue = entryState.FetchedValue
		case fileValue != nil:
			effectiveSource = "file"
			effectiveValue = fileValue
		}

		var updatedAt *time.Time
		if !entryState.UpdatedAt.IsZero() {
			ts := entryState.UpdatedAt.UTC()
			updatedAt = &ts
		}

		entry := database.MetadataProvenanceEntry{
			FileValue:       fileValue,
			FetchedValue:    entryState.FetchedValue,
			StoredValue:     storedValue,
			OverrideValue:   entryState.OverrideValue,
			OverrideLocked:  entryState.OverrideLocked,
			EffectiveValue:  effectiveValue,
			EffectiveSource: effectiveSource,
			UpdatedAt:       updatedAt,
		}

		if comparisonValues != nil {
			if cv, ok := comparisonValues[field]; ok {
				entry.ComparisonValue = cv
			}
		}

		provenance[field] = entry
	}

	addEntry("title", meta.Title, book.Title)
	addEntry("author_name", meta.Artist, authorName)
	addEntry("narrator", meta.Narrator, stringVal(book.Narrator))
	addEntry("series_name", meta.Series, seriesName)
	addEntry("publisher", meta.Publisher, stringVal(book.Publisher))
	addEntry("language", meta.Language, stringVal(book.Language))
	addEntry("audiobook_release_year", meta.Year, intVal(book.AudiobookReleaseYear))
	addEntry("isbn10", meta.ISBN10, stringVal(book.ISBN10))
	addEntry("isbn13", meta.ISBN13, stringVal(book.ISBN13))
	addEntry("genre", meta.Genre, stringVal(book.Genre))
	addEntry("album", meta.Album, book.Title)
	addEntry("asin", nonEmpty(meta.ASIN), stringVal(book.ASIN))
	var seriesIdx any
	if meta.SeriesIndex > 0 {
		seriesIdx = meta.SeriesIndex
	}
	addEntry("series_index", seriesIdx, intVal(book.SeriesSequence))
	addEntry("print_year", nonEmpty(meta.PrintYear), intVal(book.PrintYear))
	addEntry("edition", nonEmpty(meta.Edition), stringVal(book.Edition))
	addEntry("description", nonEmpty(meta.Comments), stringVal(book.Description))
	addEntry("book_id", nonEmpty(meta.BookOrganizerID), book.ID)
	addEntry("open_library_id", nonEmpty(meta.OpenLibraryID), stringVal(book.OpenLibraryID))
	addEntry("hardcover_id", nonEmpty(meta.HardcoverID), stringVal(book.HardcoverID))
	addEntry("google_books_id", nonEmpty(meta.GoogleBooksID), stringVal(book.GoogleBooksID))

	return provenance
}

func buildComparisonValuesFromMetadata(comparisonMeta *metadata.Metadata) map[string]any {
	if comparisonMeta == nil {
		return nil
	}
	compMap := map[string]any{
		"title":           nonEmpty(comparisonMeta.Title),
		"author_name":     nonEmpty(comparisonMeta.Artist),
		"narrator":        nonEmpty(comparisonMeta.Narrator),
		"series_name":     nonEmpty(comparisonMeta.Series),
		"publisher":       nonEmpty(comparisonMeta.Publisher),
		"language":        nonEmpty(comparisonMeta.Language),
		"isbn10":          nonEmpty(comparisonMeta.ISBN10),
		"isbn13":          nonEmpty(comparisonMeta.ISBN13),
		"genre":           nonEmpty(comparisonMeta.Genre),
		"album":           nonEmpty(comparisonMeta.Album),
		"asin":            nonEmpty(comparisonMeta.ASIN),
		"edition":         nonEmpty(comparisonMeta.Edition),
		"print_year":      nonEmpty(comparisonMeta.PrintYear),
		"description":     nonEmpty(comparisonMeta.Comments),
		"book_id":         nonEmpty(comparisonMeta.BookOrganizerID),
		"open_library_id": nonEmpty(comparisonMeta.OpenLibraryID),
		"hardcover_id":    nonEmpty(comparisonMeta.HardcoverID),
		"google_books_id": nonEmpty(comparisonMeta.GoogleBooksID),
	}
	if comparisonMeta.Year > 0 {
		compMap["audiobook_release_year"] = comparisonMeta.Year
	}
	if comparisonMeta.SeriesIndex > 0 {
		compMap["series_index"] = comparisonMeta.SeriesIndex
	}
	return compMap
}

func buildComparisonValuesFromBook(book *database.Book, authorName, seriesName string) map[string]any {
	if book == nil {
		return nil
	}
	compMap := map[string]any{
		"title":           nonEmpty(book.Title),
		"author_name":     nonEmpty(authorName),
		"narrator":        nonEmpty(ptrStr(book.Narrator)),
		"series_name":     nonEmpty(seriesName),
		"publisher":       nonEmpty(ptrStr(book.Publisher)),
		"language":        nonEmpty(ptrStr(book.Language)),
		"isbn10":          nonEmpty(ptrStr(book.ISBN10)),
		"isbn13":          nonEmpty(ptrStr(book.ISBN13)),
		"genre":           nonEmpty(ptrStr(book.Genre)),
		"album":           nonEmpty(book.Title),
		"asin":            nonEmpty(ptrStr(book.ASIN)),
		"edition":         nonEmpty(ptrStr(book.Edition)),
		"description":     nonEmpty(ptrStr(book.Description)),
		"book_id":         nonEmpty(book.ID),
		"open_library_id": nonEmpty(ptrStr(book.OpenLibraryID)),
		"hardcover_id":    nonEmpty(ptrStr(book.HardcoverID)),
		"google_books_id": nonEmpty(ptrStr(book.GoogleBooksID)),
	}
	if book.AudiobookReleaseYear != nil && *book.AudiobookReleaseYear > 0 {
		compMap["audiobook_release_year"] = *book.AudiobookReleaseYear
	}
	if book.SeriesSequence != nil && *book.SeriesSequence > 0 {
		compMap["series_index"] = *book.SeriesSequence
	}
	if book.PrintYear != nil && *book.PrintYear > 0 {
		compMap["print_year"] = *book.PrintYear
	}
	return compMap
}

// buildComparisonValuesFromActivityLog reconstructs a "before" tag snapshot
// from the activity log for the given book within ±5 s of ts.
func buildComparisonValuesFromActivityLog(as *activity.Service, bookID string, ts time.Time) map[string]any {
	window := 5 * time.Second
	since := ts.Add(-window)
	until := ts.Add(window)
	entries, _, err := as.Query(database.ActivityFilter{
		BookID: bookID,
		Type:   "metadata_apply",
		Since:  &since,
		Until:  &until,
		Limit:  200,
	})
	if err != nil || len(entries) == 0 {
		return nil
	}
	compMap := map[string]any{}
	for _, e := range entries {
		if e.Details == nil {
			continue
		}
		field, _ := e.Details["field"].(string)
		if field == "" {
			continue
		}
		if oldVal, ok := e.Details["old_value"]; ok && oldVal != nil {
			if s, ok := oldVal.(string); ok && s != "" {
				compMap[field] = s
			} else if oldVal != nil {
				compMap[field] = oldVal
			}
		}
	}
	if len(compMap) == 0 {
		return nil
	}
	return compMap
}
