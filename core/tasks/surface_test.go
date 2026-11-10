// file: core/tasks/surface_test.go
// version: 1.0.1
// guid: 2039e3f6-7680-4be3-9f26-2eb2f69e2519
// last-edited: 2026-11-10

package tasks_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/lifecycle"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

func TestOverlapsAndFormatRef(t *testing.T) {
	whole := tasks.Res("books", "book_files")
	a := tasks.ResID("books", "book_files", "a")
	b := tasks.ResID("books", "book_files", "b")
	other := tasks.ResID("books", "books", "a")
	for _, c := range []struct {
		x, y entity.Ref
		want bool
	}{{whole, a, true}, {a, whole, true}, {a, a, true}, {a, b, false}, {a, other, false}, {whole, whole, true},
		{whole, tasks.Res("music", "book_files"), false}} {
		if got := tasks.Overlaps(c.x, c.y); got != c.want {
			t.Fatalf("Overlaps(%v, %v) = %v", c.x, c.y, got)
		}
	}
	if tasks.FormatRef(whole) != "books/book_files" || tasks.FormatRef(a) != "books/book_files#a" {
		t.Fatalf("FormatRef: %q %q", tasks.FormatRef(whole), tasks.FormatRef(a))
	}
	var r tasks.Resource = a // an alias of entity.Ref (A4), not a new type
	if r != (entity.Ref{Module: "books", Kind: "book_files", ID: "a"}) {
		t.Fatal("Resource is not entity.Ref")
	}
}

func TestEffects_Sets(t *testing.T) {
	cases := tasks.Res("gotham", "cases")
	clues := tasks.Res("gotham", "clues")
	e := tasks.Writes(cases, cases).AlsoReads(clues).AlsoDeletes(clues)
	if !e.Declared() || e.IsReadOnly() {
		t.Fatal("Writes is not a declared writer")
	}
	if !slices.Equal(e.WriteSet(), []entity.Ref{cases, clues}) || !slices.Equal(e.ReadSet(), []entity.Ref{clues}) ||
		!slices.Equal(e.DeleteSet(), []entity.Ref{clues}) {
		t.Fatalf("sets: w %v r %v d %v", e.WriteSet(), e.ReadSet(), e.DeleteSet())
	}
	base := tasks.ReadOnly(cases)
	if more := base.AlsoReads(clues); len(more.ReadSet()) != 2 || len(base.ReadSet()) != 1 || !base.IsReadOnly() {
		t.Fatal("AlsoReads mutated its receiver")
	}
	var zero tasks.Effects
	if zero.Declared() {
		t.Fatal("zero Effects is declared")
	}
	if tasks.Key("core.storage.exclusive").Module() != "core" {
		t.Fatal("Key.Module")
	}
}

func TestLockRefs_HostOrRefused(t *testing.T) {
	if _, err := tasks.LockRefs(t.Context(), tasks.Res("gotham", "cases")); !errors.Is(err, tasks.ErrNoRunContext) {
		t.Fatalf("LockRefs on a plain ctx = %v", err)
	}
}

type checker struct{ perms []rbac.Permission }

func (c checker) Has(s rbac.Permission) bool      { return slices.Contains(c.perms, s) }
func (c checker) Satisfies(rbac.Requirement) bool { return false }
func (c checker) Confined() bool                  { return false }
func (c checker) Concrete() []rbac.Permission     { return c.perms }

func TestRedactRunView(t *testing.T) {
	v := tasks.RunView{ID: "r1", Kind: caseKind, Current: "selina.kyle@example.com"}
	if got := tasks.RedactRunView(nil, v); got.Current != "" || got.ID != "r1" {
		t.Fatalf("nil checker: %+v", got)
	}
	if got := tasks.RedactRunView(checker{[]rbac.Permission{"core:tasks.view"}}, v); got.Current != "" {
		t.Fatal("Current kept without core:users.view")
	}
	if got := tasks.RedactRunView(checker{[]rbac.Permission{"core:users.view"}}, v); got.Current != v.Current {
		t.Fatal("Current blanked for a holder of core:users.view")
	}
}

func TestRunState_Terminal(t *testing.T) {
	terminal := []tasks.RunState{tasks.StateSucceeded, tasks.StateFailed, tasks.StateTimedOut, tasks.StateCanceled, tasks.StateInterruptedDropped}
	all := append([]tasks.RunState{tasks.StateQueued, tasks.StateWaitingDeps, tasks.StateRunning, tasks.StateStopping,
		tasks.StateInterrupted, tasks.StateInterruptedQuiesced, tasks.StateAwaitingDecision}, terminal...)
	for _, s := range all {
		if s.Terminal() != slices.Contains(terminal, s) {
			t.Fatalf("%s.Terminal() = %v", s, s.Terminal())
		}
	}
}

