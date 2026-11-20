// file: internal/plugins/maintenance/credits_backfill_native_test.go
// version: 0.1.0
// guid: 0fc30514-dafc-4d26-914d-96e4228fd351
// last-edited: 2026-11-20

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// The native credits backfill's state homes (F5-18d, O10-O12), proven against
// core's own NativeStores run store.

var cbNativeT0 = time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

// putNativeRuns stores recs in one batch.
func putNativeRuns(t *testing.T, runs tasks.RunStore, recs ...tasks.RunRecord) {
	t.Helper()
	ctx := t.Context()
	b := runs.(taskstest.BatchOpener).NewBatch()
	defer func() { _ = b.Close() }()
	for _, r := range recs {
		require.NoError(t, runs.Insert(ctx, b, r))
	}
	require.NoError(t, b.Commit(ctx, storage.Sync))
}

func cbIDs(srcs []cbSourceOp) []string {
	out := make([]string, len(srcs))
	for i, s := range srcs {
		out[i] = s.id
	}
	return out
}

// O10: the class lists round-trip through the run's own rows, and core's closed
// ledger refuses them once the run is terminal.
func TestRunRowStore_ClassListsAreTheRunsOwnRows(t *testing.T) {
	runs := newNativeRuns(t)
	const id tasks.RunID = "01GOTHAMCLASSLISTS0000000A"
	putNativeRun(t, runs, nativeRunAt(id, t6g3CreditsBackfillKind, tasks.ModePreview, tasks.StateRunning, cbNativeT0))
	rows := runRowStore{ctx: t.Context(), runs: runs}

	sum, err := runClassLists{rows: rows}.SaveClassLists(string(id), map[string][]string{
		"narrator_new_from_split": {"bk-arkham", "bk-gotham"},
		"held_itunes":             {"bk-wayne"},
	})
	require.NoError(t, err)
	got, err := sum.Page(rows, string(id), "narrator_new_from_split", 0, 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"bk-arkham", "bk-gotham"}, got)
	got, err = sum.Page(rows, string(id), "held_itunes", 0, 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"bk-wayne"}, got)

	var classes []string
	require.NoError(t, rows.ScanOpRowIndex(string(id), func(_ string, index []byte) error {
		classes = append(classes, string(index))
		return nil
	}))
	slices.Sort(classes)
	assert.Equal(t, []string{"held_itunes", "narrator_new_from_split"}, classes, "the op row's Index is the run row's Class")

	require.NoError(t, runClassLists{rows: rows}.ClearClassLists(string(id)))
	n := 0
	for _, err := range runs.Rows(t.Context(), id) {
		require.NoError(t, err)
		n++
	}
	assert.Zero(t, n, "ClearClassLists leaves no row")

	// A row whose data is not JSON is refused, not stored mangled.
	err = rows.ReplaceOpRowsPrefix(string(id), "x/", 1, func(int) (database.OpRow, error) {
		return database.OpRow{ID: "x/1", Data: []byte("not json")}, nil
	})
	require.ErrorContains(t, err, "not JSON")

	finishNativeRun(t, runs, id, tasks.StateSucceeded, []byte(`{}`))
	_, err = runClassLists{rows: rows}.SaveClassLists(string(id), map[string][]string{"held_itunes": {"bk-wayne"}})
	require.Error(t, err, "a terminal run's ledger is closed")
}

// O11: the lister returns every native run of the kind that did not succeed,
// across more than one page of the run listing, without this run, a succeeded
// run, or a run of another kind.
func TestNativeCBLedger_ListsEveryUnsucceededRunAcrossPages(t *testing.T) {
	runs := newNativeRuns(t)
	var recs []tasks.RunRecord
	var want []string
	for i := range cbRunListPage + 3 {
		id := tasks.RunID(fmt.Sprintf("01GOTHAM%018d", i))
		state := tasks.StateSucceeded
		if i%500 == 7 || i == cbRunListPage+2 {
			state = tasks.StateFailed
			want = append(want, string(id))
		}
		recs = append(recs, nativeRunAt(id, t6g3CreditsBackfillKind, tasks.ModeLive, state, cbNativeT0.Add(time.Duration(i)*time.Minute)))
	}
	recs = append(recs,
		nativeRunAt("01GOTHAMCANCELED0000000000", t6g3CreditsBackfillKind, tasks.ModeLive, tasks.StateCanceled, cbNativeT0),
		nativeRunAt("01GOTHAMOWNRUN00000000000A", t6g3CreditsBackfillKind, tasks.ModeLive, tasks.StateRunning, cbNativeT0),
		nativeRunAt("01GOTHAMOTHERKIND00000000A", tasks.Kind("books.maintenance.authority-build"), tasks.ModeLive, tasks.StateFailed, cbNativeT0),
	)
	want = append(want, "01GOTHAMCANCELED0000000000")
	putNativeRuns(t, runs, recs...)
	slices.Sort(want)

	got, err := (&nativeCBLedger{ctx: t.Context(), runs: runs}).others("01GOTHAMOWNRUN00000000000A")
	require.NoError(t, err)
	assert.Equal(t, want, cbIDs(got))
	for _, s := range got {
		require.NotNil(t, s.endedAt, "%s: a terminal run's end bounds its window", s.id)
		assert.NotEmpty(t, s.fingerprint)
	}
}

