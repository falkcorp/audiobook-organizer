// file: internal/plugins/maintenance/combine_journal_check.go
// version: 1.1.0
// guid: 2b7d4e91-6c35-4a08-9f12-8d0a5e3c7b64
// last-edited: 2026-11-13

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/falkcorp/mediacore/tasks"
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

// combineJournalCheckKind is the native kind of maintenance.combine-journal-check
// (F5-08).
const combineJournalCheckKind tasks.Kind = "books." + combineJournalCheckID

// combineJournalCheckEnv is what combineJournalCheckCore needs from its engine:
// the base (Store is the only read path; the check writes nothing).
type combineJournalCheckEnv struct{ t6g3Base }

// runCombineJournalCheck is the v2 shell of maintenance.combine-journal-check.
// The two refusals before the check are HEAD's engine preconditions: no
// database, and a store that cannot read raw keys.
func (p *Plugin) runCombineJournalCheck(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	if _, ok := database.AsCapability[combineJournalKV](store); !ok {
		return fmt.Errorf("%s: store is %T, which cannot read combine journals", combineJournalCheckID, store)
	}
	_, err := combineJournalCheckCore(ctx, combineJournalCheckEnv{p.t6g3HeadBase(ctx, reporter, true)})
	return err
}

// checkCombineJournalRows is the check over any raw-key reader.
func checkCombineJournalRows(kv combineJournalKV, reporter sdk.Reporter) error {
	env := combineJournalCheckEnv{t6g3Base{
		Live:     true,
		Log:      func(level slog.Level, msg string) { _ = reporter.Log(level, msg) },
		Progress: func(done, total int, msg string) { _ = reporter.UpdateProgress(done, total, msg) },
		Store:    t6g3KVStore{kv: kv},
	}}
	_, err := combineJournalCheckCore(context.Background(), env)
	return err
}

// t6g3KVStore is a t6g3Store that answers only the raw-key reads, for
// checkCombineJournalRows' callers that hold a bare reader. Any other read
// panics on the nil embedded store, which the check never makes.
type t6g3KVStore struct {
	t6g3Store
	kv combineJournalKV
}

func (s t6g3KVStore) GetRaw(key string) ([]byte, error) { return s.kv.GetRaw(key) }
func (s t6g3KVStore) ScanPrefix(prefix string) ([]database.KVPair, error) {
	return s.kv.ScanPrefix(prefix)
}

// combineJournalCheckKind builds the native kind. Declared ReadOnly: the check
// reads every combine-journal row and writes nothing. A ReadOnly run gets no
// writer from core, so the shell builds none.
func (p *Plugin) combineJournalCheckKind(deps t6g3Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	return tasks.Task(combineJournalCheckKind, tasks.TaskSpec[struct{}, merge.CombineJournalCheck]{
		Common: t6g3Common(p.combineJournalCheckDef(), combineJournalCheckKind, tasks.ReadOnly(), sched),
		Run: func(rc *tasks.RC, _ struct{}) (merge.CombineJournalCheck, error) {
			base, err := t6g3NativeBase(combineJournalCheckKind, rc, deps, false)
			if err != nil {
				return merge.CombineJournalCheck{}, err
			}
			if err := t6g3NeedStore(combineJournalCheckKind, base); err != nil {
				return merge.CombineJournalCheck{}, err
			}
			return combineJournalCheckCore(rc.Context(), combineJournalCheckEnv{base})
		},
	})
}

// combineJournalCheckCore is the shared body of both shells. It returns the
// check; err is the refusal the purge itself makes while a row is unreadable.
func combineJournalCheckCore(_ context.Context, env combineJournalCheckEnv) (merge.CombineJournalCheck, error) {
	env.Progress(0, 0, "combine journals: reading every row")
	check, err := merge.CheckCombineJournals(env.Store)
	if err != nil {
		return merge.CombineJournalCheck{}, fmt.Errorf("%s: %w", combineJournalCheckID, err)
	}
	const logCap = 200
	for i, u := range check.Unreadable {
		if i == logCap {
			env.Log(slog.LevelError, fmt.Sprintf("... and %d more unreadable rows not listed", len(check.Unreadable)-logCap))
			break
		}
		env.Log(slog.LevelError, fmt.Sprintf("unreadable combine journal row: key=%q error=%s", u.Key, u.Err))
	}
	msg := fmt.Sprintf("combine journals: %d rows, %d unreadable", check.Rows, len(check.Unreadable))
	env.Log(slog.LevelInfo, msg)
	env.Progress(1, 1, msg)
	if len(check.Unreadable) > 0 {
		return check, &merge.UnreadableCombineJournalsError{Rows: check.Unreadable}
	}
	return check, nil
}
