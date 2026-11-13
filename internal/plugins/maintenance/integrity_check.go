// file: internal/plugins/maintenance/integrity_check.go
// version: 1.6.0
// guid: 7f4a2b3c-9d1e-4f6a-8b5c-2e0d1f3a4b5c
// last-edited: 2026-11-13

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/falkcorp/mediacore/tasks"
)

// integrityCheckKind is rule R3: the native kind of maintenance.file-integrity-check.
const integrityCheckKind tasks.Kind = "books.maintenance.file-integrity-check"

// integrityCheckDef registers the maintenance.file-integrity-check
// OperationDef. It runs nightly during the maintenance window (02:30 daily),
// between orphan-book-files-cleanup (02:15) and purge-deleted (03:00), without
// competing for the same minute.
func (p *Plugin) integrityCheckDef() sdk.OperationDef {
	sched := "30 2 * * *" // 02:30 daily — nightly maintenance window
	return sdk.OperationDef{
		ID:              "maintenance.file-integrity-check",
		Liveness:        sdk.LivenessManual,
		Plugin:          "maintenance",
		DisplayName:     "File integrity check",
		Description:     "Flags book_file rows where file_hash differs from original_file_hash with no AO tag-write on record (post_metadata_hash empty) — a candidate for external modification or bit-rot. Report-only; takes no action.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityNormal,
		ConcurrencyKey:  "maintenance.file-integrity-check",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         30 * time.Minute,
		Schedule:        &sched,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead},
		// Explicit so the def does not rely on the empty-means-settings.manage
		// default (08-X2); the scheduler now enqueues it nightly.
		Permissions: []auth.Permission{auth.PermSettingsManage},
		Run:         p.runIntegrityCheck,
	}
}

// integrityCheckResult is the counts HEAD logs on completion (rule R2 item 1:
// the core returns what HEAD's body only logged).
type integrityCheckResult struct {
	Flagged    int `json:"flagged_count"`
	TotalFiles int `json:"total_book_files"`
}

// integrityCheckEnv is what the core needs from its engine (rules R2 and R10).
type integrityCheckEnv struct {
	t6g4Base
}

// runIntegrityCheck is the v2 shell around runIntegrityCheckCore (rule R2 item 2).
func (p *Plugin) runIntegrityCheck(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	_, err := p.runIntegrityCheckCore(ctx, struct{}{}, integrityCheckEnv{
		t6g4Base: t6g4HeadBase(reporter, true, headT6G4Store{OpsStore: store, deps: p.deps}, headT6G4Writer{store: store}),
	})
	return err
}

// integrityCheckKind is the native shell (rule R2 item 3): a Task whose Run fills
// the same env from rc. The kind only reads, so it has no writer.
func (p *Plugin) integrityCheckKind(deps t6g4Deps, sched *tasks.Schedule) tasks.Definition {
	return tasks.Task(integrityCheckKind, tasks.TaskSpec[struct{}, integrityCheckResult]{
		Common: tasks.Common{
			Title:    "File integrity check",
			Help:     "Flags book_file rows where file_hash differs from original_file_hash with no AO tag-write on record (post_metadata_hash empty) — a candidate for external modification or bit-rot. Report-only; takes no action.",
			Effects:  tasks.ReadOnly(),
			Perms:    tasks.Permissions{Start: t6g4StartRequirement()},
			Resume:   tasks.ResumeDrop,
			Priority: tasks.PriorityNormal,
			// HEAD's ConcurrencyKey is its own ID: "books." + that ID.
			ConcurrencyKey: tasks.Key("books.maintenance.file-integrity-check"),
			Timeout:        30 * time.Minute,
			Schedule:       sched,
			Ready:          []tasks.ReadySignal{t6g4SignalMemDB},
		},
		Run: func(rc *tasks.RC, _ struct{}) (integrityCheckResult, error) {
			base, err := t6g4NativeBase(rc, deps, false)
			if err != nil {
				return integrityCheckResult{}, err
			}
			return p.runIntegrityCheckCore(rc.Context(), struct{}{}, integrityCheckEnv{t6g4Base: base})
		},
	})
}

// runIntegrityCheckCore is HEAD's body (rule R2 item 1): it reads book_file rows
// through env.Store and writes nothing.
func (p *Plugin) runIntegrityCheckCore(ctx context.Context, _ struct{}, env integrityCheckEnv) (integrityCheckResult, error) {
	rep := env.reporter(ctx)
	_ = rep.Log(slog.LevelInfo, "Starting file integrity scan")
	scanProg := sdk.NewProgress(rep, 0)
	scanProg.Start("Scanning book_files for hash mismatches...")

	flagged, totalFiles, err := findIntegrityMismatches(ctx, env.Store)
	if err != nil {
		return integrityCheckResult{}, fmt.Errorf("scan failed: %w", err)
	}

	_ = rep.Log(slog.LevelInfo, "File integrity scan complete",
		slog.Int("flagged_count", len(flagged)),
		slog.Int("total_book_files", totalFiles),
	)

	msg := fmt.Sprintf("File integrity check: %d file(s) flagged out of %d scanned (report-only)",
		len(flagged), totalFiles)
	_ = rep.Log(slog.LevelInfo, msg)
	scanProg.Done(msg)
	return integrityCheckResult{Flagged: len(flagged), TotalFiles: totalFiles}, nil
}

// findIntegrityMismatches returns every BookFile whose current FileHash
// differs from its OriginalFileHash with no AO tag-write on record (i.e.
// PostMetadataHash is empty). Rows with no baseline (empty OriginalFileHash)
// or whose drift is explained by an AO-caused tag write (non-empty
// PostMetadataHash) are not flagged.
//
// It compares like with like: only rows whose OriginalFileHashKind is
// database.FileHashKindSampled — the same digest FileHash holds — are checked.
// Legacy rows carry an OriginalFileHash of unknown kind (a tag write used to
// store a whole-file SHA-256 there, which differs from the sampled digest of
// the very same bytes for any large file), so comparing them would report a
// change that never happened. They are skipped until the next scan replaces
// the value with a sampled digest of known kind.
//
// This is the testable core of runIntegrityCheck. It is purely a scan — it
// never calls any store write or delete method.
func findIntegrityMismatches(ctx context.Context, store bookFileCoreScanner) (flagged []database.BookFileCore, totalFiles int, err error) {
	if ctx.Err() != nil {
		return nil, 0, ctx.Err()
	}
	files, ferr := store.GetAllBookFilesCore()
	if ferr != nil {
		return nil, 0, fmt.Errorf("GetAllBookFilesCore: %w", ferr)
	}
	flagged = make([]database.BookFileCore, 0)
	for _, f := range files {
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		if f.FileHash != "" && f.OriginalFileHash != "" &&
			f.OriginalFileHashKind == database.FileHashKindSampled &&
			f.FileHash != f.OriginalFileHash && f.PostMetadataHash == "" {
			flagged = append(flagged, f)
		}
	}
	return flagged, len(files), nil
}
