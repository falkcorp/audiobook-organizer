// file: core/storage/datamigrate/datamigrate_test.go
// version: 1.0.1
// guid: 72299917-b231-46be-ba88-bdff57fcb1e0
// last-edited: 2026-11-10

package datamigrate_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"github.com/falkcorp/mediacore/tasks"
)

// sink records every call a StepWriter forwards. It is a Sink and a
// CutoverSink, as the runner's chunk is.
type sink struct{ calls []string }

func (s *sink) Set(k, v []byte) error { s.calls = append(s.calls, "set "+string(k)); return nil }
func (s *sink) Delete(k []byte) error { s.calls = append(s.calls, "delete "+string(k)); return nil }
func (s *sink) SetIfAbsent(k, v []byte) error {
	s.calls = append(s.calls, "absent "+string(k))
	return nil
}
func (s *sink) SetIfUnchanged(k, e, v []byte) error {
	s.calls = append(s.calls, "unchanged "+string(k))
	return nil
}
func (s *sink) DeleteIfUnchanged(k, e []byte) error {
	s.calls = append(s.calls, "delete-unchanged "+string(k))
	return nil
}
func (s *sink) Hold(k []byte, reason string) { s.calls = append(s.calls, "hold "+string(k)+" "+reason) }
func (s *sink) Count(o string)               { s.calls = append(s.calls, "count "+o) }
func (s *sink) Quarantine(k, raw []byte, reason string) error {
	s.calls = append(s.calls, "quarantine "+string(k))
	return datamigrate.ErrQuarantineNotWired
}
func (s *sink) Batch() *storage.Batch { s.calls = append(s.calls, "batch"); return nil }

// TestStepWriterForwardsToItsChunk: every method reaches the runner's
// chunk, and a StepWriter no runner built panics rather than drop a write.
func TestStepWriterForwardsToItsChunk(t *testing.T) {
	s := &sink{}
	w := datamigrate.NewStepWriter(s, s)
	k := []byte("case:1")
	_ = w.Set(k, nil)
	_ = w.Delete(k)
	_ = w.SetIfAbsent(k, nil)
	_ = w.SetIfUnchanged(k, nil, nil)
	_ = w.DeleteIfUnchanged(k, nil)
	w.Hold(k, "Mr. Freeze")
	w.Count("converted")
	if err := w.Quarantine(k, nil, "Poison Ivy"); !errors.Is(err, datamigrate.ErrQuarantineNotWired) {
		t.Fatalf("Quarantine = %v, want the chunk's error", err)
	}
	_ = w.Batch()
	want := []string{"set case:1", "delete case:1", "absent case:1", "unchanged case:1", "delete-unchanged case:1",
		"hold case:1 Mr. Freeze", "count converted", "quarantine case:1", "batch"}
	if !slices.Equal(s.calls, want) {
		t.Fatalf("calls = %q, want %q", s.calls, want)
	}
	var zero datamigrate.StepWriter
	half := datamigrate.NewStepWriter(s, nil) // no CutoverSink
	for name, call := range map[string]func(){
		"zero Set":        func() { _ = zero.Set(k, nil) },
		"zero Batch":      func() { _ = zero.Batch() },
		"half Quarantine": func() { _ = half.Quarantine(k, nil, "Two-Face") },
		"half Batch":      func() { _ = half.Batch() },
	} {
		func() {
			defer func() {
				if r := recover(); r == nil || !strings.Contains(r.(string), "outside a runner's chunk") {
					t.Errorf("%s: recovered %v, want the outside-a-chunk panic", name, r)
				}
			}()
			call()
		}()
	}
}

func TestModeAndStates(t *testing.T) {
	for m, want := range map[datamigrate.Mode]string{datamigrate.BootBlocking: "boot-blocking", datamigrate.Online: "online", 0: "invalid(0)", 7: "invalid(7)"} {
		if got := m.String(); got != want {
			t.Errorf("Mode(%d) = %q, want %q", m, got, want)
		}
	}
	if datamigrate.StateDone != storage.DataMigrationDone {
		t.Fatal("the done state is not the one storage's DAG reads")
	}
	if got := datamigrate.States(); len(got) != 6 || !slices.Contains(got, datamigrate.StateHeld) || !slices.Contains(got, datamigrate.StatePending) {
		t.Fatalf("States = %v", got)
	}
}

// TestDerivedBuildTask: the rebuild task of a derived index is a Task of
// kind <module>.derived.<name>.build, read-only over its reads, restarted
// from zero, 24 h by default; a missing part is a registration problem.
func TestDerivedBuildTask(t *testing.T) {
	reads := []entity.Ref{{Module: "gotham", Kind: "case"}}
	d := datamigrate.DerivedBuildTask("gotham", "hnsw", datamigrate.DerivedBuildSpec{
		Title: "Rebuild the Batcomputer's case index", Reads: reads, PermStart: "gotham:index.rebuild",
		Run: func(*tasks.RC) error { return nil },
	})
	if p := tasks.Problems(d); len(p) != 0 {
		t.Fatalf("problems = %v", p)
	}
	if d.Kind() != "gotham.derived.hnsw.build" || d.Shape() != tasks.ShapeTask {
		t.Fatalf("kind %q shape %v", d.Kind(), d.Shape())
	}
	c := tasks.CommonOf(d)
	if c.Resume != tasks.ResumeFromZero || c.Timeout != 24*time.Hour || !c.Effects.IsReadOnly() || c.Perms.Start.IsZero() {
		t.Fatalf("common = %+v", c)
	}
	d = datamigrate.DerivedBuildTask("gotham", "hnsw", datamigrate.DerivedBuildSpec{Title: "t", PermStart: "gotham:x", Timeout: time.Hour,
		Run: func(*tasks.RC) error { return nil }})
	if tasks.CommonOf(d).Timeout != time.Hour {
		t.Fatal("an explicit Timeout was replaced")
	}
	bad := datamigrate.DerivedBuildTask("Gotham", "hnsw", datamigrate.DerivedBuildSpec{})
	if p := tasks.Problems(bad); len(p) < 3 {
		t.Fatalf("problems of an empty spec under a bad module = %v, want the kind, Title, Perms.Start and Run", p)
	}
}
