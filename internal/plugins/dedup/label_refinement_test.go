// file: internal/plugins/dedup/label_refinement_test.go
// version: 1.0.0
// guid: 8c3f1a95-2e7d-4b60-9a14-6d0b5e8c2f73
// last-edited: 2026-10-15

package dedup

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// The two children, in order, each enqueued with an EMPTY body (no apply key),
// under the parent. This is the whole "nothing is written" guarantee on this
// path; the children's Apply=false-on-empty default is pinned by the canaries in
// rebuild_gold_labels_test.go and calibrate_composite_test.go.
func TestLabelRefinementChildrenGetEmptyParams(t *testing.T) {
	p, reg, _, _ := newRunAllPlugin(t, true)
	rep := &runAllReporter{}

	if err := p.runLabelRefinement(context.Background(), nil, rep); err != nil {
		t.Fatalf("run: %v", err)
	}
	defs := reg.enqueuedDefs()
	want := []string{"dedup.rebuild-gold-labels", "dedup.calibrate-composite"}
	if len(defs) != 2 || defs[0] != want[0] || defs[1] != want[1] {
		t.Fatalf("enqueued %v, want %v in order", defs, want)
	}
	for i, params := range reg.params {
		if params != "{}" || strings.Contains(params, "apply") {
			t.Errorf("child %d params = %s, want {} with no apply key", i, params)
		}
		if reg.parents[i] != "parent-op" {
			t.Errorf("child %d parent = %q, want parent-op (child-of lineage)", i, reg.parents[i])
		}
	}
	res, ok := rep.result.(LabelRefinementResult)
	if !ok || !res.DryRun || res.RebuildOp == "" || res.CalibrateOp == "" {
		t.Fatalf("result = %#v", rep.result)
	}
}

// A rebuild that does not complete stops the chain before calibrate starts.
func TestLabelRefinementFailedRebuildAbortsBeforeCalibrate(t *testing.T) {
	p, reg, ops, _ := newRunAllPlugin(t, true)
	msg := "synthetic failure"
	ops.byDef["dedup.rebuild-gold-labels"] = []database.OperationV2Row{{Status: "failed", ErrorMessage: &msg}}

	err := p.runLabelRefinement(context.Background(), nil, &runAllReporter{})
	if err == nil || !strings.Contains(err.Error(), "failed") || !strings.Contains(err.Error(), msg) {
		t.Fatalf("err = %v, want the failed rebuild with its reason", err)
	}
	if defs := reg.enqueuedDefs(); len(defs) != 1 || defs[0] != "dedup.rebuild-gold-labels" {
		t.Fatalf("enqueued %v, want only the rebuild", defs)
	}
}

// A failed calibrate fails the parent too (the old chain only logged it).
func TestLabelRefinementFailedCalibrateFailsParent(t *testing.T) {
	p, _, ops, _ := newRunAllPlugin(t, true)
	ops.byDef["dedup.calibrate-composite"] = []database.OperationV2Row{{Status: "canceled"}}
	if err := p.runLabelRefinement(context.Background(), nil, &runAllReporter{}); err == nil {
		t.Fatal("want an error when calibrate does not complete")
	}
}

// Canceling the parent stops following: a child that never finishes does not
// hold the run, and calibrate is never enqueued.
func TestLabelRefinementCancelStopsFollowing(t *testing.T) {
	p, reg, ops, _ := newRunAllPlugin(t, true)
	ops.byDef["dedup.rebuild-gold-labels"] = []database.OperationV2Row{{Status: "running"}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.runLabelRefinement(ctx, nil, &runAllReporter{}) }()

	deadline := time.Now().Add(5 * time.Second)
	for len(reg.enqueuedDefs()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("want a context error after cancel")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not stop after cancel")
	}
	if defs := reg.enqueuedDefs(); len(defs) != 1 {
		t.Fatalf("enqueued %v, want only the rebuild", defs)
	}
}

// Def shape: gated by settings.manage (a scan.trigger-only caller is refused),
// cancellable, report-only capabilities, the old chain's 2h bound, and listed in
// OperationDefs so the op-id ledger sees it.
func TestLabelRefinementDefShape(t *testing.T) {
	var found *sdk.OperationDef
	for _, d := range (&Plugin{}).OperationDefs() {
		if d.ID == LabelRefinementDefID {
			d := d
			found = &d
		}
	}
	if found == nil {
		t.Fatal("dedup.label-refinement missing from OperationDefs")
	}
	if len(found.Permissions) != 1 || found.Permissions[0] != auth.PermSettingsManage {
		t.Errorf("Permissions = %v, want [settings.manage]", found.Permissions)
	}
	if !found.Cancellable || found.Timeout != 2*time.Hour || found.ConcurrencyKey != LabelRefinementDefID {
		t.Errorf("def = %+v", *found)
	}
	for _, c := range found.Capabilities {
		if c != sdk.CapLibraryRead {
			t.Errorf("capability %v: the parent must be read-only", c)
		}
	}
	b, _ := json.Marshal(labelRefinementChildParams{})
	if string(b) != "{}" {
		t.Errorf("child params = %s", b)
	}
}
