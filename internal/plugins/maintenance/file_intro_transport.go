// file: internal/plugins/maintenance/file_intro_transport.go
// version: 1.0.0
// guid: e17c4a92-5b08-4d63-a1f7-92d3b6c80e45
// last-edited: 2026-10-22

package maintenance

import (
	"context"
	"errors"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// The per-file intro op never decodes audio on the server: it hands each
// file's PATH and a clip length to the whisper worker, which opens the file
// itself (the library is on a share the workers mount), cuts the first
// seconds and transcribes them. This file is the one place that boundary is
// named, so the op's logic is testable without an endpoint and the adapter to
// the transport package is a single swap.

// fileIntroClipSeconds is the clip length for EVERY file, the first track and
// the rest alike, by the owner's decision (2026-10-22): one length, one model,
// so results are comparable across a book's files.
const fileIntroClipSeconds = 90

// fileIntroJob is one file for the worker to read and transcribe.
type fileIntroJob struct {
	Path    string
	Seconds int
}

// fileIntroWhisperResult is the worker's answer for one job. Model and
// Language are empty when the transport does not report them.
type fileIntroWhisperResult struct {
	Text     string
	Error    string // non-empty: this file failed (undecodable, unreadable by the worker, ...)
	Model    string
	Language string
}

// errFilePathTransportMissing is returned by the default transport. A live run
// stops on it rather than recording a failure against every file: it is a
// build problem, not a property of any file.
var errFilePathTransportMissing = errors.New(
	"transcribe-file-intros: this build has no path-based whisper transport (transcribe.TranscribeByPathBatch)")

// fileIntroWhisperFn sends jobs (keyed by an id the op chooses) to the whisper
// pool and returns one result per key. An endpoint-level outage is returned as
// a *transcribe.TransportError so the op defers instead of failing files.
// Production never reassigns it; tests substitute a stub.
var fileIntroWhisperFn = func(ctx context.Context, jobs map[string]fileIntroJob, onProgress func(done, total int)) (map[string]fileIntroWhisperResult, error) {
	return nil, errFilePathTransportMissing
}

// fileIntroSlotCap bounds the number of batches in flight however many
// endpoints are configured.
const fileIntroSlotCap = 32

// fileIntroSlots is how many batches may be in flight: the sum of the
// endpoints' own concurrency (an omitted value counts as one, matching the
// transport's pool), clamped by whisper_max_in_flight when that is set, and
// never above fileIntroSlotCap. Zero means no endpoint is configured.
//
// The transport enforces the per-endpoint limits itself; this only sizes the
// op's worker pool so it neither starves the pool nor queues far past it.
var fileIntroSlots = func() int {
	cfg := config.Snapshot()
	slots := 0
	seen := map[string]bool{}
	for _, e := range cfg.WhisperEndpoints {
		if e.URL == "" || seen[e.URL] {
			continue
		}
		seen[e.URL] = true
		slots += max(e.Concurrency, 1)
	}
	if slots == 0 && cfg.WhisperRemoteURL != "" {
		slots = 1
	}
	if cfg.WhisperMaxInFlight > 0 {
		slots = min(slots, cfg.WhisperMaxInFlight)
	}
	return min(slots, fileIntroSlotCap)
}
