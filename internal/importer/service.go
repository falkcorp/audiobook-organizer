// file: internal/importer/service.go
// version: 1.23.0
// guid: d0e1f2a3-b4c5-6d7e-8f9a-0b1c2d3e4f5b
// last-edited: 2026-10-18

package importer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/authorcredit"
	"github.com/falkcorp/audiobook-organizer/internal/bookfileaudio"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/filehash"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/logging"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/personname"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
	"github.com/falkcorp/audiobook-organizer/internal/versions"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// Store is the slice of the database this service uses: seven methods it calls
// directly, plus three forwarding constraints embedded by name.
//
// It was `= database.Store` (398 methods) with a comment saying it had been
// "temporarily widened ... because versions.CreateIngestVersion requires the
// full Store interface", and that "a future ISP pass on the versions package
// will re-narrow this." That pass happened in #2582; this is the re-narrowing
// it promised. The alias had been widened in the meantime, so nothing here was
// ever measured -- it was inherited.

// importForwarded is what this package hands its store to. Named rather than
// inlined: three entries instead of ~20, and each re-narrows on its own.
type importForwarded interface {
	// fileops.ValidateUserPath, and NewImportPathService.
	database.ImportPathStore
	// versions.CheckFingerprint and versions.LiveTorrentHolder.
	versions.TorrentHolderReader
	// versions.CreateIngestVersion.
	versions.IngestStore
}

type importEntityStore interface {
	GetAuthorByName(name string) (*database.Author, error)
	CreateAuthor(name string) (*database.Author, error)
	// SetBookAuthors credits every author of a multi-author credit.
	SetBookAuthors(bookID string, authors []database.BookAuthor) error
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

type ImportService struct {
	db          Store
	dedupEngine *dedup.Engine
	// opRegistry is the UOS operation registry. When set and
	// config.AppConfig.Dedup.OnImportViaScheduler is true, post-import dedup
	// checks are routed through the scheduler (dedup.check-book op) instead
	// of the eager goroutine. Nil when the registry is not yet available.
	opRegistry sdk.Registry
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
	// A non-HTTP caller therefore gets no organize from setting this. A direct
	// caller should pass false explicitly rather than rely on the zero value, to
	// document that the field was considered (the Deluge discover-import route
	// did, until 01-P81k retired it). If you add another direct caller of
	// ImportFile, honor this yourself or leave it false; do not assume the
	// service acts on it. It was decoded and ignored
	// everywhere until 2026-08-25, which is exactly the bug this comment exists
	// to stop recurring.
	Organize bool `json:"organize"`
	// TorrentHash is the infohash of the torrent this file came from.
	//
	// It is NOT bindable from JSON (`json:"-"`): POST /api/v1/import/file binds
	// this struct straight from the request body under a weaker permission than
	// the Deluge discover route, so a JSON tag here would let any caller store
	// a hash nobody checked. Only trusted Go callers set it. The one that did,
	// POST /api/v1/deluge/discover/import (after cross-checking the hash against
	// Deluge), was retired by 01-P81k, so no production caller sets it today; the
	// link logic below is kept for the Deluge discovery work and is tested by
	// calling ImportFile directly.
	//
	// ImportFile does NOT verify the hash against Deluge. It checks only the
	// shape (40 or 64 lower-case hex) and trusts the caller for the rest, so a
	// new caller must do its own verification first. An empty value means "no
	// torrent". A hash whose content was previously purged or blocked is
	// imported but never linked (see ImportFileResponse.TorrentLinked). A hash
	// a live version already holds is refused outright: ImportFile returns
	// ErrTorrentAlreadyLinked and creates nothing, and a caller that sets a
	// hash must handle that. When it is linked it is
	// written onto the new BookVersion (TorrentHash, Source "deluge") and the
	// new BookFile (DelugeHash, next to its VersionID). It does not mark the
	// file as copied: the later copy into the library sets
	// ImportedFromDelugeAt and DelugeOriginalPath.
	TorrentHash string `json:"-"`
}

// validTorrentHash reports whether s is exactly 40 (SHA-1) or 64 (SHA-256)
// lowercase hex characters. internal/importer does not import
// internal/deluge, so this mirrors deluge.NormalizeTorrentID's accepted
// shape; a server test asserts the two agree.
func validTorrentHash(s string) bool {
	if n := len(s); n != 40 && n != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
			return false
		}
	}
	return true
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

	// TorrentLinked reports what was actually written for
	// ImportFileRequest.TorrentHash: true only when a BookVersion carrying the
	// hash was created AND the BookFile row was linked to it (VersionID and
	// DelugeHash written in one update). It is false whenever no hash was
	// given. TorrentLinkNote says why a given hash was not linked (one of the
	// TorrentLinkNote* constants).
	//
	// Neither is serialized: the field they describe is not bindable from JSON
	// either, so POST /api/v1/import/file has nothing to report, and the one
	// caller that set a hash (the retired Deluge discover import) lifted them
	// into its own response.
	TorrentLinked   bool   `json:"-"`
	TorrentLinkNote string `json:"-"`
}

