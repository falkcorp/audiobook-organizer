// file: internal/plugins/maintenance/ai_journal_prune.go
// version: 1.0.0
// guid: d906856e-dcfc-4b03-a4ad-ca561f540095
// last-edited: 2026-09-19

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/ai/resultjournal"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// PruneAIJournalDefID is the def the ai_journal_prune scheduler task enqueues.
const PruneAIJournalDefID = "maintenance.prune-ai-journal"

// PruneAIJournalParams optionally overrides the configured retention for one
// run. Omitted (the scheduler passes nil) means ai_journal_retention_days.
type PruneAIJournalParams struct {
	RetentionDays *int `json:"retention_days,omitempty"`
}

// PruneAIJournalResult is the op's persisted result.
type PruneAIJournalResult struct {
	RetentionDays int  `json:"retention_days"`
	Deleted       int  `json:"deleted"`
	Skipped       bool `json:"skipped"` // retention 0: keep forever, nothing scanned
}

// pruneAIJournalDef deletes whisper results older than the retention window
// from the AI result journal (internal/ai/resultjournal).
//
// WHY. transcribe-book-intros journals one entry (~1-2 KB) per distinct clip it
// transcribes -- up to ~742k -- and nothing ever removed them. An entry is only
// needed to carry a result across a restart or re-run; the transcript itself
// is on the book row, so an expired entry costs at most one re-transcription.
//
// SCOPE. Only the whisper kind ("aijournal:whisper:"). Other aijournal: kinds
// (the batch poller's aijournal:batch:, #3454; future llm results) belong to
// their owners and are never read or deleted here.
//
// MEMORY. Journal.Prune pages through the keyspace (bounded page, one synced
// delete batch per page), so a 742k-entry journal never sits in memory.
//
// SCANS. It does NOT refuse while library.scan runs, matching the other
// housekeeping ops (purge-old-logs, cleanup/nightly-compact-activity-log): it
// touches only the aijournal: keyspace, which no scan reads or writes. The
// scan interlock (refuseWhileLibraryScanActive / the stand-down lease) exists
// for apply paths that mutate book rows a scan races; this op mutates none.
//
// CONCURRENCY. Its own key: the only other writer of these keys is
// transcribe-book-intros, and a race there is harmless -- an entry written
// after the cutoff is fresh and kept; one deleted just before a Lookup is a
// miss and the clip is transcribed again.
func (p *Plugin) pruneAIJournalDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              PruneAIJournalDefID,
		Liveness:        sdk.LivenessNone,
		ProgressTimeout: time.Hour, // LivenessNone requires an explicit budget
		Plugin:          "maintenance",
		DisplayName:     "Prune AI result journal",
		Description:     "Deletes journalled whisper transcription results older than ai_journal_retention_days (default 30; 0 keeps them forever). Transcripts on books are not touched.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  PruneAIJournalDefID,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         time.Hour,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runPruneAIJournal,
	}
}

func (p *Plugin) runPruneAIJournal(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	days := p.deps.AIJournalRetentionDays()
	if len(raw) > 0 && string(raw) != "null" {
		var params PruneAIJournalParams
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("prune-ai-journal: bad params: %w", err)
		}
		if params.RetentionDays != nil {
			days = *params.RetentionDays
		}
	}
	if days < 0 || days > config.MaxAIJournalRetentionDays {
		return fmt.Errorf("prune-ai-journal: retention_days must be between 0 and %d, got %d",
			config.MaxAIJournalRetentionDays, days)
	}
	if days == 0 {
		_ = reporter.Log(slog.LevelInfo, "AI journal retention is 0 (keep forever); nothing pruned")
		return opsregistry.ReporterSetResult(reporter, PruneAIJournalResult{Skipped: true})
	}

	j, err := resultjournal.New(p.deps.OpsStore(), whisperJournalKind)
	if err != nil {
		return fmt.Errorf("prune-ai-journal: %w", err)
	}
	_ = reporter.UpdateProgress(0, 1, fmt.Sprintf("Pruning whisper journal entries older than %d day(s)", days))
	deleted, err := j.Prune(ctx, time.Duration(days)*24*time.Hour)
	setErr := opsregistry.ReporterSetResult(reporter, PruneAIJournalResult{RetentionDays: days, Deleted: deleted})
	if err != nil {
		return fmt.Errorf("prune-ai-journal: deleted %d before failing: %w", deleted, err)
	}
	msg := fmt.Sprintf("Pruned %d whisper journal entr(ies) older than %d day(s)", deleted, days)
	_ = reporter.UpdateProgress(1, 1, msg)
	_ = reporter.Log(slog.LevelInfo, msg)
	if setErr != nil {
		return fmt.Errorf("prune-ai-journal: persist result: %w", setErr)
	}
	return nil
}
