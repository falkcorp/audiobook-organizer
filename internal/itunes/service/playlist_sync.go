// file: internal/itunes/service/playlist_sync.go
// version: 2.2.0
// guid: 1e9f0a8b-2c3d-4a70-b8c5-3d7e0f1b9a99
//
// iTunes playlist sync (spec 3.4 tasks 5-6).
//
// Task 5: One-time migration of iTunes dynamic playlists.
//   Reads smart playlists from the ITL, parses their Smart Criteria
//   blob, translates to our DSL, and creates UserPlaylist rows with
//   type=smart. Stores the raw criteria blob in ITunesRawCriteriaB64
//   for audit. Runs once (idempotent — skips playlists already
//   imported by iTunes PID).
//
// Task 6: Push playlists to ITL.
//   For dirty playlists with no iTunes PID, creates a new ITL
//   playlist. For dirty playlists with an existing PID, updates the
//   track list. Smart playlists are pushed as static (materialized)
//   since iTunes will manage its own smart criteria.

package itunesservice

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
)

// playlistSyncStore is the narrow slice of the service's Store that
// PlaylistSync needs.
type playlistSyncStore interface {
	database.UserPlaylistStore
}

// PlaylistSync owns the two-way iTunes-playlist sync paths (import
// smart playlists from the ITL, push dirty playlists back out).
type PlaylistSync struct {
	store    playlistSyncStore
	enqueuer Enqueuer
}

// newPlaylistSync wires a PlaylistSync with the given store and
// enqueuer. A nil enqueuer disables the push direction's ITL write-back
// enqueue (the dirty flag is still cleared).
func newPlaylistSync(store playlistSyncStore, enqueuer Enqueuer) *PlaylistSync {
	return &PlaylistSync{store: store, enqueuer: enqueuer}
}

// NewPlaylistImporter builds a PlaylistSync for the IMPORT direction only.
//
// The enqueuer is nil, so PushDirty's ITL write-back enqueue is disabled — this
// constructor exists for callers (the maintenance op) that must never write to
// the iTunes library. Import reads an already-parsed *ITLLibrary and writes only
// UserPlaylist rows in our own store.
func NewPlaylistImporter(store database.UserPlaylistStore) *PlaylistSync {
	return &PlaylistSync{store: store, enqueuer: nil}
}

// PlaylistImportOptions controls a smart-playlist migration run.
type PlaylistImportOptions struct {
	// OwnerUserID is stamped on every created playlist as CreatedByUserID.
	//
	// This matters more than it looks. The playlist API scopes list results per
	// user (ListUserPlaylistsForUser), and handlers.CallingUserID returns the
	// sentinel "_local" ONLY when no user is authenticated. So playlists
	// imported under "_local" are invisible to every logged-in account — the
	// import would report a healthy count while the Playlists page stayed
	// empty. Set this to the account that should own the imported playlists.
	// Empty falls back to adminUserID for backwards compatibility.
	OwnerUserID string

	// DryRun parses and translates every smart playlist and reports what WOULD
	// be imported, without creating a single row. Note that the underlying
	// per-playlist work (PID lookup, criteria parse, DSL translation) is
	// identical in both modes — only CreateUserPlaylist is withheld — so a dry
	// run exercises every failure mode the apply would hit.
	DryRun bool
}

// PlaylistImportItem records one smart playlist's outcome.
type PlaylistImportItem struct {
	Title  string `json:"title"`
	PID    string `json:"pid"`
	Query  string `json:"query,omitempty"`
	Status string `json:"status"` // "imported" | "would-import" | "already-imported" | "unparseable" | "create-failed"
	Err    string `json:"err,omitempty"`
}

// PlaylistImportResult is the full outcome of a migration run.
type PlaylistImportResult struct {
	SmartFound int                  `json:"smart_found"`
	Imported   int                  `json:"imported"`
	Skipped    int                  `json:"skipped"`
	Items      []PlaylistImportItem `json:"items"`
}

