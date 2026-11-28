// file: internal/books/cutover/m4/viewstamp_test.go
// version: 1.0.0
// guid: 7f3a9d52-6c18-4e04-b2a7-8d1e5c0f4b69
// last-edited: 2026-11-28

package m4_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m4"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// F5-18 reach, ruling 1a: every imported run's kind carries a View snapshot
// that says what HEAD's read gate said of it. Without one the executor hides a
// run of a kind it has no definition for from everybody (R3).

// storedViews are the View records of the def/<kind> snapshots in s.Tasks,
// keyed by kind. A snapshot row is the one whose value carries "view".
func storedViews(t testing.TB, s *snap, kinds ...string) map[string]*struct {
	Kind  string   `json:"kind"`
	Perms []string `json:"perms"`
} {
	t.Helper()
	type view = struct {
		Kind  string   `json:"kind"`
		Perms []string `json:"perms"`
	}
	out := map[string]*view{}
	for key, val := range s.Tasks {
		var snapshot struct {
			V    int   `json:"v"`
			View *view `json:"view"`
		}
		if json.Unmarshal([]byte(val), &snapshot) != nil || snapshot.View == nil {
			continue
		}
		for _, k := range kinds {
			if strings.Contains(key, k) {
				if snapshot.V != 2 {
					t.Errorf("snapshot of %s is version %d, want 2", k, snapshot.V)
				}
				out[k] = snapshot.View
			}
		}
	}
	return out
}

type viewOfCalls struct {
	mu sync.Mutex
	n  map[tasks.Kind]int
}

func (c *viewOfCalls) count(k tasks.Kind) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n[k]
}

// headLikeViewOf is what the cells give the step: a Repairs fixer is
// restricted to library.edit_metadata's scope, core's kind is left to core, the
// rest take the default.
func headLikeViewOf(calls *viewOfCalls) m4.ViewOfFunc {
	return func(k tasks.Kind) (tasks.KindView, bool, error) {
		calls.mu.Lock()
		if calls.n == nil {
			calls.n = map[tasks.Kind]int{}
		}
		calls.n[k]++
		calls.mu.Unlock()
		switch {
		case strings.HasPrefix(string(k), "core."):
			return tasks.KindView{}, false, nil
		case strings.HasPrefix(string(k), "books.repairs."):
			return tasks.KindView{Requires: rbac.AllOf("books:edit")}, true, nil
		}
		return tasks.KindView{}, true, nil
	}
}

// TestCutover_ImportedKindsAreStampedWithHEADsGate: three runs of one
// non-Repairs kind, a Repairs run of a fixer no definition registers, and a
// core kind. The first two kinds are stamped (once each in ViewOf, however many
// runs), the core kind is not, and the stamped rules are the ones ViewOf gave.
func TestCutover_ImportedKindsAreStampedWithHEADsGate(t *testing.T) {
	done := epoch.Add(time.Hour)
	fin := func(s runSpec) runSpec { s.st, s.completed = "succeeded", &done; return s }
	specs := []runSpec{
		fin(runSpec{id: runID(1), def: "dedup.book-scan"}),
		fin(runSpec{id: runID(2), def: "dedup.book-scan"}),
		fin(runSpec{id: runID(3), def: "dedup.book-scan"}),
		fin(runSpec{id: runID(4), def: "repairs.plan", params: map[string]any{"fixer_id": "hush"}}),
		fin(runSpec{id: runID(5), def: "diagnostics.export"}),
	}
	calls := &viewOfCalls{}
	r := newRig(t, withRuns(nil, specs...))
	r.viewOf = headLikeViewOf(calls)
	r.runObserve(t, func(ctx context.Context, s *snap) {
		const plain, fixer, core = "books.dedup.book-scan", "books.repairs.hush", "core.diagnostics.export"
		got := storedViews(t, s, plain, fixer, core)
		if v := got[plain]; v == nil || v.Kind != "default" || len(v.Perms) != 0 {
			t.Errorf("%s: stamped %+v, want the default (unrestricted) record", plain, v)
		}
		if v := got[fixer]; v == nil || v.Kind != "all_of" || len(v.Perms) != 1 || v.Perms[0] != "books:edit" {
			t.Errorf("%s: stamped %+v, want all_of [books:edit]", fixer, v)
		}
		if v := got[core]; v != nil {
			t.Errorf("%s: stamped %+v, want no snapshot written for a core kind", core, v)
		}
	})
	for _, k := range []tasks.Kind{"books.dedup.book-scan", "books.repairs.hush", "core.diagnostics.export"} {
		if n := calls.count(k); n != 1 {
			t.Errorf("ViewOf asked %d times for %s, want once per kind", n, k)
		}
	}
	if n := r.imp.stampCalls("books.dedup.book-scan"); n != 3 {
		t.Errorf("%d stamps staged for three runs of one kind, want one per run (a failed chunk must not leave the kind unstamped)", n)
	}
	if n := r.imp.stampCalls("core.diagnostics.export"); n != 0 {
		t.Errorf("%d stamps staged for a core kind, want 0", n)
	}
	if got, want := strings.Count(r.logs.String(), "a kind's read rule is recorded"), 2; got != want {
		t.Errorf("the log records %d kinds' rules, want %d:\n%s", got, want, r.logs.String())
	}
}

// TestCutover_AGateHEADCannotBeToldFailsTheStepNamingTheKind: ViewOf's error is
// the step's, with the kind in it, and the run is not imported unstamped.
func TestCutover_AGateHEADCannotBeToldFailsTheStepNamingTheKind(t *testing.T) {
	done := epoch.Add(time.Hour)
	r := newRig(t, withRuns(nil, runSpec{id: runID(1), def: "repairs.plan", st: "succeeded", completed: &done, params: map[string]any{"fixer_id": "hush"}}))
	r.viewOf = func(k tasks.Kind) (tasks.KindView, bool, error) {
		return tasks.KindView{}, false, errors.New("the grapnel gate is unreadable")
	}
	_, err := r.run(t)
	if err == nil || !strings.Contains(err.Error(), "books.repairs.hush") {
		t.Fatalf("the cutover returned %v, want an error naming books.repairs.hush", err)
	}
}

// TestNew_RefusesWithoutAViewOrAStamper: ViewOf is required, and an importer
// that cannot record a View rule is refused at build, not at the first run.
func TestNew_RefusesWithoutAViewOrAStamper(t *testing.T) {
	r := newRig(t, withRuns(nil))
	level := []storage.StepRef{cutover.BooksLevelStepRef(r.levelVersion())}
	cfg := r.config()
	cfg.ViewOf = nil
	if _, err := m4.New(cfg, cutover.TasksCutoverID, cutover.BooksCutoverLevel, level); err == nil || !strings.Contains(err.Error(), "ViewOf") {
		t.Errorf("New without ViewOf = %v, want an error naming ViewOf", err)
	}
	cfg = r.config()
	cfg.Importer = noStamp{cfg.Importer}
	if _, err := m4.New(cfg, cutover.TasksCutoverID, cutover.BooksCutoverLevel, level); err == nil || !strings.Contains(err.Error(), "ViewStamper") {
		t.Errorf("New over an importer that cannot stamp = %v, want an error naming ViewStamper", err)
	}
}

// noStamp hides an importer's StampView.
type noStamp struct{ tasks.Importer }
