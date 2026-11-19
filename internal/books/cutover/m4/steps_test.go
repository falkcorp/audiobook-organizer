// file: internal/books/cutover/m4/steps_test.go
// version: 1.1.0
// guid: 7f1c4a95-d3e8-4206-8b7a-92c05e6d1f34
// last-edited: 2026-11-18

package m4_test

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m4"
	"github.com/falkcorp/mediacore/storage"
)

// TestSteps_ChainedAfterOneAnother: the one constructor F5-18 calls returns the
// run families' step and then the activity rewrite, both raising the flip's
// level, the first after the refs it was given and the second after those and
// the first.
func TestSteps_ChainedAfterOneAnother(t *testing.T) {
	r := newRig(t)
	level := cutover.BooksLevelStepRef(r.levelVersion())
	cfg := m4Config(r)
	ids := m4.StepIDs{Runs: cutover.TasksCutoverID, Activity: cutover.TasksCutoverID + "-activity"}
	steps, err := m4.Steps(cfg, r.activityConfig(), ids, cutover.BooksCutoverLevel, []storage.StepRef{level})
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 2 || steps[0].ID != ids.Runs || steps[1].ID != ids.Activity {
		t.Fatalf("steps = %v", steps)
	}
	// The run families' step is the cutover; the feed's rewrite is an in-place
	// rewrite of rows that stay where they are.
	for i, s := range steps {
		if s.RaisesLevel != cutover.BooksCutoverLevel || s.Cutover != (i == 0) {
			t.Errorf("%s: RaisesLevel %d, Cutover %v", s.ID, s.RaisesLevel, s.Cutover)
		}
	}
	if len(steps[0].After) != 1 || steps[0].After[0] != level {
		t.Errorf("the first step runs after %v, want %v", steps[0].After, level)
	}
	wantAfter := []storage.StepRef{level, {Owner: steps[0].Owner, DataMigration: ids.Runs}}
	if len(steps[1].After) != 2 || steps[1].After[0] != wantAfter[0] || steps[1].After[1] != wantAfter[1] {
		t.Errorf("the activity step runs after %v, want %v", steps[1].After, wantAfter)
	}
	// The caller's slice is not shared with either step.
	after := make([]storage.StepRef, 1, 4) // spare capacity: an append would write into it
	after[0] = level
	steps, err = m4.Steps(cfg, r.activityConfig(), ids, cutover.BooksCutoverLevel, after)
	if err != nil {
		t.Fatal(err)
	}
	after[0] = storage.StepRef{Owner: "x", Version: 9}
	if steps[0].After[0] != level || steps[1].After[0] != level {
		t.Error("a step's After aliases the caller's slice")
	}

	if _, err := m4.Steps(cfg, r.activityConfig(), m4.StepIDs{Runs: "books.tasks-a", Activity: "books.tasks-a"}, cutover.BooksCutoverLevel, []storage.StepRef{level}); err == nil {
		t.Error("two steps with one ID were accepted")
	}
	if _, err := m4.Steps(cfg, r.activityConfig(), m4.StepIDs{Runs: "books.tasks-a"}, cutover.BooksCutoverLevel, []storage.StepRef{level}); err == nil {
		t.Error("an empty activity step ID was accepted")
	}
	if _, err := m4.Steps(cfg, r.activityConfig(), ids, 0, []storage.StepRef{level}); err == nil {
		t.Error("RaisesLevel 0 was accepted")
	}
	if _, err := m4.Steps(cfg, r.activityConfig(), ids, cutover.BooksCutoverLevel, nil); err == nil {
		t.Error("no After was accepted")
	}
	// Each refusal above must come from the argument it names, so the activity
	// config those calls pass is a valid one; this call is the one that is not.
	if _, err := m4.Steps(cfg, m4.ActivityConfig{}, ids, cutover.BooksCutoverLevel, []storage.StepRef{level}); err == nil {
		t.Error("an ActivityConfig without Runs was accepted")
	}
	bad := cfg
	bad.Importer = nil
	if _, err := m4.Steps(bad, r.activityConfig(), ids, cutover.BooksCutoverLevel, []storage.StepRef{level}); err == nil {
		t.Error("a Config without an Importer was accepted")
	}
}
