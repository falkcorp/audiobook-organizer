// file: internal/database/bookcore.go
// version: 1.1.0
// guid: 7f3c1e28-9a4d-4b61-8c2f-bookcore000001
// last-edited: 2026-07-05

package database

import "time"

// BookCore is the compiler-enforced slim projection of Book: every Book field
// EXCEPT the nine heavy, rarely-queried fields that stripBookForMemdb clears
// before a Book is inserted into the in-memory radix tree (see memdb_strip.go).
//
// The excluded ("heavy") fields, which live ONLY on Book, are:
//
//	Description, VersionNotes, BookSigV1, BookSigV1Mask, BookSigSegments,
//	BookSigBuiltAt, BookSigCoveragePct, Author, Series
//
// BookCore therefore describes exactly the payload a memdb-resident Book is
// guaranteed to carry. Making that partition a real type lets memdb-backed code
// paths state, at compile time, that they never touch a stripped field. This is
// purely additive today — nothing returns BookCore yet; Phase 3 wires it in.
//
// Field declarations (names, types, and struct tags) are copied verbatim from
// Book; the two reflection tests in bookcore_test.go lock the partition and the
// copy so drift is impossible.
type BookCore struct {
	ID             string `json:"id"` // ULID format
	Title          string `json:"title"`
	AuthorID       *int   `json:"author_id,omitempty"`
	SeriesID       *int   `json:"series_id,omitempty"`
	SeriesSequence *int   `json:"series_sequence,omitempty"`
	FilePath       string `json:"file_path"`
	Format         string `json:"format,omitempty"`
	Duration       *int   `json:"duration,omitempty"`
	// Extended metadata (optional)
	WorkID   *string `json:"work_id,omitempty"`
	Narrator *string `json:"narrator,omitempty"`
	Edition  *string `json:"edition,omitempty"`
	// Description is a heavy field — omitted from BookCore.
	Language             *string `json:"language,omitempty"`
	Publisher            *string `json:"publisher,omitempty"`
	Genre                *string `json:"genre,omitempty"`
	PrintYear            *int    `json:"print_year,omitempty"`
	AudiobookReleaseYear *int    `json:"audiobook_release_year,omitempty"`
	ISBN10               *string `json:"isbn10,omitempty"`
	ISBN13               *string `json:"isbn13,omitempty"`
	ASIN                 *string `json:"asin,omitempty"`
	// External provider IDs
	OpenLibraryID *string `json:"open_library_id,omitempty"`
	HardcoverID   *string `json:"hardcover_id,omitempty"`
	GoogleBooksID *string `json:"google_books_id,omitempty"`
	// iTunes import fields
	ITunesPersistentID *string    `json:"itunes_persistent_id,omitempty"`
	ITunesDateAdded    *time.Time `json:"itunes_date_added,omitempty"`
	ITunesPlayCount    *int       `json:"itunes_play_count,omitempty"`
	ITunesLastPlayed   *time.Time `json:"itunes_last_played,omitempty"`
	ITunesRating       *int       `json:"itunes_rating,omitempty"`
	ITunesBookmark     *int64     `json:"itunes_bookmark,omitempty"`
	ITunesImportSource *string    `json:"itunes_import_source,omitempty"`
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
	// VersionNotes is a heavy field — omitted from BookCore.
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
	// MetadataReviewStatus tracks manual metadata matching: null, "no_match", "matched".
	MetadataReviewStatus *string `json:"metadata_review_status,omitempty"`
	// MetadataSource records which provider supplied the last applied metadata.
	MetadataSource *string `json:"metadata_source,omitempty"`
	// BookSigV1, BookSigSegments, BookSigBuiltAt, BookSigV1Mask, BookSigCoveragePct
	// are heavy fields — omitted from BookCore.
	// ITunesSyncStatus tracks whether this book's metadata is in sync with the iTunes library.
	ITunesSyncStatus *string `json:"itunes_sync_status,omitempty"`
	// AudibleRuntimeMin is the runtime reported by Audible (in minutes) for the matched product.
	AudibleRuntimeMin *int `json:"audible_runtime_min,omitempty"`
	// DurationVerifiedAt is stamped by the duration-reextract op.
	DurationVerifiedAt *time.Time `json:"duration_verified_at,omitempty"`
	// MetadataSourceHash is sha256("{source}:{canonical_id}") set during metadata apply.
	MetadataSourceHash *string `json:"metadata_source_hash,omitempty"`
	// MergedIntoBookID is set when this book has been absorbed into a consolidated book.
	MergedIntoBookID *string `json:"merged_into_book_id,omitempty"`
	// Audible ratings (1–5 scale).
	AudibleRatingOverall     *float64 `json:"audible_rating_overall,omitempty"`
	AudibleRatingPerformance *float64 `json:"audible_rating_performance,omitempty"` // narrator/production
	AudibleRatingStory       *float64 `json:"audible_rating_story,omitempty"`       // story/content
	AudibleRatingCount       *int     `json:"audible_rating_count,omitempty"`
	AudibleNumReviews        *int     `json:"audible_num_reviews,omitempty"`
	// Google Books rating (1–5 scale).
	GoogleRatingAverage *float64 `json:"google_rating_average,omitempty"`
	GoogleRatingCount   *int     `json:"google_rating_count,omitempty"`
	// User personal ratings (1–5 scale, independent of community ratings).
	UserRatingOverall     *float64 `json:"user_rating_overall,omitempty"`
	UserRatingStory       *float64 `json:"user_rating_story,omitempty"`
	UserRatingPerformance *float64 `json:"user_rating_performance,omitempty"`
	UserRatingNotes       *string  `json:"user_rating_notes,omitempty"`
	// Cover art
	CoverURL *string `json:"cover_url,omitempty"`
	// Narrators as JSON array
	NarratorsJSON *string `json:"narrators_json,omitempty"`
	// SourceImportPath is the top-level import-path folder this book was FIRST discovered in.
	SourceImportPath *string `json:"source_import_path,omitempty"`
	// Scan cache for incremental scanning (set by scanner, not user-facing)
	LastScanMtime *int64 `json:"last_scan_mtime,omitempty"`
	LastScanSize  *int64 `json:"last_scan_size,omitempty"`
	NeedsRescan   *bool  `json:"needs_rescan,omitempty"`
	// IntroTranscription is the raw Whisper transcript of the first ~30 seconds.
	IntroTranscription *string `json:"intro_transcription,omitempty"`
	// TranscribedTitle / TranscribedAuthor / TranscribedNarrator parsed from IntroTranscription.
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
	// TranscribeStatus records the outcome of the most recent transcription attempt.
	TranscribeStatus *string `json:"transcribe_status,omitempty"`
	// TranscribeError holds a short human-readable detail for a non-ok status.
	TranscribeError *string `json:"transcribe_error,omitempty"`
	// TranscribeAttemptedAt is when the most recent transcription attempt ran.
	TranscribeAttemptedAt *time.Time `json:"transcribe_attempted_at,omitempty"`
	// Fingerprinting fields (computed, not stored in DB)
	FingerprintStatus      string     `json:"fingerprint_status,omitempty"` // "none", "partial", "complete"
	FingerprintedFileCount int        `json:"fingerprinted_file_count,omitempty"`
	TotalFileCount         int        `json:"total_file_count,omitempty"`
	CoveragePercent        int        `json:"coverage_percent,omitempty"`
	LastFingerprintedAt    *time.Time `json:"last_fingerprinted_at,omitempty"`
	// Related objects (populated via joins, not stored in DB).
	// Author and Series are heavy fields — omitted from BookCore.
	Authors              []BookAuthor                       `json:"authors,omitempty" db:"-"`
	MetadataProvenance   map[string]MetadataProvenanceEntry `json:"metadata_provenance,omitempty" db:"-"`
	MetadataProvenanceAt *time.Time                         `json:"metadata_provenance_at,omitempty" db:"-"`
}

