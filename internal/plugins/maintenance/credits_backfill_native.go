// file: internal/plugins/maintenance/credits_backfill_native.go
// version: 0.1.0
// guid: 4a859adc-3ab0-424a-9df6-f786768b55bb
// last-edited: 2026-11-20

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"slices"
	"strings"
	"sync"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/mediacore/tasks"
)

// The native credits backfill's state homes (F5-18d: O10, O11, O12). HEAD kept
// three pieces of state in its v2 operation rows: the class id lists beside the
// result (opv2:row:), the list of earlier operations whose leftovers an apply
// must judge (ListOperationsV2Since filtered on the v2 def id), and the judged
// markers, written as op rows under each judged operation's id. None of the
// three works for a native run: the run is not a v2 operation, the lister
// filters on the v2 def id, and the judged runs are terminal, so core's
// closed-ledger rule refuses any row written under their ids. Here each piece
// lives in the tasks run store instead:
//
//   - the class lists are the run's own rows (tasks.RunStore.ReplaceRows on its
//     own RunID while it runs; runRowStore);
//   - the other runs are the store's listing of the native kind, paged to the
//     end (nativeCBLedger.others);
//   - a judgement is carried by the JUDGING run, in its own result
//     (creditsBackfillResult.JudgedSources), which the listing returns with
//     every record, so it is read from the same pass and stays readable after
//     the judging run is terminal. A run that fails after judging keeps the
//     result it returns beside its error (core, F5-18d O5).
//
// HEAD's shell keeps its v2 ledger (cbOpsLedger) unchanged.

// cbRunStore is what the native credits kind needs of the tasks run store
// (tasks.RunStore has all three; F5-18 passes the kernel's NativeStores.Runs).
type cbRunStore interface {
	List(ctx context.Context, q tasks.RunQuery) iter.Seq2[tasks.RunRecord, error]
	ReplaceRows(ctx context.Context, id tasks.RunID, prefix string, n int, at func(i int) (tasks.Row, error)) error
	Rows(ctx context.Context, id tasks.RunID) iter.Seq2[tasks.Row, error]
}

// cbRunListPage is the page size of the run listing: core's clamp
// (tasks.RunFilter.Limit). The listing pages to the end and is never capped,
// because a cap would drop the oldest runs, the unsafe direction (as
// cbOpsLedger.others says of the v2 listing).
const cbRunListPage = 1000

// --- O10: the class id lists as the run's own rows ---

// runRowStore is database.OpRowStore over one store of run rows, so
// database.SaveClassLists, ClearClassLists and ClassListSummary.Page keep their
// layout, attempt nonce and checks unchanged. An op row maps onto a tasks.Row
// as ID -> ID, Index -> Class, Data -> State; State is a json.RawMessage, so a
// row's Data must be valid JSON (the class-list rows are) and a row that is
// not is refused rather than stored mangled. ctx bounds every call.
//
// The run store has no point read and no prefix scan of rows: GetOpRows and
// ScanOpRowIndex read every row of the run. A class list page over a
// library-scale result (a few hundred rows) is one such pass; the flip's reader
// of a native run's lists (the operations handler's class page) pays it per
// page.
type runRowStore struct {
	ctx  context.Context
	runs cbRunStore
}

var _ database.OpRowStore = runRowStore{}

func (s runRowStore) ReplaceOpRowsPrefix(opID, rowPrefix string, n int, at func(i int) (database.OpRow, error)) error {
	var conv func(int) (tasks.Row, error)
	if at != nil {
		conv = func(i int) (tasks.Row, error) {
			r, err := at(i)
			if err != nil {
				return tasks.Row{}, err
			}
			if !json.Valid(r.Data) {
				return tasks.Row{}, fmt.Errorf("row %q of run %s: data is not JSON, which a run row (tasks.Row.State) cannot hold", r.ID, opID)
			}
			return tasks.Row{ID: r.ID, Class: string(r.Index), State: json.RawMessage(r.Data)}, nil
		}
	}
	return s.runs.ReplaceRows(s.ctx, tasks.RunID(opID), rowPrefix, n, conv)
}

func (s runRowStore) ScanOpRowIndex(opID string, fn func(rowID string, index []byte) error) error {
	for row, err := range s.runs.Rows(s.ctx, tasks.RunID(opID)) {
		if err != nil {
			return err
		}
		if err := fn(row.ID, []byte(row.Class)); err != nil {
			return err
		}
	}
	return nil
}

func (s runRowStore) GetOpRows(opID string, rowIDs []string) ([][]byte, error) {
	want := make(map[string]int, len(rowIDs))
	for i, id := range rowIDs {
		want[id] = i
	}
	out := make([][]byte, len(rowIDs))
	for row, err := range s.runs.Rows(s.ctx, tasks.RunID(opID)) {
		if err != nil {
			return nil, err
		}
		if i, ok := want[row.ID]; ok {
			out[i] = slices.Clone([]byte(row.State))
		}
	}
	return out, nil
}

// runClassLists keeps a native run's class id lists as its own rows:
// t6g3CBResultWriter over runRowStore, never through the run's Writer (a native
// Preview keeps its lists too, and the rows are the run's ledger, not an
// Effects resource).
type runClassLists struct{ rows runRowStore }

func (c runClassLists) SaveClassLists(opID string, lists map[string][]string) (*database.ClassListSummary, error) {
	return database.SaveClassLists(c.rows, opID, lists)
}

