// file: core/kernel/dryrun_estimate_test.go
// version: 1.0.0
// guid: b24fe9c3-d4d8-485a-9234-24234d3a08b1
// last-edited: 2026-11-11

package kernel

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"github.com/falkcorp/mediacore/tasks"
)

// estimateMigration is the cutover fixture's step shaped for --estimate,
// which has no batch to give an importer: it counts and writes through the
// StepWriter's own methods only, holds one row and fails on one when asked.
func estimateMigration(failOn string) datamigrate.DataMigration {
	return datamigrate.DataMigration{
		ID: "gcpd.case-files", Owner: "gcpd", Description: "estimate the GCPD's case files",
		Source:      []storage.Range{{Start: []byte("case:"), End: []byte("case;")}},
		Parallelism: tasks.Fixed(1, "test: one shard"), Mode: datamigrate.BootBlocking,
		Compat: storage.Breaking, Cutover: true, Sensitive: []string{"case:vault/"},
		Preflight: func(_ context.Context, r storage.Reader) ([]datamigrate.Blocker, error) {
			if _, err := r.Get(context.Background(), []byte("case:file/007")); err != nil {
				return nil, err
			}
			return []datamigrate.Blocker{{Key: []byte("case:file/007"), Family: "case", Reason: "Two-Face flipped it", Fix: "storage cutover-blockers delete"}}, nil
		},
		Step: func(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
			k := string(kv.Key)
			if k == failOn {
				return errors.New("Scarecrow gas")
			}
			switch {
			case strings.HasPrefix(k, "case:vault/"):
				return w.Quarantine(kv.Key, kv.Value, "a credential the importer does not carry")
			case k == "case:file/003":
				w.Hold(kv.Key, "waiting on Commissioner Gordon")
				return nil
			}
			if err := w.Set([]byte("case/"+k), kv.Value); err != nil {
				return err
			}
			w.Count("imported")
			return nil
		},
	}
}

// TestDryRunEstimateCountsOverTheLiveStoreWithoutWriting (05-storage
// §5S.3.6 "Dry run", --estimate): over the live store opened read-only, with
// every write discarded, the report is labelled an estimate and carries the
// cutover preflight's blockers, per outcome the counts and first keys, the
// held and would-quarantine rows with reasons, the bytes the step would
// stage, and never a value; the live directory is byte-for-byte unchanged and
// no scratch is made.
func TestDryRunEstimateCountsOverTheLiveStoreWithoutWriting(t *testing.T) {
	h, c := dryRunHost(t, cutoverRows(12, 2))
	before := treeOf(t, h.db)
	rep, err := DryRun(h.ctx, c, DryRunOptions{
		Estimate: true, FirstN: 2,
		Migrations: []datamigrate.DataMigration{estimateMigration("")},
	})
	if err != nil {
		t.Fatalf("estimate: %v (report %+v)", err, rep)
	}
	if after := treeOf(t, h.db); !maps.Equal(before, after) {
		t.Errorf("the estimate changed the live directory:\nbefore %v\nafter  %v", before, after)
	}
	if !rep.Estimate || rep.Err != "" || len(rep.Steps) != 1 || rep.Took <= 0 {
		t.Fatalf("report = %+v", rep)
	}
	if len(rep.Blockers) != 1 || rep.Blockers[0].Step != "gcpd.case-files" || rep.Blockers[0].Reason != "Two-Face flipped it" {
		t.Errorf("blockers = %+v, want the preflight's one, named for the step", rep.Blockers)
	}
	s := rep.Steps[0]
	if s.ID != "gcpd.case-files" || s.State != "estimate" {
		t.Errorf("step = %s in state %s, want gcpd.case-files in state estimate", s.ID, s.State)
	}
	// 12 files: file 3 is held, 11 imported; 2 vaults quarantined.
	wantItems := map[string]int64{"imported": 11, datamigrate.OutcomeHeld: 1, datamigrate.OutcomeQuarantined: 2}
	if !maps.Equal(s.Items, wantItems) {
		t.Errorf("items = %v, want %v", s.Items, wantItems)
	}
	if got := s.First["imported"]; len(got) != 2 {
		t.Errorf("first imported keys = %v, want the first 2 (FirstN)", got)
	}
	if len(s.Held) != 1 || s.Held[0].Reason != "waiting on Commissioner Gordon" || !strings.Contains(s.Held[0].Key, "case:file/003") {
		t.Errorf("held = %+v", s.Held)
	}
	if len(s.Quarantine) != 2 || s.Quarantine[0].Reason != "a credential the importer does not carry" {
		t.Errorf("would-quarantine = %+v", s.Quarantine)
	}
	if s.Bytes <= 0 {
		t.Errorf("bytes staged = %d, want the Set writes counted", s.Bytes)
	}
	for _, line := range rep.Lines() {
		if strings.Contains(line, "safe combination") || strings.Contains(line, "Long Halloween") {
			t.Errorf("report line %q shows a value", line)
		}
	}
}

