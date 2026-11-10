// file: core/internal/storagemigrate/modes_test.go
// version: 1.0.0
// guid: e528233b-da81-41a8-ac2d-88da078cb86c
// last-edited: 2026-11-10

package storagemigrate_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/coretest/crashmatrix"
	"github.com/falkcorp/mediacore/internal/storagemigrate"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
)

// failsWith boots e with m and wants the boot to fail with the step's
// StepError wrapping want, nothing of the chunk committed.
func failsWith(t *testing.T, e *env, m datamigrate.DataMigration, want error) {
	t.Helper()
	db, err := e.boot(context.Background(), e.runner(storagemigrate.Options{}, m))
	var se *storage.StepError
	if !errors.Is(err, want) || !errors.As(err, &se) || se.Step.DataMigration != m.ID {
		t.Fatalf("boot = %v, want %s's StepError wrapping %v", err, m.ID, want)
	}
	sks, _ := db.Keyspace("core/storage")
	if _, ok, err := storagemigrate.CursorOf(context.Background(), sks, m.ID, 0); ok || err != nil {
		t.Fatalf("a cursor committed (%v, %v) with a refused chunk", ok, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestStepWriterModeRules: Quarantine and Batch are refused outside a
// Cutover step and Hold inside one; a refusal fails the chunk even when
// Step ignores the error it was handed.
func TestStepWriterModeRules(t *testing.T) {
	t.Run("Quarantine outside a cutover", func(t *testing.T) {
		m := recount(sequential())
		m.Step = func(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
			_ = w.Quarantine(kv.Key, kv.Value, "ignored by a careless step")
			return nil
		}
		failsWith(t, newEnv(t, 5), m, datamigrate.ErrCutoverOnly)
	})
	t.Run("Batch outside a cutover", func(t *testing.T) {
		m := recount(sequential())
		m.Step = func(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
			_ = w.Batch()
			return nil
		}
		failsWith(t, newEnv(t, 5), m, datamigrate.ErrCutoverOnly)
	})
	cut := gothamCutover()
	cut.Rows = 20
	legacy := func(t *testing.T) *env {
		e := &env{t: t, fs: newMem(), owners: cutoverOwners(cut)}
		cut.Seed(t, e.fs)
		return e
	}
	cutStep := func(step func(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error) datamigrate.DataMigration {
		return datamigrate.DataMigration{ID: "gotham.cutover", Owner: "gotham", Source: []storage.Range{keyRange("case:", "case;")},
			Parallelism: sequential(), Mode: datamigrate.BootBlocking, Compat: storage.Breaking, Cutover: true, Step: step}
	}
	t.Run("Hold in a cutover", func(t *testing.T) {
		failsWith(t, legacy(t), cutStep(func(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
			w.Hold(kv.Key, "Bane broke the case file")
			return nil
		}), datamigrate.ErrHoldInCutover)
	})
	t.Run("ignored Quarantine without a writer", func(t *testing.T) {
		failsWith(t, legacy(t), cutStep(func(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
			_ = w.Quarantine(kv.Key, kv.Value, "ignored by a careless step")
			return w.Delete(kv.Key)
		}), datamigrate.ErrQuarantineNotWired)
	})
	t.Run("Set in a retired claim", func(t *testing.T) {
		failsWith(t, legacy(t), cutStep(func(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
			return w.Set(kv.Key, []byte("the Penguin's rewrite"))
		}), storage.ErrClaimMode)
	})
	t.Run("sensitive quarantine", func(t *testing.T) {
		e := legacy(t)
		var flagged, plain int
		m := cutStep(func(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
			return w.Quarantine(kv.Key, kv.Value, "Scarecrow toxin")
		})
		m.Sensitive = []string{"case:0001"}
		r := e.runner(storagemigrate.Options{Quarantine: func(_ context.Context, b *storage.Batch, step string, key, raw []byte, reason string, sensitive bool) error {
			if step != "gotham.cutover" || !strings.HasPrefix(string(key), "case:") || reason != "Scarecrow toxin" {
				return fmt.Errorf("writer called with %s %q %q", step, key, reason)
			}
			if sensitive {
				flagged++
			} else {
				plain++
			}
			return nil
		}}, m)
		e.mustBoot(r)
		if flagged != 10 || plain != 10 {
			t.Fatalf("flagged %d sensitive and %d plain rows, want 10 of each (case:00010..19 under case:0001)", flagged, plain)
		}
		db := readOnly(t, e.fs, cut)
		lr, _ := db.Legacy("gotham")
		for kv, err := range lr.Scan(context.Background(), storage.ScanOptions{Start: []byte("case:"), End: []byte("case;")}) {
			if err != nil {
				t.Fatal(err)
			}
			t.Fatalf("quarantined row %s not deleted with its record", kv.Key)
		}
	})
}

// TestStepWriterPreconditions: SetIfAbsent, SetIfUnchanged and
// DeleteIfUnchanged stage Expect on a Serialized range and commit with the
// chunk; a precondition that fails at commit fails the step, and one on a
// range that is not Serialized is refused.
func TestStepWriterPreconditions(t *testing.T) {
	serialized := gotham()
	serialized.Spec.Serialized = []string{"item/"}
	t.Run("commit", func(t *testing.T) {
		e := newEnv(t, 12)
		e.owners = []storage.OwnerSpec{serialized}
		m := recount(sequential())
		m.RewritesInPlace = true
		m.Step = func(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
			var n int
			if _, err := fmt.Sscanf(string(kv.Key), "item/%05d", &n); err != nil {
				return err
			}
			switch n % 3 {
			case 0:
				return w.DeleteIfUnchanged(kv.Key, kv.Value)
			case 1:
				return w.SetIfUnchanged(kv.Key, kv.Value, []byte("1"))
			}
			if err := w.SetIfUnchanged(kv.Key, kv.Value, []byte("1")); err != nil {
				return err
			}
			return w.SetIfAbsent([]byte(fmt.Sprintf("item/%05d-new", n)), []byte("1"))
		}
		e.mustBoot(e.runner(storagemigrate.Options{ChunkItems: 5}, m))
		ks, _ := e.view(e.fs)
		got := 0
		for kv, err := range ks.Scan(context.Background(), storage.ScanOptions{Start: []byte("item/"), End: []byte("item0")}) {
			if err != nil {
				t.Fatal(err)
			}
			if string(kv.Value) != "1" {
				t.Fatalf("%s = %q, want 1", kv.Key, kv.Value)
			}
			got++
		}
		if got != 12 { // 4 deleted, 8 rewritten, 4 new
			t.Fatalf("%d item keys, want 12", got)
		}
	})
	t.Run("conflict", func(t *testing.T) {
		e := newEnv(t, 6)
		e.owners = []storage.OwnerSpec{serialized}
		m := recount(sequential())
		m.Step = func(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
			return w.SetIfUnchanged(kv.Key, []byte("the Riddler's guess"), []byte("1"))
		}
		failsWith(t, e, m, storage.ErrConflict)
	})
	t.Run("not serialized", func(t *testing.T) {
		m := recount(sequential())
		m.Step = func(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
			return w.SetIfAbsent(kv.Key, []byte("1"))
		}
		failsWith(t, newEnv(t, 4), m, storage.ErrNotSerialized)
	})
}

// cutoverOwners is c's cutover build: its claim retired.
func cutoverOwners(c crashmatrix.SyntheticCutover) []storage.OwnerSpec {
	spec := c.Spec
	spec.Keyspace = true
	spec.Claims = []storage.Claim{{Prefix: "case:", Mode: storage.ClaimRetired}}
	return []storage.OwnerSpec{{Owner: c.Owner, Spec: spec, Enabled: true}}
}
