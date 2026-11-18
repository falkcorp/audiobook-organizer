// file: internal/books/cutover/m4/parallel_test.go
// version: 1.1.0
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
					if len(logs) != 2+i%3+i%2 {
						t.Errorf("run %d imported with %d log lines, want %d (2 v3, %d v2, %d errors)", i, len(logs), 2+i%3+i%2, i%3, i%2)
					}
					if len(evts) != 2 || len(items) != 2 || len(rows) != i%3 {
						t.Errorf("run %d imported with %d events, %d items, %d rows; want 2, 2, %d", i, len(evts), len(items), len(rows), i%3)
					}
					if n := r.imp.runCalls(id); n != 1 {
						t.Errorf("run %d: the importer saw it %d times, want 1", i, n)
					}
				}
				if len(s.Quarantine) != 18 {
					t.Errorf("%d rows quarantined, want the 18 orphan rows: %+v", len(s.Quarantine), s.Quarantine)
				}
				if s.Verify.Written != runs || len(s.Verify.Failures) != 0 {
					t.Errorf("verify = %+v", s.Verify)
				}
			})
		})
	}
}
