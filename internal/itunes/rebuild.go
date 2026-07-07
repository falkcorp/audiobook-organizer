// file: internal/itunes/rebuild.go
// version: 2.2.0
// guid: 3f2e1d0c-9b8a-7c6d-5e4f-3a2b1c0d9e8f
// last-edited: 2026-07-07

package itunes

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// RebuildStore is the minimal store interface needed by ITL rebuild functions:
// read access to books plus the ability to look up book files
// (for sourcing ITunesPath from BookFile rather than the deprecated Book field).
type RebuildStore interface {
	database.BookReader
	GetBookFiles(bookID string) ([]database.BookFile, error)
	// GetAuthorByID resolves an author by id for the
	// resolveAuthorName helper. Added in SERVER-GLOBAL-STORE-AUDIT
	// phase 2 to replace the prior database.GetGlobalStore() call.
	GetAuthorByID(id int) (*database.Author, error)
}

// ComputeITLDiff reads the current ITL and the current DB state,
// and returns the ITLOperationSet that would synchronize them
// plus a preview of what that set contains.
func ComputeITLDiff(store RebuildStore, itlPath string) (*ITLOperationSet, *ITLRebuildPreview, error) {
	// Parse the current ITL to get all existing tracks.
	lib, err := ParseITL(itlPath)
	if err != nil {
		return nil, nil, fmt.Errorf("parse ITL: %w", err)
	}

	// Build a map of existing ITL tracks by PID hex string.
	itlTracks := make(map[string]*ITLTrack, len(lib.Tracks))
	for i := range lib.Tracks {
		pid := strings.ToUpper(pidToHex(lib.Tracks[i].PersistentID))
		itlTracks[pid] = &lib.Tracks[i]
	}

	// Build the "should be in ITL" set from the DB:
	// all primary-version books that have an iTunes PID.
	dbPIDs := make(map[string]*database.Book)
	const pageSize = 500
	afterID := ""
	for {
		books, err := store.GetAllBooksFullFrom(afterID, pageSize)
		if err != nil {
			return nil, nil, fmt.Errorf("get books: %w", err)
		}
		if len(books) == 0 {
			break
		}
		for i := range books {
			b := &books[i]
			// Only sync primary versions — non-primary versions
			// were merged and should have been removed from the
			// ITL by the merge cleanup in #251.
			if b.IsPrimaryVersion != nil && !*b.IsPrimaryVersion {
				continue
			}
			// Soft-deleted books should not be in the ITL.
			if b.MarkedForDeletion != nil && *b.MarkedForDeletion {
				continue
			}
			if b.ITunesPersistentID != nil && *b.ITunesPersistentID != "" {
				dbPIDs[strings.ToUpper(*b.ITunesPersistentID)] = b
			}
		}
		afterID = books[len(books)-1].ID
		if len(books) < pageSize {
			break
		}
	}

	ops := ITLOperationSet{
		Removes: make(map[string]bool),
	}
	preview := ITLRebuildPreview{
		TracksInITL: len(itlTracks),
		BooksInDB:   len(dbPIDs),
	}

	// Tracks in ITL but NOT in DB → remove.
	for pid := range itlTracks {
		if _, inDB := dbPIDs[pid]; !inDB {
			ops.Removes[pid] = true
			preview.ToRemove++
		}
	}

	// Books in DB — check for add vs update.
	for pid, book := range dbPIDs {
		track, inITL := itlTracks[pid]
		if !inITL {
			// Book has a PID but no ITL entry → add.
			// Build an ITLNewTrack from the book's metadata.
			newTrack := buildNewTrackFromBook(store, book)
			ops.Adds = append(ops.Adds, newTrack)
			preview.ToAdd++
			continue
		}

		// Both exist — check if metadata or location need updating.
		authorName := resolveAuthorName(store, book)
		narrator := ""
		if book.Narrator != nil {
			narrator = *book.Narrator
		}
		genre := "Audiobook"
		if book.Genre != nil && *book.Genre != "" {
			genre = *book.Genre
		}
		needsMetaUpdate := false
		if track.Name != book.Title {
			needsMetaUpdate = true
		}
		if track.Artist != authorName {
			needsMetaUpdate = true
		}
		if track.Album != book.Title {
			needsMetaUpdate = true
		}
		if track.Genre != genre {
			needsMetaUpdate = true
		}

		if needsMetaUpdate {
			ops.MetadataUpdates = append(ops.MetadataUpdates, ITLMetadataUpdate{
				PersistentID: pid,
				Name:         book.Title,
				Album:        book.Title,
				Artist:       authorName,
				Composer:     narrator,
				Genre:        genre,
			})
			preview.ToUpdateMeta++
		}

		// Location update.
		wantLoc := ""
		if bfs, bfErr := store.GetBookFiles(book.ID); bfErr == nil && len(bfs) > 0 && bfs[0].ITunesPath != "" {
			wantLoc = bfs[0].ITunesPath
		}
		if wantLoc != "" && track.Location != wantLoc {
			ops.LocationUpdates = append(ops.LocationUpdates, ITLLocationUpdate{
				PersistentID: pid,
				NewLocation:  wantLoc,
			})
			preview.ToUpdateLoc++
		}

		if !needsMetaUpdate && (wantLoc == "" || track.Location == wantLoc) {
			preview.AlreadySynced++
		}
	}

	return &ops, &preview, nil
}

