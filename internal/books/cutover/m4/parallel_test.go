// file: internal/books/cutover/m4/parallel_test.go
// version: 1.2.0
// guid: 52c8d0f6-3a17-4e94-b8d5-0f61e9a2c384
// last-edited: 2026-11-18

package m4_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/tasks"
)

// TestM4_ShardsDisjoint runs the step with the runner's default parallelism
// (the step declares CPU()) over runs that each own rows in every family, with
// orphans among them, and checks the partition the declaration in step.go
// states: no run is imported twice (the importer's create-only guard would
// refuse a second import, so a double would fail the boot), every run's rows
// are all imported and all deleted, and no row is lost between the worker that
// found it first and the run's driver. Run it with -race: the counters and the
// reconcile guard are the only shared state.
func TestM4_ShardsDisjoint(t *testing.T) {
	const runs = 48
	for _, chunk := range []int{1, 3, 64} {
		t.Run("chunk_"+strconv.Itoa(chunk), func(t *testing.T) {
			var specs []runSpec
			for i := 1; i <= runs; i++ {
				specs = append(specs, runSpec{
					id: runID(i), def: "library.scan", st: "succeeded", completed: tptr(epoch.Add(time.Hour)),
					evts: 2, intents: 1, items: 2, logs: 2, ckpt: i%2 == 0, ledger: i%3 == 0, snaps: i % 2, rows: i % 3, rowmarks: i % 2, marks: i % 4, derived: true,
					v2op: true, v2state: i%2 == 1, v2logs: i % 3, v2errs: i % 2, v2derived: true,
				})
			}
			raw := map[string]string{}
			for i := 100; i < 106; i++ { // orphans: rows of runs that have no record
				raw["opv3:log:"+runID(i)+":00000000000000000001:0000000001"] = `{"level":"info"}`
				raw["opv3:item:"+runID(i)+":failed:k"] = `{"key":"k"}`
				raw["opv2:log:"+runID(i)+":00000000000000000001:0000000001"] = `{"Level":"info"}`
			}
			// v1 history: operations of their own (some with the live chunk
			// journal, which is quarantined), v1 rows folded into every fifth
			// v3 run, and v1 rows of no operation.
			const v1Only = 6
			for i := 1; i <= v1Only; i++ {
				v1Op{id: runID(200 + i), typ: "dedup.book-scan", status: "completed", done: tptr(epoch.Add(time.Hour)), logs: 2, results: 1, summary: true, state: i%2 == 0, journal: i%3 == 0}.put(raw)
			}
			for i := 5; i <= runs; i += 5 {
				v1Op{id: runID(i), typ: "library.scan", status: "completed", logs: 2, results: 1}.put(raw)
			}
			raw["operationlog:"+runID(110)+":00000000000000000001:1"] = `{"id":1}`
			raw["op_result:"+runID(110)+":book-1"] = `{}`
			r := newRig(t, withRuns(nil, specs...), withRaw(raw), func(r *rig) { r.chunkItems = chunk })
			r.runObserve(t, func(ctx context.Context, s *snap) {
				if len(s.Left) != 0 {
					t.Errorf("%d source rows left: %v", len(s.Left), keysOf(s.Left))
				}
				for i := 1; i <= runs; i++ {
					id := tasks.RunID(runID(i))
					if _, err := s.Stores.Runs.Get(ctx, id); err != nil {
						t.Errorf("run %d: %v", i, err)
						continue
					}
					evts := collect(t, func(y func(tasks.PageEntry, error) bool) {
						s.Stores.Runs.Pages(ctx, id, tasks.PageQuery{Page: tasks.PageEvents})(y)
					})
					items := collect(t, func(y func(tasks.PageEntry, error) bool) {
						s.Stores.Runs.Pages(ctx, id, tasks.PageQuery{Page: tasks.PageItems})(y)
					})
					rows := collect(t, func(y func(tasks.Row, error) bool) { s.Stores.Runs.Rows(ctx, id)(y) })
					logs := collect(t, func(y func(tasks.PageEntry, error) bool) {
						s.Stores.Runs.Pages(ctx, id, tasks.PageQuery{Page: tasks.PageLog})(y)
					})
					v1Lines := 0
					if i%5 == 0 {
						v1Lines = 2
					}
					if want := 2 + i%3 + i%2 + v1Lines; len(logs) != want {
						t.Errorf("run %d imported with %d log lines, want %d (2 v3, %d v2, %d errors, %d v1)", i, len(logs), want, i%3, i%2, v1Lines)
					}
					if len(evts) != 2 || len(items) != 2 || len(rows) != i%3 {
						t.Errorf("run %d imported with %d events, %d items, %d rows; want 2, 2, %d", i, len(evts), len(items), len(rows), i%3)
					}
					if n := r.imp.runCalls(id); n != 1 {
						t.Errorf("run %d: the importer saw it %d times, want 1", i, n)
					}
				}
				for i := 1; i <= v1Only; i++ {
					id := tasks.RunID(runID(200 + i))
					if _, err := s.Stores.Runs.Get(ctx, id); err != nil {
						t.Errorf("v1 run %d: %v", i, err)
					}
					if logs := collectLog(ctx, t, s, runID(200+i)); len(logs) != 2 {
						t.Errorf("v1 run %d imported with %d log lines, want 2", i, len(logs))
					}
					if n := r.imp.runCalls(id); n != 1 {
						t.Errorf("v1 run %d: the importer saw it %d times, want 1", i, n)
					}
				}
				for i := 5; i <= runs; i += 5 {
					if n := r.imp.runCalls(tasks.RunID(runID(i))); n != 1 {
						t.Errorf("run %d with v1 rows: the importer saw it %d times, want 1", i, n)
					}
				}
				// 18 orphan v3/v2 rows, the 2 orphan v1 rows, and the 2 chunk journals.
				if len(s.Quarantine) != 22 {
					t.Errorf("%d rows quarantined, want 22 (orphans and journals): %+v", len(s.Quarantine), s.Quarantine)
				}
				if s.Verify.Written != runs+v1Only || len(s.Verify.Failures) != 0 {
					t.Errorf("verify = %+v", s.Verify)
				}
			})
		})
	}
}