// TestDryRunEstimateReportsAFailingStepAndKeepsGoing: a step that fails on a
// row ends that step's estimate as estimate-failed with the error in the
// report naming the step; the estimate still returns its report, not an
// error, so the operator sees what was counted before it.
func TestDryRunEstimateReportsAFailingStepAndKeepsGoing(t *testing.T) {
	h, c := dryRunHost(t, cutoverRows(6, 0))
	m := estimateMigration("case:file/004")
	m.Preflight = nil
	rep, err := DryRun(h.ctx, c, DryRunOptions{Estimate: true, Migrations: []datamigrate.DataMigration{m}})
	if err != nil {
		t.Fatalf("estimate = %v, want the failure in the report only", err)
	}
	if len(rep.Steps) != 1 || rep.Steps[0].State != "estimate-failed" {
		t.Fatalf("steps = %+v, want one estimate-failed", rep.Steps)
	}
	if !strings.Contains(rep.Err, "gcpd.case-files") || !strings.Contains(rep.Err, "Scarecrow gas") {
		t.Errorf("report error = %q, want the step and the cause", rep.Err)
	}
	if got := rep.Steps[0].Items["imported"]; got != 2 {
		t.Errorf("imported before the failure = %d, want 2 (files 1 and 2; 3 is held)", got)
	}
	if lines := rep.Lines(); !strings.HasPrefix(lines[len(lines)-2], "stopped: gcpd.case-files") {
		t.Errorf("report does not end with the stop reason: %q", lines)
	}
}

// TestDryRunEstimateRefusesASourceNoOwnerHolds: a migration whose owner holds
// neither a keyspace nor a legacy range for its Source stops the estimate
// with an error rather than reporting a clean count of nothing.
func TestDryRunEstimateRefusesASourceNoOwnerHolds(t *testing.T) {
	h, c := dryRunHost(t, cutoverRows(2, 0))
	m := estimateMigration("")
	m.Owner, m.Preflight = "robin", nil
	_, err := DryRun(h.ctx, c, DryRunOptions{Estimate: true, Migrations: []datamigrate.DataMigration{m}})
	if err == nil || !strings.Contains(err.Error(), "robin") {
		t.Fatalf("estimate over an owner with no source = %v, want an error naming robin", err)
	}
}

// callLog is a StepWriter's chunk that records the calls it received, so a
// test sees exactly what the dry run's wrapper forwarded to the runner.
type callLog struct{ calls []string }

func (c *callLog) log(f string, a ...any) { c.calls = append(c.calls, fmt.Sprintf(f, a...)) }

func (c *callLog) Set(k, v []byte) error { c.log("set %s=%s", k, v); return nil }
func (c *callLog) Delete(k []byte) error { c.log("delete %s", k); return nil }
func (c *callLog) SetIfAbsent(k, v []byte) error {
	c.log("setifabsent %s=%s", k, v)
	return nil
}
func (c *callLog) SetIfUnchanged(k, e, v []byte) error {
	c.log("setifunchanged %s %s=%s", k, e, v)
	return nil
}
func (c *callLog) DeleteIfUnchanged(k, e []byte) error {
	c.log("deleteifunchanged %s %s", k, e)
	return nil
}
func (c *callLog) Hold(k []byte, r string) { c.log("hold %s %s", k, r) }
func (c *callLog) Count(o string)          { c.log("count %s", o) }
func (c *callLog) Quarantine(k, raw []byte, r string) error {
	c.log("quarantine %s %s", k, r)
	return nil
}
func (c *callLog) Batch() *storage.Batch { return nil }

