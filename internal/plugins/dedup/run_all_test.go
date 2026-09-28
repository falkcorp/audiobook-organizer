// file: internal/plugins/dedup/run_all_test.go
// version: 1.1.0
// guid: ef555d0e-38d5-4c56-ae25-d1de6f868f64
// last-edited: 2026-09-28

package dedup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// ── fakes ────────────────────────────────────────────────────────────────────

type runAllProgress struct {
	cur, total int
	msg        string
}

type runAllReporter struct {
	mu          sync.Mutex
	progress    []runAllProgress
	checkpoints []string
	// ckptProgress is the last reported progress at each Checkpoint: what the
	// real reporter records as high_water_progress.
	ckptProgress []int
	result       any
	phases       []string
}

func (r *runAllReporter) UpdateProgress(cur, total int, msg string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.progress = append(r.progress, runAllProgress{cur, total, msg})
	return nil
}
func (r *runAllReporter) Log(slog.Level, string, ...slog.Attr) error { return nil }
func (r *runAllReporter) Logger() *slog.Logger                       { return slog.Default() }
func (r *runAllReporter) Checkpoint(state any) error {
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checkpoints = append(r.checkpoints, string(b))
	cur := 0
	if n := len(r.progress); n > 0 {
		cur = r.progress[n-1].cur
	}
	r.ckptProgress = append(r.ckptProgress, cur)
	return nil
}
func (r *runAllReporter) IsCanceled() bool { return false }
func (r *runAllReporter) RunPhase(ctx context.Context, name string, fn func(context.Context, opsregistry.Reporter) error) error {
	r.mu.Lock()
	r.phases = append(r.phases, name)
	r.mu.Unlock()
	return fn(ctx, r)
}
func (r *runAllReporter) Trigger(context.Context, string, any) error { return nil }
func (r *runAllReporter) SetCurrentItem(string)                      {}
func (r *runAllReporter) OpID() string                               { return "parent-op" }
func (r *runAllReporter) SetResult(v any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.result = v
	return nil
}

func (r *runAllReporter) lastCheckpoint(t *testing.T) runAllState {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.checkpoints) == 0 {
		t.Fatal("no checkpoint written")
	}
	var st runAllState
	if err := json.Unmarshal([]byte(r.checkpoints[len(r.checkpoints)-1]), &st); err != nil {
		t.Fatal(err)
	}
	return st
}

// runAllRegistry hands out child ids "child-<n>-<def>" and records each
// enqueue's def and parent.
type runAllRegistry struct {
	mu       sync.Mutex
	defs     []string
	params   []string // marshalled params of each enqueue
	parents  []string
	failDef  string
	children *runAllOps
}

func (r *runAllRegistry) RegisterOp(sdk.OperationDef) error { return nil }
func (r *runAllRegistry) EnqueueOp(_ context.Context, defID string, params any, opts ...sdk.EnqueueOption) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if defID == r.failDef {
		return "", errors.New("unknown defID")
	}
	var o opsregistry.EnqueueOptions
	for _, f := range opts {
		f(&o)
	}
	r.defs = append(r.defs, defID)
	b, _ := json.Marshal(params)
	r.params = append(r.params, string(b))
	r.parents = append(r.parents, o.ParentID)
	id := fmt.Sprintf("child-%d-%s", len(r.defs), defID)
	if r.children != nil {
		r.children.enqueued(id, defID)
	}
	return id, nil
}

func (r *runAllRegistry) enqueuedDefs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.defs...)
}

// runAllOps scripts child rows: each read of an id pops the next status from
// its script; the last one sticks. Ids with no script complete on first read.
type runAllOps struct {
	mu      sync.Mutex
	scripts map[string][]database.OperationV2Row
	// byDef scripts children by def id, applied when that def is enqueued.
	byDef map[string][]database.OperationV2Row
	reads map[string]int
}

func newRunAllOps() *runAllOps {
	return &runAllOps{
		scripts: map[string][]database.OperationV2Row{},
		byDef:   map[string][]database.OperationV2Row{},
		reads:   map[string]int{},
	}
}

