// file: internal/plugins/maintenance/combine_journal_check.go
// version: 1.0.0
// guid: 2b7d4e91-6c35-4a08-9f12-8d0a5e3c7b64
// last-edited: 2026-10-22

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// maintenance.combine-journal-check lists the combine-journal rows that cannot
// be decoded, by key.
//
// The purge's undo hold reads every combine journal to find the shells a
// combine's undo still needs. A row it cannot decode cannot say which shell it
// needs, so the purge (preview and run, and so the nightly purge) refuses
// while any such row exists (merge.UnreadableCombineJournalsError). This is the
// way to see them: one log line per unreadable row with its full store key and
// the decode error, and the operation fails with the same message the purge
// refuses with while any exist, so it is red in the operations list until the
// rows are fixed. REPORT-ONLY: it never writes. Repair is manual: fix or remove
// the row with the key shown, then purge again.
const combineJournalCheckID = merge.CombineJournalCheckOp

// combineJournalKV is the raw-key surface the check reads, resolved through
// database.AsCapability because OpsStore() can be a decorator that hides the
// concrete store's methods.
type combineJournalKV interface {
	GetRaw(key string) ([]byte, error)
	ScanPrefix(prefix string) ([]database.KVPair, error)
}

func (p *Plugin) combineJournalCheckDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          combineJournalCheckID,
		Liveness:    sdk.LivenessManual,
		Plugin:      "maintenance",
		DisplayName: "Combine journal readability check",
		Description: "Lists the combine-journal rows that cannot be decoded, with their store keys. " +
			"The purge refuses while any exist, because such a row may be an undo that still needs a " +
			"trashed book and cannot say which. Fails (naming the keys) when any is found. REPORT-ONLY: " +
			"modifies nothing; fix or remove the rows by hand, then purge again.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  combineJournalCheckID,
		Cancellable:     false,
		Timeout:         10 * time.Minute,
		// READ ONLY: never requests CapLibraryWrite.
		Capabilities: []sdk.Capability{sdk.CapLibraryRead},
		Run:          p.runCombineJournalCheck,
	}
}

func (p *Plugin) runCombineJournalCheck(_ context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	kv, ok := database.AsCapability[combineJournalKV](store)
	if !ok {
		return fmt.Errorf("%s: store is %T, which cannot read combine journals", combineJournalCheckID, store)
	}
	return checkCombineJournalRows(kv, reporter)
}

// checkCombineJournalRows is the check over any raw-key reader.
func checkCombineJournalRows(kv combineJournalKV, reporter sdk.Reporter) error {
	_ = reporter.UpdateProgress(0, 0, "combine journals: reading every row")
	check, err := merge.CheckCombineJournals(kv)
	if err != nil {
		return fmt.Errorf("%s: %w", combineJournalCheckID, err)
	}
	const logCap = 200
	for i, u := range check.Unreadable {
		if i == logCap {
			_ = reporter.Log(slog.LevelError, fmt.Sprintf("... and %d more unreadable rows not listed", len(check.Unreadable)-logCap))
			break
		}
		_ = reporter.Log(slog.LevelError, fmt.Sprintf("unreadable combine journal row: key=%q error=%s", u.Key, u.Err))
	}
	msg := fmt.Sprintf("combine journals: %d rows, %d unreadable", check.Rows, len(check.Unreadable))
	_ = reporter.Log(slog.LevelInfo, msg)
	_ = reporter.UpdateProgress(1, 1, msg)
	if len(check.Unreadable) > 0 {
		return &merge.UnreadableCombineJournalsError{Rows: check.Unreadable}
	}
	return nil
}
