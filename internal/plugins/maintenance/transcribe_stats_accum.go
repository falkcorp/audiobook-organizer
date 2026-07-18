// file: internal/plugins/maintenance/transcribe_stats_accum.go
// version: 1.1.1
// guid: 9d3b1e57-6a02-4c8f-b14d-7e9a2f5c08b1
// last-edited: 2026-07-18

package maintenance

import (
	"log/slog"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Per-book transcription outcome categories. These are written to
// Book.TranscribeStatus and counted into the stats:transcribe aggregate.
const (
	statusOK = "ok"
	// statusUnparsed: Whisper returned non-empty text but ParseAudiobookIntro
	// extracted no title. The raw transcript is still stored; only the parsed
	// fields are absent. Distinct from statusOK so the aggregate and the
	// OnlyParsedTranscription filter can exclude these low-quality results.
	statusUnparsed      = "unparsed"
	statusSourceMissing = "source_file_missing"
	statusNoAudio       = "no_audio"
	statusFFmpegError   = "ffmpeg_error"
	statusWhisperError  = "whisper_error"
	statusEmpty         = "empty"
	statusExtracted     = "extracted" // extract-only mode: WAV cached, not transcribed
)

// transcribeStatsAccum is a thread-safe accumulator for the live transcription
// aggregate. The 4 concurrent page goroutines all record outcomes into it; the
// op flushes it to PebbleDB (via sink) after each page so a monitor can read
// one key. sink may be nil when the store does not implement
// database.TranscribeStatsStore — in that case the accumulator still tracks
// counts in memory (used for the op's final log line) but never persists.
type transcribeStatsAccum struct {
	mu    sync.Mutex
	stats database.TranscribeStats
	sink  database.TranscribeStatsStore
}

func newTranscribeStatsAccum(sink database.TranscribeStatsStore, opID string, totalBooks int, startedAt time.Time) *transcribeStatsAccum {
	return &transcribeStatsAccum{
		sink: sink,
		stats: database.TranscribeStats{
			RunOpID:    opID,
			StartedAt:  startedAt,
			UpdatedAt:  startedAt,
			TotalBooks: totalBooks,
		},
	}
}

// recordOutcome increments the bucket for one attempted book's status.
func (a *transcribeStatsAccum) recordOutcome(status string, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stats.Attempted++
	switch status {
	case statusOK:
		a.stats.OK++
	case statusUnparsed:
		a.stats.Unparsed++
	case statusSourceMissing:
		a.stats.SourceMissing++
	case statusNoAudio:
		a.stats.NoAudio++
	case statusFFmpegError:
		a.stats.FFmpegError++
	case statusWhisperError:
		a.stats.WhisperError++
	case statusEmpty:
		a.stats.Empty++
	case statusExtracted:
		a.stats.Extracted++
	}
	a.stats.UpdatedAt = now
}

// recordSkipped counts books skipped because they already have a transcript
// (only_missing mode) and were never attempted this run.
func (a *transcribeStatsAccum) recordSkipped(n int) {
	a.mu.Lock()
	a.stats.SkippedExisting += n
	a.mu.Unlock()
}

// recordCacheHits counts WAV clips served from the on-disk cache (no ffmpeg).
func (a *transcribeStatsAccum) recordCacheHits(n int) {
	if n == 0 {
		return
	}
	a.mu.Lock()
	a.stats.CacheHits += n
	a.mu.Unlock()
}

// flush persists the current counters. Safe to call concurrently. No-op when
// sink is nil. markDone=true stamps the run as finished.
func (a *transcribeStatsAccum) flush(markDone bool) {
	if a.sink == nil {
		return
	}
	a.mu.Lock()
	if markDone {
		a.stats.Done = true
	}
	snapshot := a.stats // copy under lock
	a.mu.Unlock()
	// M1 (2026-07 error-correction sweep): a persist failure here used to be
	// swallowed with `_ =`. flush is called after every page, so a
	// transiently failing sink previously left the live-monitor aggregate
	// silently stale with no signal that anything was wrong.
	if err := a.sink.PutTranscribeStats(&snapshot); err != nil {
		slog.Warn("transcribe stats: persist failed — live monitor may show stale counts",
			"run_op_id", snapshot.RunOpID, "attempted", snapshot.Attempted, "err", err)
	}
}

// snapshot returns a copy of the current counters for logging.
func (a *transcribeStatsAccum) snapshot() database.TranscribeStats {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stats
}