func (o *runAllOps) enqueued(id, defID string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if s, ok := o.byDef[defID]; ok {
		o.scripts[id] = s
	}
}

func (o *runAllOps) GetOperationV2(id string) (*database.OperationV2Row, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.reads[id]++
	s, ok := o.scripts[id]
	if !ok || len(s) == 0 {
		return &database.OperationV2Row{ID: id, Status: "completed"}, nil
	}
	row := s[0]
	if len(s) > 1 {
		o.scripts[id] = s[1:]
	}
	row.ID = id
	return &row, nil
}

func newRunAllPlugin(t *testing.T, embeddings bool) (*Plugin, *runAllRegistry, *runAllOps, *int) {
	t.Helper()
	prev := config.AppConfig.Dedup.EmbeddingsEnabled
	config.AppConfig.Dedup.EmbeddingsEnabled = embeddings
	t.Cleanup(func() { config.AppConfig.Dedup.EmbeddingsEnabled = prev })

	ops := newRunAllOps()
	reg := &runAllRegistry{children: ops}
	previews := 0
	p := &Plugin{
		registry:           reg,
		opStatus:           ops,
		runAllPollInterval: time.Millisecond,
		rescorePreviewFn: func(context.Context) (dedupengine.RescoreResult, error) {
			previews++
			return dedupengine.RescoreResult{Inspected: 40, Changed: 3}, nil
		},
	}
	return p, reg, ops, &previews
}

// liveJSON is an explicit live request; `{}` is a preview.
var liveJSON = json.RawMessage(`{"dry_run":false}`)

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var allChildDefs = []string{"dedup.embed-scan", "acoustid.scan", "dedup.full-scan", "dedup.llm-review"}

// ── tests ────────────────────────────────────────────────────────────────────

func TestRunAll_Def(t *testing.T) {
	def := (&Plugin{}).runAllDef()
	if def.ID != RunAllDefID || def.ResumePolicy != sdk.ResumeRestart || def.Liveness != sdk.LivenessManual {
		t.Fatalf("def = %s resume=%v liveness=%v", def.ID, def.ResumePolicy, def.Liveness)
	}
	// DedupeQueuedRuns would collapse a live press onto a running preview.
	if def.DedupeQueuedRuns || def.ConcurrencyKey == "" {
		t.Fatalf("want a ConcurrencyKey and no DedupeQueuedRuns, got %v / %q", def.DedupeQueuedRuns, def.ConcurrencyKey)
	}
	// It writes through its children, so it must declare the write (which puts
	// it under the write_op_modes.golden preview rule).
	writes := false
	for _, c := range def.Capabilities {
		if c == sdk.CapLibraryWrite {
			writes = true
		}
	}
	if !writes {
		t.Fatal("dedup.run-all must declare library.write")
	}
	found := false
	for _, d := range (&Plugin{}).OperationDefs() {
		if d.ID == RunAllDefID {
			found = true
		}
	}
	if !found {
		t.Fatal("dedup.run-all missing from OperationDefs (the op-ID ledger test enumerates that list)")
	}
}

