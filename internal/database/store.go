// file: internal/database/store.go
// version: 2.128.3
// guid: 8a9b0c1d-2e3f-4a5b-6c7d-8e9f0a1b2c3d
// last-edited: 2026-10-27

package database

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Store defines the full database surface. Most services should depend
// on a narrower sub-interface defined in iface_*.go; Store itself is
// used by the server bootstrap and test fixtures that genuinely need
// wide access. See docs/archive/superpowers/plans/2026-04-17-store-interface-segregation-design.md.
//
// The 40 sub-interfaces it used to embed directly are grouped by domain below.
// This is a REGROUPING, not a narrowing: Store's method set is byte-identical
// and no consumer moved. It exists because `interfacebloat` counts embedded
// entries, so a flat list of 40 was the last finding in the whole repo and held
// the width baseline at 1. Each group is also a usable dependency in its own
// right for anything that genuinely needs a whole domain.
//
// scripts/verify_interface_split.py proves the signature set is unchanged.
// The six composites below are deliberately UNEXPORTED, and that is the whole
// point of them.
//
// Exporting them would create six brand-new nameable wide types -- catalogStore
// alone reaches ~60 methods -- each declaring <= 8 entries and therefore
// permanently invisible to interfacebloat, the very linter this grouping exists
// to satisfy. A consumer could then depend on database.CatalogStore and be as
// over-coupled as it was before, with the gate reporting 0. That is the exact
// re-widening this lane spent 28 PRs undoing: the established idiom here is a
// consumer-side interface declared where it is USED (250 of them exist outside
// this package, median 2 methods), not a broad one offered from here.
//
// Unexported costs nothing. Their methods are exported, so Store stays usable
// and implementable from any package; only the GROUPING NAMES are package-private.
//
// One of them would also have been actively misleading:
// internal/server/handlers/operations already declares its own OperationsStore,
// a narrow consumer interface. Two types with that name meaning opposite things
// is a trap for whoever reads the second one first.
type Store interface {
	catalogStore
	mediaStore
	accountStore
	enrichmentStore
	operationsStore
	platformStore
}

// catalogStore is the library's bibliographic entities -- what a book IS.
type catalogStore interface {
	BookStore
	AuthorStore
	SeriesStore
	NarratorStore
	WorkStore
	BookVersionStore
	CollectionStore
}

// mediaStore is the physical/playable layer -- files on disk, their segments,
// ordering, and where a listener is in them.
type mediaStore interface {
	BookFileStore
	BookSegmentStore
	ChapterReader
	PlaylistStore
	UserPlaylistStore
	PathHistoryStore
	PlaybackStore
}

// ChapterReader reads the per-book chapter table ("chapters:<bookID>").
//
// READ ONLY, and on Store deliberately. The table is written by the scanner and
// the chapters backfill, which hold the concrete store; the reason this is on
// the Store interface at all is that dedup now scores chapter structure
// (SigChapterStructure), and production wrapped the store in the Bleve
// indexedStore decorator (removed in 07-S3). A capability assertion for a method
// that is NOT on this interface silently misses through any such decorator -- the shape that hid
// LiveBookIDsAtPath in #3335 -- and a dedup signal that never fires is
// indistinguishable from one that found nothing. Declaring it here makes a
// decorator that forgets to forward it fail the BUILD instead.
//
// Writes stay off Store on purpose: nothing needs to write chapters through
// this interface, and widening it further would be surface for no caller.
type ChapterReader interface {
	GetChaptersForBook(bookID string) ([]Chapter, error)
}

// accountStore is identity and authorisation.
type accountStore interface {
	UserStore
	SessionStore
	OAuthIdentityStore
	RoleStore
	APIKeyStore
	InviteStore
	UserPreferenceStore
}

// enrichmentStore is everything that DESCRIBES a book without being part of it:
// fetched metadata, its caches and rejections, external identifiers, tags.
type enrichmentStore interface {
	MetadataStore
	MetadataCacheStore
	RejectedMetadataStore
	ExternalIDStore
	TagStore
	UserTagStore
	HashBlocklistStore
}

// operationsStore is background work -- ops v1/v2, maintenance jobs, the activity
// feed, AI jobs, and the human review queue.
type operationsStore interface {
	OperationStore
	OpsV2Store
	MaintenanceStore
	SystemActivityStore
	AIJobsStore
	ReviewStore
}

// platformStore is process- and deployment-level concerns rather than library
// content: lifecycle, configuration, import roots, raw KV, and aggregates.
type platformStore interface {
	LifecycleStore
	ImportPathStore
	ITunesStateStore
	RawKVStore
	SettingsStore
	StatsStore
	UserPositionStore
}

// BookAlternativeTitle represents a variant name for a book — romaji
// vs English, ampersand vs "and", subtitle reordering, rebrands, etc.
// Used by the dedup engine's Layer 1 exact title matching and by
// library search so either form finds the book.
type BookAlternativeTitle struct {
	ID        int64     `json:"id"`
	BookID    string    `json:"book_id"`
	Title     string    `json:"title"`
	Source    string    `json:"source"` // "user", "metadata_fetch", "auto_ampersand", etc.
	Language  string    `json:"language,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Common data structures used by all store implementations
// Note: These are defined here instead of in web.go to avoid circular dependencies

// Author represents an audiobook author
type Author struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// AuthorAlias represents a pen name, handle, or alternative name for an author
type AuthorAlias struct {
	ID        int       `json:"id"`
	AuthorID  int       `json:"author_id"`
	AliasName string    `json:"alias_name"`
	AliasType string    `json:"alias_type"` // pen_name, real_name, handle, abbreviation, alias
	CreatedAt time.Time `json:"created_at"`
}

// Narrator represents an audiobook narrator
type Narrator struct {
	ID        int       `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// BookNarrator represents the many-to-many relationship between books and narrators
type BookNarrator struct {
	BookID     string `json:"book_id"`
	NarratorID int    `json:"narrator_id"`
	Role       string `json:"role"`     // narrator, co-narrator
	Position   int    `json:"position"` // 0 = primary
}

// Series represents an audiobook series
type Series struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	AuthorID *int   `json:"author_id,omitempty"`
	// HeldBy names what keeps this row alive while it holds no books (a
	// repair whose undo links books back to it; see HoldSeries). DeleteSeries
	// refuses a held row, and the orphan prune passes it over.
	HeldBy string `json:"held_by,omitempty"`
}

