// file: internal/plugins/maintenance/clear_apply_rename_failures.go
// version: 1.1.0
// guid: 1c9f4a67-52b8-4d03-ae71-8f605d2c9b34
// last-edited: 2026-09-07

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

func (p *Plugin) runClearApplyRenameFailures(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	var params clearApplyRenameFailuresParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("parse params: %w", err)
		}
	}
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("clear-apply-rename-failures: no store")
	}

	wanted := make(map[string]struct{}, len(params.BookIDs))
	for _, id := range params.BookIDs {
		if s := strings.TrimSpace(id); s != "" {
			wanted[s] = struct{}{}
		}
	}

	prefs, err := store.GetAllPreferencesForUser("_system")
	if err != nil {
		return fmt.Errorf("list _system preferences: %w", err)
	}

	res := clearApplyRenameFailuresResult{Scanned: len(prefs)}
	for _, pref := range prefs {
		if !strings.HasPrefix(pref.Key, organizer.ApplyRenameFailurePrefix) {
			continue
		}
		// A blank value is an ALREADY-CLEARED record, not a live one: clearing
		// writes "" rather than deleting the row (the same convention
		// ClearCheckpoints uses), so skipping it here is what keeps the
		// reported count honest.
		if strings.TrimSpace(pref.Value) == "" {
			continue
		}
		bookID := strings.TrimPrefix(pref.Key, organizer.ApplyRenameFailurePrefix)
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

		if params.Apply {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := store.SetUserPreferenceForUser("_system", pref.Key, ""); err != nil {
				res.Errors++
				_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("could not clear record for book %s: %v", bookID, err))
			} else {
				row.Cleared = true
				res.Cleared++
			}
		}
		res.Rows = append(res.Rows, row)
	}

	sort.Slice(res.Rows, func(i, j int) bool { return res.Rows[i].BookID < res.Rows[j].BookID })

	summary := fmt.Sprintf("Apply rename-failure records: %d matched, %d cleared (apply=%t)",
		res.Matched, res.Cleared, params.Apply)
	_ = reporter.Log(slog.LevelInfo, summary)
	_ = reporter.UpdateProgress(1, 1, summary)
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
		_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("could not persist result data: %v", err))
	}
	return nil
}