func TestRunAll_FreshRunsStepsInOrder(t *testing.T) {
	p, reg, _, previews := newRunAllPlugin(t, true)
	rep := &runAllReporter{}

	if err := p.runRunAll(context.Background(), liveJSON, rep); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := reg.enqueuedDefs(); strings.Join(got, ",") != strings.Join(allChildDefs, ",") {
		t.Fatalf("enqueue order = %v, want %v", got, allChildDefs)
	}
	for i, parent := range reg.parents {
		if parent != "parent-op" {
			t.Errorf("child %d parent = %q, want parent-op", i, parent)
		}
	}
	if *previews != 1 {
		t.Fatalf("rescore preview ran %d times, want 1", *previews)
	}
	wantPhases := "embeddings,acoustic,find,ai-review,rescore-preview"
	if got := strings.Join(rep.phases, ","); got != wantPhases {
		t.Fatalf("phases = %s, want %s", got, wantPhases)
	}

	res, ok := rep.result.(RunAllResult)
	if !ok {
		t.Fatalf("result type %T", rep.result)
	}
	if len(res.Steps) != 5 || res.RescorePreview == nil || res.RescorePreview.Changed != 3 {
		t.Fatalf("result = %+v", res)
	}

	// Progress never goes backwards and ends at total.
	total := 5 * runAllStepUnits
	prev := -1
	for _, pr := range rep.progress {
		if pr.total != total {
			t.Fatalf("progress total %d, want %d", pr.total, total)
		}
		if pr.cur < prev {
			t.Fatalf("progress went backwards: %d after %d (%q)", pr.cur, prev, pr.msg)
		}
		prev = pr.cur
	}
	if last := rep.progress[len(rep.progress)-1]; last.cur != total {
		t.Fatalf("final progress %d/%d", last.cur, last.total)
	}

	st := rep.lastCheckpoint(t)
	if st.StepIndex != 5 || st.ChildOpID != "" || len(st.Finished) != 5 {
		t.Fatalf("final checkpoint = %+v", st)
	}
}

func TestRunAll_EmbeddingsOffSkipsThatStep(t *testing.T) {
	p, reg, _, _ := newRunAllPlugin(t, false)
	rep := &runAllReporter{}
	if err := p.runRunAll(context.Background(), liveJSON, rep); err != nil {
		t.Fatalf("run: %v", err)
	}
	want := allChildDefs[1:]
	if got := reg.enqueuedDefs(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("enqueued %v, want %v", got, want)
	}
	if res := rep.result.(RunAllResult); len(res.Skipped) != 1 || res.Skipped[0] != "embeddings" {
		t.Fatalf("skipped = %v", res.Skipped)
	}
}

// Every checkpoint must carry every key: the registry merges the blob into
// params key by key, so an omitted child_op_id would resurrect a stale child.
func TestRunAll_CheckpointsCarryEveryKey(t *testing.T) {
	p, _, _, _ := newRunAllPlugin(t, true)
	rep := &runAllReporter{}
	if err := p.runRunAll(context.Background(), liveJSON, rep); err != nil {
		t.Fatal(err)
	}
	sawChild := false
	for _, c := range rep.checkpoints {
		for _, k := range []string{`"plan":`, `"skipped":`, `"step_index":`, `"child_op_id":`, `"finished":`} {
			if !strings.Contains(c, k) {
				t.Fatalf("checkpoint %s lacks %s", c, k)
			}
		}
		if strings.Contains(c, `"child_op_id":"child-3-dedup.full-scan"`) && strings.Contains(c, `"step_index":2`) {
			sawChild = true
		}
	}
	if !sawChild {
		t.Fatalf("no checkpoint recorded the find step's child before waiting on it: %v", rep.checkpoints)
	}
}

func TestRunAll_ResumeFollowsRecordedChild(t *testing.T) {
	p, reg, ops, previews := newRunAllPlugin(t, true)
	ops.scripts["old-find"] = []database.OperationV2Row{
		{Status: "running", ProgressCurrent: 10, ProgressTotal: 100},
		{Status: "completed"},
	}
	params := mustJSON(t, runAllState{DryRun: opmode.Live(),
		Plan:      []string{"embeddings", "acoustic", "find", "ai-review", "rescore-preview"},
		StepIndex: 2,
		ChildOpID: "old-find",
		Finished: []RunAllStepRecord{
			{ID: "embeddings", ChildOpID: "e"}, {ID: "acoustic", ChildOpID: "a"},
		},
	})
	rep := &runAllReporter{}
	if err := p.runRunAll(context.Background(), params, rep); err != nil {
		t.Fatalf("resume: %v", err)
	}
	// Steps 0-2 are NOT re-run; the recorded child is followed instead.
	if got := reg.enqueuedDefs(); strings.Join(got, ",") != "dedup.llm-review" {
		t.Fatalf("resume enqueued %v, want only dedup.llm-review", got)
	}
	if ops.reads["old-find"] < 2 {
		t.Fatalf("recorded child read %d times, want it followed to completion", ops.reads["old-find"])
	}
	res := rep.result.(RunAllResult)
	if len(res.Steps) != 5 || res.Steps[2].ChildOpID != "old-find" || *previews != 1 {
		t.Fatalf("result after resume = %+v", res)
	}
	// Resumed progress starts at the recorded step, not at zero.
	if first := rep.progress[0]; first.cur != 2*runAllStepUnits {
		t.Fatalf("first resumed progress = %d, want %d", first.cur, 2*runAllStepUnits)
	}
}