// Book represents an audiobook
type Book struct {
	ID             string `json:"id"` // ULID format
	Title          string `json:"title"`
	AuthorID       *int   `json:"author_id,omitempty"`
	SeriesID       *int   `json:"series_id,omitempty"`
	SeriesSequence *int   `json:"series_sequence,omitempty"`
	FilePath       string `json:"file_path"`
	Format         string `json:"format,omitempty"`
	Duration       *int   `json:"duration,omitempty"`
	// Extended metadata (optional)
	WorkID               *string `json:"work_id,omitempty"`
	Narrator             *string `json:"narrator,omitempty"`
	Edition              *string `json:"edition,omitempty"`
	Description          *string `json:"description,omitempty"`
	Language             *string `json:"language,omitempty"`
	Publisher            *string `json:"publisher,omitempty"`
	Genre                *string `json:"genre,omitempty"`
	PrintYear            *int    `json:"print_year,omitempty"`
	AudiobookReleaseYear *int    `json:"audiobook_release_year,omitempty"`
	ISBN10               *string `json:"isbn10,omitempty"`
	ISBN13               *string `json:"isbn13,omitempty"`
	ASIN                 *string `json:"asin,omitempty"`
	// Content-matcher SIGNAL fields (captured from provider data previously
	// decoded-then-dropped). These are identify/match signals, NOT authoritative
	// served fields: e.g. SeriesPositionRaw preserves the decimal ("1.5") that the
	// served *int SeriesSequence cannot hold, and Abridged/SeriesSecondary have no
	// served counterpart. Never treat these as the canonical served value.
	Abridged                *bool   `json:"abridged,omitempty"`                  // Audible format_type; nil = unknown
	Subtitle                *string `json:"subtitle,omitempty"`                  // provider subtitle (Audible/Audnexus)
	PageCount               *int    `json:"page_count,omitempty"`                // print page count (Hardcover)
	SeriesPositionRaw       *string `json:"series_position_raw,omitempty"`       // raw series position incl. decimals
	SeriesSecondary         *string `json:"series_secondary,omitempty"`          // secondary series name (Audnexus)
	SeriesSecondaryPosition *string `json:"series_secondary_position,omitempty"` // secondary series position
	// External provider IDs
	OpenLibraryID *string `json:"open_library_id,omitempty"`
	HardcoverID   *string `json:"hardcover_id,omitempty"`
	GoogleBooksID *string `json:"google_books_id,omitempty"`
	// iTunes import fields
	ITunesPersistentID *string    `json:"itunes_persistent_id,omitempty"`
	ITunesDateAdded    *time.Time `json:"itunes_date_added,omitempty"`
	ITunesPlayCount    *int       `json:"itunes_play_count,omitempty"`
	ITunesLastPlayed   *time.Time `json:"itunes_last_played,omitempty"`
	// ITunesPlayCountBumpedAt is the listening-activity time of the finish
	// the position sync last counted into ITunesPlayCount. The sync bumps the
	// count only for a finish newer than this, so re-running it over the same
	// finished book does not add a play each time. A dedicated field rather
	// than ITunesLastPlayed, which the library sync overwrites from iTunes.
	ITunesPlayCountBumpedAt *time.Time `json:"itunes_play_count_bumped_at,omitempty"`
	ITunesRating            *int       `json:"itunes_rating,omitempty"`
	ITunesBookmark          *int64     `json:"itunes_bookmark,omitempty"`
	ITunesImportSource      *string    `json:"itunes_import_source,omitempty"`
	// Deprecated: use book_files.itunes_path instead. Will be removed in a future migration.
	ITunesPath       *string `json:"itunes_path,omitempty"`
	OriginalFilename *string `json:"original_filename,omitempty"`
	// Media info fields
	Bitrate    *int    `json:"bitrate_kbps,omitempty"`
	Codec      *string `json:"codec,omitempty"`
	SampleRate *int    `json:"sample_rate_hz,omitempty"`
	Channels   *int    `json:"channels,omitempty"`
	BitDepth   *int    `json:"bit_depth,omitempty"`
	Quality    *string `json:"quality,omitempty"`
	// Version management
	IsPrimaryVersion *bool   `json:"is_primary_version,omitempty"`
	VersionGroupID   *string `json:"version_group_id,omitempty"`
	VersionNotes     *string `json:"version_notes,omitempty"`
	// File hash tracking for deduplication
	FileHash *string `json:"file_hash,omitempty"`
	FileSize *int64  `json:"file_size,omitempty"`
	// Lifecycle tracking
	OriginalFileHash    *string    `json:"original_file_hash,omitempty"`
	OrganizedFileHash   *string    `json:"organized_file_hash,omitempty"`
	LibraryState        *string    `json:"library_state,omitempty"`
	Quantity            *int       `json:"quantity,omitempty"`
	MarkedForDeletion   *bool      `json:"marked_for_deletion,omitempty"`
	MarkedForDeletionAt *time.Time `json:"marked_for_deletion_at,omitempty"`
	// PreTrashLibraryState is the LibraryState a trash path overwrote with
	// "deleted", so a restore can put it back (database.RestoreLibraryStateFromTrash).
	// Nil when the row is live, or was trashed by a path that kept its state.
	PreTrashLibraryState *string `json:"pre_trash_library_state,omitempty"`
	// QuarantineReason is set when a file is moved to .failed/. Non-nil means quarantined.
	QuarantineReason *string    `json:"quarantine_reason,omitempty"`
	QuarantinedAt    *time.Time `json:"quarantined_at,omitempty"`
	CreatedAt        *time.Time `json:"created_at,omitempty"`
	// UpdatedAt is set on every DB write (system-level).
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
	// MetadataUpdatedAt is set only when user-visible metadata fields change.
	MetadataUpdatedAt *time.Time `json:"metadata_updated_at,omitempty"`
	// LastWrittenAt is set when metadata is written back to the audio files on disk.
	LastWrittenAt *time.Time `json:"last_written_at,omitempty"`
	// LastOrganizeOperationID is the operation ID of the last organize run that processed this book.
	LastOrganizeOperationID *string `json:"last_organize_operation_id,omitempty"`
	// LastOrganizedAt is when this book was last stamped by an organize run (organized, re-organized, or confirmed correct).
	LastOrganizedAt *time.Time `json:"last_organized_at,omitempty"`
	// MetadataReviewStatus tracks metadata matching: null (nobody has ruled on
	// this book), "no_match", "matched", or "audio_confirmed" — the last written
	// by metafetch/service_apply.go when a candidate title matched the book's own
	// transcribed audio. This comment listed only the first three until
	// 2026-09-08, years after the apply path started writing the fourth, and a
	// switch in handlers/metadata_cache.go trusted it: `audio_confirmed` books
	// were reported as still awaiting review.
	MetadataReviewStatus *string `json:"metadata_review_status,omitempty"`
	// MetadataSource records which provider supplied the last applied metadata
	// (e.g. "audible", "google_books", "openlibrary"). Used to selectively
	// re-fetch from a specific source in the future.
	MetadataSource *string `json:"metadata_source,omitempty"`
	// Book signature fields for unified per-book audio fingerprint (spec: 2026-05-03-unified-book-fingerprint.md).
	// BookSigV1 is the base64-encoded down-sampled book signature (4096 uint32s).
	// BookSigSegments is the pre-downsample segment count (for diagnostics).
	// BookSigBuiltAt is when the signature was last synthesized.
	BookSigV1          *string    `json:"book_sig_v1,omitempty"`
	BookSigSegments    *int       `json:"book_sig_segments,omitempty"`
	BookSigBuiltAt     *time.Time `json:"book_sig_built_at,omitempty"`
	BookSigV1Mask      *string    `json:"book_sig_v1_mask,omitempty"`      // 4096-bit coverage mask (base64)
	BookSigCoveragePct *int       `json:"book_sig_coverage_pct,omitempty"` // 0–100; nil = full coverage
	// BookSigVersion is the fingerprint.BookSignatureVersion the signature was
	// synthesized under. nil/0 = legacy: built before 2026-09-19 from
	// misdecoded (compressed-bitstream) prints, so it is not comparable with
	// anything and every similarity consumer must treat it as MISSING
	// evidence (HasCurrentBookSig), never as a mismatch.
	BookSigVersion *int `json:"book_sig_version,omitempty"`
	// ITunesSyncStatus tracks whether this book's metadata is in sync with the iTunes library.
	// Values: "synced" (up-to-date in ITL), "dirty" (changed since last write-back),
	// "unlinked" (no iTunes presence), "pending" (new, needs adding to iTunes),
	// "purge_pending" (quarantined, delete from iTunes on next sync).
	ITunesSyncStatus *string `json:"itunes_sync_status,omitempty"`
	// AudibleRuntimeMin is the runtime reported by Audible (in minutes) for
	// the matched product. Stored during metadata apply so the maintenance
	// scan-duration-mismatch endpoint can compare it against the local file
	// Duration without making live API calls.
	AudibleRuntimeMin *int `json:"audible_runtime_min,omitempty"`
	// DurationVerifiedAt is stamped by the duration-reextract op after it
	// confirms or corrects a book's duration. Used to skip recently-verified
	// books on subsequent runs (see SkipAgeDays param).
	DurationVerifiedAt *time.Time `json:"duration_verified_at,omitempty"`
	// MetadataSourceHash is sha256("{source}:{canonical_id}") set during
	// metadata apply. Enables O(1) dedup detection: books sharing an ASIN
	// or ISBN-13 will have the same hash and are almost certainly the same
	// title. Format: sha256("audible:B0XXXXXXXX"), sha256("openlibrary:/works/OL123W"), etc.
	MetadataSourceHash *string `json:"metadata_source_hash,omitempty"`
	// MergedIntoBookID is set when this book has been absorbed into a consolidated
	// book by the chapter-consolidation maintenance operation (MATCH-2).
	// The primary book's ID is stored here so the merge is auditable.
	MergedIntoBookID *string `json:"merged_into_book_id,omitempty"`
	// Audible ratings (1–5 scale). Performance and Story are audiobook-specific
	// dimensions absent from other providers.
	AudibleRatingOverall     *float64 `json:"audible_rating_overall,omitempty"`
	AudibleRatingPerformance *float64 `json:"audible_rating_performance,omitempty"` // narrator/production
	AudibleRatingStory       *float64 `json:"audible_rating_story,omitempty"`       // story/content
	AudibleRatingCount       *int     `json:"audible_rating_count,omitempty"`
	AudibleNumReviews        *int     `json:"audible_num_reviews,omitempty"`
	// Google Books rating (1–5 scale).
	GoogleRatingAverage *float64 `json:"google_rating_average,omitempty"`
	GoogleRatingCount   *int     `json:"google_rating_count,omitempty"`
	// User personal ratings (1–5 scale, independent of community ratings).
	// UI/API for setting these is TODO — columns are reserved so data can be
	// populated once the rating UI is built.
	UserRatingOverall     *float64 `json:"user_rating_overall,omitempty"`
	UserRatingStory       *float64 `json:"user_rating_story,omitempty"`
	UserRatingPerformance *float64 `json:"user_rating_performance,omitempty"`
	UserRatingNotes       *string  `json:"user_rating_notes,omitempty"`
	// Cover art
	CoverURL *string `json:"cover_url,omitempty"`
	// Narrators as JSON array
	NarratorsJSON *string `json:"narrators_json,omitempty"`
	// SourceImportPath is the top-level import-path folder this book was FIRST
	// discovered in (e.g. "/mnt/bigdata/books/newbooks"). Set on CreateBook only;
	// never mutated on UpdateBook. Survives auto-organize moves so that
	// CountBooksByPathPrefix can correctly count books even after they have been
	// relocated to RootDir.
	SourceImportPath *string `json:"source_import_path,omitempty"`
	// Scan cache for incremental scanning (set by scanner, not user-facing)
	LastScanMtime *int64 `json:"last_scan_mtime,omitempty"`
	LastScanSize  *int64 `json:"last_scan_size,omitempty"`
	NeedsRescan   *bool  `json:"needs_rescan,omitempty"`
	// IntroTranscription is the raw Whisper transcript of the first ~30 seconds
	// of the book's first audio file. Populated by maintenance.transcribe-book-intros.
	// Never used as the canonical title/author — see TranscribedTitle/Author/Narrator.
	IntroTranscription *string `json:"intro_transcription,omitempty"`
	// TranscribedTitle / TranscribedAuthor / TranscribedNarrator are the fields
	// parsed from IntroTranscription ("TITLE by AUTHOR. Read by NARRATOR.").
	// Stored separately from Title/Author/Narrator so transcription errors cannot
	// overwrite curated metadata. UI can show these as a "suggested correction".
	TranscribedTitle    *string `json:"transcribed_title,omitempty"`
	TranscribedAuthor   *string `json:"transcribed_author,omitempty"`
	TranscribedNarrator *string `json:"transcribed_narrator,omitempty"`
	// TranscribedTranslator is credited between author and narrator in
	// translated works; before it existed the author absorbed it.
	TranscribedTranslator *string `json:"transcribed_translator,omitempty"`
	// TranscribedCoverArtist is the album/cover art credit ("Cover art by X").
	TranscribedCoverArtist *string `json:"transcribed_cover_artist,omitempty"`
	// IntroTranscribedAt is when IntroTranscription was last populated.
	IntroTranscribedAt *time.Time `json:"intro_transcribed_at,omitempty"`
	// TranscribeStatus records the outcome of the most recent transcription
	// ATTEMPT for this book, written per-book by maintenance.transcribe-book-intros.
	// One of: "ok", "source_file_missing", "no_audio", "ffmpeg_error",
	// "whisper_error", "path_unmapped", "empty". This is the queryable drill-down field — count
	// books by status to see exactly why transcription is/isn't producing data
	// (e.g. how many books have stale FilePaths after an organize move).
	TranscribeStatus *string `json:"transcribe_status,omitempty"`
	// TranscribeError holds a short human-readable detail for a non-ok status —
	// typically the tail of ffmpeg stderr or the Whisper error string. Empty on ok.
	TranscribeError *string `json:"transcribe_error,omitempty"`
	// TranscribeAttemptedAt is when the most recent transcription attempt ran
	// (success OR failure), distinct from IntroTranscribedAt which only advances
	// on a successful transcription.
	TranscribeAttemptedAt *time.Time `json:"transcribe_attempted_at,omitempty"`
	// Fingerprinting fields (computed, not stored in DB)
	FingerprintStatus      string     `json:"fingerprint_status,omitempty"` // "none", "partial", "complete"
	FingerprintedFileCount int        `json:"fingerprinted_file_count,omitempty"`
	TotalFileCount         int        `json:"total_file_count,omitempty"`
	CoveragePercent        int        `json:"coverage_percent,omitempty"`
	LastFingerprintedAt    *time.Time `json:"last_fingerprinted_at,omitempty"`
	// Related objects (populated via joins, not stored in DB)
	Author               *Author                            `json:"author,omitempty" db:"-"`
	Authors              []BookAuthor                       `json:"authors,omitempty" db:"-"`
	Series               *Series                            `json:"series,omitempty" db:"-"`
	MetadataProvenance   map[string]MetadataProvenanceEntry `json:"metadata_provenance,omitempty" db:"-"`
	MetadataProvenanceAt *time.Time                         `json:"metadata_provenance_at,omitempty" db:"-"`
}