// O12: a judgement rides in the judging run's result and is read back from the
// listing, from a succeeded or a failed judging run; it holds while the judged
// run's record is unchanged, and a result that does not decode fails closed.
func TestNativeCBLedger_AJudgementIsReadFromTheJudgingRunsResult(t *testing.T) {
	failed := nativeRunAt("01GOTHAMJOKER0000000000000", t6g3CreditsBackfillKind, tasks.ModeLive, tasks.StateFailed, cbNativeT0)
	judging := func(id tasks.RunID, state tasks.RunState, judged map[string]string) tasks.RunRecord {
		r := nativeRunAt(id, t6g3CreditsBackfillKind, tasks.ModeLive, state, cbNativeT0.Add(2*time.Hour))
		raw, err := json.Marshal(creditsBackfillResult{JudgedSources: judged})
		require.NoError(t, err)
		r.Result = raw
		return r
	}
	fp := nativeCBFingerprint(failed)
	for name, c := range map[string]struct {
		judge tasks.RunRecord
		want  []string
	}{
		"judged by a succeeded run": {judging("01GOTHAMROBIN0000000000000", tasks.StateSucceeded, map[string]string{string(failed.ID): fp}), nil},
		"judged by a failed run":    {judging("01GOTHAMROBIN0000000000000", tasks.StateFailed, map[string]string{string(failed.ID): fp}), []string{"01GOTHAMROBIN0000000000000"}},
		"judged in another state":   {judging("01GOTHAMROBIN0000000000000", tasks.StateSucceeded, map[string]string{string(failed.ID): fp + "|again"}), []string{string(failed.ID)}},
		"judged another run":        {judging("01GOTHAMROBIN0000000000000", tasks.StateSucceeded, map[string]string{"01GOTHAMPENGUIN00000000000": fp}), []string{string(failed.ID)}},
	} {
		t.Run(name, func(t *testing.T) {
			runs := newNativeRuns(t)
			putNativeRuns(t, runs, failed, c.judge)
			got, err := (&nativeCBLedger{ctx: t.Context(), runs: runs}).others("01GOTHAMOWNRUN00000000000A")
			require.NoError(t, err)
			// A failed judging run is itself a run whose leftovers are judged.
			if len(c.want) == 0 {
				assert.Empty(t, got)
				return
			}
			assert.Equal(t, c.want, cbIDs(got))
		})
	}

	t.Run("an unreadable result fails closed", func(t *testing.T) {
		runs := newNativeRuns(t)
		bad := nativeRunAt("01GOTHAMRIDDLER00000000000", t6g3CreditsBackfillKind, tasks.ModeLive, tasks.StateSucceeded, cbNativeT0)
		bad.Result = json.RawMessage(`"a riddle"`)
		putNativeRuns(t, runs, failed, bad)
		_, err := (&nativeCBLedger{ctx: t.Context(), runs: runs}).others("01GOTHAMOWNRUN00000000000A")
		require.ErrorContains(t, err, "01GOTHAMRIDDLER00000000000")
	})

	t.Run("markJudged keeps the judgement for the result", func(t *testing.T) {
		l := &nativeCBLedger{ctx: t.Context(), runs: newNativeRuns(t)}
		assert.Nil(t, l.judgedSources())
		require.Error(t, l.markJudged(cbSourceOp{id: string(failed.ID)}), "a judgement needs its fingerprint")
		require.NoError(t, l.markJudged(cbSourceOp{id: string(failed.ID), fingerprint: fp}))
		assert.Equal(t, map[string]string{string(failed.ID): fp}, l.judgedSources())
	})
}