func TestRunAll_ResumeCompletedChildAdvances(t *testing.T) {
	p, reg, _, _ := newRunAllPlugin(t, true)
	params := mustJSON(t, runAllState{DryRun: opmode.Live(),
		Plan: []string{"find", "ai-review"}, StepIndex: 0, ChildOpID: "done-find",
	})
	if err := p.runRunAll(context.Background(), params, &runAllReporter{}); err != nil {
		t.Fatal(err)
	}
	if got := reg.enqueuedDefs(); strings.Join(got, ",") != "dedup.llm-review" {
		t.Fatalf("enqueued %v", got)
	}
}

func TestRunAll_ResumeInterruptedChildReEnqueues(t *testing.T) {
	p, reg, ops, _ := newRunAllPlugin(t, true)
	ops.scripts["requeued-away"] = []database.OperationV2Row{{Status: "interrupted_dropped"}}
	params := mustJSON(t, runAllState{DryRun: opmode.Live(),
		Plan: []string{"find", "ai-review"}, StepIndex: 0, ChildOpID: "requeued-away",
	})
	if err := p.runRunAll(context.Background(), params, &runAllReporter{}); err != nil {
		t.Fatal(err)
	}
	if got := reg.enqueuedDefs(); strings.Join(got, ",") != "dedup.full-scan,dedup.llm-review" {
		t.Fatalf("enqueued %v, want the interrupted step re-enqueued first", got)
	}
}

func TestRunAll_ResumeFailedChildStops(t *testing.T) {
	p, reg, ops, previews := newRunAllPlugin(t, true)
	msg := "boom"
	ops.scripts["bad"] = []database.OperationV2Row{{Status: "failed", ErrorMessage: &msg}}
	params := mustJSON(t, runAllState{DryRun: opmode.Live(), Plan: []string{"find", "rescore-preview"}, ChildOpID: "bad"})
	err := p.runRunAll(context.Background(), params, &runAllReporter{})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want the child's failure", err)
	}
	if len(reg.enqueuedDefs()) != 0 || *previews != 0 {
		t.Fatalf("later steps ran after a failed step: %v previews=%d", reg.enqueuedDefs(), *previews)
	}
}

func TestRunAll_ChildFailureStopsLaterSteps(t *testing.T) {
	p, reg, ops, previews := newRunAllPlugin(t, true)
	ops.byDef["acoustid.scan"] = []database.OperationV2Row{{Status: "running"}, {Status: "canceled"}}
	err := p.runRunAll(context.Background(), liveJSON, &runAllReporter{})
	if err == nil || !strings.Contains(err.Error(), "Comparing audio fingerprints") {
		t.Fatalf("err = %v", err)
	}
	if got := reg.enqueuedDefs(); strings.Join(got, ",") != "dedup.embed-scan,acoustid.scan" {
		t.Fatalf("enqueued %v", got)
	}
	if *previews != 0 {
		t.Fatal("rescore preview ran after a failed step")
	}
}

func TestRunAll_EnqueueErrorStops(t *testing.T) {
	p, reg, _, _ := newRunAllPlugin(t, true)
	reg.failDef = "acoustid.scan"
	if err := p.runRunAll(context.Background(), liveJSON, &runAllReporter{}); err == nil {
		t.Fatal("want an error when a step cannot be enqueued")
	}
	if got := reg.enqueuedDefs(); strings.Join(got, ",") != "dedup.embed-scan" {
		t.Fatalf("enqueued %v", got)
	}
}

