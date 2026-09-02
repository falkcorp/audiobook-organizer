// file: internal/importer/service.go
// version: 1.7.1
// guid: d0e1f2a3-b4c5-6d7e-8f9a-0b1c2d3e4f5b
// last-edited: 2026-09-02

package importer

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
	"github.com/falkcorp/audiobook-organizer/internal/logging"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/versions"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// Store is the slice of the database this service uses: seven methods it calls
// directly, plus four forwarding constraints embedded by name.
//
// It was `= database.Store` (398 methods) with a comment saying it had been
// "temporarily widened ... because versions.CreateIngestVersion requires the
// full Store interface", and that "a future ISP pass on the versions package
// will re-narrow this." That pass happened in #2582; this is the re-narrowing
// it promised. The alias had been widened in the meantime, so nothing here was
// ever measured -- it was inherited.

// importForwarded is what this package hands its store to. Named rather than
// inlined: four entries instead of ~20, and each re-narrows on its own.
type importForwarded interface {
	// fileops.ValidateUserPath, and NewImportPathService.
	database.ImportPathStore
	// merge.BookTitle.
	merge.BookReader
	// versions.CheckFingerprint.
	versions.FingerprintReader
	// versions.CreateIngestVersion.
	versions.IngestStore
}

type importEntityStore interface {
	GetAuthorByName(name string) (*database.Author, error)
	CreateAuthor(name string) (*database.Author, error)
	GetSeriesByName(name string, authorID *int) (*database.Series, error)
	CreateSeries(name string, authorID *int) (*database.Series, error)
}

type importBookStore interface {
	CreateBook(book *database.Book) (*database.Book, error)
	GetAllBooksCore(limit, offset int) ([]database.BookCore, error)
	GetBookByFileHash(hash string) (*database.Book, error)
	// CreateBookFile was ABSENT until 2026-08-25, which is why imported books
	// had a row and audio on disk and nothing connecting the two. The gap was
	// invisible precisely because it was an interface gap: nothing in this
	// package could call the method, so nothing looked like it was failing to.
	CreateBookFile(file *database.BookFile) error
}

type Store interface {
	importForwarded
	importEntityStore
	importBookStore
}

// collisionStore is what CheckImportCollisions needs: two direct lookups plus
// the three checks it forwards into. Notably NOT the full importer.Store --
// it neither creates entities nor ingests versions.
type collisionStore interface {
	database.ImportPathStore
	merge.BookReader
	versions.FingerprintReader

	GetAllBooksCore(limit, offset int) ([]database.BookCore, error)
	GetBookByFileHash(hash string) (*database.Book, error)
}

type ImportService struct {
	db          Store
	provisioner *itunesservice.TrackProvisioner
	dedupEngine *dedup.Engine
	// opRegistry is the UOS operation registry. When set and
	// config.AppConfig.Dedup.OnImportViaScheduler is true, post-import dedup
	// checks are routed through the scheduler (dedup.check-book op) instead
	// of the eager goroutine. Nil when the registry is not yet available.
	opRegistry sdk.Registry
}

// SetTrackProvisioner wires the iTunes track provisioner for newly-imported
// books. Pass nil to disable ITL track provisioning (e.g. in tests).
func (is *ImportService) SetTrackProvisioner(p *itunesservice.TrackProvisioner) {
	is.provisioner = p
}

func (is *ImportService) SetDedupEngine(e *dedup.Engine) {
	is.dedupEngine = e
}

// SetRegistry wires the UOS operation registry. When set and
// DedupOnImportViaScheduler is enabled in config, post-import dedup
// checks are enqueued as dedup.check-book operations instead of
// running via an eager background goroutine.
func (is *ImportService) SetRegistry(r sdk.Registry) {
	is.opRegistry = r
}

func NewImportService(db Store) *ImportService {
	return &ImportService{db: db}
}

type ImportFileRequest struct {
	FilePath string `json:"file_path" binding:"required"`
	// Organize is honored by the HTTP LAYER ONLY —
	// handlers.FilesystemHandler.ImportFile enqueues a library.organize op for
	// the created book. ImportFile below does not read it and must not: the
	// importer has no organizer dependency, and organizing inline would bypass
	// the op registry's concurrency gate.
	//
	// A non-HTTP caller therefore gets no organize from setting this. That is
	// why deluge_discovery.go:95 passes false explicitly rather than relying on
	// the zero value — it documents that the field was considered. If you add
	// another direct caller of ImportFile, honor this yourself or leave it
	// false; do not assume the service acts on it. It was decoded and ignored
	// everywhere until 2026-08-25, which is exactly the bug this comment exists
	// to stop recurring.
	Organize bool `json:"organize"`
}

