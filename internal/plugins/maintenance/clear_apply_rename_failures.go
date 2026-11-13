// file: internal/plugins/maintenance/clear_apply_rename_failures.go
// version: 1.3.0
// guid: 1c9f4a67-52b8-4d03-ae71-8f605d2c9b34
// last-edited: 2026-11-13

// Clears the metadata apply pipeline's DURABLE RENAME-FAILURE records.
//
// 🔴 WHY THIS EXISTS. The apply write-back records a durable failure for a book
// whose rename was blocked by an unresolvable path collision, and skips that
// book on later runs — that is the fix for "one bad item constantly makes it
// fail" (organizer/apply_failure.go). The record self-heals on its own when the
// blocking file changes, goes away, or the book starts targeting a different
// path, so in the normal case nothing needs to be run.
//
// This op exists for the abnormal case: a MISCLASSIFICATION. If the resolver
// ever records a failure for a reason that turns out to be wrong, the record
// must not be permanent, and an operator must not have to edit a preference
// keyspace by hand to undo it. Dry-run by default; apply=true blanks the
// records so the affected books are attempted again on the next apply.
//
// It never touches a file and never touches a book_file row.
package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/falkcorp/mediacore/tasks"
)

type clearApplyRenameFailuresParams struct {
	// Apply must be explicitly true to clear. Default false = report only.
	Apply bool `json:"apply"`
	// BookIDs scopes the clear to specific books. Empty means every recorded
	// failure. Scoping matters: the common reason to run this is that ONE
	// classification looked wrong, and clearing the whole set would put every
	// genuinely-blocked book back into the retry loop the records exist to
	// break.
	BookIDs []string `json:"book_ids,omitempty"`
}

// clearApplyRenameFailureRow is one record, reported so a dry run tells the
// operator exactly what it would clear and why the book was blocked.
type clearApplyRenameFailureRow struct {
	BookID       string `json:"book_id"`
	TargetPath   string `json:"target_path"`
	OccupantPath string `json:"occupant_path"`
	Reason       string `json:"reason"`
	RecordedAt   string `json:"recorded_at"`
	Cleared      bool   `json:"cleared"`
}

type clearApplyRenameFailuresResult struct {
	Scanned int                          `json:"scanned"`
	Matched int                          `json:"matched"`
	Cleared int                          `json:"cleared"`
	Errors  int                          `json:"errors"`
	Rows    []clearApplyRenameFailureRow `json:"rows"`
}

func (p *Plugin) clearApplyRenameFailuresDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.clear-apply-rename-failures",
		Preview:         sdk.PreviewSpec{Params: clearApplyRenameFailuresParams{}},
		Liveness:        sdk.LivenessNone,
		ProgressTimeout: 10 * time.Minute,
		Plugin:          "maintenance",
		DisplayName:     "Clear apply rename-failure records",
		Description:     "Clears the durable rename-failure records the metadata apply pipeline writes for books whose rename was blocked by an unresolvable path collision. Those records make the apply skip a known-broken book instead of re-attempting it on every run, and they clear themselves when the blocking file changes or goes away — so run this only to undo a misclassification. Dry-run by default; apply=true blanks the records. Optional book_ids scopes it. Touches no files and no book_file rows.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.clear-apply-rename-failures",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         10 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runClearApplyRenameFailures,
	}
}

// clearApplyRenameFailuresKind is the native kind of
// maintenance.clear-apply-rename-failures (F5-08).
const clearApplyRenameFailuresKind tasks.Kind = "books.maintenance.clear-apply-rename-failures"

// clearApplyRenameFailuresScope is what the body takes of the params: HEAD's
// params without Apply, which is the run's mode (env.Live) and never a param on
// the native side (05-tasks 5T.3.7 mode contract item 3).
type clearApplyRenameFailuresScope struct {
	BookIDs []string `json:"book_ids,omitempty"`
}

// clearApplyRenameFailuresEnv is what runClearApplyRenameFailuresCore needs
// from its engine: the base (Live is HEAD's apply=true).
type clearApplyRenameFailuresEnv struct{ t6g3Base }

// runClearApplyRenameFailures is the v2 shell of
// maintenance.clear-apply-rename-failures.
func (p *Plugin) runClearApplyRenameFailures(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	var params clearApplyRenameFailuresParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("parse params: %w", err)
		}
	}
	if p.deps.OpsStore() == nil {
		return fmt.Errorf("clear-apply-rename-failures: no store")
	}
	env := clearApplyRenameFailuresEnv{p.t6g3HeadBase(ctx, reporter, params.Apply)}
	res, err := runClearApplyRenameFailuresCore(ctx, clearApplyRenameFailuresScope{BookIDs: params.BookIDs}, env)
	if err != nil {
		return err
	}
	// Persist the full row list on the operation record, the same way
	// reconcile.go and dedup_ops.go do, so a dry run's decisions are readable
	// afterwards rather than living only in the log.
	//
	// This wrote via store.UpdateOperationResultData(ctxOpID(ctx), ...), which
	// resolves a v1 `operation:` row that no live op has, so it always returned
	// "operation not found". Here the error was only logged, so the failure was
	// invisible: every run reported success while the result data it promises
	// was never stored. Now it goes to the run's own v2 row.
	if err := opsregistry.ReporterSetResult(reporter, res); err != nil {
		env.Log(slog.LevelWarn, fmt.Sprintf("could not persist result data: %v", err))
	}
	return nil
}