func TestRunAll_MirrorsChildProgress(t *testing.T) {
	p, _, ops, _ := newRunAllPlugin(t, true)
	ops.byDef["dedup.full-scan"] = []database.OperationV2Row{
		{Status: "queued"},
		{Status: "running", ProgressCurrent: 50, ProgressTotal: 100, ProgressMessage: "Scanning books: 50 / 100"},
		{Status: "running", ProgressCurrent: 5, ProgressTotal: 100, ProgressMessage: "Composing scores: 5 / 100"},
		{Status: "completed"},
	}
	rep := &runAllReporter{}
	params := mustJSON(t, runAllState{DryRun: opmode.Live(), Plan: []string{"find"}})
	if err := p.runRunAll(context.Background(), params, rep); err != nil {
		t.Fatal(err)
	}
	var sawQueued, sawHalf, sawPhase2 bool
	for _, pr := range rep.progress {
		switch {
		case strings.Contains(pr.msg, "waiting to start"):
			sawQueued = true
		case strings.Contains(pr.msg, "Scanning books: 50 / 100"):
			sawHalf = pr.cur == 500
		case strings.Contains(pr.msg, "Composing scores"):
			// The bar holds its position through full-scan's second phase.
			sawPhase2 = pr.cur == 500
		}
	}
	if !sawQueued || !sawHalf || !sawPhase2 {
		t.Fatalf("queued=%v half=%v phase2=%v progress=%+v", sawQueued, sawHalf, sawPhase2, rep.progress)
	}
}

func TestRunAll_RejectsUnknownStep(t *testing.T) {
	p, reg, _, _ := newRunAllPlugin(t, true)
	params := json.RawMessage(`{"plan":["find","dedupe-everything"]}`)
	if err := p.runRunAll(context.Background(), params, &runAllReporter{}); err == nil ||
		!strings.Contains(err.Error(), "unknown step") {
		t.Fatalf("err = %v", err)
	}
	if len(reg.enqueuedDefs()) != 0 {
		t.Fatal("ran steps from an invalid plan")
	}
}

func TestRunAll_CancelDuringWaitReturnsContextError(t *testing.T) {
	p, _, ops, _ := newRunAllPlugin(t, true)
	ops.byDef["dedup.full-scan"] = []database.OperationV2Row{{Status: "running"}}
	rep := &runAllReporter{}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	err := p.runRunAll(ctx, mustJSON(t, runAllState{DryRun: opmode.Live(), Plan: []string{"find", "ai-review"}}), rep)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	// The last checkpoint still names the running child, so a restart resumes it.
	st := rep.lastCheckpoint(t)
	if st.StepIndex != 0 || st.ChildOpID == "" {
		t.Fatalf("checkpoint after cancel = %+v", st)
	}
}

// A run whose first child sits queued must still checkpoint a non-zero
// high_water_progress, or three restarts in that window trip the registry's
// boot-loop guard and force-drop it.
func TestRunAll_QueuedFirstStepCheckpointsNonZeroHighWater(t *testing.T) {
	p, _, ops, _ := newRunAllPlugin(t, false)
	ops.byDef["acoustid.scan"] = []database.OperationV2Row{
		{Status: "queued"}, {Status: "queued"}, {Status: "completed"},
	}
	rep := &runAllReporter{}
	if err := p.runRunAll(context.Background(), mustJSON(t, runAllState{DryRun: opmode.Live(), Plan: []string{"acoustic", "find"}}), rep); err != nil {
		t.Fatal(err)
	}
	// Find a checkpoint taken while step 0 was still in flight.
	for i, c := range rep.checkpoints {
		var st runAllState
		_ = json.Unmarshal([]byte(c), &st)
		if st.StepIndex == 0 && st.ChildOpID != "" && rep.ckptProgress[i] > 0 {
			return
		}
	}
	t.Fatalf("no step-0 checkpoint with progress > 0: checkpoints=%v progress=%v", rep.checkpoints, rep.ckptProgress)
}

