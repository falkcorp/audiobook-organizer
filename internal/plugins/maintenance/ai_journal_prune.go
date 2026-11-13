// file: internal/plugins/maintenance/ai_journal_prune.go
// version: 1.2.0
// guid: d906856e-dcfc-4b03-a4ad-ca561f540095
// last-edited: 2026-11-13

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
	"github.com/falkcorp/mediacore/tasks"
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
	var params PruneAIJournalParams
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("prune-ai-journal: bad params: %w", err)
		}
	}
	env := pruneAIJournalEnv{
		t6g1Env:     t6g1HeadEnv(reporter, true),
		DefaultDays: p.deps.AIJournalRetentionDays(),
		Writer:      t6g1HeadWriter{p.deps.OpsStore()},
	}
	res, err := pruneAIJournalCore(ctx, params, env)
	var setErr error
	if res != nil {
		setErr = opsregistry.ReporterSetResult(reporter, *res)
	}
	if err != nil {
		return err
	}
	if setErr != nil && !res.Skipped {
		return fmt.Errorf("prune-ai-journal: persist result: %w", setErr)
	}
	return setErr
}

// pruneAIJournalKind is rule R3's kind for the def above.
const pruneAIJournalKind = tasks.Kind("books." + PruneAIJournalDefID)

// pruneAIJournalWriter is the one write the op makes: pruning one journal kind.
type pruneAIJournalWriter interface {
	// AIJournalPruner opens the journal of one kind. A construction error is
	// distinct from a prune error: the op stops on the first and carries on past
	// the second.
	AIJournalPruner(kind string) (t6g1JournalPruner, error)
}

// t6g1JournalPruner is resultjournal.Journal's Prune.
type t6g1JournalPruner interface {
	Prune(ctx context.Context, maxAge time.Duration) (int, error)
}

// pruneAIJournalEnv is what the core takes from its shell.
type pruneAIJournalEnv struct {
	t6g1Env
	// DefaultDays is ai_journal_retention_days, the retention when the params
	// carry none.
	DefaultDays int
	Writer      pruneAIJournalWriter
}

// pruneAIJournalParallelism is the op's concurrency: one journal kind at a
// time (HEAD declared none).
func pruneAIJournalParallelism() tasks.Parallelism {
	return tasks.Sequential("HEAD declared no concurrency (sequential)")
}

// pruneAIJournalCore is the body of runPruneAIJournal. The result is nil when
// the run set none (a validation failure or a construction error); it is set
// before a prune error is returned, as at HEAD.
func pruneAIJournalCore(ctx context.Context, params PruneAIJournalParams, env pruneAIJournalEnv) (*PruneAIJournalResult, error) {
	days := env.DefaultDays
	if params.RetentionDays != nil {
		days = *params.RetentionDays
	}
	if days < 0 || days > config.MaxAIJournalRetentionDays {
		return nil, fmt.Errorf("prune-ai-journal: retention_days must be between 0 and %d, got %d",
			config.MaxAIJournalRetentionDays, days)
	}
	if days == 0 {
		env.Log(slog.LevelInfo, "AI journal retention is 0 (keep forever); nothing pruned")
		return &PruneAIJournalResult{Skipped: true}, nil
	}

	// EVERY kind, not just whisper's. This walked one hardcoded kind while
	// whisper was the only one; a second kind (llm.filename_parse) would then
	// have grown forever -- one entry per distinct filename in the library --
	// with nothing reporting a problem. resultjournal.AllKinds() is the list,
	// so a kind added there is pruned by construction.
	kinds := resultjournal.AllKinds()
	deleted := 0
	var firstErr error
	for i, kind := range kinds {
		j, err := env.Writer.AIJournalPruner(kind)
		if err != nil {
			return nil, fmt.Errorf("prune-ai-journal: %s: %w", kind, err)
		}
		env.Progress(i, len(kinds),
			fmt.Sprintf("Pruning %s journal entries older than %d day(s)", kind, days))
		n, perr := j.Prune(ctx, time.Duration(days)*24*time.Hour)
		deleted += n
		if perr != nil {
			// Keep going: one unprunable kind must not leave the others
			// unpruned. The first error is returned once every kind has been
			// attempted, with the count actually deleted.
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", kind, perr)
			}
		}
	}
	res := &PruneAIJournalResult{RetentionDays: days, Deleted: deleted}
	if firstErr != nil {
		return res, fmt.Errorf("prune-ai-journal: deleted %d before failing: %w", deleted, firstErr)
	}
	msg := fmt.Sprintf("Pruned %d AI journal entr(ies) across %d kind(s) older than %d day(s)", deleted, len(kinds), days)
	env.Progress(len(kinds), len(kinds), msg)
	env.Log(slog.LevelInfo, msg)
	return res, nil
}

// pruneAIJournalNative is the native kind. The op writes the journal and
// nothing else, and HEAD declared no Preview, so a Preview run is refused.
func (p *Plugin) pruneAIJournalNative(deps t6g1Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := t6g1Common(pruneAIJournalKind, sched)
	c.Title = "Prune AI result journal"
	c.Help = "Deletes journalled whisper transcription results older than ai_journal_retention_days (default 30; 0 keeps them forever). Transcripts on books are not touched."
	c.Effects = tasks.Writes(tasks.Res("books", "aux_stores"))
	c.Resume = tasks.ResumeDrop
	c.Priority = tasks.PriorityLow
	c.ConcurrencyKey = tasks.Key("books." + PruneAIJournalDefID)
	c.Timeout = time.Hour
	c.ProgressTimeout = time.Hour
	return tasks.Task(pruneAIJournalKind, tasks.TaskSpec[PruneAIJournalParams, PruneAIJournalResult]{
		Common: c,
		Run: func(rc *tasks.RC, params PruneAIJournalParams) (PruneAIJournalResult, error) {
			if rc.Mode() != tasks.ModeLive {
				return PruneAIJournalResult{}, fmt.Errorf("%s: preview not supported; start with mode live", pruneAIJournalKind)
			}
			env := pruneAIJournalEnv{
				t6g1Env:     t6g1NativeEnv(rc),
				DefaultDays: deps.AIJournalRetentionDays(),
				Writer:      deps.NewWriters(rc.Writer()).PruneAIJournal,
			}
			res, err := pruneAIJournalCore(rc.Context(), params, env)
			if res == nil {
				res = &PruneAIJournalResult{}
			}
			return *res, err
		},
	})
}