// clearApplyRenameFailuresKind builds the native kind. HEAD declares a Preview,
// so the native kind runs in either mode. Its params are decoded from the raw
// payload so an apply key is refused rather than ignored: a caller who sends
// apply=true must not get a silent preview. Writes found:
// SetUserPreferenceForUser blanks a record in the _system preference keyspace
// (user_state); the op never touches a file or a book_file row.
func (p *Plugin) clearApplyRenameFailuresKind(deps t6g3Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	eff := tasks.Writes(tasks.Res("books", "user_state"))
	return tasks.Task(clearApplyRenameFailuresKind, tasks.TaskSpec[json.RawMessage, clearApplyRenameFailuresResult]{
		Common: t6g3Common(p.clearApplyRenameFailuresDef(), clearApplyRenameFailuresKind, eff, sched),
		Run: func(rc *tasks.RC, raw json.RawMessage) (clearApplyRenameFailuresResult, error) {
			var scope clearApplyRenameFailuresScope
			if err := t6g3DecodeParams(clearApplyRenameFailuresKind, raw, &scope, "apply", "dry_run", "dryRun"); err != nil {
				return clearApplyRenameFailuresResult{}, err
			}
			base, err := t6g3NativeBase(clearApplyRenameFailuresKind, rc, deps, true)
			if err != nil {
				return clearApplyRenameFailuresResult{}, err
			}
			if err := t6g3NeedStore(clearApplyRenameFailuresKind, base); err != nil {
				return clearApplyRenameFailuresResult{}, err
			}
			return runClearApplyRenameFailuresCore(rc.Context(), scope, clearApplyRenameFailuresEnv{base})
		},
	})
}

// runClearApplyRenameFailuresCore is the shared body of both shells. It returns
// the result instead of storing it.
func runClearApplyRenameFailuresCore(ctx context.Context, params clearApplyRenameFailuresScope, env clearApplyRenameFailuresEnv) (clearApplyRenameFailuresResult, error) {
	wanted := make(map[string]struct{}, len(params.BookIDs))
	for _, id := range params.BookIDs {
		if s := strings.TrimSpace(id); s != "" {
			wanted[s] = struct{}{}
		}
	}

	prefs, err := env.Store.GetAllPreferencesForUser("_system")
	if err != nil {
		return clearApplyRenameFailuresResult{}, fmt.Errorf("list _system preferences: %w", err)
	}
	res := clearApplyRenameFailuresResult{Scanned: len(prefs)}
	for _, pref := range prefs {
		prefix := organizer.ApplyRenameFailurePrefix
		if !strings.HasPrefix(pref.Key, prefix) {
			// Organize's in-place collision skips share the record shape under
			// their own prefix; this op is the escape hatch for both.
			prefix = organizer.OrganizeCollisionSkipPrefix
			if !strings.HasPrefix(pref.Key, prefix) {
				continue
			}
		}
		// A blank value is an ALREADY-CLEARED record, not a live one: clearing
		// writes "" rather than deleting the row (the same convention
		// ClearCheckpoints uses), so skipping it here is what keeps the
		// reported count honest.
		if strings.TrimSpace(pref.Value) == "" {
			continue
		}
		bookID := strings.TrimPrefix(pref.Key, prefix)
		if len(wanted) > 0 {
			if _, ok := wanted[bookID]; !ok {
				continue
			}
		}

		row := clearApplyRenameFailureRow{BookID: bookID}
		var rec organizer.ApplyRenameFailure
		if err := json.Unmarshal([]byte(pref.Value), &rec); err == nil {
			row.TargetPath = rec.TargetPath
			row.OccupantPath = rec.OccupantPath
			row.Reason = rec.Reason
			row.RecordedAt = rec.RecordedAt
		} else {
			// An unreadable record is still worth clearing — it is exactly the
			// kind of stuck state this op exists for.
			row.Reason = fmt.Sprintf("unreadable record: %v", err)
		}
		res.Matched++

		if env.Live {
			if err := ctx.Err(); err != nil {
				return clearApplyRenameFailuresResult{}, err
			}
			if err := env.Writer.SetUserPreferenceForUser("_system", pref.Key, ""); err != nil {
				res.Errors++
				env.Log(slog.LevelWarn, fmt.Sprintf("could not clear record for book %s: %v", bookID, err))
			} else {
				row.Cleared = true
				res.Cleared++
			}
		}
		res.Rows = append(res.Rows, row)
	}

	sort.Slice(res.Rows, func(i, j int) bool { return res.Rows[i].BookID < res.Rows[j].BookID })

	summary := fmt.Sprintf("Apply rename-failure records: %d matched, %d cleared (apply=%t)",
		res.Matched, res.Cleared, env.Live)
	env.Log(slog.LevelInfo, summary)
	env.Progress(1, 1, summary)
	return res, nil
}
