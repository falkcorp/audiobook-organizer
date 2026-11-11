// file: internal/books/cutover/config_cutover_idempotent_test.go
// version: 1.0.0
// guid: 0e9b4d6a-71c3-4f85-9a2d-b36c58e1f407
// last-edited: 2026-11-11

package cutover

import (
	"bytes"
	"context"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/coretest/crashmatrix"
	"github.com/falkcorp/mediacore/storage/datamigrate"
)

// owned reports whether M2 reads or writes key: the settings rows. The rest
// of books' legacy range is what the seeding's own boot wrote (activity and
// operation rows under ULIDs minted at seed time, different in every seeded
// copy), and TestConfigCutover_RowsGone shows M2 leaves it alone.
func owned(key string) bool { return strings.HasPrefix(key, "setting:") }

// sameOutcome fails for any difference between two migrated stores that M2
// owns: the secrets, the unreadable markers, the staged keys, the quarantine
// records (by key, reason and value, not by sequence number) and every
// settings row left in books' legacy range.
func sameOutcome(t testing.TB, got, want *snapshot) {
	t.Helper()
	// A sealed quarantine record's secret is named by its sequence number,
	// which a crash can skip (the lease commits on its own); the records are
	// compared by content below.
	notQuarantine := func(m map[string]string) map[string]string {
		out := map[string]string{}
		for k, v := range m {
			if !strings.HasPrefix(k, "core.storage.quarantine.") {
				out[k] = v
			}
		}
		return out
	}
	if g, w := notQuarantine(got.Secrets), notQuarantine(want.Secrets); !maps.Equal(g, w) {
		t.Errorf("secrets differ: %v vs %v", slices.Sorted(maps.Keys(g)), slices.Sorted(maps.Keys(w)))
	}
	if !slices.Equal(slices.Sorted(maps.Keys(got.Unreadable)), slices.Sorted(maps.Keys(want.Unreadable))) {
		t.Errorf("unreadable markers differ: %v vs %v", slices.Sorted(maps.Keys(got.Unreadable)), slices.Sorted(maps.Keys(want.Unreadable)))
	}
	if len(got.Config) != len(want.Config) {
		t.Errorf("%d config rows, want %d", len(got.Config), len(want.Config))
	}
	for k, v := range want.Config {
		if g, ok := got.Config[k]; !ok || !jsonSame(g, v) {
			t.Errorf("config row %s differs (present %v)", k, ok)
		}
	}
	flat := func(s *snapshot) []string {
		var out []string
		for _, q := range s.Quarantine {
			out = append(out, q.Key+"|"+q.Reason+"|"+q.Value)
		}
		slices.Sort(out)
		return out
	}
	if g, w := flat(got), flat(want); !slices.Equal(g, w) {
		t.Errorf("quarantine records differ: %d vs %d", len(g), len(w))
	}
	for k, v := range want.Legacy {
		if !owned(k) {
			continue
		}
		if g, ok := got.Legacy[k]; !ok || !bytes.Equal(g, v) {
			t.Errorf("legacy key %s differs (present %v)", k, ok)
		}
	}
	for k := range got.Legacy {
		if _, ok := want.Legacy[k]; !ok && owned(k) {
			t.Errorf("legacy key %s should not exist", k)
		}
	}
}

// TestConfigCutover_Idempotent: M2a killed at every durable write of its
// boot, at every chunk boundary and inside each chunk, and booted again,
// ends in the store an uninterrupted run leaves, with its done record
// written: a rerun neither doubles a row nor loses one, and never meets its
// own leftovers (a create-only secret that would conflict, a blob stripped
// twice).
func TestConfigCutover_Idempotent(t *testing.T) {
	for _, c := range []struct {
		name, corpus string
		opts         []func(*rig)
	}{
		{"stage1", "stage1", []func(*rig){seedKnown}},
		{"overlay", "overlay", nil},
		// A chunk that could hold every key of M2's range, not one item.
		{"stage1-wide-chunk", "stage1", []func(*rig){seedKnown, func(r *rig) { r.chunkItems = 100 }}},
	} {
		t.Run(c.name, func(t *testing.T) {
			want, err := newRig(t, c.corpus, c.opts...).run(t)
			if err != nil {
				t.Fatalf("uninterrupted M2: %v", err)
			}
			r := newRig(t, c.corpus, c.opts...)
			crashmatrix.RunChunks(t, crashmatrix.ChunkScenario{
				Path:       rigStore,
				Seed:       func(t testing.TB, fs vfs.FS) { r.seed(t, fs) },
				Migrations: []datamigrate.DataMigration{r.migration()},
				Boot: func(ctx context.Context, fs vfs.FS, migs []datamigrate.DataMigration) error {
					return r.boot(ctx, fs, migs, nil)
				},
				Check: func(t testing.TB, fs vfs.FS) {
					var doneBefore bool
					r.preMigrate = func(ctx context.Context, e *bootEnv) error {
						sks, _ := e.db.Keyspace("core/storage")
						var err error
						doneBefore, err = ConfigCutoverDone(ctx, sks)
						return err
					}
					defer func() { r.preMigrate = nil }()
					got, err := r.runOn(t, fs)
					if err != nil {
						t.Fatalf("reading the migrated store: %v", err)
					}
					if !doneBefore || got.State != "done" {
						t.Errorf("M2a's record is not done after the boot completed (done before reading: %v, state %q)", doneBefore, got.State)
					}
					sameOutcome(t, got, want)
				},
				Inside: true,
			})
		})
	}
}