// BookSummary represents the subset of Book fields needed for library list views.
// It excludes heavy fields like full descriptions, raw cover bytes, and embeddings.
// This struct is designed for use in paginated list queries to improve performance
// by avoiding unnecessary data transfer. Companion field to Book for read-only
// projection use cases (PROJ-2 SQL query optimization).
type BookSummary struct {
	ID                string     `json:"id"`
	Title             string     `json:"title"`
	AuthorID          *int       `json:"author_id,omitempty"`
	SeriesID          *int       `json:"series_id,omitempty"`
	SeriesSequence    *int       `json:"series_sequence,omitempty"`
	FilePath          string     `json:"file_path"`
	Format            string     `json:"format,omitempty"`
	Duration          *int       `json:"duration,omitempty"`
	OriginalFilename  *string    `json:"original_filename,omitempty"`
	FileSize          *int64     `json:"file_size,omitempty"`
	FileHash          *string    `json:"file_hash,omitempty"`
	OriginalFileHash  *string    `json:"original_file_hash,omitempty"`
	OrganizedFileHash *string    `json:"organized_file_hash,omitempty"`
	LibraryState      *string    `json:"library_state,omitempty"`
	QuarantinedAt     *time.Time `json:"quarantined_at,omitempty"`
	QuarantineReason  *string    `json:"quarantine_reason,omitempty"`
	CoverURL          *string    `json:"cover_url,omitempty"`
	Narrator          *string    `json:"narrator,omitempty"`
	// NarratorsJSON carries the SECOND narrator tier into the summary projection.
	// Narrator data lives in three places (see resolveNarrators in the abs mapper):
	// the BookNarrator junction, this JSON array, and the legacy Narrator column.
	// Without this field a summary-only caller can read tiers 1 and 3 but silently
	// miss tier 2 — which is exactly how the Narrators tab came to show 8 names for
	// a library where ~57% of visible books have a narrator stored.
	NarratorsJSON *string `json:"narrators_json,omitempty"`
	// TranscribedTitle is the Whisper-parsed intro title, carried in the
	// summary projection so filter-pushdown callers (e.g.
	// FilterSpec.OnlyParsedTranscription) can distinguish parsed from
	// raw-but-unparsed transcriptions without a full-book fetch. Empty for the
	// vast majority of books; nil unless the intro parsed into a title.
	TranscribedTitle     *string    `json:"transcribed_title,omitempty"`
	CreatedAt            *time.Time `json:"created_at,omitempty"`
	UpdatedAt            *time.Time `json:"updated_at,omitempty"`
	MetadataUpdatedAt    *time.Time `json:"metadata_updated_at,omitempty"`
	IsPrimaryVersion     *bool      `json:"is_primary_version,omitempty"`
	VersionGroupID       *string    `json:"version_group_id,omitempty"`
	MetadataReviewStatus *string    `json:"metadata_review_status,omitempty"`
}

// Work represents a logical title-level grouping that may span multiple editions,
// narrations, languages, or publishers. Books can optionally reference a Work via WorkID.
type Work struct {
	ID        string   `json:"id"`    // ULID format
	Title     string   `json:"title"` // Canonical title
	AuthorID  *int     `json:"author_id,omitempty"`
	SeriesID  *int     `json:"series_id,omitempty"`
	AltTitles []string `json:"alt_titles,omitempty"` // Optional alternate titles
}

// Playlist represents an auto-generated series playlist (the old
// M3U-style playlist generator). For the 3.4 user-facing playlist
// feature, see UserPlaylist below.
type Playlist struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	SeriesID *int   `json:"series_id,omitempty"`
	FilePath string `json:"file_path"`
}

// UserPlaylist represents a user-created playlist (spec 3.4) —
// either a static ordered book list or a smart (live-evaluated)
// filter expression. Distinct from the auto-generated Playlist
// type above which is part of the older series-playlist generator.
type UserPlaylist struct {
	ID          string `json:"id"` // ULID
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Type discriminator: "static" or "smart"
	Type string `json:"type"`
	// Static playlists: ordered book ID list.
	BookIDs []string `json:"book_ids,omitempty"`
	// Smart playlists: DSL query string, sort + limit directives.
	Query string `json:"query,omitempty"`
	// SortJSON is a JSON-encoded []{field, direction} for stable
	// ordering in smart playlists.
	SortJSON string `json:"sort_json,omitempty"`
	Limit    int    `json:"limit,omitempty"`
	// MaterializedBookIDs caches the last evaluation of a smart
	// playlist for fast iTunes-sync pushes without re-running the
	// query. Refreshed on every sync pass.
	MaterializedBookIDs []string `json:"materialized_book_ids,omitempty"`
	// MaterializedTotal is how many books the last evaluation matched when it
	// matched more than MaxMaterializedMembers and MaterializedBookIDs holds only
	// the first of them. Zero means the list is complete.
	MaterializedTotal int `json:"materialized_total,omitempty"`
	// ITunesPersistentID links the playlist to its iTunes row
	// once it's been pushed. Null until first sync.
	ITunesPersistentID string `json:"itunes_persistent_id,omitempty"`
	// ITunesRawCriteriaB64 stores the original iTunes Smart
	// Criteria blob for playlists imported from iTunes (migration
	// audit trail). Empty for app-native playlists.
	ITunesRawCriteriaB64 string    `json:"itunes_raw_criteria_b64,omitempty"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
	CreatedByUserID      string    `json:"created_by_user_id,omitempty"`
	// Dirty marks the playlist as pending iTunes push.
	Dirty   bool `json:"dirty"`
	Version int  `json:"version"`
}

// UserPlaylist type constants.
const (
	UserPlaylistTypeStatic = "static"
	UserPlaylistTypeSmart  = "smart"
)

// Collection is a SERVER-WIDE, shared grouping of books (the ABS
// "collection" concept). Every user sees the same collections; unlike
// UserPlaylist there is no per-user scoping, which is why creation is
// gated on auth.PermCollectionsManage rather than being self-service.
//
// Like UserPlaylist it is either static (an explicit ordered book list)
// or dynamic (a live-evaluated query). The query DSL, sort directives
// and evaluator are deliberately THE SAME ones smart playlists use --
// playlist.EvaluateSmartPlaylist is called directly -- so there is one
// query language in this system, not two that drift.
type Collection struct {
	ID          string `json:"id"` // ULID
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Type discriminator: "static" or "dynamic".
	Type string `json:"type"`
	// Static collections: ordered book ID list.
	BookIDs []string `json:"book_ids,omitempty"`
	// Dynamic collections: DSL query string, sort + limit directives.
	Query string `json:"query,omitempty"`
	// SortJSON is a JSON-encoded []{field, direction} for stable ordering.
	SortJSON string `json:"sort_json,omitempty"`
	Limit    int    `json:"limit,omitempty"`
	// MaterializedBookIDs caches the last evaluation of a dynamic
	// collection so a listing does not have to re-run the query.
	// Advisory only: readers that need live results re-evaluate.
	//
	// It is EMPTY on the stored row for a collection whose query uses a
	// per-user field (read_status, progress_pct, last_played): those members are
	// evaluated for the viewer on every read and never stored, because a stored
	// list would be one user's listening state served to everyone. A response may
	// carry the viewer's own members here; it is never written back.
	MaterializedBookIDs []string `json:"materialized_book_ids,omitempty"`
	// MaterializedTotal is how many books the last evaluation matched when it
	// matched more than MaxMaterializedMembers and MaterializedBookIDs holds only
	// the first of them (a response for a per-viewer collection carries the
	// viewer's own total here). Zero means the list is complete.
	MaterializedTotal int       `json:"materialized_total,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
	// CreatedByUserID records authorship only. It decides neither visibility
	// (collections are shared by definition) nor who a per-user query is
	// evaluated for: that is always the viewer, never the creator.
	CreatedByUserID string `json:"created_by_user_id,omitempty"`
	Version         int    `json:"version"`
}

// Collection type constants.
const (
	CollectionTypeStatic  = "static"
	CollectionTypeDynamic = "dynamic"
)

// PlaylistItem represents an item in a playlist
type PlaylistItem struct {
	ID         int `json:"id"`
	PlaylistID int `json:"playlist_id"`
	BookID     int `json:"book_id"`
	Position   int `json:"position"`
}

// ImportPath represents a managed import path monitored for new audiobooks
type ImportPath struct {
	ID        int        `json:"id"`
	Path      string     `json:"path"`
	Name      string     `json:"name"`
	Enabled   bool       `json:"enabled"`
	CreatedAt time.Time  `json:"created_at"`
	LastScan  *time.Time `json:"last_scan,omitempty"`
	BookCount int        `json:"book_count"`
}