// BuildNewTrackFromBook constructs an ITLNewTrack from a database
// Book for insertion into the ITL. Fills as many fields as
// possible from the book's metadata.
func BuildNewTrackFromBook(store RebuildStore, book *database.Book) ITLNewTrack {
	return buildNewTrackFromBook(store, book)
}

// buildNewTrackFromBook constructs an ITLNewTrack from a database
// Book for insertion into the ITL. Fills as many fields as
// possible from the book's metadata.
func buildNewTrackFromBook(store RebuildStore, book *database.Book) ITLNewTrack {
	authorName := resolveAuthorName(store, book)
	genre := "Audiobook"
	if book.Genre != nil && *book.Genre != "" {
		genre = *book.Genre
	}
	location := book.FilePath
	if bfs, bfErr := store.GetBookFiles(book.ID); bfErr == nil && len(bfs) > 0 && bfs[0].ITunesPath != "" {
		location = bfs[0].ITunesPath
	}
	totalTime := 0
	if book.Duration != nil {
		totalTime = *book.Duration * 1000 // convert seconds → ms
	}
	size := int64(0)
	if book.FileSize != nil {
		size = *book.FileSize
	}

	// Note: ITLNewTrack doesn't carry a Composer field — narrator
	// is pushed via a follow-up MetadataUpdate after the add. The
	// batcher's existing flush path handles that automatically for
	// books that go through the normal write-back pipeline. For the
	// rebuild path, the MetadataUpdate pass handles narrator via
	// the Composer field on ITLMetadataUpdate.
	return ITLNewTrack{
		Location:  location,
		Name:      book.Title,
		Album:     book.Title,
		Artist:    authorName,
		Genre:     genre,
		Kind:      "MPEG audio file", // default; the real kind comes from the file format
		Size:      int(size),
		TotalTime: totalTime,
	}
}

// resolveAuthorName returns the author name for a book. Takes the
// caller's RebuildStore so we no longer depend on the package-level
// database.GetGlobalStore (SERVER-GLOBAL-STORE-AUDIT phase 2). A nil
// store falls back to the inline Author field — keeps tests that build
// a Book{Author: &Author{Name: "..."}} working without wiring a store.
func resolveAuthorName(store RebuildStore, book *database.Book) string {
	if book.Author != nil {
		return book.Author.Name
	}
	if book.AuthorID != nil && store != nil {
		if author, err := store.GetAuthorByID(*book.AuthorID); err == nil && author != nil {
			return author.Name
		}
	}
	return ""
}

// ITLRebuildPreview summarizes the diff between the DB and the
// current ITL file without applying any changes. Returned by
// the dry-run path so the user can review before committing.
type ITLRebuildPreview struct {
	TracksInITL   int `json:"tracks_in_itl"`
	BooksInDB     int `json:"books_in_db"`
	ToRemove      int `json:"to_remove"`
	ToAdd         int `json:"to_add"`
	ToUpdateMeta  int `json:"to_update_metadata"`
	ToUpdateLoc   int `json:"to_update_location"`
	AlreadySynced int `json:"already_synced"`
}

// ITLRebuildResult is the outcome of an applied rebuild.
type ITLRebuildResult struct {
	Preview ITLRebuildPreview `json:"preview"`
	Applied bool              `json:"applied"`
	Error   string            `json:"error,omitempty"`
}