type ImportFileResponse struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	FilePath string `json:"file_path"`
	// AuthorResolved reports whether this import produced a book with a usable
	// author. It is not decoration: organizer.FilterBooksNeedingOrganization
	// DEFERS any book for which HasResolvedAuthor is false
	// (internal/organizer/service.go:715, internal/organizer/organizer.go:404),
	// because renaming an author-less book would bake the placeholder into its
	// path -- the 2026-08-11 mass-reorganize mechanism.
	//
	// The HTTP layer uses this to decline an organize up front rather than
	// queue an op that is structurally guaranteed to skip the book and then
	// report success. Without it, organize-on-import stayed silently inert for
	// untagged files even after the book_file gate below was closed -- two
	// gates, and closing one is not closing the other.
	//
	// This reports a FACT this function computed (did the book get an author),
	// not a re-implementation of the organizer's rule. If the two ever diverge
	// the failure is a queued op that defers, i.e. the old behavior, not
	// something worse.
	AuthorResolved bool `json:"author_resolved"`
}

func (is *ImportService) ImportFile(req *ImportFileRequest) (*ImportFileResponse, error) {
	// Validate the path is inside an allowed directory before any filesystem
	// access (go/path-injection); returns the cleaned absolute path.
	absPath, err := fileops.ValidateUserPath(is.db, req.FilePath)
	if err != nil {
		return nil, err
	}
	fileInfo, err := os.Stat(absPath)
	if err != nil {
		return nil, fmt.Errorf("file not found or inaccessible: %w", err)
	}
	// Normalize to absolute path for downstream processing and DB storage
	req.FilePath = absPath

	if fileInfo.IsDir() {
		return nil, fmt.Errorf("path is a directory, not a file")
	}

	// Check if file extension is supported
	ext := strings.ToLower(filepath.Ext(req.FilePath))
	supported := slices.Contains(config.AppConfig.SupportedExtensions, ext)

	if !supported {
		return nil, fmt.Errorf("unsupported file type: %s", ext)
	}

	// Extract metadata — use folder-aware assembly for generic part filenames.
	var meta metadata.Metadata
	if metadata.IsGenericPartFilename(req.FilePath) {
		dirPath := filepath.Dir(req.FilePath)
		firstFile := metadata.FindFirstAudioFile(dirPath, config.AppConfig.SupportedExtensions)
		if firstFile == "" {
			firstFile = req.FilePath
		}
		bm, bmErr := metadata.AssembleBookMetadata(dirPath, firstFile, 0, 0)
		if bmErr != nil {
			return nil, fmt.Errorf("failed to assemble metadata: %w", bmErr)
		}
		meta = metadata.Metadata{
			Title:       bm.Title,
			Artist:      bm.PrimaryAuthor(),
			Series:      bm.SeriesName,
			SeriesIndex: bm.SeriesPosition,
			Narrator:    bm.Narrator,
			Language:    bm.Language,
			Publisher:   bm.Publisher,
			ISBN10:      bm.ISBN10,
			ISBN13:      bm.ISBN13,
		}
	} else {
		var err error
		meta, err = metadata.ExtractMetadata(req.FilePath, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to extract metadata: %w", err)
		}
	}

	// Create book record
	book := &database.Book{
		Title:            meta.Title,
		FilePath:         req.FilePath,
		OriginalFilename: new(filepath.Base(req.FilePath)),
	}

	// Set author if available
	if meta.Artist != "" {
		normalizedArtist := dedup.NormalizeAuthorName(meta.Artist)
		// Creation gate (C413): copyright fragments and entity shrapnel from
		// artist tags must not become author rows. A book with NO author is
		// honest; an author named "&#169" is a repair job.
		if dedup.IsDirtyAuthorName(normalizedArtist) {
			slog.Warn("importer: artist tag rejected as author name",
				"artist", logging.Sanitize(meta.Artist), "path", logging.Sanitize(req.FilePath))
		} else {
			author, err := is.db.GetAuthorByName(normalizedArtist)
			if err != nil {
				author, err = is.db.CreateAuthor(normalizedArtist)
				if err != nil {
					return nil, fmt.Errorf("failed to create author: %w", err)
				}
			}
			if author != nil {
				book.AuthorID = &author.ID
			}
		}
	}

	// Set series if available
	if meta.Series != "" && book.AuthorID != nil {
		series, err := is.db.GetSeriesByName(meta.Series, book.AuthorID)
		if err != nil {
			series, err = is.db.CreateSeries(meta.Series, book.AuthorID)
			if err != nil {
				return nil, fmt.Errorf("failed to create series: %w", err)
			}
		}
		if series != nil {
			book.SeriesID = &series.ID
			if meta.SeriesIndex > 0 {
				book.SeriesSequence = &meta.SeriesIndex
			}
		}
	}

	// Set additional metadata
	if meta.Album != "" && book.Title == "" {
		book.Title = meta.Album
	}
	if meta.Narrator != "" {
		book.Narrator = new(meta.Narrator)
	}
	if meta.Language != "" {
		book.Language = new(meta.Language)
	}
	if meta.Publisher != "" {
		book.Publisher = new(meta.Publisher)
	}
	if meta.ISBN10 != "" {
		book.ISBN10 = new(meta.ISBN10)
	}
	if meta.ISBN13 != "" {
		book.ISBN13 = new(meta.ISBN13)
	}

	// Create book in database
	created, err := is.db.CreateBook(book)
	if err != nil {
		return nil, fmt.Errorf("failed to create book: %w", err)
	}

	// Create version row for the imported file (spec 3.1).
	if _, verErr := versions.CreateIngestVersion(is.db, versions.IngestVersionParams{
		BookID: created.ID, FilePath: created.FilePath,
		Format: created.Format, Source: "imported",
	}); verErr != nil {
		slog.Warn("create ingest version", "id", created.ID, "err", verErr)
	}

	// Give the imported book its book_file row.
	//
	// Without this the book row exists and the audio exists and NOTHING
	// CONNECTS THEM. Every other path that ingests audio creates book_file
	// rows — merge, itunes, metafetch, organizer, both maintenance packages,
	// and the scan path via server.ensureSingleFileBookFile — and this one did
	// not. The capability was not even in importBookStore, so it could not have.
	//
	// (An earlier draft said "nine packages". Measured: seven, before this
	// change added the eighth. The count was never load-bearing, so it is now a
	// list that can be checked rather than a number that quietly rots.)
	//
	// The immediate consequence was that organize-on-import is INERT:
	// FilterBooksNeedingOrganization (organizer/service.go:689-696) drops any
	// book whose FilePath is outside RootDir and which has zero book_files,
	// counting it into skippedMissingFiles behind a log.Debug. An imported file
	// is outside RootDir BY DEFINITION — that is what importing means — so
	// every import-triggered organize would have been silently skipped, and the
	// handler would still have reported a queued op id.
	//
	// This is always the single-file shape: ImportFile rejects directories
	// above, and fileInfo/ext are the values already computed for that check.
	// It therefore does NOT go through scanner.createBookFilesForBook, which
	// also normalizes Book.FilePath to the containing directory — correct for a
	// multi-file book, wrong here. Same reasoning as
	// server.ensureSingleFileBookFile, which exists to backfill exactly this
	// gap after the fact.
	//
	// VersionID is deliberately left unset, matching every other creation path
	// (only versions/fingerprint.go populates it today).
	//
	// A failure here does NOT fail the import. The book row is already
	// committed, so returning an error would tell the caller to retry an import
	// that already succeeded, duplicating the book. Warn loudly instead — this
	// is the row everything downstream depends on.
	bf := &database.BookFile{
		BookID:           created.ID,
		FilePath:         created.FilePath,
		OriginalFilename: filepath.Base(created.FilePath),
		Format:           strings.TrimPrefix(ext, "."),
		FileSize:         fileInfo.Size(),
		TrackNumber:      1,
		TrackCount:       meta.TrackTotal,
		DiscNumber:       meta.DiscNumber,
		DiscCount:        meta.DiscTotal,
	}
	if meta.TrackNumber > 0 {
		bf.TrackNumber = meta.TrackNumber
	}
	if bfErr := is.db.CreateBookFile(bf); bfErr != nil {
		slog.Warn("import: could not create book_file row — the book has no route to its audio, and organize will skip it",
			"book_id", created.ID, "path", created.FilePath, "err", bfErr)
	}

	// Provision ITL track via the injected iTunes service.
	// Nil provisioner → iTunes disabled or not wired; book is still created.
	if is.provisioner != nil {
		if err := is.provisioner.ProvisionAll(created); err != nil {
			slog.Warn("ITL track provisioning failed", "id", created.ID, "err", err)
		}
	}

	// Post-import dedup check — two paths, selected by config flag:
	//
	//   flag ON  (DedupOnImportViaScheduler=true):
	//     Enqueue dedup.check-book via the UOS scheduler. The op is Batchable
	//     (M3) so burst enqueues are coalesced, and Requires book_sig_v1 (M4)
	//     so the check is deferred until fingerprinting has completed.
	//
	//   flag OFF (default):
	//     Eager goroutine — existing behavior, unchanged for instant rollback.
	if config.AppConfig.Dedup.OnImportViaScheduler && is.opRegistry != nil {
		if _, err := is.opRegistry.EnqueueOp(context.Background(), "dedup.check-book",
			map[string]any{"book_id": created.ID}); err != nil {
			slog.Warn("dedup-on-import: EnqueueOp dedup.check-book", "id", created.ID, "err", err)
		}
	} else if is.dedupEngine != nil {
		go func(id string) {
			if _, err := is.dedupEngine.CheckBook(context.Background(), id); err != nil {
				slog.Warn("dedup-on-import CheckBook", "id", id, "err", err)
			}
		}(created.ID)
	}

	return &ImportFileResponse{
		ID:             created.ID,
		Title:          created.Title,
		FilePath:       created.FilePath,
		AuthorResolved: created.AuthorID != nil || created.Author != nil,
	}, nil
}
