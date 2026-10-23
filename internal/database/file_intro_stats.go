// file: internal/database/file_intro_stats.go
// version: 1.0.0
// guid: b4d7e2a9-6c15-4f38-8e0b-5a1d9c3f7e62
// last-edited: 2026-10-22

package database

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// statsTranscribeFilesKey holds the live counters for the most recent (or
// in-flight) maintenance.transcribe-file-intros run. It is deliberately a
// different key from statsTranscribeKey: that one is the per-BOOK run's
// aggregate, owned by the book op and read by scripts/transcribe_monitor.py,
// and the two ops count different things (books vs book_file rows) and can run
// in the same week.
const statsTranscribeFilesKey = "stats:transcribe-files"

// FileIntroStats is the live aggregate for a per-file intro run. Every count is
// in book_file rows, the grain the op works at and the grain coverage is
// reported at. Persisted as JSON under statsTranscribeFilesKey and served by
// GET /api/v1/maintenance/file-intro-stats.
type FileIntroStats struct {
	RunOpID   string    `json:"run_op_id"`
	StartedAt time.Time `json:"started_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Done      bool      `json:"done"`
	DryRun    bool      `json:"dry_run"`

	// Coverage at the book_file grain. Eligible is every file the op would
	// transcribe in a full run (present, with a hash and a supported format);
	// TranscribedAtStart is how many of those already had a result when this
	// run began; TranscribedNow is the same count refreshed as results land.
	Eligible           int `json:"eligible"`
	TranscribedAtStart int `json:"transcribed_at_start"`
	TranscribedNow     int `json:"transcribed_now"`

	// Selected is the number of files this run set out to work on: Eligible
	// minus those already done (under only_missing). At the end of a run that
	// reached every batch, Attempted + Reused + Deferred + Stale == Selected.
	Selected int `json:"selected"`

	// Outcomes. Attempted is the sum of the outcome buckets below it.
	Attempted     int `json:"attempted"`
	OK            int `json:"ok"`
	Unparsed      int `json:"unparsed"`
	Silence       int `json:"silence"`
	WhisperError  int `json:"whisper_error"`
	SourceMissing int `json:"source_file_missing"`
	// Reused counts files that took a transcript from another file with the
	// same FileHash instead of being sent to whisper.
	Reused int `json:"reused"`
	// Deferred counts files that got no outcome because the endpoint was
	// unreachable; the next only_missing run picks them up.
	Deferred int `json:"deferred"`
	// Stale counts results dropped because the row's hash changed or the row
	// vanished between selection and write.
	Stale int `json:"stale"`
	// BookFills counts empty book transcripts filled from an ordinal-0 file.
	BookFills int `json:"book_fills"`

	// Files left out of Eligible, by reason.
	SkippedNoHash      int `json:"skipped_no_hash"`
	SkippedMissing     int `json:"skipped_missing"`
	SkippedUnsupported int `json:"skipped_unsupported"`
	// SkippedFailed counts files held back by a durable failure status that
	// only retry_failed re-attempts.
	SkippedFailed int `json:"skipped_failed"`
}

// PutFileIntroStats writes the counters. Sync, like PutTranscribeStats, so a
// monitor polling across a restart still sees the last-known state.
func (p *PebbleStore) PutFileIntroStats(s *FileIntroStats) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := p.db.Set([]byte(statsTranscribeFilesKey), data, pebble.Sync); err != nil {
		slog.Error("pebble Set stats:transcribe-files", "error", err)
		return err
	}
	return nil
}

// GetFileIntroStats reads the counters; (nil, nil) means no run ever wrote
// them.
func (p *PebbleStore) GetFileIntroStats() (*FileIntroStats, error) {
	val, closer, err := p.db.Get([]byte(statsTranscribeFilesKey))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	var s FileIntroStats
	if err := json.Unmarshal(val, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// FileIntroStatsStore is the capability the op and the stats handler resolve
// with AsCapability.
type FileIntroStatsStore interface {
	PutFileIntroStats(*FileIntroStats) error
	GetFileIntroStats() (*FileIntroStats, error)
}
