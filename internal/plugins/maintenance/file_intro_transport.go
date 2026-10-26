// file: internal/plugins/maintenance/file_intro_transport.go
// version: 1.3.0
// guid: e17c4a92-5b08-4d63-a1f7-92d3b6c80e45
// last-edited: 2026-10-26

package maintenance

import (
	"context"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/transcribe"
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
	// AvoidWorkers names the machines that already struck this audio with a
	// decoder timeout (their opaque identities, joined with
	// database.IntroTimeoutWorkerSeparator). The pool sends the job to another
	// machine when one is healthy, since only another machine's strike can
	// settle the file (transcribe.BatchOptions.AvoidWorkers). Empty when none.
	AvoidWorkers string
}

// fileIntroWhisperResult is the worker's answer for one job. Model and
// Language are empty when the transport does not report them.
type fileIntroWhisperResult struct {
	Text     string
	Error    string // non-empty: this file failed (undecodable, unreadable by the worker, ...)
	Model    string
	Language string
	// Condemned is the dispatcher's own verdict (the file alone left several
	// workers stuck), carried from transcribe.BatchResult: the one kind of
	// timeout that is still struck when the dispatch it came from failed.
	Condemned bool
	// Endpoint is the URL of the endpoint that gave the answer, for logs only
	// (transcribe.BatchResult.Endpoint): an internal address, never stored.
	Endpoint string
	// Worker is the opaque identity of the machine that gave the answer
	// (transcribe.BatchResult.Worker), stored with a timeout strike: a later
	// strike must come from a machine that has not struck the file.
	Worker string
}

// fileIntroWhisperFn sends jobs (keyed by an id the op chooses) to the whisper
// pool and returns one result per key. An endpoint-level outage is returned as
// a *transcribe.TransportError so the op defers instead of failing files. On an
// error the returned map may still hold the results that settled before it
// (nil when none did); a key missing from it was not answered.
// Production uses transcribeFilesByPath; tests substitute a stub.
var fileIntroWhisperFn = transcribeFilesByPath

// transcribeFilesByPath is the production transport: transcribe.TranscribeByPathBatch,
// which runs through the same endpoint pool as the book op (capability gate,
// priority spill, cooldown, per-endpoint in-flight caps) restricted to workers
// that advertise the path pipeline with every mapped root readable. It fails
// closed: with no such worker the error is a TransportError and nothing is
// decoded on the server.
//
// The op does not pass a result journal. Its resume state is the done marker
// on each file row, written right after each batch returns, so a kill loses at
// most the batches in flight.
func transcribeFilesByPath(ctx context.Context, jobs map[string]fileIntroJob, onProgress func(done, total int)) (map[string]fileIntroWhisperResult, error) {
	pj, avoid := fileIntroPathJobs(jobs)
	res, err := transcribe.TranscribeByPathBatch(ctx, pj, transcribe.BatchOptions{OnProgress: onProgress, AvoidWorkers: avoid})
	// The results that settled before a failure are converted and returned WITH
	// the error: the caller applies them and defers the rest, instead of
	// throwing away clips the workers already finished.
	return fileIntroResultsFromBatch(res), err
}

// fileIntroPathJobs converts the op's jobs to the transport's, with the
// machines each job's file was already struck by (nil when none was).
func fileIntroPathJobs(jobs map[string]fileIntroJob) (map[string]transcribe.PathJob, map[string]string) {
	pj := make(map[string]transcribe.PathJob, len(jobs))
	var avoid map[string]string
	for k, j := range jobs {
		pj[k] = transcribe.PathJob{Path: j.Path, Seconds: j.Seconds}
		if j.AvoidWorkers != "" {
			if avoid == nil {
				avoid = make(map[string]string)
			}
			avoid[k] = j.AvoidWorkers
		}
	}
	return pj, avoid
}

// fileIntroResultsFromBatch converts the transport's results to the op's type,
// nil when there are none (an error return that answered nothing).
func fileIntroResultsFromBatch(res map[string]transcribe.BatchResult) map[string]fileIntroWhisperResult {
	if len(res) == 0 {
		return nil
	}
	out := make(map[string]fileIntroWhisperResult, len(res))
	for k, r := range res {
		// BatchResult carries no model or language; IntroMeta records them when
		// the transport starts reporting them.
		out[k] = fileIntroWhisperResult{Text: r.Text, Error: r.Error, Condemned: r.Condemned, Endpoint: r.Endpoint, Worker: r.Worker}
	}
	return out
}

// fileIntroPreflight reports whether any configured worker can take path jobs
// at all, with the per-endpoint reason when none can. A live run checks it once
// up front so a run against workers that predate the path pipeline (or whose
// mount map is unset) stops with the reason instead of deferring every batch.
var fileIntroPreflight = func(ctx context.Context) error {
	if ok, reason := transcribe.PathPipelineAvailable(ctx); !ok {
		return fmt.Errorf("%s: no whisper worker can take path jobs: %s", fileIntroOpID, reason)
	}
	return nil
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