func (c runClassLists) ClearClassLists(opID string) error {
	return database.ClearClassLists(c.rows, opID)
}

// --- O11 and O12: the other runs, and the judgements ---

// cbLedger is where a credits backfill finds the other runs whose leftovers it
// must judge and records that it judged one (seedLeftovers,
// markLeftoversJudged).
type cbLedger interface {
	// others is every other run of the backfill whose last attempt did not
	// complete and whose current state no earlier apply has judged, by id.
	others(own string) ([]cbSourceOp, error)
	// markJudged records that src's leftovers were judged in its current state.
	markJudged(src cbSourceOp) error
}

// nativeCBLedger is cbLedger over the tasks run store.
type nativeCBLedger struct {
	ctx  context.Context
	runs cbRunStore

	mu     sync.Mutex
	judged map[string]string // source run id -> the fingerprint this run judged
}

// cbJudgedOnly is the part of a stored credits result the ledger reads.
type cbJudgedOnly struct {
	JudgedSources map[string]string `json:"judged_sources"`
}

// others lists every run of the native kind, paged to the end, and returns the
// ones that are not own, did not succeed, and are not judged in their current
// state by any run's stored result. Every read fails closed: a listing error
// or a stored result that does not decode stops the apply, naming the run.
//
// Not listed, and so not judged: a run core.tasks.prune deleted. Prune deletes
// terminal runs by age, and a judging run always starts after the runs it
// judges ended, so prune removes a judged run no later than its judge: a
// judgement is never lost while the run it covers is still listed. Its journal
// rows stay (opchange: is not pruned with the run), so the run can still be
// reverted; the records it may have left are no longer looked for here, as
// with a discarded v2 operation (purge-empty-narrators removes unlinked ones).
func (l *nativeCBLedger) others(own string) ([]cbSourceOp, error) {
	judged := map[string]map[string]bool{}
	var cands []tasks.RunRecord
	q := tasks.RunQuery{Kinds: []tasks.Kind{t6g3CreditsBackfillKind}, RunFilter: tasks.RunFilter{Limit: cbRunListPage}}
	for {
		n := 0
		for rec, err := range l.runs.List(l.ctx, q) {
			if err != nil {
				return nil, fmt.Errorf("list credits-backfill runs for the records other runs left: %w", err)
			}
			n++
			q.After = rec.ID
			if err := addJudged(judged, rec); err != nil {
				return nil, err
			}
			if string(rec.ID) != own && rec.State != tasks.StateSucceeded {
				cands = append(cands, rec)
			}
		}
		if n == 0 {
			break
		}
	}
	var out []cbSourceOp
	for _, rec := range cands {
		src := cbSourceOp{id: string(rec.ID), fingerprint: nativeCBFingerprint(rec)}
		if !rec.Finished.IsZero() {
			end := rec.Finished
			src.endedAt = &end
		}
		if judged[src.id][src.fingerprint] {
			continue
		}
		out = append(out, src)
	}
	slices.SortFunc(out, func(a, b cbSourceOp) int { return strings.Compare(a.id, b.id) })
	return out, nil
}

// addJudged adds the judgements rec's stored result carries. A run with no
// result carries none; a result that does not decode fails closed, as HEAD's
// unreadable marker does.
func addJudged(judged map[string]map[string]bool, rec tasks.RunRecord) error {
	if len(rec.Result) == 0 || string(rec.Result) == "null" {
		return nil
	}
	var r cbJudgedOnly
	if err := json.Unmarshal(rec.Result, &r); err != nil {
		return fmt.Errorf("read the judgements stored on credits-backfill run %s: %w%s", rec.ID, err, cbDiscardRemedy(string(rec.ID)))
	}
	for src, fp := range r.JudgedSources {
		if judged[src] == nil {
			judged[src] = map[string]bool{}
		}
		judged[src][fp] = true
	}
	return nil
}

// markJudged records the judgement in this run's memory; the native shell puts
// it in the run's result (judgedSources), the only place a judging run's own
// state can be written once the runs it judged are terminal.
func (l *nativeCBLedger) markJudged(src cbSourceOp) error {
	if src.id == "" || src.fingerprint == "" {
		return errors.New("a judgement needs the run id and the fingerprint it was made against")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.judged == nil {
		l.judged = map[string]string{}
	}
	l.judged[src.id] = src.fingerprint
	return nil
}

// judgedSources is a copy of the judgements this run made (nil when none).
func (l *nativeCBLedger) judgedSources() map[string]string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.judged) == 0 {
		return nil
	}
	out := make(map[string]string, len(l.judged))
	for k, v := range l.judged {
		out[k] = v
	}
	return out
}

// nativeCBFingerprint is the state of a run's record a judgement of its
// leftovers is good for (cbSourceFingerprint's native twin). Every claim of a
// new attempt bumps Attempt and FenceEpoch, an abandon bumps FenceEpoch, an
// operator's resume re-queues the run and so changes State and then Attempt,
// progress moves Done, and the end stamps Finished: a run that runs again after
// a judgement is judged again.
func nativeCBFingerprint(rec tasks.RunRecord) string {
	stamp := func(t time.Time) int64 {
		if t.IsZero() {
			return 0
		}
		return t.UnixNano()
	}
	return fmt.Sprintf("%s|attempt=%d|fence=%d|yields=%d|started=%d|finished=%d|done=%d",
		rec.State, rec.Attempt, rec.FenceEpoch, rec.Yields, stamp(rec.Started), stamp(rec.Finished), rec.Done)
}
