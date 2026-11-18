// file: internal/books/cutover/m4/crash_test.go
// version: 1.0.2
// guid: 9a3d6e18-f2b4-4c07-85d1-6e0a7b49c2f3
// last-edited: 2026-11-18

package m4_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
)

// TestCutover_ResumableCrashMatrix kills the step at every row it is handed,
// in four ways, at two chunk sizes, and requires the resumed boot to end exactly
// where an uninterrupted boot ends: the same task keyspace byte for byte, no row
// of HEAD's left, the same quarantine, and every run imported once.
//
// The four kills are the two places a process can die around a row's work and
// the two ways of dying there: the step returns an error before or after the
// row's work (the boot fails, the store is as the failed boot left it), or the
// filesystem is cloned at that point as a power cut would leave it (only what
// was synced) and the clone is booted. After the import and before the chunk's
// commit is the window the importer's idempotent write exists for: nothing of
// the run is durable, and the resumed boot imports it from the top.
func TestCutover_ResumableCrashMatrix(t *testing.T) {
	var specs []runSpec
	done := epoch.Add(time.Hour)
	for i := 1; i <= 4; i++ {
		specs = append(specs, runSpec{
			id: runID(i), def: "library.scan", st: "succeeded", completed: tptr(done),
			evts: 2, intents: 1, items: 1, logs: 1, ckpt: i%2 == 0, rows: i % 2, rowmarks: i % 2, derived: i == 1,
		})
	}
	specs = append(specs, runSpec{id: runID(5), def: "library.scan", st: "succeeded", completed: tptr(done), evts: 1, corruptRun: true})
	raw := map[string]string{"opv3:item:" + runID(90) + ":failed:k": `{"key":"k"}`} // an orphan
	// The rows no run owns: a clock group of two rows (one imports, one defers),
	// both markers and a bucket of two subjects, each importing in one batch.
	fire := epoch.Add(2 * time.Hour)
	raw["opv3:sched:library.scan"] = schedRowJSON("library.scan", &fire, nil, 1)
	setting(raw, "scheduled_task_last_tick.library_scan", fire.Add(time.Hour).Format(time.RFC3339))
	setting(raw, "maintenance_window_last_run", "2026-11-04")
	setting(raw, "registry.operations_paused", `{"paused":true,"reason":"Arkham","by":"bruce.wayne","since_unix_nano":1}`)
	setting(raw, "registry.scan_standdown", `{"holder_op_id":"`+runID(1)+`","scan_op_id":"`+runID(2)+`","lease_expiry_unix_nano":2}`)
	// A subject's completions, one stale, and its revision row.
	raw["op:deprev:book:year-one"] = revJSON(2)
	raw["op:completion:book:year-one:dedup.book-scan"] = revJSON(2)
	raw["op:completion:book:year-one:metafetch.asin-backfill"] = revJSON(1)
	raw["op:completion:book:year-one:dedup.book-scan:file-1"] = revJSON(2)
	raw["op:batch:dedup.book-scan:book:year-one"] = batchRowJSON("book", "year-one", fire)
	raw["op:batch:dedup.book-scan:book:killing-joke"] = batchRowJSON("book", "killing-joke", fire.Add(time.Minute))

	type mode struct {
		name          string
		before, power bool
	}
	modes := []mode{
		{"error_before_work", true, false},
		{"error_after_work", false, false},
		{"power_cut_before_work", true, true},
		{"power_cut_after_work", false, true},
	}

	for _, chunk := range []int{1, 5} {
		t.Run("chunk_"+strconv.Itoa(chunk), func(t *testing.T) {
			opts := []func(*rig){withRuns(nil, specs...), withRaw(raw), func(r *rig) {
				r.chunkItems = chunk
				r.parallelism = func(dm *datamigrate.DataMigration) { dm.Parallelism = fixedOne() }
			}}
			seeded := newRig(t, opts...).seed(t)

			// The uninterrupted boot is the oracle; it also counts the rows the
			// runner hands the step.
			ref := newRig(t, opts...)
			dm := ref.migration()
			inner := dm.Step
			var steps atomic.Int64
			dm.Step = func(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
				steps.Add(1)
				return inner(ctx, kv, w)
			}
			want, err := ref.runWith(t, crashClone(seeded), dm)
			if err != nil {
				t.Fatalf("uninterrupted boot: %v\n%s", err, ref.logs)
			}
			if want.State != "done" || len(want.Left) != 0 {
				t.Fatalf("the oracle is wrong: state %q, %d rows left", want.State, len(want.Left))
			}
			if len(want.Quarantine) < 2 {
				t.Fatalf("the oracle quarantined %d rows, want the corrupt run and the orphan at least: %+v", len(want.Quarantine), want.Quarantine)
			}
			n := int(steps.Load())

			for _, m := range modes {
				for i := range n {
					t.Run(fmt.Sprintf("%s/row_%02d", m.name, i), func(t *testing.T) {
						r := newRig(t, opts...)
						fsys := crashClone(seeded)
						var cut *vfs.MemFS

						dm := r.migration()
						inner := dm.Step
						var calls atomic.Int64
						dm.Step = func(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
							if int(calls.Add(1))-1 != i {
								return inner(ctx, kv, w)
							}
							if m.before {
								if m.power {
									cut = crashClone(fsys)
								}
								return errKilled
							}
							if err := inner(ctx, kv, w); err != nil {
								return err
							}
							if m.power {
								cut = crashClone(fsys)
							}
							return errKilled
						}
						if _, err := r.runWith(t, fsys, dm); !errors.Is(err, errKilled) {
							t.Fatalf("the killed boot returned %v, want the injected kill", err)
						}
						target := vfs.FS(fsys)
						if m.power {
							if cut == nil {
								t.Fatal("no power-cut clone was taken")
							}
							target = cut
						}
						got, err := r.runOn(t, target)
						if err != nil {
							t.Fatalf("resumed boot: %v\n%s", err, r.logs)
						}
						if got.State != "done" || len(got.Left) != 0 {
							t.Fatalf("state %q, %d source rows left: %v", got.State, len(got.Left), keysOf(got.Left))
						}
						if !reflect.DeepEqual(got.Tasks, want.Tasks) {
							t.Errorf("the task keyspace differs from an uninterrupted boot's:\n%s", diffMaps(got.Tasks, want.Tasks))
						}
						if !reflect.DeepEqual(got.Quarantine, want.Quarantine) {
							t.Errorf("quarantine = %+v, want %+v", got.Quarantine, want.Quarantine)
						}
						if len(got.Verify.Failures) != 0 {
							t.Errorf("verify failures: %v", got.Verify.Failures)
						}
					})
				}
			}
		})
	}
}

// diffMaps names the first few keys two maps disagree on.
func diffMaps(got, want map[string]string) string {
	var out string
	shown := 0
	note := func(s string) bool {
		out += s
		shown++
		return shown == 6
	}
	for _, k := range slices.Sorted(maps.Keys(want)) {
		if g, ok := got[k]; !ok {
			if note(fmt.Sprintf("  missing %q\n", k)) {
				return out
			}
		} else if g != want[k] {
			if note(fmt.Sprintf("  %q differs: got %q, want %q\n", k, g, want[k])) {
				return out
			}
		}
	}
	for _, k := range slices.Sorted(maps.Keys(got)) {
		if _, ok := want[k]; !ok {
			if note(fmt.Sprintf("  unexpected %q\n", k)) {
				return out
			}
		}
	}
	return out
}
