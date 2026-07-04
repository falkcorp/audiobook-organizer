// file: internal/itunes/service/track_provisioner.go
// version: 1.2.1
// guid: 8e768742-5ace-4e4b-8495-9550ed4620b5
// last-edited: 2026-07-03
//
// TrackProvisioner generates ITL tracks for books that weren't imported
// from iTunes (e.g. books added via scan or manual upload). For each
// book file without an iTunes Persistent ID, it:
//   1. Generates a random PID
//   2. Stores it in external_id_map with provenance='generated'
//   3. Updates the BookFile with the PID + Windows-mapped path
//   4. Enqueues an ITL "add track" via the batcher
//
// Moved from internal/server/itunes_track_provisioner.go during Phase 2 M1.
// Converted from free functions to *TrackProvisioner methods with injected
// store + enqueuer + config.

package itunesservice

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
)

// provisionerStore is the narrow slice of the Service's Store that
// TrackProvisioner needs. AuthorReader lets us resolve author name for
// the ITL "Artist" field; BookFileStore lets us update the PID + path;
// ExternalIDStore is where the generated PID mapping lives.
type provisionerStore interface {
	database.AuthorReader
	database.BookFileStore
	database.ExternalIDStore
}

// TrackProvisioner provisions ITL tracks for non-iTunes books.
// One instance per Service; Service.New creates it wired to the
// injected Store + batcher + config.
type TrackProvisioner struct {
	store    provisionerStore
	enqueuer Enqueuer
	cfg      Config
}

// newTrackProvisioner constructs a TrackProvisioner from its deps.
// enqueuer may be nil — ProvisionAll/Provision skip the batcher's
// EnqueueAdd call when nil. The default Service.New() wiring passes
// nil here and the server calls SetEnqueuer after the batcher is
// available (the batcher lives on Server until Phase 2 M1 step 3
// moves it into the service).
func newTrackProvisioner(store provisionerStore, enqueuer Enqueuer, cfg Config) *TrackProvisioner {
	return &TrackProvisioner{store: store, enqueuer: enqueuer, cfg: cfg}
}

// SetEnqueuer wires (or re-wires) the batcher the provisioner
// should enqueue ITL adds against. Safe to call at any time —
// Provision reads the field on each call. Pass nil to disable
// enqueues (e.g. tests, disabled write-back mode).
func (p *TrackProvisioner) SetEnqueuer(e Enqueuer) {
	p.enqueuer = e
}