// Fixed notes for ImportFileResponse.TorrentLinkNote. They are API text, so
// they never carry store or upstream error detail; that goes to the log.
const (
	// TorrentLinkNotePurged: the hash names content that was previously
	// purged or blocked, so the file is imported but never linked to it.
	TorrentLinkNotePurged = "content previously purged"
	// TorrentLinkNoteVersionFailed: the version row could not be created, so
	// neither the version nor the file row carries the hash.
	TorrentLinkNoteVersionFailed = "version not recorded"
	// TorrentLinkNoteFileFailed: the book_file row could not be written, so
	// the version carrying the hash was created with nothing to link to.
	TorrentLinkNoteFileFailed = "book file row not recorded"
	// TorrentLinkNoteLinkFailed: the book_file row and the version carrying
	// the hash were both created, but the write linking them (VersionID and
	// DelugeHash on the row) did not land: it failed, or its compare-and-set
	// refused the row because another writer changed it first (see
	// versions.CreateIngestVersion). The row does not carry the hash; after a
	// refusal it keeps the other writer's VersionID, DelugeHash or path. The
	// version still holds the torrent index, so a retry is refused as
	// already linked.
	TorrentLinkNoteLinkFailed = "book file row not linked to its version"
	// TorrentLinkNoteAlreadyLinked: a live version of another book took the
	// hash in the one-to-one torrent index AFTER the already-linked guard
	// passed (only a writer that does not take the per-torrent lock can do
	// that), when the book already exists and cannot be refused. Linking would
	// move the index to the new book and orphan the existing link, so the file
	// is imported unlinked and the existing link is left alone. A holder found
	// BEFORE the book is created is refused with ErrTorrentAlreadyLinked
	// instead, and sets no note.
	TorrentLinkNoteAlreadyLinked = "torrent already linked to another book"
	// TorrentLinkNoteLookupFailed: the torrent index could not be read, so
	// whether the hash is already linked is unknown and it is not linked.
	TorrentLinkNoteLookupFailed = "torrent index not readable"
)

// ErrTorrentAlreadyLinked is returned by ImportFile, before it creates
// anything, when ImportFileRequest.TorrentHash is held by a live version. The
// concrete error is a *TorrentAlreadyLinkedError naming the holder, so a
// caller can both test errors.Is(err, ErrTorrentAlreadyLinked) and read the
// book that owns the torrent with errors.As.
//
// It is a refusal, not a failure: the torrent is already in the library, so
// the request is a retry or a duplicate, and importing it would only mint a
// second, unlinked book. The discover handler maps it to the same 409 its own
// pre-check returns. The handler's pre-check runs outside the per-torrent
// lock, so a doubled request passes it twice; this is the check that holds.
var ErrTorrentAlreadyLinked = errors.New("torrent already linked to a book in the library")

// TorrentAlreadyLinkedError is the concrete error behind
// ErrTorrentAlreadyLinked.
type TorrentAlreadyLinkedError struct {
	// BookID and VersionID name the live holder of the torrent.
	BookID, VersionID string
}

func (e *TorrentAlreadyLinkedError) Error() string {
	return fmt.Sprintf("%s: held by version %s of book %s", ErrTorrentAlreadyLinked, e.VersionID, e.BookID)
}

