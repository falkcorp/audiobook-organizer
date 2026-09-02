// file: internal/audiobooks/helpers.go
// version: 1.6.1
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234560010
// last-edited: 2026-09-02
//
// Private utilities needed by the audiobooks service package. Most still mirror
// equivalent helpers in internal/server/ (stringPtr, boolPtr, decodeRawValue,
// asExternalIDStore, stringVal, intVal, nonEmpty, buildMetadataProvenance) and
// are standalone so that the audiobooks package does not import internal/server
// (which would cycle).
//
// The metadata-state codec is the exception: its three helpers were byte-identical
// here, in internal/server and in internal/metafetch, so they now live in
// internal/metastate -- a leaf package all three import. A leaf is the way out of
// the cycle that forced the copies; the remaining mirrors above are candidates for
// the same treatment.

package audiobooks

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/metastate"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// --- basic pointer helpers --------------------------------------------------

func ptrStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// --- metadata field state ---------------------------------------------------

// metadataFieldState represents the persisted state of one metadata field.
// metadataFieldState is metafetch's type, not a local one.
//
// It was a field-for-field, tag-for-tag copy of metafetch.MetadataFieldState
// until 2026-09-01. An ALIAS rather than a rename because the name is used ~40
// times in this package and the two are the same type either way -- what
// mattered was deleting the second definition, not the second spelling.
type metadataFieldState = metafetch.MetadataFieldState

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
	pref, err := svc.store.GetUserPreference(metastate.Key(bookID))
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
			FetchedValue:   metastate.Decode(entry.FetchedValue),
			OverrideValue:  metastate.Decode(entry.OverrideValue),
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
//
// Measured with an empty-interface compiler probe: metadataStateSvc calls
// exactly one store method. It previously embedded database.MetadataStore and
// database.UserPreferenceStore, which forced every caller to carry both full
// surfaces to reach this single write.
type metadataStateStore interface {
	RecordMetadataChange(record *database.MetadataChangeRecord) error
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
	prev, _ := metastate.Encode(previousValue)
	next, _ := metastate.Encode(newValue)
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

// nonEmpty is metafetch.NonEmpty. BuildMetadataProvenance moved to metafetch and
// depends on it, so the implementation moved with it rather than becoming a
// third copy; this alias keeps all 43 call sites in this file unchanged.
var nonEmpty = metafetch.NonEmpty

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
func buildComparisonValuesFromActivityLog(ctx context.Context, as *activity.Service, bookID string, ts time.Time) map[string]any {
	window := 5 * time.Second
	since := ts.Add(-window)
	until := ts.Add(window)
	entries, _, err := as.Query(ctx, database.ActivityFilter{
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