// Every field of the record that a later attempt, an abandon, a resume,
// progress or the end changes moves the fingerprint.
func TestNativeCBFingerprint_MovesWithEveryLaterAttempt(t *testing.T) {
	base := nativeRunAt("01GOTHAMJOKER0000000000000", t6g3CreditsBackfillKind, tasks.ModeLive, tasks.StateFailed, cbNativeT0)
	fp := nativeCBFingerprint(base)
	for name, mut := range map[string]func(*tasks.RunRecord){
		"state":    func(r *tasks.RunRecord) { r.State = tasks.StateQueued },
		"attempt":  func(r *tasks.RunRecord) { r.Attempt++ },
		"fence":    func(r *tasks.RunRecord) { r.FenceEpoch++ },
		"yields":   func(r *tasks.RunRecord) { r.Yields++ },
		"started":  func(r *tasks.RunRecord) { r.Started = r.Started.Add(time.Second) },
		"finished": func(r *tasks.RunRecord) { r.Finished = time.Time{} },
		"done":     func(r *tasks.RunRecord) { r.Done++ },
	} {
		r := base
		mut(&r)
		assert.NotEqual(t, fp, nativeCBFingerprint(r), name)
	}
}

// End to end: a native apply finds the record a failed NATIVE run's journal
// names (no v2 operation row exists for it), deletes it as a leftover, carries
// the judgement in its own result, and the next apply's listing skips that run.
func TestT6G3_CreditsBackfillNativeJudgesAFailedNativeRunAndCarriesTheJudgement(t *testing.T) {
	f, rec := cbLeftoverFixture(t, false)
	const crashed tasks.RunID = "01GOTHAMCRASHEDRUN00000000"
	cbSeedRow(t, f, rec.CreatedAt.Add(-time.Second), database.OperationChange{
		OperationID: string(crashed),
		ChangeType:  undo.ChangeTypeNarratorCreateIntent, FieldName: "narrator", NewValue: "Renee Montoya",
	})
	n := newT6G3CBNative(f, t6g3ScanKey)
	crashedRec := nativeRunAt(crashed, t6g3CreditsBackfillKind, tasks.ModeLive, tasks.StateFailed, rec.CreatedAt.Add(-time.Hour))
	crashedRec.Finished = rec.CreatedAt.Add(time.Second)
	putNativeRun(t, n.runs, crashedRec)
	got, err := n.runs.Get(t.Context(), crashed)
	require.NoError(t, err)

	host, rc := newT6G3Host(t6g3CreditsBackfillKind, tasks.ModeLive, &t6g3Gate{id: "credits-live"})
	host.SetCPUSlots(3)
	raw, err := t6g3Run(t, n.kind(), rc, "")
	require.NoError(t, err)
	var res creditsBackfillResult
	require.NoError(t, json.Unmarshal(raw, &res))
	assert.Equal(t, 1, res.UnusedNarratorsDeleted, "the failed native run's leftover is deleted")
	assert.Equal(t, []string{"Renee Montoya"}, res.DeletedUnusedNarratorNames)
	assert.Equal(t, map[string]string{string(crashed): nativeCBFingerprint(got)}, res.JudgedSources)

	// The run ends with that result; the next apply's listing skips the judged run.
	finishNativeRun(t, n.runs, cbFakeRunID, tasks.StateSucceeded, raw)
	others, err := (&nativeCBLedger{ctx: t.Context(), runs: n.runs}).others("01GOTHAMNEXTRUN00000000000")
	require.NoError(t, err)
	assert.Empty(t, cbIDs(others), "a run judged in its current state is not read again")
}

// A run that judged and then failed before it had a result still returns the
// judgements; with neither, the result is the zero value (no result).
func TestWithJudged(t *testing.T) {
	assert.Equal(t, creditsBackfillResult{}, withJudged(nil, nil))
	j := map[string]string{"01GOTHAMJOKER0000000000000": "fp"}
	assert.Equal(t, creditsBackfillResult{JudgedSources: j}, withJudged(nil, j))
	assert.Equal(t, creditsBackfillResult{BooksToWrite: 2, JudgedSources: j}, withJudged(&creditsBackfillResult{BooksToWrite: 2}, j))
	assert.Equal(t, creditsBackfillResult{BooksToWrite: 2}, withJudged(&creditsBackfillResult{BooksToWrite: 2}, nil))
}