// Operation represents an async operation
type Operation struct {
	ID           string     `json:"id"`
	UserID       string     `json:"user_id,omitempty"`
	Type         string     `json:"type"`
	Status       string     `json:"status"`
	Progress     int        `json:"progress"`
	Total        int        `json:"total"`
	Message      string     `json:"message"`
	FolderPath   *string    `json:"folder_path,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
	ErrorMessage *string    `json:"error_message,omitempty"`
	ResultData   *string    `json:"result_data,omitempty"`
}

// OperationLog represents a log entry for an operation
type OperationLog struct {
	ID          int       `json:"id"`
	OperationID string    `json:"operation_id"`
	Level       string    `json:"level"`
	Message     string    `json:"message"`
	Details     *string   `json:"details,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// OperationChange tracks a single destructive change made during an operation for undo support.
type OperationChange struct {
	ID          string `json:"id"`
	OperationID string `json:"operation_id"`
	UserID      string `json:"user_id,omitempty"`
	BookID      string `json:"book_id"`
	ChangeType  string `json:"change_type"` // "file_move", "metadata_update", "tag_write"
	FieldName   string `json:"field_name"`
	// SeriesID names the series a series-scoped row (change type
	// "series_rename") changed. Book-scoped rows leave it nil, and rows stored
	// before the field existed decode with it nil.
	SeriesID   *int       `json:"series_id,omitempty"`
	OldValue   string     `json:"old_value"`
	NewValue   string     `json:"new_value"`
	RevertedAt *time.Time `json:"reverted_at,omitempty"`
	// CreatedSeq and RevertedSeq are the journal clock's stamps for CreatedAt and
	// RevertedAt (pebble_store_journal_clock.go): strictly increasing per store
	// whatever the wall clock does, so the order of two rows is the order of their
	// sequences. CreatedAt and RevertedAt stay wall times (what the owner sees,
	// and what a file's mtime is compared with). A row stored before the fields
	// existed has 0 and is ordered by its wall time (undo.CreatedKey,
	// undo.RevertedKey). RevertedSeq is 0 while the row stands.
	CreatedSeq  int64 `json:"created_seq,omitempty"`
	RevertedSeq int64 `json:"reverted_seq,omitempty"`
	// Voided marks a row its writer took back because the write it
	// describes was refused and never made (repairs.Writer.JournalStep,
	// LockFields). RevertedAt is set with it, so no revert acts on it; the
	// revert counts, the preflight and the change log leave it out
	// altogether, since nothing happened.
	Voided    bool      `json:"voided,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	// Source names who wrote the row when the operation row alone cannot
	// say: a repairs fixer's id, stamped by repairs.Writer.Journal. The
	// journal row outlives its operation row (registry.Discard deletes a
	// failed or interrupted op's row and keeps its opchange rows), so a
	// fixer that must recognise its own earlier writes reads this, not the
	// op row. Empty on rows from writers that do not stamp it and on rows
	// stored before the field existed.
	Source string `json:"source,omitempty"`
}

// SystemActivityLog represents a log entry from a housekeeping goroutine.
type SystemActivityLog struct {
	ID        int       `json:"id"`
	UserID    string    `json:"user_id,omitempty"`
	Source    string    `json:"source"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

// OperationSummaryLog represents a completed operation persisted for history across restarts.
type OperationSummaryLog struct {
	ID          string     `json:"id"`
	Type        string     `json:"type"`
	Status      string     `json:"status"`
	Progress    float64    `json:"progress"`
	Result      *string    `json:"result,omitempty"` // JSON-encoded result
	Error       *string    `json:"error,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// OperationResult holds structured per-book output for a bulk operation.
type OperationResult struct {
	ID          int       `json:"id"`
	OperationID string    `json:"operation_id"`
	BookID      string    `json:"book_id"`
	ResultJSON  string    `json:"result_json"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
}

// UserPreference represents a user preference setting
type UserPreference struct {
	ID        int       `json:"id"`
	Key       string    `json:"key"`
	Value     *string   `json:"value,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// DoNotImport represents a blocked file hash to prevent reimport
type DoNotImport struct {
	Hash      string    `json:"hash"`
	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"created_at"`
}

// LibraryFingerprintRecord stores the last-known state of an iTunes Library.xml file.
type LibraryFingerprintRecord struct {
	Path      string    `json:"path"`
	Size      int64     `json:"size"`
	ModTime   time.Time `json:"mod_time"`
	CRC32     uint32    `json:"crc32"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Extended models (Pebble only; SQLite may ignore)

// User represents an application user (ULID IDs)
type User struct {
	ID               string    `json:"id"`
	Username         string    `json:"username"`
	Email            string    `json:"email"`
	PasswordHashAlgo string    `json:"password_hash_algo"`
	PasswordHash     string    `json:"password_hash"`
	Roles            []string  `json:"roles"`
	Status           string    `json:"status"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	Version          int       `json:"version"`
	// CredentialEpoch goes up by exactly one, in the same write as the change,
	// every time the account's credentials or status change: a password change,
	// an admin reset, a reset link, a deactivation or reactivation. Only the
	// field-scoped ops behind database.UserCredentialWriter (SetUserPassword,
	// SetUserStatus, BumpUserCredentialEpoch, all via mutateUserCredentials) move
	// it; PebbleStore.UpdateUser refuses any change to it, the password or Status
	// (ErrUserProtectedField). Every ABS session is stamped with
	// the epoch of the user record its login was verified against, and a session
	// whose stamp does not match the current epoch is refused on every refresh and
	// every bearer request (ABSSession.CredentialsCurrent). That is what closes the
	// window between verifying a password and writing a new one: a session minted
	// with the old password in that window carries the old epoch and dies when the
	// new one lands. Missing (pre-2026-10-19 rows) reads as 0, as does a missing
	// stamp on a session, so nothing is signed out on deploy. Version is not
	// reused for this: it is 1 on every row and no writer moves it.
	CredentialEpoch int64 `json:"credential_epoch,omitempty"`
	// EmailSource records how Email got onto the account (EmailSource*
	// constants), and so whether the address is verified: setup, admin,
	// invite and idp are; self and invite_link are not (see emailProven).
	// Only the store sets it; UpdateUser ignores a caller's value. "" is a row
	// written by a build that did not know the field: the store stamps it at
	// the next open (ReconcileVerifiedEmails) and treats it as unverified
	// until then.
	EmailSource string `json:"email_source,omitempty"`
	// MergedIntoUserID is the id of the account this one was folded into by
	// the admin "merge user" tool (internal/usermerge), and MergedAt when. Set
	// only by PebbleStore.RetireMergedUser, cleared only by an undo
	// (UnretireMergedUser); PebbleStore.UpdateUser refuses any change to either
	// (ErrUserProtectedField), and ReactivateUser refuses an account that has
	// one set ("undo the merge instead"). A merged account is locked, has no
	// email and holds no sign-ins; its row stays so history written under its
	// id (activity log, operation actors) still resolves to a name.
	MergedIntoUserID string     `json:"merged_into_user_id,omitempty"`
	MergedAt         *time.Time `json:"merged_at,omitempty"`
}

// User.EmailSource values.
const (
	EmailSourceSetup      = "setup"       // first-run setup, bootstrap, the system user (CreateUser)
	EmailSourceAdmin      = "admin"       // set by an admin for the user (SetUserEmailAsAdmin)
	EmailSourceInvite     = "invite"      // bound by accepting an invitation emailed to it by an admin
	EmailSourceIdP        = "idp"         // verified by an identity provider at login
	EmailSourceSelf       = "self"        // typed in through self-service (UpdateUser); unverified
	EmailSourceInviteLink = "invite_link" // bound by an older build from a copied invite link; unverified
)

// BookVersion represents one version of a book's content (spec 3.1
// library centralization). Every book has at least one version with
// status = active; alt versions (different format, quality, source)
// live under `.versions/{version_id}/` on the filesystem. File-level
// data (paths, hashes, sizes) lives on BookFile rows scoped by
// version_id; this struct carries only version-level metadata.
type BookVersion struct {
	ID                 string     `json:"id"`
	BookID             string     `json:"book_id"`
	Status             string     `json:"status"` // see BookVersionStatus* constants below
	Format             string     `json:"format"` // m4b | mp3 | flac | ...
	Source             string     `json:"source"` // deluge | manual | transcoded | imported
	SourceOriginalPath string     `json:"source_original_path,omitempty"`
	TorrentHash        string     `json:"torrent_hash,omitempty"` // infohash, fast-path fingerprint match
	IngestDate         time.Time  `json:"ingest_date"`
	PurgedDate         *time.Time `json:"purged_date,omitempty"`
	// TrashedAt is when the version last entered the trash; nil whenever
	// Status is not BookVersionStatusTrash. The 14-day trash window is
	// counted from here (versions.CleanupTrashedVersions), never from
	// CreatedAt. Rows trashed before this field existed decode as nil; the
	// cleanup stamps them on first sight instead of purging them. Maintained
	// by PebbleStore.UpdateBookVersion -- see the comment there.
	TrashedAt *time.Time `json:"trashed_at,omitempty"`
	// RetiredFromBookID, RetireOpID and RetiredAt are set only on a version of
	// Status BookVersionStatusRetired: the book whose files this version holds
	// (its former owner, now a retired shell), the operation that retired them,
	// and when. CreateBookVersion indexes RetiredFromBookID
	// (idx:bv:from:<book id>) so GetRetiredVersionsFromBook answers "what was
	// retired out of this book" without scanning every version.
	RetiredFromBookID string     `json:"retired_from_book_id,omitempty"`
	RetireOpID        string     `json:"retire_op_id,omitempty"`
	RetiredAt         *time.Time `json:"retired_at,omitempty"`
	// PurgeKept lists the files a finished purge did NOT remove, with why: a
	// file another book_file row (a retired version's included) or another
	// book names stays where it is. Set by versions.PurgeVersion on the write
	// that marks the version inactive_purged, so a purge that kept files is
	// not reported as one that removed them all. Empty when it removed
	// everything.
	PurgeKept []PurgeFileNote `json:"purge_kept,omitempty"`
	// PurgeFailed, PurgeFailedAt: the files a purge could not remove for a
	// reason that retrying will not change (permission, read-only filesystem),
	// the reason for each, and when the version entered
	// BookVersionStatusPurgeFailed. Empty in every other status.
	PurgeFailed   []PurgeFileNote `json:"purge_failed,omitempty"`
	PurgeFailedAt *time.Time      `json:"purge_failed_at,omitempty"`
	MetadataJSON  string          `json:"metadata_json,omitempty"` // source-specific catch-all
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
	Version       int             `json:"version"`
}

// PurgeFileNote is one file a purge left on disk (BookVersion.PurgeKept) or
// could not remove (BookVersion.PurgeFailed), and why.
type PurgeFileNote struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// BookVersionStatus values. See spec 3.1 for the full state
// machine — the transitions users trigger (trash/restore, swap
// primary, hard-delete from Purged view) are enforced in the
// server layer, not in the store.
const (
	BookVersionStatusPending              = "pending"
	BookVersionStatusActive               = "active"
	BookVersionStatusAlt                  = "alt"
	BookVersionStatusSwappingIn           = "swapping_in"
	BookVersionStatusSwappingOut          = "swapping_out"
	BookVersionStatusTrash                = "trash"
	BookVersionStatusInactivePurged       = "inactive_purged"
	BookVersionStatusBlockedForRedownload = "blocked_for_redownload"
	// BookVersionStatusRetired marks a version that holds the retired book_file
	// rows of a book that was retired into the version's own book (see
	// RetireBookFiles). It is NOT a state a user moves a version to, and it is
	// excluded from every lifecycle that acts on versions: it is never
	// auto-promoted, never trashed, never purged (purging deletes the files),
	// and it leaves this status only through UnretireBookFiles.
	BookVersionStatusRetired = "retired"
	// BookVersionStatusPurging marks a trashed version whose purge has been
	// claimed: PurgeVersion sets it, under the version's stripe, before the
	// first file is removed, and it ends in BookVersionStatusInactivePurged. A
	// version that stays in this status is a purge that failed part way (some
	// files removed, one could not be): it is retried by the trash cleanup and
	// by "purge now", and it can NOT be restored, because some of its files are
	// already gone. UpdateBookVersion enforces both halves (see
	// refusePurgeTransition). It is not a state a user moves a version to.
	BookVersionStatusPurging = "purging"
	// BookVersionStatusPurgeFailed marks a version whose purge failed for a
	// reason retrying cannot change (a file it may not remove: permission, a
	// read-only filesystem). It is the end of the automatic retries: the trash
	// cleanup counts it as needing attention instead of failing on it every
	// night. Like purging it cannot be restored (some files are already gone).
	// The owner can retry the purge ("Purge now": the claim accepts it), or
	// hard-delete the version, which removes the record and leaves the files
	// named in BookVersion.PurgeFailed where they are. Only PurgeVersion sets
	// it, from purging.
	BookVersionStatusPurgeFailed = "purge_failed"
)

// ErrVersionPurging is returned by UpdateBookVersion when the stored version
// is purging (or purge_failed) and the write would move it anywhere but the
// purge states or inactive_purged: a restore, a re-trash, a swap, or a stale
// writer putting back a copy it read before the claim.
var ErrVersionPurging = errors.New("version is being purged and cannot change status")

// ErrVersionPurgeClaimLost is returned by UpdateBookVersion when a write sets
// BookVersionStatusPurging and the stored version is none of trash, purging
// and purge_failed (or a write sets purge_failed and the stored version is not
// purging): it was restored (or otherwise left the trash) after the caller read
// it, so its files must not be removed.
var ErrVersionPurgeClaimLost = errors.New("version is no longer in the trash, so its purge was not claimed")

// Role is a named bundle of permissions for the multi-user model
// (spec 3.7). Permissions are Go string constants validated at
// route-registration time, carried inline on the Role to avoid a
// separate role_permissions join table. ID is typically the
// lowercase role name (e.g. "admin", "editor", "viewer") so seeded
// roles have stable, well-known IDs; custom roles get a ULID.
type Role struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Permissions []string  `json:"permissions"`
	IsSeed      bool      `json:"is_seed,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	Version     int       `json:"version"`
}

// APIKey is a scoped bearer token for a user. Only the SHA-256 hash of the
// raw token is stored; the raw token is shown to the user once at creation.
// Status follows AWS IAM semantics: "active" | "inactive" | "revoked".
type APIKey struct {
	ID            string     `json:"id"`
	UserID        string     `json:"user_id"`
	Name          string     `json:"name"`
	Description   string     `json:"description"`
	TokenHash     string     `json:"token_hash"`
	Scopes        []string   `json:"scopes"`
	Status        string     `json:"status"`
	CreatedAt     time.Time  `json:"created_at"`
	LastUsedAt    *time.Time `json:"last_used_at,omitempty"`
	LastUsedIP    string     `json:"last_used_ip,omitempty"`
	UseCount      int64      `json:"use_count"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	DeactivatedAt *time.Time `json:"deactivated_at,omitempty"`
	RevokedAt     *time.Time `json:"revoked_at,omitempty"`
}

// Invite is a single-use admin-generated token for creating a new
// user account (spec 3.7). Token is the PK since lookup is always
// by the token-in-URL the invitee opens. ConsumeInvite is atomic:
// invite row deleted + user created + role membership written in
// one Pebble batch.
type Invite struct {
	Token    string `json:"token"`
	Username string `json:"username"`
	// Email is the invitee's address, stored lowercase. Optional. On accept it
	// becomes the new user's email and is granted SSO sign-in
	// (InviteEmailGrantStore).
	Email           string     `json:"email,omitempty"`
	RoleID          string     `json:"role_id"`
	CreatedByUserID string     `json:"created_by_user_id"`
	CreatedAt       time.Time  `json:"created_at"`
	ExpiresAt       time.Time  `json:"expires_at"`
	UsedAt          *time.Time `json:"used_at,omitempty"`
	// EmailSentAt is when the invitation email last went out; nil = never.
	// EmailSentTo is the address it went to. Accept binds Email to the new
	// account only when the invitation was delivered to exactly that address:
	// then holding the token proves control of the inbox. A link the admin
	// copied by hand proves nothing about the address.
	EmailSentAt *time.Time `json:"email_sent_at,omitempty"`
	EmailSentTo string     `json:"email_sent_to,omitempty"`
	// AcceptedUserID is the user the accept created.
	AcceptedUserID string `json:"accepted_user_id,omitempty"`
	// DisplacedUserIDs are the accounts that had typed the bound address into
	// their profile and lost it to this accept (they keep their accounts).
	DisplacedUserIDs []string `json:"displaced_user_ids,omitempty"`
	// EmailBinding and SSOGrant record what accept did with Email, for the
	// admin: EmailBinding is InviteEmailBound, InviteEmailNotEmailed or
	// InviteEmailCreatorNotAdmin;
	// SSOGrant is InviteSSOGranted or InviteSSOCreatorNotAdmin. Empty when the
	// invite had no email.
	EmailBinding string `json:"email_binding,omitempty"`
	SSOGrant     string `json:"sso_grant,omitempty"`
	// GuestPolicy* record the Cloudflare Access guest-policy sync run on
	// accept, so the admin can see it: status is one of the
	// InviteGuestPolicy* constants; detail is a short reason on failure.
	GuestPolicyStatus string     `json:"guest_policy_status,omitempty"`
	GuestPolicyDetail string     `json:"guest_policy_detail,omitempty"`
	GuestPolicyAt     *time.Time `json:"guest_policy_at,omitempty"`
}

// Session represents an authenticated session token
type Session struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	IP        string    `json:"ip"`
	UserAgent string    `json:"user_agent"`
	Revoked   bool      `json:"revoked"`
	Version   int       `json:"version"`
	// Origin is how the session was issued (SessionOrigin*). "" on a
	// session issued without one (every session before 2026-10-06, a
	// temp-login link, an invite): such a session is NOT proof of the
	// user's own interactive login (auth.MethodSessionDelegated).
	Origin string `json:"origin,omitempty"`
}