// TestDryRunRecorderForwardsEveryWriteAndRecordsThem: wrapping a migration
// for the dry run changes nothing the runner sees (every write, hold, count
// and quarantine reaches the chunk unchanged, in order) while the recorder
// keeps the bytes staged per step, the first keys per outcome capped at
// FirstN, and the held items with reasons.
func TestDryRunRecorderForwardsEveryWriteAndRecordsThem(t *testing.T) {
	rec := &dryRunRecorder{first: 2, steps: map[string]*storage.DryRunStep{}}
	m := datamigrate.DataMigration{ID: "gcpd.case-files", Step: func(_ context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
		if err := w.Set([]byte("a"), []byte("1")); err != nil { // 2 bytes
			return err
		}
		if err := w.SetIfAbsent([]byte("b"), []byte("22")); err != nil { // 3
			return err
		}
		if err := w.SetIfUnchanged([]byte("c"), []byte("old"), []byte("333")); err != nil { // 4
			return err
		}
		if err := w.DeleteIfUnchanged([]byte("dd"), []byte("old")); err != nil { // 2
			return err
		}
		if err := w.Delete([]byte("eee")); err != nil { // 3
			return err
		}
		w.Hold(kv.Key, "waiting on Commissioner Gordon")
		w.Count("imported")
		return w.Quarantine(kv.Key, []byte("raw"), "Joker venom")
	}}
	chunk := &callLog{}
	wrapped := rec.wrap([]datamigrate.DataMigration{m})
	for _, key := range []string{"case:1", "case:2", "case:3"} {
		w := datamigrate.NewStepWriter(chunk, chunk)
		if err := wrapped[0].Step(context.Background(), storage.KV{Key: []byte(key)}, w); err != nil {
			t.Fatal(err)
		}
	}
	if len(chunk.calls) != 3*8 || chunk.calls[0] != "set a=1" || chunk.calls[5] != "hold case:1 waiting on Commissioner Gordon" ||
		chunk.calls[6] != "count imported" || chunk.calls[7] != "quarantine case:1 Joker venom" {
		t.Fatalf("the chunk received %d calls, first 8 = %q", len(chunk.calls), chunk.calls[:8])
	}
	s := rec.steps["gcpd.case-files"]
	if s == nil {
		t.Fatal("the recorder saw no step")
	}
	if s.Bytes != 3*(2+3+4+2+3) {
		t.Errorf("bytes = %d, want %d", s.Bytes, 3*(2+3+4+2+3))
	}
	if len(s.Held) != 3 || s.Held[2].Reason != "waiting on Commissioner Gordon" {
		t.Errorf("held = %+v", s.Held)
	}
	for outcome, want := range map[string][]string{
		"imported":                     {`"case:1"`, `"case:2"`},
		datamigrate.OutcomeQuarantined: {`"case:1"`, `"case:2"`},
		datamigrate.OutcomeHeld:        {`"case:1"`, `"case:2"`},
	} {
		if got := s.First[outcome]; !slices.Equal(got, want) {
			t.Errorf("first %s keys = %v, want the first 2: %v", outcome, got, want)
		}
	}
}

// TestDiscardSinkDiscardsEveryWriteButCountsThem: --estimate's StepWriter
// keeps nothing and gives an importer no batch; it totals the bytes each
// write would have staged, skips an empty outcome, lists held and
// quarantined rows and caps the first keys per outcome.
func TestDiscardSinkDiscardsEveryWriteButCountsThem(t *testing.T) {
	d := &discardSink{step: &storage.DryRunStep{ID: "gcpd.case-files", Items: map[string]int64{}, First: map[string][]string{}}, first: 1}
	w := datamigrate.NewStepWriter(d, d)
	d.key = []byte("case:1")
	for _, err := range []error{
		w.Set([]byte("a"), []byte("1")),
		w.SetIfAbsent([]byte("b"), []byte("22")),
		w.SetIfUnchanged([]byte("c"), []byte("old"), []byte("333")),
		w.DeleteIfUnchanged([]byte("dd"), []byte("old")),
		w.Delete([]byte("eee")),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	w.Count("")
	w.Count("imported")
	d.key = []byte("case:2")
	w.Count("imported")
	w.Hold([]byte("case:3"), "waiting on Commissioner Gordon")
	if err := w.Quarantine([]byte("case:4"), []byte("raw"), "Joker venom"); err != nil {
		t.Fatal(err)
	}
	if w.Batch() != nil {
		t.Error("an estimate handed an importer a batch")
	}
	s := d.step
	if s.Bytes != 2+3+4+2+3 {
		t.Errorf("bytes = %d, want %d", s.Bytes, 2+3+4+2+3)
	}
	if s.Items["imported"] != 2 || s.Items[datamigrate.OutcomeHeld] != 1 || s.Items[datamigrate.OutcomeQuarantined] != 1 || len(s.Items) != 3 {
		t.Errorf("items = %v (an empty outcome must not be counted)", s.Items)
	}
	if got := s.First["imported"]; len(got) != 1 || got[0] != `"case:1"` {
		t.Errorf("first imported = %v, want just the first key (cap 1)", got)
	}
	if len(s.Held) != 1 || s.Held[0].Reason != "waiting on Commissioner Gordon" || len(s.Quarantine) != 1 || s.Quarantine[0].Reason != "Joker venom" {
		t.Errorf("held %+v, quarantine %+v", s.Held, s.Quarantine)
	}
}