// MigrateSmartPlaylists reads smart playlists from the ITL library
// and creates UserPlaylist rows for each. Idempotent — playlists
// already imported (by iTunes PID) are skipped.
//
// Read-only with respect to the iTunes library: it consumes an already-parsed
// *ITLLibrary and writes only to our own store.
func (p *PlaylistSync) MigrateSmartPlaylists(lib *itunes.ITLLibrary, opts PlaylistImportOptions) PlaylistImportResult {
	res := PlaylistImportResult{}
	if lib == nil {
		return res
	}

	owner := opts.OwnerUserID
	if owner == "" {
		owner = adminUserID
	}

	for _, pl := range lib.Playlists {
		if !pl.IsSmart || len(pl.SmartCriteria) == 0 {
			continue
		}
		res.SmartFound++

		pid := hex.EncodeToString(pl.PersistentID[:])
		item := PlaylistImportItem{Title: pl.Title, PID: pid}

		existing, _ := p.store.GetUserPlaylistByITunesPID(pid)
		if existing != nil {
			res.Skipped++
			item.Status = "already-imported"
			res.Items = append(res.Items, item)
			continue
		}

		parsed, err := itunes.ParseSmartCriteria(pl.SmartCriteria)
		if err != nil {
			slog.Warn("parse smart criteria for playlist", "pl", pl.Title, "pid", pid, "err", err)
			res.Skipped++
			item.Status = "unparseable"
			item.Err = err.Error()
			res.Items = append(res.Items, item)
			continue
		}
		dslQuery := itunes.TranslateSmartCriteria(parsed)
		item.Query = dslQuery

		if opts.DryRun {
			res.Imported++
			item.Status = "would-import"
			res.Items = append(res.Items, item)
			continue
		}

		rawB64 := base64.StdEncoding.EncodeToString(pl.SmartCriteria)

		_, err = p.store.CreateUserPlaylist(&database.UserPlaylist{
			Name:                 pl.Title,
			Type:                 database.UserPlaylistTypeSmart,
			Query:                dslQuery,
			ITunesPersistentID:   pid,
			ITunesRawCriteriaB64: rawB64,
			Description:          fmt.Sprintf("Imported from iTunes smart playlist %q", pl.Title),
			CreatedByUserID:      owner,
		})
		if err != nil {
			slog.Warn("create playlist", "pl", pl.Title, "err", err)
			res.Skipped++
			item.Status = "create-failed"
			item.Err = err.Error()
			res.Items = append(res.Items, item)
			continue
		}
		res.Imported++
		item.Status = "imported"
		res.Items = append(res.Items, item)
	}

	return res
}

// PushDirty writes dirty playlists to the ITL. Smart playlists are
// materialized first (the materialized_book_ids field is used).
// Returns the number pushed.
//
// Placeholder that enqueues the playlist book IDs for the ITL
// write-back batcher. Full ITL playlist creation requires the ITL
// writer to support playlist insertion, which is tracked separately.
func (p *PlaylistSync) PushDirty() int {
	dirties, err := p.store.ListDirtyUserPlaylists()
	if err != nil {
		slog.Warn("list dirty playlists", "err", err)
		return 0
	}

	pushed := 0
	for i := range dirties {
		pl := &dirties[i]

		bookIDs := pl.BookIDs
		if pl.Type == database.UserPlaylistTypeSmart {
			bookIDs = pl.MaterializedBookIDs
		}

		if len(bookIDs) == 0 {
			continue
		}

		if p.enqueuer != nil {
			for _, bid := range bookIDs {
				p.enqueuer.Enqueue(bid)
			}
		}

		pl.Dirty = false
		if err := p.store.UpdateUserPlaylist(pl); err != nil {
			slog.Warn("clear dirty for", "pl", pl.ID, "err", err)
			continue
		}
		pushed++
	}

	return pushed
}