// cbStopCtx is a run context the test stops from inside the run's work, with
// the error a cancel (context.Canceled) or the executor's timeout
// (context.DeadlineExceeded) leaves on it.
type cbStopCtx struct {
	context.Context
	err  error
	once sync.Once
	done chan struct{}
}

func newCBStopCtx(err error) *cbStopCtx {
	return &cbStopCtx{Context: context.Background(), err: err, done: make(chan struct{})}
}

func (c *cbStopCtx) stop()                 { c.once.Do(func() { close(c.done) }) }
func (c *cbStopCtx) Done() <-chan struct{} { return c.done }
func (c *cbStopCtx) Err() error {
	select {
	case <-c.done:
		return c.err
	default:
		return nil
	}
}

// runStoppedCB runs a native credits preview over the census fixture on one
// worker, stops its context while it reads the last book, and returns the
// result the kind hands the executor (which keeps it on the failed record,
// F5-18d O5).
func runStoppedCB(t *testing.T, n *t6g3CBNative, stopErr error) (json.RawMessage, creditsBackfillResult, *t6g3Host) {
	t.Helper()
	ids, err := n.rec.ListBookIDs()
	require.NoError(t, err)
	require.Greater(t, len(ids), 1)
	ctx := newCBStopCtx(stopErr)
	var reads atomic.Int32
	n.rec.onGetBook = func() {
		if int(reads.Add(1)) == len(ids)-1 {
			ctx.stop()
		}
	}
	host := &t6g3Host{FakeRun: taskstest.NewFakeRun(ctx, t6g3CreditsBackfillKind), mode: tasks.ModePreview, w: &t6g3Gate{}}
	host.SetCPUSlots(1)
	raw, err := t6g3Run(t, n.kind(), tasks.NewRC(host), "")
	require.ErrorIs(t, err, stopErr)
	require.NotEmpty(t, raw, "a stopped run returns what it saw")
	var res creditsBackfillResult
	require.NoError(t, json.Unmarshal(raw, &res))
	listed := 0
	for _, c := range res.Classes {
		listed += c.Count
	}
	require.Positive(t, listed, "the run filed books under its classes before it stopped, so it had lists to keep")
	return raw, res, host
}

var cbStopErrs = []struct {
	name string
	err  error
}{{"canceled", context.Canceled}, {"timed out", context.DeadlineExceeded}}

// A canceled or timed-out native run stores its class lists after its context
// is done: they still become its rows, and the result it returns, which core
// keeps on the record, is the summary without them (F5-18d review M1). A
// library-scale run's lists are megabytes.
func TestT6G3_CreditsBackfillNativeAStoppedRunKeepsItsListsAsRowsNotInItsResult(t *testing.T) {
	for _, tc := range cbStopErrs {
		t.Run(tc.name, func(t *testing.T) {
			f := newCBFixture(t)
			seedCBCensusFixture(f)
			n := newT6G3CBNative(f, t6g3ScanKey)
			raw, res, _ := runStoppedCB(t, n, tc.err)
			assert.NotContains(t, string(raw), `"book_ids"`, "the stored result never carries the lists")
			assert.True(t, res.ClassLists.Valid(), "the result names the rows its lists are in")
			assert.NotZero(t, n.classListRows(t), "the lists are the stopped run's rows")
		})
	}
}

// A stopped run whose lists cannot be stored (here: the run store has no such
// run, so ReplaceRows refuses) still returns no lists: the counts and samples
// stay, the lists and a row summary do not.
func TestT6G3_CreditsBackfillNativeAStoppedRunWhoseListsAreNotStoredReturnsNoLists(t *testing.T) {
	for _, tc := range cbStopErrs {
		t.Run(tc.name, func(t *testing.T) {
			f := newCBFixture(t)
			seedCBCensusFixture(f)
			n := newT6G3CBNative(f, t6g3ScanKey)
			n.runs = newNativeRuns(t)
			n.deps.Runs = n.runs
			raw, res, host := runStoppedCB(t, n, tc.err)
			assert.NotContains(t, string(raw), `"book_ids"`, "a result whose lists were not stored never carries them")
			assert.Nil(t, res.ClassLists, "no rows were stored, so none are named")
			host.mu.Lock()
			logs := strings.Join(host.logs, "\n")
			host.mu.Unlock()
			assert.Contains(t, logs, "credits-backfill: result not stored")
		})
	}
}