// Core returns the BookCore projection of b — a copy of every Book field that
// survives the memdb strip (i.e. every field except the nine heavy ones listed
// on BookCore). It is purely a projection: pointer, slice, and map fields are
// copied by reference, exactly as a struct assignment would. TestBookCoreCopiesAllFields
// asserts that no field is silently dropped here.
func (b *Book) Core() BookCore {
	return BookCore{
		ID:                       b.ID,
		Title:                    b.Title,
		AuthorID:                 b.AuthorID,
		SeriesID:                 b.SeriesID,
		SeriesSequence:           b.SeriesSequence,
		FilePath:                 b.FilePath,
		Format:                   b.Format,
		Duration:                 b.Duration,
		WorkID:                   b.WorkID,
		Narrator:                 b.Narrator,
		Edition:                  b.Edition,
		Language:                 b.Language,
		Publisher:                b.Publisher,
		Genre:                    b.Genre,
		PrintYear:                b.PrintYear,
		AudiobookReleaseYear:     b.AudiobookReleaseYear,
		ISBN10:                   b.ISBN10,
		ISBN13:                   b.ISBN13,
		ASIN:                     b.ASIN,
		OpenLibraryID:            b.OpenLibraryID,
		HardcoverID:              b.HardcoverID,
		GoogleBooksID:            b.GoogleBooksID,
		ITunesPersistentID:       b.ITunesPersistentID,
		ITunesDateAdded:          b.ITunesDateAdded,
		ITunesPlayCount:          b.ITunesPlayCount,
		ITunesLastPlayed:         b.ITunesLastPlayed,
		ITunesRating:             b.ITunesRating,
		ITunesBookmark:           b.ITunesBookmark,
		ITunesImportSource:       b.ITunesImportSource,
		ITunesPath:               b.ITunesPath,
		OriginalFilename:         b.OriginalFilename,
		Bitrate:                  b.Bitrate,
		Codec:                    b.Codec,
		SampleRate:               b.SampleRate,
		Channels:                 b.Channels,
		BitDepth:                 b.BitDepth,
		Quality:                  b.Quality,
		IsPrimaryVersion:         b.IsPrimaryVersion,
		VersionGroupID:           b.VersionGroupID,
		FileHash:                 b.FileHash,
		FileSize:                 b.FileSize,
		OriginalFileHash:         b.OriginalFileHash,
		OrganizedFileHash:        b.OrganizedFileHash,
		LibraryState:             b.LibraryState,
		Quantity:                 b.Quantity,
		MarkedForDeletion:        b.MarkedForDeletion,
		MarkedForDeletionAt:      b.MarkedForDeletionAt,
		QuarantineReason:         b.QuarantineReason,
		QuarantinedAt:            b.QuarantinedAt,
		CreatedAt:                b.CreatedAt,
		UpdatedAt:                b.UpdatedAt,
		MetadataUpdatedAt:        b.MetadataUpdatedAt,
		LastWrittenAt:            b.LastWrittenAt,
		LastOrganizeOperationID:  b.LastOrganizeOperationID,
		LastOrganizedAt:          b.LastOrganizedAt,
		MetadataReviewStatus:     b.MetadataReviewStatus,
		MetadataSource:           b.MetadataSource,
		ITunesSyncStatus:         b.ITunesSyncStatus,
		AudibleRuntimeMin:        b.AudibleRuntimeMin,
		DurationVerifiedAt:       b.DurationVerifiedAt,
		MetadataSourceHash:       b.MetadataSourceHash,
		MergedIntoBookID:         b.MergedIntoBookID,
		AudibleRatingOverall:     b.AudibleRatingOverall,
		AudibleRatingPerformance: b.AudibleRatingPerformance,
		AudibleRatingStory:       b.AudibleRatingStory,
		AudibleRatingCount:       b.AudibleRatingCount,
		AudibleNumReviews:        b.AudibleNumReviews,
		GoogleRatingAverage:      b.GoogleRatingAverage,
		GoogleRatingCount:        b.GoogleRatingCount,
		UserRatingOverall:        b.UserRatingOverall,
		UserRatingStory:          b.UserRatingStory,
		UserRatingPerformance:    b.UserRatingPerformance,
		UserRatingNotes:          b.UserRatingNotes,
		CoverURL:                 b.CoverURL,
		NarratorsJSON:            b.NarratorsJSON,
		SourceImportPath:         b.SourceImportPath,
		LastScanMtime:            b.LastScanMtime,
		LastScanSize:             b.LastScanSize,
		NeedsRescan:              b.NeedsRescan,
		IntroTranscription:       b.IntroTranscription,
		TranscribedTitle:         b.TranscribedTitle,
		TranscribedAuthor:        b.TranscribedAuthor,
		TranscribedNarrator:      b.TranscribedNarrator,
		TranscribedTranslator:    b.TranscribedTranslator,
		TranscribedCoverArtist:   b.TranscribedCoverArtist,
		IntroTranscribedAt:       b.IntroTranscribedAt,
		TranscribeStatus:         b.TranscribeStatus,
		TranscribeError:          b.TranscribeError,
		TranscribeAttemptedAt:    b.TranscribeAttemptedAt,
		FingerprintStatus:        b.FingerprintStatus,
		FingerprintedFileCount:   b.FingerprintedFileCount,
		TotalFileCount:           b.TotalFileCount,
		CoveragePercent:          b.CoveragePercent,
		LastFingerprintedAt:      b.LastFingerprintedAt,
		Authors:                  b.Authors,
		MetadataProvenance:       b.MetadataProvenance,
		MetadataProvenanceAt:     b.MetadataProvenanceAt,
	}
}

