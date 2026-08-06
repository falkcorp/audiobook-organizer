// file: internal/database/bookfilecore.go
// version: 1.1.0
// guid: 715f4b68-2d23-4f52-b1dd-1b3d0357a4f6
// last-edited: 2026-08-06

package database

import "time"

// BookFileCore is the projection of BookFile that survives the memdb strip
// performed by stripBookFileForMemdb. It contains every BookFile field EXCEPT
// the heavy set that memdb clears before insertion:
//
//	FingerprintFailureReason, FingerprintFailureDetail, FingerprintDiagnosticJSON,
//	AcoustIDFingerprint, AcoustIDSeg0..6, IntroTranscription
//
// IntroTranscription is the only member of the per-file intro-transcription
// group that is stripped. The other SEVEN (TranscribedTitle/Author/Narrator,
// IntroTranscribedAt, TranscribeStatus, TranscribeError, TranscribeAttemptedAt)
// are RETAINED here — they are the small, queryable fields, and the raw
// transcript carries ~99% of the group's bytes. See memdb_strip.go for the math.
//
// Note the two fingerprint-adjacent fields that are intentionally RETAINED
// (they are NOT stripped in memdb_strip.go and therefore belong on Core):
//
//	FingerprintFailedAt              — 24B/row, read by the LSH index builder.
//	AcoustIDFingerprintDurationSec   — preserved as the fingerprint_status proxy.
//
// Keeping BookFileCore mechanically in sync with the strip set is enforced by
// the reflection tests in bookfilecore_test.go: the name-set diff between
// BookFile and BookFileCore must equal exactly the stripped set, and Core()
// must copy every BookFileCore field.
//
// json tags are carried verbatim from BookFile so a BookFileCore serializes
// identically to a stripped BookFile.
type BookFileCore struct {
	ID     string `json:"id"`
	BookID string `json:"book_id"`

	VersionID          string `json:"version_id,omitempty"`
	FilePath           string `json:"file_path"`
	OriginalFilename   string `json:"original_filename,omitempty"`
	ITunesPath         string `json:"itunes_path,omitempty"`
	ITunesPersistentID string `json:"itunes_persistent_id,omitempty"`
	TrackNumber        int    `json:"track_number,omitempty"`
	TrackCount         int    `json:"track_count,omitempty"`
	DiscNumber         int    `json:"disc_number,omitempty"`
	DiscCount          int    `json:"disc_count,omitempty"`
	Title              string `json:"title,omitempty"`

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
	PostMetadataHash string            `json:"post_metadata_hash,omitempty"`

	// AcoustIDFingerprintDurationSec is RETAINED on Core (not stripped).
	AcoustIDFingerprintDurationSec float64 `json:"acoustid_fingerprint_duration_sec,omitempty"`

	// FingerprintFailedAt is RETAINED on Core (not stripped).
	FingerprintFailedAt *time.Time `json:"fingerprint_failed_at,omitempty"`

	AcoustIDOnlineRecordingID string     `json:"acoustid_online_recording_id,omitempty"`
	AcoustIDOnlineScore       float64    `json:"acoustid_online_score,omitempty"`
	AcoustIDOnlineLookedUpAt  *time.Time `json:"acoustid_online_looked_up_at,omitempty"`
	OrganizeMethod            string     `json:"organize_method,omitempty"`
	Missing                   bool       `json:"missing"`
	SkipScan                  bool       `json:"skip_scan"`
	CreatedAt                 time.Time  `json:"created_at"`
	UpdatedAt                 time.Time  `json:"updated_at"`

	DelugeHash           string     `json:"deluge_hash,omitempty"`
	DownloadHash         string     `json:"download_hash,omitempty"`
	DelugeOriginalPath   string     `json:"deluge_original_path,omitempty"`
	ImportedFromDelugeAt *time.Time `json:"imported_from_deluge_at,omitempty"`

	// Per-file intro transcription — the 7 RETAINED fields. IntroTranscription
	// (the raw transcript) is deliberately absent: it is the stripped one.
	TranscribedTitle      *string    `json:"transcribed_title,omitempty"`
	TranscribedAuthor     *string    `json:"transcribed_author,omitempty"`
	TranscribedNarrator   *string    `json:"transcribed_narrator,omitempty"`
	IntroTranscribedAt    *time.Time `json:"intro_transcribed_at,omitempty"`
	TranscribeStatus      *string    `json:"transcribe_status,omitempty"`
	TranscribeError       *string    `json:"transcribe_error,omitempty"`
	TranscribeAttemptedAt *time.Time `json:"transcribe_attempted_at,omitempty"`
}

// Core returns the BookFileCore projection of f — every BookFile field that
// survives the memdb strip, copied by value. The heavy fingerprint-diagnostic
// fields (see BookFileCore doc) are dropped. Purely additive: it neither reads
// nor mutates state beyond f.
func (f *BookFile) Core() BookFileCore {
	return BookFileCore{
		ID:                             f.ID,
		BookID:                         f.BookID,
		VersionID:                      f.VersionID,
		FilePath:                       f.FilePath,
		OriginalFilename:               f.OriginalFilename,
		ITunesPath:                     f.ITunesPath,
		ITunesPersistentID:             f.ITunesPersistentID,
		TrackNumber:                    f.TrackNumber,
		TrackCount:                     f.TrackCount,
		DiscNumber:                     f.DiscNumber,
		DiscCount:                      f.DiscCount,
		Title:                          f.Title,
		RawTags:                        f.RawTags,
		Format:                         f.Format,
		Codec:                          f.Codec,
		Duration:                       f.Duration,
		FileSize:                       f.FileSize,
		BitrateKbps:                    f.BitrateKbps,
		SampleRateHz:                   f.SampleRateHz,
		Channels:                       f.Channels,
		BitDepth:                       f.BitDepth,
		FileHash:                       f.FileHash,
		OriginalFileHash:               f.OriginalFileHash,
		PostMetadataHash:               f.PostMetadataHash,
		AcoustIDFingerprintDurationSec: f.AcoustIDFingerprintDurationSec,
		FingerprintFailedAt:            f.FingerprintFailedAt,
		AcoustIDOnlineRecordingID:      f.AcoustIDOnlineRecordingID,
		AcoustIDOnlineScore:            f.AcoustIDOnlineScore,
		AcoustIDOnlineLookedUpAt:       f.AcoustIDOnlineLookedUpAt,
		OrganizeMethod:                 f.OrganizeMethod,
		Missing:                        f.Missing,
		SkipScan:                       f.SkipScan,
		CreatedAt:                      f.CreatedAt,
		UpdatedAt:                      f.UpdatedAt,
		DelugeHash:                     f.DelugeHash,
		DownloadHash:                   f.DownloadHash,
		DelugeOriginalPath:             f.DelugeOriginalPath,
		ImportedFromDelugeAt:           f.ImportedFromDelugeAt,
		TranscribedTitle:               f.TranscribedTitle,
		TranscribedAuthor:              f.TranscribedAuthor,
		TranscribedNarrator:            f.TranscribedNarrator,
		IntroTranscribedAt:             f.IntroTranscribedAt,
		TranscribeStatus:               f.TranscribeStatus,
		TranscribeError:                f.TranscribeError,
		TranscribeAttemptedAt:          f.TranscribeAttemptedAt,
	}
}