// Session origins (Session.Origin).
const (
	// SessionOriginPassword: the user signed in with username and password.
	SessionOriginPassword = "password"
	// SessionOriginOAuth: the user signed in through an OAuth/OIDC provider.
	SessionOriginOAuth = "oauth"
)

// InteractiveLogin reports whether the session came from the user's own
// sign-in (password or OAuth), rather than a link someone else minted
// (temp-login, invite) or an unknown origin.
func (s *Session) InteractiveLogin() bool {
	return s != nil && (s.Origin == SessionOriginPassword || s.Origin == SessionOriginOAuth)
}

// UserPreferenceKV represents per-user preference (key/value)
type UserPreferenceKV struct {
	UserID    string    `json:"user_id"`
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	UpdatedAt time.Time `json:"updated_at"`
	Version   int       `json:"version"`
}

// BookSegment represents a physical file segment of a book
type BookSegment struct {
	ID           string    `json:"id"`
	BookID       int       `json:"book_id"` // link to numeric book ID (legacy)
	FilePath     string    `json:"file_path"`
	Format       string    `json:"format"`
	SizeBytes    int64     `json:"size_bytes"`
	DurationSec  int       `json:"duration_seconds"`
	TrackNumber  *int      `json:"track_number,omitempty"`
	TotalTracks  *int      `json:"total_tracks,omitempty"`
	SegmentTitle *string   `json:"segment_title,omitempty"`
	FileHash     *string   `json:"file_hash,omitempty"`
	Active       bool      `json:"active"`
	SupersededBy *string   `json:"superseded_by,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	Version      int       `json:"version"`
}

// BookFile represents an individual audio file within a book.
// Replaces BookSegment with ULID string book_id, iTunes integration fields,
// and comprehensive audio metadata.
type BookFile struct {
	ID     string `json:"id"`
	BookID string `json:"book_id"`
	// VersionID ties this file to a specific book_versions row when
	// library centralization (spec 3.1) is rolled out. NULL until
	// migration runs; after migration, every file belongs to exactly
	// one version. Cascaded delete with book_versions.
	VersionID string `json:"version_id,omitempty"`
	// Retired marks a row that was once a live file of its book and now
	// survives only as a retired book version held by another book (see
	// BookVersionStatusRetired and RetireBookFiles). A retired row is NOT a file
	// of the book it is keyed under: the list reads (GetBookFiles, the Core and
	// visitor variants, the counts and duplicate-hash stats) and the single-
	// identity lookups (GetBookFileByPath, ByPID, ByAcoustID, ByAcoustIDFuzzy,
	// GetBookBySegmentFileHash) skip it, so the holder's track list, runtime,
	// size and ABS export never see it. GetBookFileByID, BookFilesAtPath,
	// BookFilesWithHash, LookupAcoustIDCandidates and LSHProbe still return it
	// (see the list in iface_retired.go). The retired-aware readers
	// (GetBookFilesIncludingRetired, GetRetiredBookFiles,
	// GetAllBookFilesCoreIncludingRetired, GetBookFileByPIDIncludingRetired,
	// GetBookFileByPathIncludingRetired) always return it.
	//
	// omitzero, never omitempty, for the same v1/v2 reason as Scan: a live row
	// serializes byte-identically to a row written before the field existed.
	// Only RetireBookFiles sets it and only UnretireBookFiles clears it; every
	// other write path preserves it (bookFileFieldClasses, bfStoredOwned).
	Retired            bool   `json:"retired,omitzero"`
	FilePath           string `json:"file_path"`
	OriginalFilename   string `json:"original_filename,omitempty"`
	ITunesPath         string `json:"itunes_path,omitempty"`
	ITunesPersistentID string `json:"itunes_persistent_id,omitempty"`
	TrackNumber        int    `json:"track_number,omitempty"`
	TrackCount         int    `json:"track_count,omitempty"`
	DiscNumber         int    `json:"disc_number,omitempty"`
	DiscCount          int    `json:"disc_count,omitempty"`
	Title              string `json:"title,omitempty"`
	// RawTags is the LOSSLESS capture of every tag this file carried at import
	// (all ID3 frames + MP4 atoms + custom keys), normalized to strings, minus
	// binary artwork. Populated on scan/import so full provenance is always
	// recoverable — no future bug can lose tag data we never recorded. Additive
	// JSON field; nil on rows imported before lossless capture (backfill later).
	RawTags          map[string]string `json:"raw_tags,omitempty"`
	Format           string            `json:"format,omitempty"`
	Codec            string            `json:"codec,omitempty"`
	Duration         int               `json:"duration,omitempty"`
	FileSize         int64             `json:"file_size,omitempty"`
	BitrateKbps      int               `json:"bitrate_kbps,omitempty"`
	SampleRateHz     int               `json:"sample_rate_hz,omitempty"`
	Channels         int               `json:"channels,omitempty"`
	BitDepth         int               `json:"bit_depth,omitempty"`
	FileHash         string            `json:"file_hash,omitempty"`
	OriginalFileHash string            `json:"original_file_hash,omitempty"`
	// OriginalFileHashKind names the digest OriginalFileHash holds.
	// FileHashKindSampled (filehash.BookFileHash, the same digest as FileHash)
	// is the only kind the merge freezes and the only kind the integrity check
	// compares against FileHash. Empty means a legacy value of unknown kind.
	OriginalFileHashKind string `json:"original_file_hash_kind,omitempty"`
	// Scan is the staged library scan's view of this file's completeness.
	// omitzero, never omitempty: v1 and v2 disagree about what omitempty means
	// for bools, ints and empty structs, so omitempty would change this row's
	// shape when the store moves to encoding/json/v2. See
	// internal/database/scan_state.go for the measured table.
	Scan ScanState `json:"scan,omitzero"`
	// --- per-file scan cache (per docs/plans/2026-08-24-per-file-scan-cache-design.md) ---
	//
	// The scan cache was keyed per BOOK while the skip decision is made per FILE,
	// during the walk, before any book is known. For a single-file book the two
	// grains coincide; for a multi-file book they diverge the moment
	// createBookFilesForBookWithAudio normalizes Book.FilePath to the containing directory.
	// The result is arithmetic, not an anomaly: an 80-file audiobook has ONE book
	// row, so a per-file write-back that looks the book up BY PATH can stamp at
	// most 1 file in 80 and the other 79 are re-read and re-hashed on every scan,
	// forever. Measured on prod 2026-08-24: "436 of 500 scan-cache write-backs
	// skipped because no book row exists at the path".
	//
	// POINTERS are the load-bearing choice, and they are what actually separates
	// "never scanned" (nil) from "scanned and measured zero". A real 0-byte file
	// exists and must not read as unscanned. The compiler enforces this: changing
	// any of these three to a value type fails to build at 7 sites (measured), so
	// the distinction cannot be erased by accident and needs no test to defend it.
	//
	// omitzero rather than omitempty is deliberate but is NOT what prevents that
	// corruption, and an earlier draft of this comment wrongly claimed it was.
	// The ScanState hazard documented above is real for VALUE types -- v2's
	// omitempty means "encodes to an empty JSON value", so a bare int64/bool
	// would start emitting 0/false. It does not apply to a pointer: nil encodes
	// to null, which v2 does omit. Measured both ways, under GOEXPERIMENT=jsonv2
	// and without it, omitempty here is presently equivalent. omitzero is kept
	// because it means one thing in both encoders and stays correct if one of
	// these fields is ever changed to a value type -- defence in depth, not the
	// defence.
	LastScanMtime *int64 `json:"last_scan_mtime,omitzero"`
	LastScanSize  *int64 `json:"last_scan_size,omitzero"`
	NeedsRescan   *bool  `json:"needs_rescan,omitzero"`
	// PostMetadataHash is the SHA-256 of the file immediately after a metadata
	// tag write. It differs from OriginalFileHash because tag writes add/change
	// bytes in the header. Store it so pre-write identity is always recoverable.
	PostMetadataHash string `json:"post_metadata_hash,omitempty"`
	// AcoustIDFingerprint is the whole-file chromaprint, raw uint32 stream
	// stored as little-endian bytes. 4 bytes per frame at 8 frames/sec.
	// Populated by FileWholeFingerprint. Preferred over the segment fields
	// for similarity matching.
	AcoustIDFingerprint []byte `json:"acoustid_fingerprint,omitempty"`
	// AcoustIDFingerprintDurationSec is the duration fpcalc actually
	// measured while decoding (may differ from container metadata when the
	// container is lying about duration).
	AcoustIDFingerprintDurationSec float64 `json:"acoustid_fingerprint_duration_sec,omitempty"`
	// AcoustIDFPVersion is the fingerprint.PrintEncodingVersion this row's
	// AcoustIDFingerprint (and the Seg0 derived from it) was written under.
	// 0 = legacy: written before 2026-09-19, when fpcalc's compressed output
	// was misread as frames, so the bytes are not frames. Fuzzy comparisons
	// must treat a legacy row as MISSING (HasCurrentPrint); exact string
	// matches on the stored segment stay valid.
	AcoustIDFPVersion int `json:"acoustid_fp_version,omitempty"`
	// 7-segment fields. Deprecated — kept for back-compat reads during the
	// whole-file migration. New writes only populate Seg0 as a transition
	// fallback; Seg1..6 are no longer written.
	AcoustIDSeg0 string `json:"acoustid_seg0,omitempty"`
	AcoustIDSeg1 string `json:"acoustid_seg1,omitempty"`
	AcoustIDSeg2 string `json:"acoustid_seg2,omitempty"`
	AcoustIDSeg3 string `json:"acoustid_seg3,omitempty"`
	AcoustIDSeg4 string `json:"acoustid_seg4,omitempty"`
	AcoustIDSeg5 string `json:"acoustid_seg5,omitempty"`
	AcoustIDSeg6 string `json:"acoustid_seg6,omitempty"`
	// FingerprintFailedAt records when the last ffmpeg/fpcalc attempt
	// failed (corrupt file, unsupported codec, etc.). Set by the
	// fingerprint backfill so we don't loop forever retrying the same
	// unreadable files. Force-rescan clears it. nil = never attempted
	// or last attempt succeeded.
	FingerprintFailedAt       *time.Time `json:"fingerprint_failed_at,omitempty"`
	FingerprintFailureReason  *string    `json:"fingerprint_failure_reason,omitempty"`  // e.g. "corrupt_audio"
	FingerprintFailureDetail  *string    `json:"fingerprint_failure_detail,omitempty"`  // short human-readable explanation
	FingerprintDiagnosticJSON *string    `json:"fingerprint_diagnostic_json,omitempty"` // full FileDiagnostic JSON blob
	// AcoustIDOnlineRecordingID is the top-scoring MusicBrainz recording ID
	// returned by acoustid.org's web /v2/lookup for this file's whole-file
	// chromaprint. Populated by the acoustid.lookup-online op. Empty when
	// the lookup hasn't been run, the API has no match, or the top score
	// is below AcoustIDOnlineMinScore (0.85).
	AcoustIDOnlineRecordingID string `json:"acoustid_online_recording_id,omitempty"`
	// AcoustIDOnlineScore is the top match's score (0..1). 0 when no match.
	AcoustIDOnlineScore float64 `json:"acoustid_online_score,omitempty"`
	// AcoustIDOnlineLookedUpAt records when the last /v2/lookup completed.
	// Used to avoid re-querying — the API has rate limits and the answer
	// is stable for a stable fingerprint.
	AcoustIDOnlineLookedUpAt *time.Time `json:"acoustid_online_looked_up_at,omitempty"`
	OrganizeMethod           string     `json:"organize_method,omitempty"` // "reflink", "hardlink", "copy", "symlink"
	Missing                  bool       `json:"missing"`
	SkipScan                 bool       `json:"skip_scan"` // user-set: exclude file from scans/fingerprinting
	CreatedAt                time.Time  `json:"created_at"`
	UpdatedAt                time.Time  `json:"updated_at"`
	// Deluge integration fields (spec: deluge-protected-paths-design).
	// DelugeHash is the torrent info-hash (40-char hex string).
	// DelugeOriginalPath is the file path before copy-into-library.
	// ImportedFromDelugeAt is when the copy completed.
	DelugeHash string `json:"deluge_hash,omitempty"`
	// DownloadHash is the hash of the originally-downloaded file. It is
	// distinct from DelugeHash (the torrent info-hash): DownloadHash is
	// auto-populated from DelugeHash for files imported via Deluge, but may
	// also be manually set/corrected for files that did not arrive via
	// Deluge.
	DownloadHash         string     `json:"download_hash,omitempty"`
	DelugeOriginalPath   string     `json:"deluge_original_path,omitempty"`
	ImportedFromDelugeAt *time.Time `json:"imported_from_deluge_at,omitempty"`

	// ---- Per-file intro transcription -------------------------------------
	//
	// These mirror the Book-level fields of the same names (see Book, above) but
	// are recorded PER FILE. The Book-level copies are retained as a derived
	// convenience (file 1's values) for existing consumers such as
	// maintenance.auto-match-transcribed; they are NOT deprecated by these.
	//
	// Why per-file at all: an audiobook opens with a spoken
	// "<Title> by <Author>, read by <Narrator>" announcement, which marks a book
	// START. Storing that on Book alone captures exactly ONE file's opening, so a
	// folder holding 12 files that are one book is indistinguishable from 12 files
	// that are 12 separate books. Per file, the sequence is decisive — real prod
	// rows read "This is a reading of Overlord, Book 7. This part includes the
	// prologue and Chapter 1" / "...includes Chapter 2" / "...Volume 7, Chapter 3",
	// which proves continuation. At book level that signal is invisible.

	// IntroTranscription is the raw Whisper transcript of the first ~90 seconds of
	// THIS file. It is the one field in this group that stripBookFileForMemdb
	// clears — see the memory math in memdb_strip.go. Readers that need it must go
	// Pebble-direct (GetBookFile / GetBookFiles), never the memdb projection.
	IntroTranscription *string `json:"intro_transcription,omitempty"`
	// TranscribedTitle / TranscribedAuthor / TranscribedNarrator are the fields
	// parsed from IntroTranscription ("TITLE by AUTHOR. Read by NARRATOR.").
	// Stored separately from the file's own tags so transcription errors cannot
	// overwrite curated metadata. Retained in memdb: these are what a query
	// filters on, and they are small.
	TranscribedTitle    *string `json:"transcribed_title,omitempty"`
	TranscribedAuthor   *string `json:"transcribed_author,omitempty"`
	TranscribedNarrator *string `json:"transcribed_narrator,omitempty"`
	// TranscribedTranslator is credited between author and narrator in
	// translated works; before it existed the author absorbed it.
	TranscribedTranslator *string `json:"transcribed_translator,omitempty"`
	// TranscribedCoverArtist is the album/cover art credit ("Cover art by X").
	TranscribedCoverArtist *string `json:"transcribed_cover_artist,omitempty"`
	// IntroTranscribedAt is when IntroTranscription was last populated.
	IntroTranscribedAt *time.Time `json:"intro_transcribed_at,omitempty"`
	// TranscribeStatus records the outcome of the most recent transcription
	// ATTEMPT for this file. One of: "ok", "source_file_missing", "no_audio",
	// "ffmpeg_error", "whisper_error", "path_unmapped", "empty". This is the
	// queryable drill-down field — count files by status to see exactly why transcription is or isn't
	// producing data.
	TranscribeStatus *string `json:"transcribe_status,omitempty"`
	// TranscribeError holds a short human-readable detail for a non-ok status —
	// typically the tail of ffmpeg stderr or the Whisper error string. Empty on ok.
	TranscribeError *string `json:"transcribe_error,omitempty"`
	// TranscribeAttemptedAt is when the most recent transcription attempt ran
	// (success OR failure), distinct from IntroTranscribedAt which only advances
	// on a successful transcription.
	TranscribeAttemptedAt *time.Time `json:"transcribe_attempted_at,omitempty"`

	// ---- Per-file intro provenance (maintenance.transcribe-file-intros) ----
	//
	// These extend the group above; they do not duplicate it. They record WHAT
	// produced a per-file result and are what make the op resumable and
	// reversible. Which op owns which field:
	//
	//   transcribe-file-intros  writes every field of the group (the transcript,
	//                           the parsed credits, status/error/attempted-at,
	//                           and the four below) through
	//                           BookFileIntroStore.SetBookFileIntro.
	//   intro-migrate-single-file writes the transcript, the parsed credits and
	//                           IntroTranscribedAt/TranscribeStatus only; it
	//                           copies a book-level result and leaves the four
	//                           below empty, which is how a migrated row is told
	//                           from a transcribed one.
	//   rescans / upserts       write none of them; the merge keeps the stored
	//                           values (bookFileFieldClasses).

	// IntroSourceHash is the FileHash of the bytes the stored transcript was made
	// from, and the op's DONE MARKER: a file is done exactly when
	// IntroSourceHash == FileHash and FileHash is non-empty. A file whose bytes
	// changed (a new FileHash) reads as not done and is re-transcribed. It is on
	// BookFileCore (memdb-retained) because work selection reads Core rows.
	IntroSourceHash string `json:"intro_source_hash,omitempty"`
	// IntroRunOpID is the id of the operation that wrote this file's result. It
	// is what clear-by-run-id selects on, so it is memdb-retained like
	// IntroSourceHash. It survives rescans (the merge keeps it), which is the
	// reason it lives on the row rather than in an op-side list.
	IntroRunOpID string `json:"intro_run_op_id,omitempty"`
	// IntroTimeouts counts how many times the Whisper worker's decoder ran past
	// its per-clip deadline on THIS file's audio (transcribe_timeout), and
	// IntroTimeoutHash is the FileHash those strikes were counted against: a
	// replaced file (new hash) starts again at zero. At the strike limit the
	// file's TranscribeStatus becomes transcribe_timeout, which both transcribe
	// ops skip. Written only by BookFileIntroStore.RecordIntroTimeout; cleared
	// with the rest of the result by ClearBookFileIntro.
	IntroTimeouts    int    `json:"intro_timeouts,omitempty"`
	IntroTimeoutHash string `json:"intro_timeout_hash,omitempty"`
	// IntroTimeoutRunID is the run that counted the latest strike. A strike is
	// one per (file, run): a run that sees the same file time out again (a
	// retry round, or a worker that stalls on everything) is not a second
	// piece of evidence about the audio, so a repeat from the same run is
	// refused rather than counted.
	IntroTimeoutRunID string `json:"intro_timeout_run_id,omitempty"`
	// IntroTimeoutAt is when the latest strike was counted, or when a strike that
	// was not counted recorded the file's first known machine
	// (IntroTimeoutOutcome.IdentityRecorded). A run id cannot say that two
	// different ops saw ONE stall (each op has its own id), so a strike is also
	// refused while that moment is closer than the caller's MinGap.
	// Travels with the strikes; zero (never recorded) means "long ago".
	IntroTimeoutAt *time.Time `json:"intro_timeout_at,omitempty"`
	// IntroTimeoutWorker names the machines whose answers were the counted
	// strikes, as opaque worker identities ("m:" + a hash the worker reports, or
	// "u:" + a hash of its URL for an old worker) joined with " + ". Never an
	// endpoint address: this field is returned in API responses, and endpoint
	// URLs are internal. A further strike counts only from a machine not named
	// here, so one sick machine -- a Mac sharing its box with a CI job on two
	// different nights, or two server ports on that one box -- cannot make a
	// file permanent by itself. Empty on a strike no machine was named for.
	IntroTimeoutWorker string `json:"intro_timeout_worker,omitempty"`
	// IntroMeta is how the clip was made and which model heard it. Not queried by
	// any filter, so stripBookFileForMemdb clears it like IntroTranscription.
	IntroMeta *IntroMeta `json:"intro_meta,omitempty"`
}

// IntroMeta records the settings behind one per-file intro transcript. Every
// field is the value the Whisper endpoint or the op reported at the time, kept
// so a later run with a different model or clip length can be told apart.
type IntroMeta struct {
	// Model is the model the answering endpoint reported (empty when it did not).
	Model string `json:"model,omitempty"`
	// Language is the language Whisper detected, when the endpoint reports one.
	Language string `json:"language,omitempty"`
	// ClipStartSec and ClipLengthSec describe the audio the transcript covers:
	// the clip begins ClipStartSec seconds into the file and runs ClipLengthSec
	// seconds (90 for every file this op transcribes).
	ClipStartSec  int `json:"clip_start_sec"`
	ClipLengthSec int `json:"clip_length_sec,omitempty"`
}

// UserPosition is one user's resume point for one segment of one
// book (spec 3.6). Keyed per (user, book, segment) — a multi-file
// audiobook has one row per chapter/segment actively listened to.
// The canonical "where am I in the book" is derived from the most
// recently-updated UserPosition for the book.
type UserPosition struct {
	UserID          string    `json:"user_id"`
	BookID          string    `json:"book_id"`
	SegmentID       string    `json:"segment_id"`
	PositionSeconds float64   `json:"position_seconds"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// UserBookState is the per-(user, book) derived state used by UI and
// filters (spec 3.6): read status, last activity timestamp, resume
// pointer, cached listened-seconds and progress percent. Recomputed
// when positions change; can be manually overridden (status_manual).
type UserBookState struct {
	UserID         string    `json:"user_id"`
	BookID         string    `json:"book_id"`
	Status         string    `json:"status"` // see UserBookStatus* constants
	StatusManual   bool      `json:"status_manual"`
	LastActivityAt time.Time `json:"last_activity_at"`
	// FinishedAt is when the book last BECAME Finished. SetUserBookState
	// stamps it on the transition into Finished and carries it forward
	// while the status stays Finished, so writers neither maintain it nor
	// clobber it with a fresh literal; it is nil whenever the status is not
	// Finished. Unlike LastActivityAt, a later position write does not move
	// it, which is what lets the iTunes push count one finish once. Finished
	// rows written before 2026-09-13 have no stamp until they finish again.
	FinishedAt           *time.Time `json:"finished_at,omitempty"`
	LastSegmentID        string     `json:"last_segment_id,omitempty"`
	TotalListenedSeconds float64    `json:"total_listened_seconds,omitempty"`
	ProgressPct          int        `json:"progress_pct"` // 0-100
	// HideFromContinueListening backs the Audiobookshelf surface's
	// `mediaProgress[].hideFromContinueListening` flag — "remove this from Continue
	// Listening" without discarding the position. Additive: rows written before this
	// field existed unmarshal to false, which is the correct default (nothing was
	// ever hidden).
	//
	// It is a USER INTENT, not derived state. Anything that rewrites a
	// UserBookState must read-modify-write rather than construct a fresh literal, or
	// the next playback sync silently un-hides what the user hid.
	HideFromContinueListening bool `json:"hide_from_continue_listening,omitempty"`
	// ProgressResetAt is the tombstone of the user's last "reset progress"
	// (ABS DELETE /api/me/progress/:id). An offline session whose position
	// last moved (updatedAt) before it is a listen the user has since
	// discarded, so offline replay (/api/session/local[-all]) discards it
	// instead of resurrecting the old position. Additive: nil on rows written
	// before it existed.
	ProgressResetAt *time.Time `json:"progress_reset_at,omitempty"`
	// ProgressResetPositions are the positions (seconds) the user discarded
	// with a reset, oldest first, at most MaxProgressResetPositions. While any
	// exist, offline replay discards a session that lands back on one of them
	// whatever its timestamps say, because clients re-stamp a replayed
	// backlog's updatedAt (session_local_all.go). Additive: nil on rows
	// written before it existed.
	ProgressResetPositions []float64 `json:"progress_reset_positions,omitempty"`
	UpdatedAt              time.Time `json:"updated_at"`
}

// MaxProgressResetPositions bounds UserBookState.ProgressResetPositions: the
// newest resets are kept, the oldest dropped.
const MaxProgressResetPositions = 8

// UserBookStatus values. Auto-computed from UserPosition rows
// unless StatusManual=true, in which case the server leaves the
// stored value alone. See spec 3.6 §6-7 for the state machine.
const (
	UserBookStatusUnstarted  = "unstarted"
	UserBookStatusInProgress = "in_progress"
	UserBookStatusFinished   = "finished"
	UserBookStatusAbandoned  = "abandoned"
)

// FinishedThreshold is the fraction of total duration that triggers
// an auto-flip to UserBookStatusFinished. Spec 3.6 §6.
const FinishedThreshold = 0.95

// PlaybackEvent immutable event
type PlaybackEvent struct {
	UserID      string    `json:"user_id"`
	BookID      int       `json:"book_id"`
	SegmentID   string    `json:"segment_id"`
	PositionSec int       `json:"position_seconds"`
	EventType   string    `json:"event_type"` // progress|start|pause|complete
	PlaySpeed   float64   `json:"play_speed"`
	CreatedAt   time.Time `json:"created_at"`
	Version     int       `json:"version"`
}

// PlaybackProgress latest snapshot
type PlaybackProgress struct {
	UserID      string    `json:"user_id"`
	BookID      int       `json:"book_id"`
	SegmentID   string    `json:"segment_id"`
	PositionSec int       `json:"position_seconds"`
	Percent     float64   `json:"percent_complete"`
	UpdatedAt   time.Time `json:"updated_at"`
	Version     int       `json:"version"`
}

// BookStats aggregated counters
type BookStats struct {
	BookID        int `json:"book_id"`
	PlayCount     int `json:"play_count"`
	ListenSeconds int `json:"listen_seconds"`
	Version       int `json:"version"`
}

// UserStats aggregated counters
type UserStats struct {
	UserID        string `json:"user_id"`
	ListenSeconds int    `json:"listen_seconds"`
	Version       int    `json:"version"`
}

// MetadataFieldState persists per-field metadata provenance (fetched vs override)
// using JSON-encoded values to preserve original types.
type MetadataFieldState struct {
	BookID         string  `json:"book_id"`
	Field          string  `json:"field"`
	FetchedValue   *string `json:"fetched_value,omitempty"`  // JSON-encoded value
	OverrideValue  *string `json:"override_value,omitempty"` // JSON-encoded value
	OverrideLocked bool    `json:"override_locked"`
	// LockSource says who set OverrideLocked when it was not a person: a
	// Repairs apply writes RepairLockSource(opID). Empty means a person's lock
	// (or no lock). Every path where a person sets, edits, locks or unlocks a
	// field clears it, so a lock a person touched is theirs. See
	// metadata_field_lock_source.go.
	LockSource string    `json:"lock_source,omitempty"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// MetadataChangeRecord tracks a single change to a metadata field for undo/audit.
type MetadataChangeRecord struct {
	ID            int       `json:"id"`
	BookID        string    `json:"book_id"`
	Field         string    `json:"field"`
	PreviousValue *string   `json:"previous_value,omitempty"` // JSON-encoded
	NewValue      *string   `json:"new_value,omitempty"`      // JSON-encoded
	ChangeType    string    `json:"change_type"`              // "fetched", "override", "clear", "undo", "bulk_update"
	Source        string    `json:"source,omitempty"`         // e.g. "Open Library", "manual", "AI parsing"
	ChangedAt     time.Time `json:"changed_at"`
	// BatchID is shared by every row one metadata apply wrote (and by the
	// "undo" rows that reverted them), so undo-last-apply reverts exactly one
	// apply. Rows recorded before 2026-09-13 have none.
	BatchID string `json:"batch_id,omitempty"`
	// PreviousRef / NewRef carry the row ids behind a foreign-key field
	// (author_name, series), whose PreviousValue / NewValue hold display names.
	PreviousRef *MetadataChangeRef `json:"previous_ref,omitempty"`
	NewRef      *MetadataChangeRef `json:"new_ref,omitempty"`
}

// MetadataChangeRef is the id side of an author_name or series history row.
type MetadataChangeRef struct {
	AuthorID *int `json:"author_id,omitempty"`
	SeriesID *int `json:"series_id,omitempty"`
	// BookAuthors is the book_authors join; BookAuthorsKnown says it was read
	// (an empty join and an unread one are different things to an undo).
	BookAuthors      []BookAuthor `json:"book_authors,omitempty"`
	BookAuthorsKnown bool         `json:"book_authors_known,omitempty"`
}

// BookSnapshot represents an immutable snapshot of a book at a point in time.
type BookSnapshot struct {
	BookID    string    `json:"book_id"`
	Timestamp time.Time `json:"timestamp"`
	Data      []byte    `json:"data"` // Full JSON-serialized Book
}

// DeferredITunesUpdate records a path change that should be applied to the
// iTunes library (.itl) the next time write-back is enabled and a sync runs.
type DeferredITunesUpdate struct {
	ID           int        `json:"id"`
	BookID       string     `json:"book_id"`
	PersistentID string     `json:"persistent_id"`
	OldPath      string     `json:"old_path"`
	NewPath      string     `json:"new_path"`
	UpdateType   string     `json:"update_type"`
	CreatedAt    time.Time  `json:"created_at"`
	AppliedAt    *time.Time `json:"applied_at,omitempty"`
}

// KVPair is a raw key-value pair from the store.
type KVPair struct {
	Key   string
	Value []byte
}

// RawWrite is one staged operation of RawBatchApplier.ApplyRawBatch: a set of Key
// to Value, or a delete of Key when Delete is true (Value is ignored then).
// Delete is explicit so a nil or empty Value can never be read as a delete.
type RawWrite struct {
	Key    string
	Value  []byte
	Delete bool
}

// ExternalIDMapping maps an external identifier (iTunes PID, Audible ASIN, etc.) to a book.
type ExternalIDMapping struct {
	ID          int        `json:"id"`
	Source      string     `json:"source"`
	ExternalID  string     `json:"external_id"`
	BookID      string     `json:"book_id"`
	TrackNumber *int       `json:"track_number,omitempty"`
	FilePath    string     `json:"file_path,omitempty"`
	Tombstoned  bool       `json:"tombstoned"`
	Provenance  string     `json:"provenance,omitempty"` // "itunes", "generated", "recycled"
	RemovedAt   *time.Time `json:"removed_at,omitempty"` // when we sent ITL remove; null = live
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// BookPathChange records a file path change (rename/move) for a book.
type BookPathChange struct {
	ID         int       `json:"id"`
	BookID     string    `json:"book_id"`
	OldPath    string    `json:"old_path"`
	NewPath    string    `json:"new_path"`
	ChangeType string    `json:"change_type"`
	CreatedAt  time.Time `json:"created_at"`
}

// BookTag represents one tag row on a book. Source distinguishes
// user-applied tags ("user") from system-applied provenance tags
// ("system", e.g. dedup:merge-survivor:llm-auto, metadata:source:*).
// BookID is populated on reads that need to identify the owning book
// (historically PebbleStore serialized it into the JSON value); for
// the per-book read path it's left empty since the caller already
// has the ID.
type BookTag struct {
	BookID    string    `json:"book_id,omitempty"`
	Tag       string    `json:"tag"`
	Source    string    `json:"source,omitempty"` // "user" | "system"
	CreatedAt time.Time `json:"created_at"`
}

// TagWithCount represents a tag with its usage count.
type TagWithCount struct {
	Tag    string `json:"tag"`
	Count  int    `json:"count"`
	Source string `json:"source,omitempty"` // "user" or "system" — empty when mixed
}

// ScanCacheEntry holds mtime/size for incremental scan skip checks.
type ScanCacheEntry struct {
	Mtime       int64
	Size        int64
	NeedsRescan bool
}

// LibraryStats holds all aggregated library statistics. On PebbleDB it is persisted
// under the key "stats:library" and recomputed on TTL expiry or explicit invalidation.
// Computed in two range scans (book: then book_file:) — no per-book point lookups.
type LibraryStats struct {
	TotalBooks         int            `json:"total_books"`
	TotalFiles         int            `json:"total_files"`
	TotalAuthors       int            `json:"total_authors"`
	TotalSeries        int            `json:"total_series"`
	TotalDuration      int64          `json:"total_duration"`
	TotalSize          int64          `json:"total_size"`
	OrganizedBooks     int            `json:"organized_books"`
	UnorganizedBooks   int            `json:"unorganized_books"`
	OrganizedSize      int64          `json:"organized_size"`
	UnorganizedSize    int64          `json:"unorganized_size"`
	BooksByImportPath  map[int]int    `json:"books_by_import_path"`
	SizeByImportPath   map[int]int64  `json:"size_by_import_path"`
	StateDistribution  map[string]int `json:"state_distribution"`
	FormatDistribution map[string]int `json:"format_distribution"`
	// BrokenFiles is the number of distinct books that have at least one recorded
	// file error (from the book_file_errors_by_book: index). Populated alongside
	// TotalBooks/TotalFiles in computeLibraryStats so all three counts share a
	// single cache entry and invalidation path.
	BrokenFiles int `json:"broken_files"`
	// FingerprintedBooks/PartiallyFingerprintedBooks/UnfingerprintedBooks
	// classify every non-deleted book by whether its active book_files have
	// any/all/none of them fingerprinted (BookFile.GetAcoustIDSeg0() != "",
	// which already falls back to the memdb-safe AcoustIDFingerprintDurationSec
	// proxy — see bookfile_fingerprint.go). Computed alongside the existing
	// book_file pass so no extra scan is added.
	FingerprintedBooks          int `json:"fingerprinted_books"`
	PartiallyFingerprintedBooks int `json:"partially_fingerprinted_books"`
	UnfingerprintedBooks        int `json:"unfingerprinted_books"`
	// FingerprintCoveragePercent = FingerprintedBooks * 100 / (TotalBooks
	// excluding non-primary duplicates counted elsewhere); 0 when TotalBooks==0.
	FingerprintCoveragePercent int       `json:"fingerprint_coverage_percent"`
	ComputedAt                 time.Time `json:"computed_at"`
}

// DashboardStats is an alias kept for callers that haven't migrated to LibraryStats yet.
type DashboardStats = LibraryStats

// MetadataRejection records a metadata candidate that was rejected for a book,
// either by the user explicitly or automatically (below threshold, duration
// mismatch, wrong language, etc.).
type MetadataRejection struct {
	ID              string    `json:"id"`
	BookID          string    `json:"book_id"`
	Source          string    `json:"source"` // "audible", "openlibrary", "googlebooks", etc.
	CandidateASIN   string    `json:"candidate_asin,omitempty"`
	CandidateISBN   string    `json:"candidate_isbn,omitempty"`
	CandidateTitle  string    `json:"candidate_title,omitempty"`
	CandidateAuthor string    `json:"candidate_author,omitempty"`
	RejectionReason string    `json:"rejection_reason"` // "user_rejected", "below_threshold", "duration_mismatch", "wrong_language", "skipped"
	Score           float64   `json:"score,omitempty"`
	RejectedAt      time.Time `json:"rejected_at"`
}

// DuplicateFileGroup is a set of book_file records that all share the same
// original_file_hash — i.e. the same physical audio content at different paths.
type DuplicateFileGroup struct {
	Hash      string              `json:"hash"`
	FileCount int                 `json:"file_count"`
	BookCount int                 `json:"book_count"`       // distinct book IDs
	TotalSize int64               `json:"total_size_bytes"` // sum of file sizes
	Files     []DuplicateFileInfo `json:"files"`
}

// DuplicateFileInfo is one entry within a DuplicateFileGroup.
type DuplicateFileInfo struct {
	BookFileID string `json:"book_file_id"`
	BookID     string `json:"book_id"`
	BookTitle  string `json:"book_title"`
	FilePath   string `json:"file_path"` // book_files.file_path
	BookPath   string `json:"book_path"` // books.file_path (directory)
	FileSize   int64  `json:"file_size_bytes"`
}

// BookFileHashStats is returned by GetBookFileHashStats and shows hash-coverage
// for the entire book_files table, broken down by library root path.
type BookFileHashStats struct {
	TotalBookFiles   int                      `json:"total_book_files"`
	WithFileHash     int                      `json:"with_file_hash"`
	MissingFileHash  int                      `json:"missing_file_hash"`
	WithOriginalHash int                      `json:"with_original_hash"`
	TotalBooks       int                      `json:"total_books"`
	BooksWithNoFiles int                      `json:"books_with_no_files"`
	ByLibrary        []BookFileHashStatsByLib `json:"by_library"`
}

// BookFileHashStatsByLib breaks down hash coverage for one library root path.
type BookFileHashStatsByLib struct {
	Path        string `json:"path"`
	TotalFiles  int    `json:"total_files"`
	WithHash    int    `json:"with_hash"`
	MissingHash int    `json:"missing_hash"`
}

// BookMetadataHashStats is returned by GetBookMetadataHashStats and shows
// metadata_source_hash coverage across all books, broken down by library.
type BookMetadataHashStats struct {
	TotalBooks          int                          `json:"total_books"`
	WithMetadataHash    int                          `json:"with_metadata_hash"`
	MissingMetadataHash int                          `json:"missing_metadata_hash"`
	WithASINOrISBN      int                          `json:"with_asin_or_isbn"`
	MissingHashHasID    int                          `json:"missing_hash_has_id"` // can be backfilled
	ByLibrary           []BookMetadataHashStatsByLib `json:"by_library"`
}

// BookMetadataHashStatsByLib breaks down metadata_source_hash coverage for one library root.
type BookMetadataHashStatsByLib struct {
	Path                string `json:"path"`
	TotalBooks          int    `json:"total_books"`
	WithMetadataHash    int    `json:"with_metadata_hash"`
	MissingMetadataHash int    `json:"missing_metadata_hash"`
	MissingHashHasID    int    `json:"missing_hash_has_id"`
}

// AcoustIDStats describes AcoustID fingerprint coverage across all book files.
type AcoustIDStats struct {
	TotalFiles      int                      `json:"total_files"`
	WithFingerprint int                      `json:"with_fingerprint"` // ≥1 segment populated
	ByLibrary       []AcoustIDStatsByLibrary `json:"by_library"`
	// WithFingerprintZeroDuration counts fingerprinted rows where
	// AcoustIDFingerprintDurationSec==0 (STOREFID DurationSec invariant check).
	// The PR-B ops gate on this field surviving the memdb strip as a proxy for
	// "has a fingerprint" — a nonzero count here means that proxy is unsound
	// for those rows and they're being silently skipped by those ops.
	WithFingerprintZeroDuration int `json:"with_fingerprint_zero_duration"`
}

// AcoustIDStatsByLibrary breaks down fingerprint coverage for one library root.
type AcoustIDStatsByLibrary struct {
	LibraryRoot     string `json:"library_root"`
	TotalFiles      int    `json:"total_files"`
	WithFingerprint int    `json:"with_fingerprint"`
}

// Global store instance — use GetGlobalStore/SetGlobalStore for concurrent access.
// Direct assignment is allowed in single-goroutine contexts (init, main).
var globalStore Store
var globalStoreMu sync.RWMutex

// GetGlobalStore returns the global store with read-lock protection.
func GetGlobalStore() Store {
	globalStoreMu.RLock()
	s := globalStore
	globalStoreMu.RUnlock()
	return s
}

// SetGlobalStore sets the global store with write-lock protection.
func SetGlobalStore(s Store) {
	globalStoreMu.Lock()
	globalStore = s
	globalStoreMu.Unlock()
}

// InitializeStore initializes the database store based on configuration
// and returns it. The package-level globalStore is also set for back-compat
// with code paths that still use GetGlobalStore(); new code should prefer
// the returned Store and pass it explicitly down the call chain
// (SERVER-GLOBAL-STORE-AUDIT, in flight).
//
// The SQLite backend was removed in fable5 TASK-022. Any dbType value other
// than "pebble" (or empty) returns an error.
func InitializeStore(dbType, path string) (Store, error) {
	var s Store
	var err error

	switch dbType {
	case "sqlite", "sqlite3":
		return nil, fmt.Errorf("database_type %q is not supported: SQLite3 support has been removed and PebbleDB is the only database backend; set database_type to \"pebble\" (or drop --db-type %s) and point database_path at a PebbleDB directory", dbType, dbType)
	case "pebble", "":
		// PebbleDB is the default and only supported backend. Opening it can
		// replay a large WAL, so ask systemd for start time first.
		extendStartTimeout()
		s, err = NewPebbleStore(path)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize PebbleDB store: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported database type: %s (only 'pebble' is supported)", dbType)
	}

	SetGlobalStore(s)

	// Run migrations to ensure schema is up to date
	if err := RunMigrations(s); err != nil {
		return nil, fmt.Errorf("failed to run migrations: %w", err)
	}

	// Every boot, not only when migration 065 runs: rebuild the opv3 records
	// a previous binary left stale (ReconcileOpsV3WithV2). Synchronous and
	// here, before the caller builds the server and the operations registry,
	// because the registry's boot resume acts on opv3 records. A failure is
	// logged, not returned: opv2: is still the source of truth the registry
	// runs from, and the next boot retries.
	if r, ok := s.(opsV3Reconciler); ok {
		if _, err := r.ReconcileOpsV3WithV2(context.Background()); err != nil {
			opsV3Log.Error("opv3 reconcile failed; opv3 records may be stale until each run's next write, and the next boot retries: %v", err)
		}
	}

	return s, nil
}

// opsV3Reconciler is the boot reconcile's one capability, asserted at run
// time so Store is not widened for one boot step (every production store is
// a *PebbleStore and has it).
type opsV3Reconciler interface {
	ReconcileOpsV3WithV2(ctx context.Context) (OpsV3ReconcileReport, error)
}

// CloseStore closes the global store
func CloseStore() error {
	// Grab and nil the global ref under the write lock so a concurrent
	// GetGlobalStore (which only takes the read lock) can never observe
	// a store that CloseStore has already started closing.
	globalStoreMu.Lock()
	store := globalStore
	globalStore = nil
	globalStoreMu.Unlock()

	if store != nil {
		return store.Close()
	}
	return nil
}