// ToBook reconstructs a Book from its BookCore projection. The nine heavy
// fields (Description, VersionNotes, BookSigV1, BookSigV1Mask,
// BookSigSegments, BookSigBuiltAt, BookSigCoveragePct, Author, Series) are
// left at their zero value — BookCore never carried them, so this is not a
// lossy conversion relative to what the caller already had.
//
// This exists for call sites that receive a Core-typed result (e.g. from
// GetBooksByAuthorIDCore) but must hand it to shared code that still takes
// *Book/[]Book and is used elsewhere with genuinely full-fidelity Book
// values (so widening THAT code's signature to BookCore is out of scope).
// Added alongside STOREFID P3-W2; see
// docs/specs/2026-07-05-store-getter-fidelity-unification.md.
func (c *BookCore) ToBook() Book {
	return Book{
		ID:                       c.ID,
		Title:                    c.Title,
		AuthorID:                 c.AuthorID,
		SeriesID:                 c.SeriesID,
		SeriesSequence:           c.SeriesSequence,
		FilePath:                 c.FilePath,
		Format:                   c.Format,
		Duration:                 c.Duration,
		WorkID:                   c.WorkID,
		Narrator:                 c.Narrator,
		Edition:                  c.Edition,
		Language:                 c.Language,
		Publisher:                c.Publisher,
		Genre:                    c.Genre,
		PrintYear:                c.PrintYear,
		AudiobookReleaseYear:     c.AudiobookReleaseYear,
		ISBN10:                   c.ISBN10,
		ISBN13:                   c.ISBN13,
		ASIN:                     c.ASIN,
		OpenLibraryID:            c.OpenLibraryID,
		HardcoverID:              c.HardcoverID,
		GoogleBooksID:            c.GoogleBooksID,
		ITunesPersistentID:       c.ITunesPersistentID,
		ITunesDateAdded:          c.ITunesDateAdded,
		ITunesPlayCount:          c.ITunesPlayCount,
		ITunesLastPlayed:         c.ITunesLastPlayed,
		ITunesRating:             c.ITunesRating,
		ITunesBookmark:           c.ITunesBookmark,
		ITunesImportSource:       c.ITunesImportSource,
		ITunesPath:               c.ITunesPath,
		OriginalFilename:         c.OriginalFilename,
		Bitrate:                  c.Bitrate,
		Codec:                    c.Codec,
		SampleRate:               c.SampleRate,
		Channels:                 c.Channels,
		BitDepth:                 c.BitDepth,
		Quality:                  c.Quality,
		IsPrimaryVersion:         c.IsPrimaryVersion,
		VersionGroupID:           c.VersionGroupID,
		FileHash:                 c.FileHash,
		FileSize:                 c.FileSize,
		OriginalFileHash:         c.OriginalFileHash,
		OrganizedFileHash:        c.OrganizedFileHash,
		LibraryState:             c.LibraryState,
		Quantity:                 c.Quantity,
		MarkedForDeletion:        c.MarkedForDeletion,
		MarkedForDeletionAt:      c.MarkedForDeletionAt,
		QuarantineReason:         c.QuarantineReason,
		QuarantinedAt:            c.QuarantinedAt,
		CreatedAt:                c.CreatedAt,
		UpdatedAt:                c.UpdatedAt,
		MetadataUpdatedAt:        c.MetadataUpdatedAt,
		LastWrittenAt:            c.LastWrittenAt,
		LastOrganizeOperationID:  c.LastOrganizeOperationID,
		LastOrganizedAt:          c.LastOrganizedAt,
		MetadataReviewStatus:     c.MetadataReviewStatus,
		MetadataSource:           c.MetadataSource,
		ITunesSyncStatus:         c.ITunesSyncStatus,
		AudibleRuntimeMin:        c.AudibleRuntimeMin,
		DurationVerifiedAt:       c.DurationVerifiedAt,
		MetadataSourceHash:       c.MetadataSourceHash,
		MergedIntoBookID:         c.MergedIntoBookID,
		AudibleRatingOverall:     c.AudibleRatingOverall,
		AudibleRatingPerformance: c.AudibleRatingPerformance,
		AudibleRatingStory:       c.AudibleRatingStory,
		AudibleRatingCount:       c.AudibleRatingCount,
		AudibleNumReviews:        c.AudibleNumReviews,
		GoogleRatingAverage:      c.GoogleRatingAverage,
		GoogleRatingCount:        c.GoogleRatingCount,
		UserRatingOverall:        c.UserRatingOverall,
		UserRatingStory:          c.UserRatingStory,
		UserRatingPerformance:    c.UserRatingPerformance,
		UserRatingNotes:          c.UserRatingNotes,
		CoverURL:                 c.CoverURL,
		NarratorsJSON:            c.NarratorsJSON,
		SourceImportPath:         c.SourceImportPath,
		LastScanMtime:            c.LastScanMtime,
		LastScanSize:             c.LastScanSize,
		NeedsRescan:              c.NeedsRescan,
		IntroTranscription:       c.IntroTranscription,
		TranscribedTitle:         c.TranscribedTitle,
		TranscribedAuthor:        c.TranscribedAuthor,
		TranscribedNarrator:      c.TranscribedNarrator,
		TranscribedTranslator:    c.TranscribedTranslator,
		TranscribedCoverArtist:   c.TranscribedCoverArtist,
		IntroTranscribedAt:       c.IntroTranscribedAt,
		TranscribeStatus:         c.TranscribeStatus,
		TranscribeError:          c.TranscribeError,
		TranscribeAttemptedAt:    c.TranscribeAttemptedAt,
		FingerprintStatus:        c.FingerprintStatus,
		FingerprintedFileCount:   c.FingerprintedFileCount,
		TotalFileCount:           c.TotalFileCount,
		CoveragePercent:          c.CoveragePercent,
		LastFingerprintedAt:      c.LastFingerprintedAt,
		Authors:                  c.Authors,
		MetadataProvenance:       c.MetadataProvenance,
		MetadataProvenanceAt:     c.MetadataProvenanceAt,
	}
}
