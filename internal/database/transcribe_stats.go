// file: internal/database/transcribe_stats.go
// version: 1.3.0
// guid: 7f2a9c14-3b6d-4e81-9a05-2c8e1d4f6b73
// last-edited: 2026-09-05

package database

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// statsTranscribeKey is the single PebbleDB key holding the live aggregate
// counters for the most recent (or in-flight) transcription run. It is updated
// incrementally by maintenance.transcribe-book-intros after each page so an
// external monitor can read one key instead of scanning 48K book records or
// scraping ephemeral op logs.
const statsTranscribeKey = "stats:transcribe"

// TranscribeStats is the live aggregate for a transcription run. Counts are
// cumulative across the run identified by RunOpID. Persisted as JSON under
// statsTranscribeKey. A monitor reads it via GET /api/v1/maintenance/transcribe-stats.
type TranscribeStats struct {
	RunOpID   string    `json:"run_op_id"`  // op that produced these counters
	StartedAt time.Time `json:"started_at"` // when the run began
	UpdatedAt time.Time `json:"updated_at"` // last counter write
	Done      bool      `json:"done"`       // run finished (success or error)
	// TotalBooks is the number of books this run set out to attempt — the
	// denominator of every progress line. Under only_missing (the default)
	// that is the books WITHOUT a transcript at run start, not the library
	// size: books already transcribed are counted in SkippedExisting and are
	// never part of TotalBooks. At the end of a run that reached every page,
	// Attempted + Deferred == TotalBooks.
	TotalBooks int `json:"total_books"`

	// Per-outcome cumulative counts. Attempted = sum of the outcome buckets
	// below; SkippedExisting is books skipped because they already had a
	// transcript (only_missing mode) and were never attempted this run.
	Attempted int `json:"attempted"`
	OK        int `json:"ok"`
	// Unparsed counts books where Whisper returned non-empty text but
	// ParseAudiobookIntro extracted no title. The raw transcript IS stored
	// (usable via reparse_only), but TranscribedTitle stays empty, so these
	// are excluded by FilterSpec.OnlyParsedTranscription. OK now implies a
	// parsed title; Unparsed is the low-quality remainder.
	Unparsed        int `json:"unparsed"`
	SourceMissing   int `json:"source_file_missing"`
	NoAudio         int `json:"no_audio"`
	FFmpegError     int `json:"ffmpeg_error"`
	WhisperError    int `json:"whisper_error"`
	Empty           int `json:"empty"`
	SkippedExisting int `json:"skipped_existing"`
	// Deferred counts books inside TotalBooks that got no outcome this run:
	// an endpoint outage (the whole page is retried next run rather than
	// marked whisper_error), a page whose temp dir could not be created, or a
	// row that was readable at selection but not at page time. They still
	// lack a transcript and the next only_missing run picks them up.
	Deferred int `json:"deferred"`
	// Unreadable counts books the index listed that the selection could not
	// read — no row behind the id, or a store error. Outside TotalBooks.
	Unreadable int `json:"unreadable"`
	CacheHits  int `json:"cache_hits"`
	// Extracted counts books whose WAV clip was (re)built in extract-only mode —
	// cache rebuilt without invoking the GPU. Zero in normal transcription runs.
	Extracted int `json:"extracted"`
}

// PutTranscribeStats writes the aggregate counters. Sync is intentional so a
// monitor polling across a server restart still sees the last-known state.
func (p *PebbleStore) PutTranscribeStats(s *TranscribeStats) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := p.db.Set([]byte(statsTranscribeKey), data, pebble.Sync); err != nil {
		slog.Error("pebble Set stats:transcribe", "error", err)
		return err
	}
	return nil
}

// GetTranscribeStats reads the aggregate counters. Returns (nil, nil) when no
// run has ever written them (key absent) so the caller can report "never run".
func (p *PebbleStore) GetTranscribeStats() (*TranscribeStats, error) {
	val, closer, err := p.db.Get([]byte(statsTranscribeKey))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	var s TranscribeStats
	if err := json.Unmarshal(val, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// TranscribeStatsStore is the narrow capability interface satisfied by
// *PebbleStore. The op and HTTP handler type-assert the concrete store to this
// (mirroring the backup.Checkpointable pattern) so the store-wide Store
// interface stays untouched.
type TranscribeStatsStore interface {
	PutTranscribeStats(*TranscribeStats) error
	GetTranscribeStats() (*TranscribeStats, error)
}
