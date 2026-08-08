// file: internal/plugins/maintenance/repair_transcribe_status.go
// version: 1.1.0
// guid: a5e3c81f-7204-4b96-9d3a-1f68b05e2c47
// last-edited: 2026-08-07

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/transcribe"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// Repairs transcribe_status rows that record a TRANSPORT failure rather than a
// transcription failure.
//
// ── What happened ────────────────────────────────────────────────────────────
//
// On 2026-07-01 the remote Whisper server was unreachable for a full day. Every
// batch that ran that day failed at the HTTP layer, and processTranscribePage's
// whole-batch error path marks EVERY book in the page whisper_error:
//
//	batchResults, err := transcribe.TranscribeBatch(...)
//	if err != nil {
//	    for bookID := range batchJobs { applyOutcome(..., statusWhisperError, ...) }
//	}
//
// Measured on prod 2026-08-07: 76.7% of a 300-book random sample carried
// whisper_error, and 229 of those 230 books HAD good transcript text — dated
// 2026-06-27, four days BEFORE the failure. Across a 400-book sample the
// failures clustered into 17 distinct timestamps, every one of them on
// 2026-07-01, every error a connection failure to the transcription endpoint.
//
// So the transcripts were never damaged: applyOutcome correctly refuses to
// overwrite good text with nothing. Only the STATUS is wrong. The library
// degraded into "everything looks broken while everything is fine" — the worst
// possible state for any query that filters on status, including the tiered
// backfill's "what still needs work" query.
//
// ── The principle ────────────────────────────────────────────────────────────
//
// 🔴 An unreachable endpoint is NO ATTEMPT MADE, not a failed attempt. It is
// absence of evidence about the file, and recording it as a per-file failure
// attributes to the file a problem that belonged to the network. This is the
// same rule the intro classifier applies one layer up, where an absent
// transcript yields "unknown" rather than "prose".
//
// ── What this op does ────────────────────────────────────────────────────────
//
// For rows whose stored error is a TRANSPORT error (not a model/codec error):
//
//	text present, not [SILENCE]  -> recompute status from the text itself
//	                                (credits -> ok, otherwise -> unparsed)
//	no text                      -> clear status and error entirely, back to
//	                                never-attempted
//	[SILENCE] sentinel           -> left alone; that is a real terminal state
//
// It never calls Whisper and never touches transcript text. Rows whose error is
// a genuine transcription failure (a model crash on one file, an ffmpeg codec
// error) are deliberately left alone — those are honest records.

type repairStatusParams struct {
	// DryRun reports what WOULD change without writing. Defaults to TRUE.
	DryRun *bool `json:"dry_run,omitempty"`
	// LastBookID resumes past a prior run's checkpoint.
	LastBookID string `json:"last_book_id,omitempty"`
}

const (
	repairRecomputedOK       = "recomputed_ok"
	repairRecomputedUnparsed = "recomputed_unparsed"
	repairClearedNeverTried  = "cleared_to_never_attempted"
	repairWouldChange        = "would_change"
	repairSkipNotFailed      = "skip_status_not_a_failure"
	repairSkipNotTransport   = "skip_genuine_failure_kept"
	repairSkipSilence        = "skip_silence_sentinel"
	repairWriteFailed        = "write_failed"
)

const repairStatusPageSize = 500

// transportErrorMarkers identify a failure that happened BELOW the transcriber:
// the request never reached a working model. Matching is on generic HTTP/network
// wording, never on any host address.
var transportErrorMarkers = []string{
	`post "http`, // Go's http.Post error prefix
	"connection refused",
	"no such host",
	"context deadline exceeded",
	"connection reset",
	"i/o timeout",
	"unexpected eof",
	": eof",
	"dial tcp",
	"server misbehaving",
	"network is unreachable",
}

// isTransportFailure reports whether a stored TranscribeError describes a
// transport problem rather than a transcription problem.
//
// Deliberately conservative: an unrecognised error is treated as a GENUINE
// failure and left alone. Wrongly clearing a real failure hides a broken file;
// wrongly keeping one costs a re-run. The asymmetry favours keeping.
func isTransportFailure(errText string) bool {
	e := strings.ToLower(strings.TrimSpace(errText))
	if e == "" {
		return false
	}
	for _, m := range transportErrorMarkers {
		if strings.Contains(e, m) {
			return true
		}
	}
	return false
}

// repairVerdict is the decision for one book, computed without any I/O.
type repairVerdict struct {
	Reason string
	// NewStatus is the status to store; nil means CLEAR the field.
	NewStatus *string
	// Write is true when the row should be updated.
	Write bool
}

// classifyStatusRepair decides what to do with one book's transcription status.
// Pure: no store, no clock, so the rules are directly testable.
func classifyStatusRepair(b database.Book) repairVerdict {
	status := ""
	if b.TranscribeStatus != nil {
		status = *b.TranscribeStatus
	}
	switch status {
	case statusWhisperError, statusFFmpegError, statusSourceMissing, statusNoAudio, statusEmpty:
		// candidate — fall through
	default:
		// ok / unparsed / unset: nothing to repair.
		return repairVerdict{Reason: repairSkipNotFailed}
	}

	errText := ""
	if b.TranscribeError != nil {
		errText = *b.TranscribeError
	}
	if !isTransportFailure(errText) {
		// A real transcription failure. Honest record; keep it.
		return repairVerdict{Reason: repairSkipNotTransport}
	}

	text := ""
	if b.IntroTranscription != nil {
		text = strings.TrimSpace(*b.IntroTranscription)
	}

	// The sentinel is a REAL terminal state (every retry returned zero chars),
	// not a transport artifact. Clearing it would put the book back in the queue
	// forever at GPU cost.
	if text == transcribe.SilenceSentinel {
		return repairVerdict{Reason: repairSkipSilence}
	}

	if text == "" {
		// No text and the endpoint was down: we genuinely do not know anything
		// about this file. Clear back to never-attempted rather than assert a
		// failure that belonged to the network.
		return repairVerdict{Reason: repairClearedNeverTried, NewStatus: nil, Write: true}
	}

	// Text survived the outage. Derive the truthful status from the text itself.
	c := transcribe.ClassifyIntro(text, transcribe.UnknownPosition)
	if c.Kind == transcribe.IntroKindCredits {
		s := statusOK
		return repairVerdict{Reason: repairRecomputedOK, NewStatus: &s, Write: true}
	}
	s := statusUnparsed
	return repairVerdict{Reason: repairRecomputedUnparsed, NewStatus: &s, Write: true}
}