// RebuildITLFromDB removes ALL existing tracks from the ITL file and re-inserts
// every primary-version, non-deleted book from the DB that has an iTunes PID.
// This is the "nuclear" rebuild path for when incremental diff is impractical.
// The existing ITL is used as a structural template so the container format,
// compression, and encryption are preserved — only the track data is replaced.
// The result is written to outputPath via ApplyITLOperations.
//
// K15: acknowledgeShrink is the explicit operator gate for a rebuild that
// would shrink the library by more than half. WHY: rebuild sources its track
// list from the DB; an under-populated / mid-migration DB silently turns
// "rebuild" into "mass deletion" (the July 2026 stub class), and Force
// (needed legitimately for bounded-delta here) would otherwise be the only
// gate. A >50% shrink without acknowledgment is an error, never a write.
func RebuildITLFromDB(store RebuildStore, itlPath, outputPath string, acknowledgeShrink bool) (*ITLRebuildResult, error) {
	lib, err := ParseITL(itlPath)
	if err != nil {
		return nil, fmt.Errorf("parse ITL: %w", err)
	}

	// Collect ALL existing PIDs to remove.
	removes := make(map[string]bool, len(lib.Tracks))
	for i := range lib.Tracks {
		pid := strings.ToUpper(pidToHex(lib.Tracks[i].PersistentID))
		removes[pid] = true
	}

	// Collect all DB books to add back.
	var adds []ITLNewTrack
	const pageSize = 500
	afterID := ""
	for {
		books, err := store.GetAllBooksFullFrom(afterID, pageSize)
		if err != nil {
			return nil, fmt.Errorf("get books page after %q: %w", afterID, err)
		}
		if len(books) == 0 {
			break
		}
		for i := range books {
			b := &books[i]
			if b.IsPrimaryVersion != nil && !*b.IsPrimaryVersion {
				continue
			}
			if b.MarkedForDeletion != nil && *b.MarkedForDeletion {
				continue
			}
			if b.ITunesPersistentID == nil || *b.ITunesPersistentID == "" {
				continue
			}
			adds = append(adds, buildNewTrackFromBook(store, b))
		}
		afterID = books[len(books)-1].ID
		if len(books) < pageSize {
			break
		}
	}

	preview := ITLRebuildPreview{
		TracksInITL: len(lib.Tracks),
		BooksInDB:   len(adds),
		ToRemove:    len(removes),
		ToAdd:       len(adds),
	}

	// K15 shrink gate: a rebuild that halves the library needs an explicit
	// operator acknowledgment — Force alone must not be able to authorize it.
	if len(lib.Tracks) > 0 && len(adds)*2 < len(lib.Tracks) && !acknowledgeShrink {
		return nil, fmt.Errorf(
			"rebuild would shrink library from %d to %d tracks (>50%%); refusing without acknowledge_shrink — the DB may be under-populated (K15)",
			len(lib.Tracks), len(adds))
	}
	// Report-mode magnitude: always leave the blast radius in the log, even
	// when the write is authorized (K15 — Force must never be silent).
	slog.Warn("RebuildITLFromDB applying nuclear rebuild",
		"op", "itl-rebuild", "tracks_before", len(lib.Tracks),
		"tracks_after", len(adds), "removes", len(removes),
		"acknowledge_shrink", acknowledgeShrink)

	// Nuclear rebuild removes EVERY existing track then re-adds from the DB, so
	// it intentionally blows past the bounded-delta cap — pass Force (SPEC §2;
	// structural guards still apply, and the library-identity guard (K13) still
	// checks PID continuity against the sidecar: a healthy DB re-adds the same
	// PIDs, an under-populated one fails the overlap check).
	ops := ITLOperationSet{Removes: removes, Adds: adds}
	if _, err := ApplyITLOperations(itlPath, outputPath, ops, ForceContractConfig()); err != nil {
		return nil, fmt.Errorf("apply rebuild ops: %w", err)
	}

	return &ITLRebuildResult{Preview: preview, Applied: true}, nil
}

// BuildExportITL builds a partial ITL containing only the requested bookIDs.
// It strips all tracks from the template ITL and inserts only the matching
// DB books. The resulting bytes are returned (not written to disk); the caller
// is responsible for sending them to the client.
// If bookIDs is empty, all primary-version non-deleted books with PIDs are included.
func BuildExportITL(store RebuildStore, templatePath string, bookIDs []string) ([]byte, error) {
	// Collect the requested books from the store.
	wantIDs := make(map[string]bool, len(bookIDs))
	for _, id := range bookIDs {
		wantIDs[id] = true
	}

	var adds []ITLNewTrack
	const pageSize = 500
	afterID := ""
	for {
		books, err := store.GetAllBooksFullFrom(afterID, pageSize)
		if err != nil {
			return nil, fmt.Errorf("get books page after %q: %w", afterID, err)
		}
		if len(books) == 0 {
			break
		}
		for i := range books {
			b := &books[i]
			if b.IsPrimaryVersion != nil && !*b.IsPrimaryVersion {
				continue
			}
			if b.MarkedForDeletion != nil && *b.MarkedForDeletion {
				continue
			}
			if b.ITunesPersistentID == nil || *b.ITunesPersistentID == "" {
				continue
			}
			if len(wantIDs) > 0 && !wantIDs[b.ID] {
				continue
			}
			adds = append(adds, buildNewTrackFromBook(store, b))
		}
		afterID = books[len(books)-1].ID
		if len(books) < pageSize {
			break
		}
	}

	lib, err := ParseITL(templatePath)
	if err != nil {
		return nil, fmt.Errorf("parse template ITL: %w", err)
	}

	removes := make(map[string]bool, len(lib.Tracks))
	for i := range lib.Tracks {
		pid := strings.ToUpper(pidToHex(lib.Tracks[i].PersistentID))
		removes[pid] = true
	}

	// Partial export strips ALL template tracks then re-adds the requested
	// subset — another intentional full-replacement that must Force past
	// bounded-delta (SPEC §2; structural guards still enforced).
	ops := ITLOperationSet{Removes: removes, Adds: adds}
	result, err := ApplyITLOperationsInMemory(templatePath, ops, ForceContractConfig())
	if err != nil {
		return nil, fmt.Errorf("build export ITL: %w", err)
	}
	return result, nil
}