func TestCauses_WrapContextErrors(t *testing.T) {
	for _, e := range []error{tasks.ErrCanceled, tasks.ErrShutdown, tasks.ErrQuiesced, tasks.ErrAbandoned, tasks.ErrItemsStopped} {
		if !errors.Is(e, context.Canceled) {
			t.Fatalf("%v does not wrap context.Canceled", e)
		}
	}
	if !errors.Is(tasks.ErrTimedOut, context.DeadlineExceeded) {
		t.Fatal("ErrTimedOut does not wrap context.DeadlineExceeded")
	}
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(tasks.ErrShutdown)
	if tasks.ErrShutdown != lifecycle.ErrShutdown || !lifecycle.IsShutdown(ctx) {
		t.Fatal("ErrShutdown is not lifecycle.ErrShutdown re-exported")
	}
}

func TestEnums_StringsAndModeRequest(t *testing.T) {
	if tasks.ShapeTask.String() != "task" || tasks.ShapeBatch.String() != "batch" || tasks.ShapeFixer.String() != "fixer" || tasks.Shape(0).String() != "unset" {
		t.Fatal("Shape strings")
	}
	if tasks.ResumeContinue.String() != "continue" || tasks.ResumeAsk.String() != "ask" || tasks.ResumePolicy(0).String() != "unset" {
		t.Fatal("ResumePolicy strings")
	}
	if tasks.ResumeContinue != 1 || tasks.ResumeFromZero != 2 || tasks.ResumeDrop != 3 || tasks.ResumeAsk != 4 {
		t.Fatal("ResumePolicy values are not iota + 1")
	}
	if tasks.ModeLive.String() != "live" || tasks.ModePreview.String() != "preview" || tasks.Mode(0).String() != "unset" {
		t.Fatal("Mode strings")
	}
	if m, ok := tasks.ModeRequestLive.Mode(); !ok || m != tasks.ModeLive {
		t.Fatal("ModeRequestLive")
	}
	if _, ok := tasks.ModeRequestUnset.Mode(); ok {
		t.Fatal("ModeRequestUnset resolved")
	}
	for o, want := range map[tasks.SourceOrder]string{tasks.Snapshot: "snapshot", tasks.AppendOnly: "append_only", tasks.Unordered: "unordered", 0: "unset"} {
		if o.String() != want {
			t.Fatalf("SourceOrder(%d) = %q", o, o.String())
		}
	}
	if c := tasks.Cooperative(time.Second); c.IsZero() || c.Grace() != time.Second || c.IsUninterruptible() {
		t.Fatal("Cooperative accessors")
	}
	if c := tasks.Uninterruptible("atomic swap", time.Minute); !c.IsUninterruptible() || c.Reason() != "atomic swap" || c.Budget() != time.Minute {
		t.Fatal("Uninterruptible accessors")
	}
	if !(tasks.CancelContract{}).IsZero() || tasks.AtItemBoundaries().IsZero() {
		t.Fatal("CancelContract zero")
	}
}

func TestScheduleConfig_JSON(t *testing.T) {
	var c tasks.ScheduleConfig
	if err := json.Unmarshal([]byte(`{"enabled": true, "every": "24h"}`), &c); err != nil {
		t.Fatal(err)
	}
	if !c.Enabled || c.Every != config.Duration(24*time.Hour) {
		t.Fatalf("decoded %+v", c)
	}
	if err := json.Unmarshal([]byte(`{"every": 86400}`), &c); err == nil {
		t.Fatal("a bare number duration decoded")
	}
	if tasks.SignalStorage != health.ComponentID("core.storage") {
		t.Fatal("SignalStorage")
	}
}

func TestSourceAndRowHelpers(t *testing.T) {
	src := tasks.DirtySet("dirty/", func(context.Context, []string) ([]int, error) { return nil, nil })
	if p, load, ok := src.Dirty(); !ok || p != "dirty/" || load == nil || src.Order != tasks.Unordered {
		t.Fatal("DirtySet")
	}
	if _, _, ok := (tasks.Source[int]{}).Dirty(); ok {
		t.Fatal("a plain Source reports Dirty")
	}
	if n, ok := tasks.Known(5).Value(); !ok || n != 5 {
		t.Fatal("Known")
	}
	if _, ok := tasks.Unknown().Value(); ok {
		t.Fatal("Unknown")
	}
	s := tasks.Summary{Counters: map[string]int64{"x": 2}, Marks: map[string][]string{"applied": {"b", "a"}}}
	if s.Counter("x") != 2 || !slices.Equal(s.Marked("applied"), []string{"a", "b"}) {
		t.Fatal("Summary helpers")
	}
	r1 := tasks.Row{Inputs: map[string]string{"b": "2", "a": "1"}}
	r2 := tasks.Row{Inputs: map[string]string{"a": "1", "b": "2"}}
	if r1.Fingerprint() != r2.Fingerprint() || r1.Fingerprint() == (tasks.Row{Inputs: map[string]string{"a": "1"}}).Fingerprint() {
		t.Fatal("Fingerprint is not canonical")
	}
	held := r1
	held.Hold = "owner review"
	retry := held
	retry.Retry = true
	if held.Fingerprint() == r1.Fingerprint() || retry.Fingerprint() != r1.Fingerprint() {
		t.Fatal("Fingerprint hold rule")
	}
}
