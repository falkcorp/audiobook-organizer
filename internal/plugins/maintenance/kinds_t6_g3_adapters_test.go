// file: internal/plugins/maintenance/kinds_t6_g3_adapters_test.go
// version: 1.0.0
// guid: b3f6a1d2-7c48-4e95-a0d3-92e5c1f84b67
// last-edited: 2026-11-13

package maintenance

// Reporter-shaped adapters over the group-3 cores. HEAD's shells build their
// cores' env directly, so nothing in production calls these; the tests that
// predate the cores' extraction keep their call shape through them. They live
// here, not in a production file, so the deadcode ratchet does not count them.

import (
	"context"
	"errors"
	"log/slog"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

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

// compactProgressToReporter adapts the compaction store's progress events to
// reporter.UpdateProgress. Total is unknown up front (the store bounds its
// loop by the data, not by a pre-count), so it is reported as 0 and the UI
// shows an indeterminate bar with the running count. ev.Result is the store's
// running total for this CompactByDay call, so it is forwarded as is.
//
// Done and Heartbeat handling lived here while a migrating wrapper fanned
// compaction out across a Pebble and a SQLite store (per-backend completion
// stamps, a cross-backend cumulative counter, liveness-only SQLite scan
// windows). The single Pebble store emits none of those, so they went with
// the SQLite backend on 2026-10-19.
func compactProgressToReporter(reporter sdk.Reporter) database.CompactProgress {
	return compactProgressVia(func(done, total int, msg string) { _ = reporter.UpdateProgress(done, total, msg) })
}

// runCreditsBackfillScan is the run over HEAD's reporter: the adapter the HEAD
// shell and the tests call. It returns a result whenever it got as far as
// listing books, including on cancel or failure (Complete=false).
func runCreditsBackfillScan(ctx context.Context, env creditsBackfillEnv, params creditsBackfillParams, reporter sdk.Reporter) (*creditsBackfillResult, error) {
	env.eng = headCBEngine{ctx: ctx, reporter: reporter, scan: env.scan}
	return runCreditsBackfillRun(ctx, env, params)
}

// storeCreditsBackfillResult stores the run's result over a row store: the HEAD
// adapter of persistCreditsBackfillResult (the reporter's result slot).
func storeCreditsBackfillResult(reporter sdk.Reporter, rows database.OpRowStore, opID string, res *creditsBackfillResult) error {
	if rows == nil {
		return errors.New("no operation id or row store to keep the class id lists under")
	}
	_, err := persistCreditsBackfillResult(cbRowsWriter{rows}, opID, res,
		func(r *creditsBackfillResult) error { return registry.ReporterSetResult(reporter, r) },
		func(level slog.Level, msg string) { _ = reporter.Log(level, msg) })
	return err
}

// cbRowsWriter is cbResultWriter over a database.OpRowStore.
type cbRowsWriter struct{ rows database.OpRowStore }

func (w cbRowsWriter) SaveClassLists(opID string, lists map[string][]string) (*database.ClassListSummary, error) {
	return database.SaveClassLists(w.rows, opID, lists)
}
func (w cbRowsWriter) ClearClassLists(opID string) error {
	return database.ClearClassLists(w.rows, opID)
}