func (p *Plugin) repairTranscribeStatusDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.repair-transcribe-status",
		Plugin:          "maintenance",
		DisplayName:     "Repair transcribe status after a transport outage",
		Description:     "Fixes transcribe_status rows that record a TRANSPORT failure (the Whisper endpoint was unreachable) rather than a transcription failure. A day-long outage on 2026-07-01 marked ~77% of the library whisper_error while their transcripts, dated four days earlier, survived intact. This recomputes the status from the stored text (credits -> ok, else unparsed), or clears it back to never-attempted where there is no text. Never calls Whisper, never touches transcript text, and leaves genuine failures and the [SILENCE] sentinel alone. Defaults to dry_run=true.",
		ResumePolicy:    sdk.ResumeRestart,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.repair-transcribe-status",
		// GetBookByID→mutate→UpdateBook whole-row write-back on Book rows.
		Writes:          []sdk.Resource{sdk.ResBooks},
		Cancellable:     true,
		Isolate:         false,
		Timeout:         2 * time.Hour,
		ProgressTimeout: 5 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runRepairTranscribeStatus,
	}
}

func (p *Plugin) runRepairTranscribeStatus(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	store := p.deps.Store()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}

	var params repairStatusParams
	if len(rawParams) > 0 {
		_ = json.Unmarshal(rawParams, &params)
	}
	dryRun := params.DryRun == nil || *params.DryRun
	log := reporter.Logger()

	allIDs, err := store.ListBookIDs()
	if err != nil {
		return fmt.Errorf("list book ids: %w", err)
	}
	startIdx := 0
	if params.LastBookID != "" {
		for i, id := range allIDs {
			if id == params.LastBookID {
				startIdx = i + 1
				break
			}
		}
	}
	total := len(allIDs)
	log.Info("repair-transcribe-status: starting", "dry_run", dryRun, "total_books", total)
	if startIdx >= total {
		_ = reporter.UpdateProgress(1, 1, "Done — nothing to repair")
		return nil
	}

	var mu sync.Mutex
	counts := map[string]int{}

	// Pages partition books into DISJOINT sets so no two workers touch the same
	// row — UpdateBook is a read-modify-write with no store-level lock.
	pages := chunkIDs(allIDs[startIdx:], repairStatusPageSize)
	err = registry.RunItems(ctx, reporter, pages, func(ctx context.Context, ids []string) error {
		local := map[string]int{}
		for _, id := range ids {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			b, gerr := store.GetBookByID(id)
			if gerr != nil || b == nil {
				continue
			}
			v := classifyStatusRepair(*b)
			if !v.Write {
				local[v.Reason]++
				continue
			}
			if dryRun {
				local[repairWouldChange]++
				local[v.Reason]++
				continue
			}
			// Only the two status fields change. GetBookByID returns the full
			// row, so writing it back preserves everything else — and critically
			// this op never assigns IntroTranscription, so the transcript cannot
			// be disturbed by a repair.
			b.TranscribeStatus = v.NewStatus
			b.TranscribeError = nil
			if _, uerr := store.UpdateBook(b.ID, b); uerr != nil {
				log.Warn("repair-transcribe-status: update failed", "book_id", b.ID, "err", uerr)
				local[repairWriteFailed]++
				continue
			}
			local[v.Reason]++
		}
		mu.Lock()
		for k, n := range local {
			counts[k] += n
		}
		mu.Unlock()
		return nil
	}, registry.RunItemsOptions{
		Concurrency:   8,
		ProgressTotal: len(pages),
		Label:         func(i, t int) string { return fmt.Sprintf("Page %d/%d", i+1, t) },
	})

	mu.Lock()
	snap := make(map[string]int, len(counts))
	for k, v := range counts {
		snap[k] = v
	}
	mu.Unlock()

	log.Info("repair-transcribe-status: complete",
		"dry_run", dryRun,
		"recomputed_ok", snap[repairRecomputedOK],
		"recomputed_unparsed", snap[repairRecomputedUnparsed],
		"cleared_to_never_attempted", snap[repairClearedNeverTried],
		"skip_status_not_a_failure", snap[repairSkipNotFailed],
		"skip_genuine_failure_kept", snap[repairSkipNotTransport],
		"skip_silence_sentinel", snap[repairSkipSilence],
		"write_failed", snap[repairWriteFailed],
		"total_books", total)

	changed := snap[repairRecomputedOK] + snap[repairRecomputedUnparsed] + snap[repairClearedNeverTried]
	verb := "Would repair"
	if !dryRun {
		verb = "Repaired"
	}
	_ = reporter.UpdateProgress(1, 1, fmt.Sprintf(
		"%s %d books — %d ok, %d unparsed, %d cleared; kept %d genuine failures, %d silence (of %d)",
		verb, changed, snap[repairRecomputedOK], snap[repairRecomputedUnparsed],
		snap[repairClearedNeverTried], snap[repairSkipNotTransport], snap[repairSkipSilence], total))
	return err
}