func TestRunAll_OperatorPauseKeepsParentAlive(t *testing.T) {
	p, _, ops, _ := newRunAllPlugin(t, true)
	p.operationsPausedFn = func() bool { return true }
	same := database.OperationV2Row{Status: "running", ProgressCurrent: 3, ProgressTotal: 10}
	ops.byDef["dedup.full-scan"] = []database.OperationV2Row{same, same, same, {Status: "completed"}}
	rep := &runAllReporter{}
	if err := p.runRunAll(context.Background(), mustJSON(t, runAllState{DryRun: opmode.Live(), Plan: []string{"find"}}), rep); err != nil {
		t.Fatal(err)
	}
	paused := 0
	for _, pr := range rep.progress {
		if strings.Contains(pr.msg, "paused by an operator") {
			paused++
		}
	}
	// An unchanged row would report nothing; under a pause every poll reports.
	if paused < 3 {
		t.Fatalf("paused progress reports = %d, want one per poll (3)", paused)
	}
}

// Preview (the default for `{}`) must write nothing: no child op is enqueued,
// no checkpoint names a child, and only the read-only rescore preview runs.
func TestRunAll_PreviewWritesNothing(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, json.RawMessage(`{}`), json.RawMessage(`{"dry_run":true}`)} {
		p, reg, _, previews := newRunAllPlugin(t, true)
		rep := &runAllReporter{}
		if err := p.runRunAll(context.Background(), raw, rep); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if got := reg.enqueuedDefs(); len(got) != 0 {
			t.Fatalf("%s: preview enqueued %v", raw, got)
		}
		if *previews != 1 {
			t.Fatalf("%s: rescore preview ran %d times, want 1", raw, *previews)
		}
		for _, c := range rep.checkpoints {
			if !strings.Contains(c, `"child_op_id":""`) {
				t.Fatalf("%s: preview checkpoint names a child: %s", raw, c)
			}
		}
		res, ok := rep.result.(RunAllResult)
		if !ok || !res.DryRun || res.RescorePreview == nil {
			t.Fatalf("%s: result = %+v", raw, rep.result)
		}
		var skipped []string
		for _, s := range res.PreviewSkipped {
			skipped = append(skipped, s.DefID)
		}
		if strings.Join(skipped, ",") != strings.Join(allChildDefs, ",") {
			t.Fatalf("%s: preview_skipped = %v, want every child %v", raw, skipped, allChildDefs)
		}
	}
}

// A live run starts every child with an explicit dry_run=false, so a child
// that later gains a preview-by-default mode still runs for real.
func TestRunAll_LiveRunsChildrenInApplyMode(t *testing.T) {
	p, reg, _, _ := newRunAllPlugin(t, true)
	rep := &runAllReporter{}
	if err := p.runRunAll(context.Background(), liveJSON, rep); err != nil {
		t.Fatal(err)
	}
	if len(reg.params) != len(allChildDefs) {
		t.Fatalf("enqueued %d children, want %d", len(reg.params), len(allChildDefs))
	}
	for i, prm := range reg.params {
		dry, err := opmode.ParseDryRun(reg.defs[i], json.RawMessage(prm))
		if err != nil || dry {
			t.Fatalf("child %s params %s resolve to dry_run=%v (err %v), want live", reg.defs[i], prm, dry, err)
		}
	}
	if res := rep.result.(RunAllResult); res.DryRun || len(res.PreviewSkipped) != 0 {
		t.Fatalf("live result = %+v", res)
	}
}

func TestRunAll_NoOpStoreFailsClearly(t *testing.T) {
	p := &Plugin{registry: &runAllRegistry{}}
	if err := p.runRunAll(context.Background(), liveJSON, &runAllReporter{}); err == nil ||
		!strings.Contains(err.Error(), "operation store") {
		t.Fatalf("err = %v", err)
	}
}
