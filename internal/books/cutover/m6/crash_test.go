// file: internal/books/cutover/m6/crash_test.go
// version: 1.0.1
// guid: e2a74c19-5b60-4d83-8f1e-0c9d3b6a7e45
// last-edited: 2026-11-19

package m6_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
)

// flatten is every row a boot left that the tests compare, one string per row,
// so two boots' results diff as sets. The quarantine records' secret names carry
// a sequence number a resumed boot may assign differently, so sealed values are
// compared by value, not by name.
func flatten(s *snap) []string {
	var out []string
	for k, v := range s.Legacy {
		if !strings.HasPrefix(k, "preference:") && k != "counter:preference" {
			out = append(out, fmt.Sprintf("legacy %q=%q", k, v))
		}
	}
	for owner, rows := range s.Rows {
		for k, v := range rows {
			if owner == string(ownerSecrets) && strings.HasPrefix(k, "core.storage.quarantine.") {
				k = "core.storage.quarantine.<n>"
			}
			out = append(out, fmt.Sprintf("%s %q=%q", owner, k, v))
		}
	}
	for _, q := range s.Quarantine {
		out = append(out, fmt.Sprintf("quarantine %q %q %q sealed=%v value=%q", q.Key, q.Reason, q.Step, q.Sealed, q.Value))
	}
	slices.Sort(out)
	return out
}

func diff(got, want []string) string {
	gs, ws := map[string]int{}, map[string]int{}
	for _, g := range got {
		gs[g]++
	}
	for _, w := range want {
		ws[w]++
	}
	var out []string
	for _, k := range slices.Sorted(maps.Keys(ws)) {
		if gs[k] != ws[k] {
			out = append(out, fmt.Sprintf("  want %dx, got %dx: %s", ws[k], gs[k], k))
		}
	}
	for _, k := range slices.Sorted(maps.Keys(gs)) {
		if _, ok := ws[k]; !ok {
			out = append(out, fmt.Sprintf("  unexpected %dx: %s", gs[k], k))
		}
	}
	if len(out) > 8 {
		out = append(out[:8], fmt.Sprintf("  ... and %d more", len(out)-8))
	}
	return strings.Join(out, "\n")
}

// TestCutoverCrashAtEveryChunk is M6's cell of the cutover crash matrix
// (05-storage section 5S.3.13). The step is killed at every row the shard
// visits, in four ways, at two chunk sizes, and the resumed boot must end
// exactly where an uninterrupted boot ends: every legacy row, every native row,
// every audit record and every quarantine record the same, none doubled and none
// lost (I1), the secrets store holding the same names.
//
// The four kills are the two places a process can die around a row's work and
// the two ways of dying there: the step returns an error before or after the
// row's work (the boot fails, the store is as the failed boot left it), or the
// filesystem is cloned at that point as a power cut would leave it (only what
// was synced) and the clone is booted. After the row's work and before its
// chunk's commit is the window the importers' create-only idempotence is for:
// the native rows were staged, the source rows' deletes were not durable, and
// the resumed boot imports the row again without a conflict.
//
// M6's rows are not independent: one visit to an aijob: row pulls its payload
// and batch-index rows forward, and the first visit to the config group converts
// and deletes all of it. A kill on such a visit loses that whole chunk, which is
// what makes this the cell that matters.
func TestCutoverCrashAtEveryChunk(t *testing.T) {
	// The renamed fixture adds a source whose credentials are carried, one of
	// them unreadable, so a resumed boot is shown not to see its own carried
	// secret or marker as an orphan under the new name: the carry is staged in
	// the chunk batch that converts and deletes the blob, so a resume either
	// re-reads the blob with nothing carried or never reads it again.
	for _, fx := range []struct {
		name      string
		opts      []func(*rig)
		instances int
		carried   map[string]string
	}{
		{"head", nil, len(wantInstances), nil},
		{"renamed", []func(*rig){renamedSource, func(r *rig) {
			r.secrets.seed["integrations.metadata.batcave_db.credentials.token"] = "fixture-batcave-db-token-fox"
			delete(r.secrets.seed, "integrations.metadata.batcave_db.credentials.api_key")
			r.secrets.unreadable["integrations.metadata.batcave_db.credentials.api_key"] = true
		}}, len(wantInstances) + 1, map[string]string{
			"integrations.metadata.batcave-db.credentials.token":   "fixture-batcave-db-token-fox",
			"integrations.metadata.batcave-db.credentials.api_key": unreadableMarker + "corrupt",
		}},
	} {
		t.Run(fx.name, func(t *testing.T) { crashAtEveryChunk(t, fx.opts, fx.instances, fx.carried) })
	}
}

// crashAtEveryChunk kills the step at every row visit in four ways and checks
// the resumed boot matches an uninterrupted one. carried names the secrets the
// oracle must hold, so a fixture whose carry silently did nothing cannot pass
// by both runs agreeing on nothing.
func crashAtEveryChunk(t *testing.T, extra []func(*rig), wantN int, carried map[string]string) {
	type mode struct {
		name          string
		before, power bool
	}
	modes := []mode{
		{"error_before_row", true, false},
		{"error_after_row", false, false},
		{"power_cut_before_row", true, true},
		{"power_cut_after_row", false, true},
	}
	var oracles [][]string
	for _, chunk := range []int{1, 4} {
		t.Run("chunk_"+strconv.Itoa(chunk), func(t *testing.T) {
			opts := append([]func(*rig){withRows(headRows(t))}, extra...)
			opts = append(opts, func(r *rig) { r.chunkItems = chunk })
			seeded := newRig(t, opts...).seed(t)

			// The uninterrupted boot is the oracle, and counts the rows the shard visits.
			ref := newRig(t, opts...)
			dm := ref.migration()
			inner := dm.Step
			var visits atomic.Int64
			dm.Step = func(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
				visits.Add(1)
				return inner(ctx, kv, w)
			}
			want, err := ref.runWith(t, crashClone(seeded), dm)
			if err != nil {
				t.Fatalf("uninterrupted boot: %v\n%s", err, ref.logs)
			}
			if want.State != "done" || len(want.Verify.Failures) != 0 || len(instances(t, want)) != wantN || len(jobs(t, want)) != 7 {
				t.Fatalf("the oracle is wrong: state %q verify %+v", want.State, want.Verify)
			}
			for name, v := range carried {
				if got := string(want.Rows[string(ownerSecrets)][name]); got != v {
					t.Fatalf("the oracle holds %q under %s, want %q: the fixture does not exercise the carry", got, name, v)
				}
			}
			wantRows := flatten(want)
			oracles = append(oracles, wantRows)
			n := int(visits.Load())
			if n < 10 {
				t.Fatalf("the shard visited %d rows; the fixture should visit at least 10", n)
			}

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
						if got.State != "done" || len(got.Verify.Failures) != 0 {
							t.Fatalf("state %q verify %+v", got.State, got.Verify)
						}
						if d := diff(flatten(got), wantRows); d != "" {
							t.Errorf("the resumed boot differs from an uninterrupted one:\n%s", d)
						}
					})
				}
			}
		})
	}
	if len(oracles) == 2 && !slices.Equal(oracles[0], oracles[1]) {
		t.Errorf("chunk size changed the result:\n%s", diff(oracles[1], oracles[0]))
	}
}