// Is makes errors.Is(err, ErrTorrentAlreadyLinked) hold for every holder.
func (e *TorrentAlreadyLinkedError) Is(target error) bool { return target == ErrTorrentAlreadyLinked }

func (is *ImportService) ImportFile(req *ImportFileRequest) (*ImportFileResponse, error) {
	if req.TorrentHash != "" && !validTorrentHash(req.TorrentHash) {
		return nil, fmt.Errorf("invalid torrent hash: want 40 or 64 lowercase hex characters")
	}
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
		bm, bmErr := metadata.AssembleBookMetadataWith(dirPath, firstFile, 0, 0, scanner.FolderNameEvidence())
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

	// Set author if available. importAuthors holds every author of a
	// multi-author credit; they are credited once the book exists.
	var importAuthors []database.Author
	if meta.Artist != "" {
		normalizedArtist := dedup.NormalizeAuthorName(meta.Artist)
		// Creation gate (C413): copyright fragments and entity shrapnel from
		// artist tags must not become author rows. A book with NO author is
		// accurate; an author named "&#169" is a repair job.
		//
		// The shared gate (personname.PrepareAuthorNameForCreation) also runs
		// here, so "read by narrator", "14 BBY" and friends are refused the
		// same way, and a store-side refusal (database.ErrImplausibleAuthorName)
		// means "no author" rather than a failed import.
		prepared, why := personname.PrepareAuthorNameForCreation(normalizedArtist)
		if dedup.IsDirtyAuthorName(normalizedArtist) || why != "" {
			slog.Warn("importer: artist tag rejected as author name",
				"artist", logging.Sanitize(meta.Artist), "path", logging.Sanitize(req.FilePath), "reason", string(why))
		} else {
			// Looked up with the shared helper (authorcredit.Lookup), which
			// never creates: a credit naming several existing authors links
			// them (the first is the primary, all are credited once the book
			// exists) and a single existing author links it. On a miss, ONE
			// person-shaped name ("Stephen King") is created, as this
			// importer (and every Deluge auto-import through it) always did;
			// only a credit of several names none of which exists stays
			// authorless (owner decision; #3729 review). A lookup error is a
			// failed import. A combined credit whose pieces are all different
			// existing authors is no author (ErrCombinedCredit). A collective
			// credit ("Full Cast") names no person and is never created.
			authors, err := authorcredit.Lookup(is.db, prepared, authorcredit.CleanGate)
			if err == nil && len(authors) == 0 && !authorcredit.IsMultiName(prepared) && personname.LooksLikePersonName(prepared) &&
				!authorcredit.IsCollectiveCredit(prepared) {
				if _, ok := authorcredit.CleanGate(prepared); ok {
					authors, err = authorcredit.Resolve(is.db, prepared, authorcredit.CleanGate)
				}
			}
			switch {
			case errors.Is(err, authorcredit.ErrCombinedCredit):
				logger.New("importer").Warn("artist tag %q joins existing authors the splitter will not split; not crediting it (path %s)",
					logger.SanitizeLogValue(meta.Artist), logger.SanitizeLogValue(req.FilePath))
			case errors.Is(err, database.ErrImplausibleAuthorName):
				// The store's gate refused it: no author, as above.
			case err != nil:
				return nil, fmt.Errorf("failed to resolve author: %w", err)
			case len(authors) > 0:
				book.AuthorID = &authors[0].ID
				importAuthors = authors
			}
		}
	}

	// Set series if available. A series named after the book's author is the
	// "Author - Title" split read as "Series - Title" (author junk), so it is
	// neither looked up nor created (seriesNamesAuthor).
	if meta.Series != "" && book.AuthorID != nil && !seriesNamesAuthor(meta.Series, meta.Artist, importAuthors) {
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

	// Identity hash, computed ONCE per import and written onto the book_file
	// row at creation; versions.CreateIngestVersion trusts a row that already
	// carries one and does not read the file again. filehash.BookFileHash is
	// the algorithm dedup's exact-file collector expects. Its cost is
	// bounded: a whole-file read at or below its 100 MB threshold, 10 MB from
	// each end above it, so a multi-GB file is never read in full. This is the
	// same read CreateIngestVersion did before; it moved, it was not added.
	//
	// Hashed HERE, before the torrent guards and the per-torrent lock below,
	// so the lock is never held across a read that can take seconds on a NAS,
	// and so the window in which a book has no file row stays short. A
	// failure leaves the hash empty (CreateIngestVersion retries it once, a
	// backfill after that) and never fails the import.
	fileHash, hashErr := filehash.BookFileHash(req.FilePath)
	if hashErr != nil {
		logger.New("importer").Warn("identity hash failed for %s; file_hash left empty for backfill: %s",
			logger.SanitizeLogValue(req.FilePath), logger.SanitizeLogValue(hashErr.Error()))
		fileHash = ""
	}

	// Per-torrent link lock, held from the guards below through
	// CreateIngestVersion's return (see versions.LockTorrent). Without it two
	// concurrent imports of one torrent both pass the already-linked guard,
	// both create a book, and both create a version carrying the hash, so the
	// index ends up on the second book while the first book's row still claims
	// the torrent. With it the second waits, then finds the first as the live
	// holder and is refused with ErrTorrentAlreadyLinked before it creates
	// anything: one book, as a sequential retry through the discover handler
	// (a 409 from its own pre-check) already gave. The handler's pre-check
	// runs outside this lock, so it cannot stop a doubled request; this guard
	// can. An empty hash takes no lock. releaseTorrent is idempotent: called
	// once the version exists, and deferred for the early returns in between
	// (the refusal included).
	releaseTorrent := sync.OnceFunc(versions.LockTorrent(req.TorrentHash))
	defer releaseTorrent()

	// Purge guard for the torrent link, BEFORE the book row exists. Content
	// the owner purged or blocked must never come back linked to its torrent:
	// a linked row joins the bulk-import candidates and later cleanup trusts
	// the link. The import itself still goes ahead (a refusal has never failed
	// an import), the hash is dropped, and no version is created for it, which
	// is what CreateIngestVersion's own fingerprint refusal did before this
	// check moved up. linkHash is local so req is never mutated.
	linkHash := req.TorrentHash
	var linkNote string
	purged := false
	if linkHash != "" {
		if m := versions.CheckFingerprint(is.db, linkHash, nil); m != nil && m.Matched {
			logger.New("importer").Warn("torrent %s matches previously %s content (book %s, version %s); importing %s without the torrent link",
				linkHash, logger.SanitizeLogValue(m.Status), logger.SanitizeLogValue(m.BookID), logger.SanitizeLogValue(m.VersionID),
				logger.SanitizeLogValue(req.FilePath))
			linkHash = ""
			linkNote = TorrentLinkNotePurged
			purged = true
		}
	}
	// Already-linked guard, also BEFORE the book row exists. The torrent index
	// is one-to-one and CreateBookVersion overwrites it unconditionally, so a
	// second link (a retried or doubled import of the same torrent) would move the index
	// from the book that owns the torrent to this new one, orphaning the first
	// link. The torrent is already in the library, so the request is refused
	// with ErrTorrentAlreadyLinked rather than imported as a second, unlinked
	// copy: the duplicate book is the harm, not just the link. The discover
	// handler's own pre-check returns the same 409 for the sequential case;
	// this guard is what catches a request that raced past it, because it runs
	// under the per-torrent lock, where the first import's link is already
	// visible. A failed index read is different: it says nothing about the
	// content, so the import goes ahead unlinked (TorrentLinkNoteLookupFailed).
	if linkHash != "" {
		holder, holdErr := versions.LiveTorrentHolder(is.db, linkHash)
		switch {
		case holdErr != nil:
			logger.New("importer").Warn("could not read the torrent index for %s; importing %s without the torrent link: %s",
				linkHash, logger.SanitizeLogValue(req.FilePath), logger.SanitizeLogValue(holdErr.Error()))
			linkHash = ""
			linkNote = TorrentLinkNoteLookupFailed
		case holder != nil:
			logger.New("importer").Warn("torrent %s is already linked to book %s (version %s); refusing to import %s as a duplicate",
				linkHash, logger.SanitizeLogValue(holder.BookID), logger.SanitizeLogValue(holder.ID),
				logger.SanitizeLogValue(req.FilePath))
			return nil, &TorrentAlreadyLinkedError{BookID: holder.BookID, VersionID: holder.ID}
		}
	}

	// Create book in database
	created, err := is.db.CreateBook(book)
	if err != nil {
		return nil, fmt.Errorf("failed to create book: %w", err)
	}

	// A multi-author credit: every author in the junction, in credit order.
	// The book exists, so a failure here is logged, not an import failure.
	if len(importAuthors) > 1 {
		if err := is.db.SetBookAuthors(created.ID, authorcredit.Credits(created.ID, importAuthors)); err != nil {
			logger.New("importer").Warn("could not record the co-authors of book %s: %s", created.ID, logger.SanitizeLogValue(err.Error()))
		}
	}

	// Give the imported book its book_file row, BEFORE its version.
	//
	// The order is the fix for IMPORT-ORPHANED-VERSIONS (2026-10-10).
	// versions.CreateIngestVersion links the version it creates to an EXISTING
	// row; it never creates one. This block used to run after it, so every
	// import -- torrent or not -- created a version, found no row to link,
	// logged "version is orphaned", and left the row with no VersionID and no
	// FileHash: `unverifiable` to the torrent cleanup fixer until a hash
	// backfill ran.
	//
	// Without this row the book row exists and the audio exists and NOTHING
	// CONNECTS THEM. Every other path that ingests audio creates book_file
	// rows — merge, itunes, metafetch, organizer, both maintenance packages,
	// and the scan path via server.ensureSingleFileBookFile.
	//
	// The immediate consequence of a missing row is that organize-on-import is
	// INERT: FilterBooksNeedingOrganization drops any book whose FilePath is
	// outside RootDir and which has zero book_files, counting it into
	// skippedMissingFiles behind a log.Debug. An imported file is outside
	// RootDir BY DEFINITION, so every import-triggered organize would be
	// silently skipped while the handler still reported a queued op id.
	//
	// This is always the single-file shape: ImportFile rejects directories
	// above, and fileInfo/ext are the values already computed for that check.
	// It therefore does NOT go through scanner.createBookFilesForBookWithAudio, which
	// also normalizes Book.FilePath to the containing directory — correct for a
	// multi-file book, wrong here. Same reasoning as
	// server.ensureSingleFileBookFile, which exists to backfill exactly this
	// gap after the fact.
	//
	// VersionID and DelugeHash are deliberately unset here: CreateIngestVersion
	// writes both in the one update that links the version, so the row never
	// claims a torrent that no version carries.
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
		FileHash:         fileHash,
		TrackNumber:      1,
		TrackCount:       meta.TrackTotal,
		DiscNumber:       meta.DiscNumber,
		DiscCount:        meta.DiscTotal,
	}
	if meta.TrackNumber > 0 {
		bf.TrackNumber = meta.TrackNumber
	}
	// Single-file shape (see above), so the book's duration is the file's; the
	// import reads no media info of its own, so without one this is a header
	// read. A row with Duration 0 shows the book as "0" in ABS, which sums the
	// rows. A probe failure leaves 0 and is logged; it never fails the import.
	known := bookfileaudio.Known{SingleFileBook: true}
	if created.Duration != nil {
		known.BookDurationSec = *created.Duration
	}
	bookfileaudio.EnsureDuration(bf, known, nil)
	// A durability-unknown error means the row WAS written (only its fsync is
	// in doubt), so it is linked like any other row.
	bfErr := is.db.CreateBookFile(bf)
	bfWritten := bfErr == nil || errors.Is(bfErr, database.ErrBookFileDurabilityUnknown)
	if bfErr != nil {
		logger.New("importer").Warn("book_file row write for book %s (%s) reported an error (row written: %t): %s",
			created.ID, logger.SanitizeLogValue(created.FilePath), bfWritten, logger.SanitizeLogValue(bfErr.Error()))
	}
	if !bfWritten {
		slog.Warn("import: could not create book_file row — the book has no route to its audio, and organize will skip it",
			"book_id", created.ID, "path", created.FilePath)
	}

	// Create the version row and link it to the book_file row above (spec
	// 3.1). A purged torrent gets no version at all, exactly as when
	// CreateIngestVersion refused it.
	//
	// When the row was not written the version is STILL created, unlinked (its
	// orphan warning is then accurate): with a torrent hash it holds the
	// one-to-one torrent index, and that index is what makes a retried
	// import of the same torrent a 409 instead of a second book.
	//
	// Failure states, none of which needs a deletion to recover:
	//   - version create fails: the row keeps FileHash, has no VersionID and
	//     no DelugeHash, and the import still succeeds (no caller retry, no
	//     duplicate). CreateIngestVersion over the same row later links it.
	//   - link write fails: the version exists unlinked (logged), the row is
	//     as in the previous case, and the torrent is reported not linked.
	//     CreateIngestVersion over the row later ADOPTS that version instead
	//     of creating a second one.
	//   - row already linked (a re-run): CreateIngestVersion returns the
	//     existing version and creates nothing.
	torrentLinked := false
	if purged {
		logger.New("importer").Warn("not creating a version for book %s: its torrent content was previously purged", created.ID)
	} else {
		versionSource := "imported"
		if linkHash != "" {
			versionSource = "deluge"
		}
		params := versions.IngestVersionParams{
			BookID: created.ID, FilePath: created.FilePath,
			Format: created.Format, Source: versionSource,
			TorrentHash: linkHash,
			// Created above: nothing can be stranded, so CreateIngestVersion
			// skips the stranded-version lookup, and a failure of that one
			// read cannot cost the version. Its holder read (hash set) still
			// runs and can fail on its own; see IngestVersionParams.NewBook.
			NewBook: true,
		}
		if bfWritten {
			params.BookFileID = bf.ID
		}
		ver, linked, verErr := versions.CreateIngestVersion(is.db, params)
		if errors.Is(verErr, versions.ErrTorrentLinkedElsewhere) {
			// The guard above passed under the lock, so only a writer that
			// does not take the lock can get here. Same outcome as the guard:
			// an ordinary unlinked version, and the existing link left alone.
			logger.New("importer").Warn("torrent linked elsewhere after the guard passed for book %s; importing without the link: %s",
				created.ID, logger.SanitizeLogValue(verErr.Error()))
			linkNote = TorrentLinkNoteAlreadyLinked
			linkHash = ""
			params.TorrentHash, params.Source = "", "imported"
			ver, linked, verErr = versions.CreateIngestVersion(is.db, params)
		}
		releaseTorrent()
		switch {
		case verErr != nil:
			slog.Warn("create ingest version", "id", created.ID, "err", verErr)
			if linkHash != "" {
				linkNote = TorrentLinkNoteVersionFailed
			}
		case linkHash == "":
			// No torrent to report on.
		case !bfWritten:
			linkNote = TorrentLinkNoteFileFailed
		case !linked:
			linkNote = TorrentLinkNoteLinkFailed
		case ver != nil && ver.TorrentHash == linkHash:
			torrentLinked = true
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
		if err := dedup.EnqueueCheckBook(context.Background(), is.opRegistry, created.ID); err != nil {
			slog.Warn("dedup-on-import: enqueue failed", "id", created.ID, "err", err)
		}
	} else if is.dedupEngine != nil {
		go func(id string) {
			if _, err := is.dedupEngine.CheckBook(context.Background(), id); err != nil {
				slog.Warn("dedup-on-import CheckBook", "id", id, "err", err)
			}
		}(created.ID)
	}

	return &ImportFileResponse{
		ID:              created.ID,
		Title:           created.Title,
		FilePath:        created.FilePath,
		AuthorResolved:  created.AuthorID != nil || created.Author != nil,
		TorrentLinked:   torrentLinked,
		TorrentLinkNote: linkNote,
	}, nil
}

// seriesNamesAuthor reports whether series names the artist credit or one of
// the authors it resolved to (personname.NamesCredit).
func seriesNamesAuthor(series, artist string, authors []database.Author) bool {
	if personname.NamesCredit(series, artist) {
		return true
	}
	for _, a := range authors {
		if personname.NamesCredit(series, a.Name) {
			return true
		}
	}
	return false
}
