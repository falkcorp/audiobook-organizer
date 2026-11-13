// file: internal/books/cutover/m5/crash_test.go
// version: 1.0.0
// guid: 0d4f8a61-72c3-49e5-b9a0-6e18c35d7f02
// last-edited: 2026-11-13

package m5_test

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

// TestM5_CrashMatrixNoDuplicates kills the step at every row, in four ways,
// at two chunk sizes, and requires that the resumed boot ends exactly where an
// uninterrupted boot ends: every row journaled once, none left, the quarantined
// row quarantined once.
//
// The four kills are the two places a process can die around a row's work and
// the two ways of dying there: the step returns an error before or after the
// row's import (the boot fails, the store is as the failed boot left it), or
// the filesystem is cloned at that point as a power cut would leave it (only
// what was synced) and the clone is booted. After the import and before the
// chunk's commit is the window the importer's idempotent key exists for: the
// record is durable, the source row's delete is not, and the resumed boot
// imports the row again. The row's Import calls say so (twice), and the journal
// still holds one record.
func TestM5_CrashMatrixNoDuplicates(t *testing.T) {
	const n = 10
	const quarantinedAt = 4 // the row at this index in key order has a level HEAD never wrote
	base := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	rows := synth(t, n, base)
	keys := slices.Sorted(maps.Keys(rows))
	rows[keys[quarantinedAt]] = []byte(`{"id":5,"source":"scanner","level":"debug","message":"Hush","created_at":"2026-11-05T00:00:04Z"}`)

	type mode struct {
		name string
		// before is whether the kill lands before the row's import (else after
		// it); power is whether it is a power cut (a clone of the filesystem)
		// instead of a returned error.
		before, power bool
	}
	modes := []mode{
		{"error_before_import", true, false},
		{"error_after_import", false, false},
		{"power_cut_before_import", true, true},
		{"power_cut_after_import", false, true},
	}

	for _, chunk := range []int{1, 4} {
		t.Run("chunk_"+strconv.Itoa(chunk), func(t *testing.T) {
			opts := []func(*rig){withRows(rows), func(r *rig) {
				r.chunkItems = chunk
				r.parallelism = func(dm *datamigrate.DataMigration) { dm.Parallelism = fixedOne() }
			}}
			seeded := newRig(t, "", opts...).seed(t)

			// The uninterrupted boot is the oracle.
			ref := newRig(t, "", opts...)
			want, err := ref.runOn(t, crashClone(seeded))
			if err != nil {
				t.Fatalf("uninterrupted boot: %v\n%s", err, ref.logs)
			}
			if want.State != "done" || len(want.Left) != 0 || len(want.Journal) != n-1 || len(want.Quarantine) != 1 {
				t.Fatalf("the oracle is wrong: state %q left %d journal %d quarantine %d", want.State, len(want.Left), len(want.Journal), len(want.Quarantine))
			}

			for _, m := range modes {
				for i := range n {
					t.Run(fmt.Sprintf("%s/row_%02d", m.name, i), func(t *testing.T) {
						r := newRig(t, "", opts...)
						fsys := crashClone(seeded)
						var cut *vfs.MemFS // the power-cut clone, when the mode takes one

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
							t.Fatalf("state %q, %d syslog: rows left", got.State, len(got.Left))
						}
						if !reflect.DeepEqual(got.Journal, want.Journal) {
							t.Errorf("the journal differs from an uninterrupted boot's: %d records, want %d\n%s", len(got.Journal), len(want.Journal), diffJournal(got.Journal, want.Journal))
						}
						if !reflect.DeepEqual(got.Quarantine, want.Quarantine) {
							t.Errorf("quarantine = %+v, want %+v", got.Quarantine, want.Quarantine)
						}
						perSource := map[string]int{}
						for _, j := range got.Journal {
							perSource[j.SourceID]++
						}
						for _, k := range keys {
							if perSource[k] > 1 {
								t.Errorf("%s has %d journal records", k, perSource[k])
							}
						}

						// Import calls: the chunk holding the kill is the one whose
						// work is lost, so the rows of it that were already imported
						// are imported again after the resume, once.
						chunkStart := i / chunk * chunk
						importedEnd := i // rows [0, importedEnd) were stepped before the kill
						if !m.before {
							importedEnd = i + 1
						}
						for k, key := range keys {
							wantCalls := 1
							if k == quarantinedAt {
								wantCalls = 0 // quarantined, never imported
							}
							if k >= chunkStart && k < importedEnd && k != quarantinedAt {
								wantCalls = 2
							}
							if c := r.j.callsFor(key); c != wantCalls {
								t.Errorf("row %d: %d Import calls, want %d", k, c, wantCalls)
							}
						}
						// Verify's counts are the resumed boot's own: it steps the rows
						// from the first chunk that did not commit, no earlier one.
						wantRead := int64(n - chunkStart)
						wantQuarantined := int64(0)
						if quarantinedAt >= chunkStart {
							wantQuarantined = 1
						}
						if v := got.Verify; v.Read != wantRead || v.Quarantined != wantQuarantined || v.Written != wantRead-wantQuarantined || len(v.Failures) != 0 {
							t.Errorf("verify = %+v, want read %d, written %d, quarantined %d", v, wantRead, wantRead-wantQuarantined, wantQuarantined)
						}
					})
				}
			}
		})
	}
}

// diffJournal names the first few records two journals disagree on.
func diffJournal(got, want map[string]journaled) string {
	var out string
	shown := 0
	for _, k := range slices.Sorted(maps.Keys(want)) {
		if g, ok := got[k]; !ok || g != want[k] {
			out += fmt.Sprintf("  %s: got %+v (present %v), want %+v\n", k, g, ok, want[k])
			if shown++; shown == 5 {
				return out
			}
		}
	}
	for _, k := range slices.Sorted(maps.Keys(got)) {
		if _, ok := want[k]; !ok {
			out += fmt.Sprintf("  %s: unexpected %+v\n", k, got[k])
			if shown++; shown == 5 {
				return out
			}
		}
	}
	return out
}