// Provision generates a PID for a single book file, stores it in
// external_id_map, updates the BookFile, and enqueues an ITL add.
// Skips if the book file already has a PID or if auto write-back is
// disabled via config.
func (p *TrackProvisioner) Provision(book *database.Book, bookFile *database.BookFile) error {
	if !p.cfg.AutoWriteBack {
		return nil
	}
	if bookFile.ITunesPersistentID != "" {
		return nil // already has a PID
	}
	// Only the primary version of a book gets written into iTunes.
	// Non-primary versions (alternate formats, lower-quality rips, the
	// "loser" side of a merge) must NOT generate PIDs or get added to
	// the ITL — historically this was the source of large amounts of
	// duplicate iTunes tracks for the same audiobook.
	if book != nil && book.IsPrimaryVersion != nil && !*book.IsPrimaryVersion {
		return nil
	}

	// Generate a new PID
	pid := itunes.GeneratePIDHex()

	// Build the Windows-mapped path for iTunes
	itunesPath := linuxToWindowsPath(bookFile.FilePath)

	// Store PID in external_id_map with provenance='generated'
	trackNum := bookFile.TrackNumber
	mapping := &database.ExternalIDMapping{
		Source:     "itunes",
		ExternalID: pid,
		BookID:     book.ID,
		FilePath:   bookFile.FilePath,
		Provenance: "generated",
	}
	if trackNum > 0 {
		mapping.TrackNumber = &trackNum
	}
	if err := p.store.CreateExternalIDMapping(mapping); err != nil {
		return fmt.Errorf("storing PID mapping: %w", err)
	}

	// Set provenance (CreateExternalIDMapping doesn't set it)
	_ = p.store.SetExternalIDProvenance("itunes", pid, "generated")

	// Update BookFile with the new PID
	bookFile.ITunesPersistentID = pid
	bookFile.ITunesPath = itunesPath
	if err := p.store.UpsertBookFile(bookFile); err != nil {
		return fmt.Errorf("updating book file with PID: %w", err)
	}

	// Determine format-based "Kind" string
	kind := kindFromExt(filepath.Ext(bookFile.FilePath))

	// TASK-006: ITLNewTrack.Location is the 0x0D side — it must be a canonical
	// Windows path, never a URL or staging-dir leak (CRIT-2 / SPEC §1b). Normalize
	// itunesPath through LocationPair; if it is unmappable, SKIP the ITL add (the
	// PID/mapping are still persisted so a later corrected provision can retry)
	// rather than enqueue a corrupt 0x0D location.
	enqueueLocation, locOK := normalizeITunesLocation(pid, itunesPath)

	// Enqueue the ITL add — nil-safe for tests that don't wire a batcher
	if p.enqueuer != nil && locOK {
		p.enqueuer.EnqueueAdd(itunes.ITLNewTrack{
			Location:    enqueueLocation,
			Name:        bookFile.Title,
			Album:       book.Title,
			Artist:      p.bookAuthor(book),
			Genre:       "Audiobook",
			Kind:        kind,
			Size:        int(bookFile.FileSize),
			TotalTime:   bookFile.Duration * 1000, // seconds to ms
			TrackNumber: bookFile.TrackNumber,
			BitRate:     bookFile.BitrateKbps,
			SampleRate:  bookFile.SampleRateHz,
			DiscNumber:  bookFile.DiscNumber,
		})
	}

	slog.Info("Provisioned ITL track book pid path", "book", book.ID, "pid", pid, "itunesPath", itunesPath)
	return nil
}

// ProvisionAll provisions ITL tracks for all files of a book. Failures
// are logged and skipped, not returned — the caller wants best-effort
// semantics (one broken file shouldn't block the rest of the book's
// files from landing in iTunes).
func (p *TrackProvisioner) ProvisionAll(book *database.Book) error {
	files, err := p.store.GetBookFiles(book.ID)
	if err != nil {
		return err
	}
	for i := range files {
		if err := p.Provision(book, &files[i]); err != nil {
			slog.Warn("Failed to provision ITL track for file", "fileID", files[i].ID, "err", err)
		}
	}
	return nil
}

// bookAuthor returns the author name for a book, or empty string if
// the book has no author or the author can't be loaded.
func (p *TrackProvisioner) bookAuthor(book *database.Book) string {
	if book.AuthorID == nil {
		return ""
	}
	author, err := p.store.GetAuthorByID(*book.AuthorID)
	if err != nil || author == nil {
		return ""
	}
	return author.Name
}

// linuxToWindowsPath maps a Linux path to the Windows SMB path iTunes
// expects for tracks backed by the shared library share.
func linuxToWindowsPath(p string) string {
	const linuxRoot = "/mnt/bigdata/books/audiobook-organizer/"
	const windowsRoot = `W:\audiobook-organizer\`
	if strings.HasPrefix(p, linuxRoot) {
		return windowsRoot + strings.ReplaceAll(
			strings.TrimPrefix(p, linuxRoot), "/", `\`,
		)
	}
	return p
}

// kindFromExt returns the iTunes "Kind" string for a file extension.
// Defaults to "AAC audio file" for unknown extensions — matches legacy
// scanner behavior.
func kindFromExt(ext string) string {
	switch strings.ToLower(ext) {
	case ".m4b", ".m4a", ".aac":
		return "AAC audio file"
	case ".mp3":
		return "MPEG audio file"
	case ".ogg":
		return "Ogg Vorbis file"
	case ".flac":
		return "FLAC audio file"
	case ".wav":
		return "WAV audio file"
	default:
		return "AAC audio file"
	}
}
