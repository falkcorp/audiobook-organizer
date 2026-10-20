// file: internal/plugins/maintenance/boot_backfill_params_test.go
// version: 1.3.1
// guid: 8c1e4d27-5b3a-4f69-9e02-7a6d3b8f1c45
// last-edited: 2026-10-20

package maintenance

// 04-P6: the boot backfill goroutines became enqueues of these ops. Boot passes
// params that select the sentinel-gated behaviour the goroutines had; absent
// params keep an operator's run exactly as it was. These tests pin both halves
// and the no-op tag a nothing-to-do run carries.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// p6BootDeps records what the boot-enqueued ops asked their deps for. skipped
// is every dep's "already done" answer. block makes BackfillExternalIDs and
// QuarantineKnownBadFiles wait for ctx, standing in for a long library walk.
type p6BootDeps struct {
	fakeDeps
	mu             sync.Mutex
	gotIfNeeded    []bool
	skipped        bool
	quarantined    int
	quarantineRuns int
	transcodeErr   error
	block          bool
	started        chan struct{}
}

func (d *p6BootDeps) waitForCancel(ctx context.Context) error {
	if d.started != nil {
		close(d.started)
	}
	<-ctx.Done()
	return ctx.Err()
}

func (d *p6BootDeps) BackfillExternalIDs(ctx context.Context, ifNeeded bool, _ func(int, int, string)) (bool, error) {
	d.mu.Lock()
	d.gotIfNeeded = append(d.gotIfNeeded, ifNeeded)
	d.mu.Unlock()
	if d.block {
		return false, d.waitForCancel(ctx)
	}
	return ifNeeded && d.skipped, nil
}

func (d *p6BootDeps) StripMovementAtoms(_ context.Context) bool { return d.skipped }

func (d *p6BootDeps) RemuxMalformedM4BFiles(_ context.Context, _ func(int, int, string)) (bool, error) {
	return d.skipped, nil
}

func (d *p6BootDeps) TranscodeMalformedM4BFiles(_ context.Context, _ func(int, int, string)) (bool, error) {
	return d.skipped, d.transcodeErr
}

func (d *p6BootDeps) QuarantineKnownBadFiles(ctx context.Context) (int, bool, error) {
	d.mu.Lock()
	d.quarantineRuns++
	d.mu.Unlock()
	if d.block {
		return 0, false, d.waitForCancel(ctx)
	}
	return d.quarantined, d.skipped, nil
}

var _ ServerDeps = (*p6BootDeps)(nil)

// p6AttrReporter keeps each Log line's attrs so a test can see NoOpAttr.
type p6AttrReporter struct {
	fakeReporter
	mu    sync.Mutex
	lines []p6LogLine
}

type p6LogLine struct {
	level slog.Level
	msg   string
	noOp  bool
}

func (r *p6AttrReporter) Log(level slog.Level, msg string, attrs ...slog.Attr) error {
	noOp := false
	for _, a := range attrs {
		if a.Key == opsregistry.NoOpAttrKey && a.Value.Kind() == slog.KindBool && a.Value.Bool() {
			noOp = true
		}
	}
	r.mu.Lock()
	r.lines = append(r.lines, p6LogLine{level: level, msg: msg, noOp: noOp})
	r.mu.Unlock()
	return r.fakeReporter.Log(level, msg, attrs...)
}

func (r *p6AttrReporter) noOpLines() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, l := range r.lines {
		if l.noOp {
			n++
		}
	}
	return n
}

func p6Params(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRunExternalIDBackfill_BootParams(t *testing.T) {
	cases := []struct {
		name       string
		raw        json.RawMessage
		skipped    bool
		wantIfNeed bool
		wantNoOp   int
	}{
		// An operator's run: no params, full pass, never the once-gated path.
		{name: "absent params run in full", raw: nil, skipped: true, wantIfNeed: false, wantNoOp: 0},
		{name: "empty object runs in full", raw: json.RawMessage(`{}`), skipped: true, wantIfNeed: false, wantNoOp: 0},
		// Boot: once-gated; a completed sentinel means nothing to do, tagged no-op.
		{name: "if_needed with completed sentinel is a tagged no-op", raw: p6Params(t, map[string]bool{"if_needed": true}), skipped: true, wantIfNeed: true, wantNoOp: 1},
		{name: "if_needed with work to do is not tagged", raw: p6Params(t, map[string]bool{"if_needed": true}), skipped: false, wantIfNeed: true, wantNoOp: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := &p6BootDeps{skipped: tc.skipped}
			rep := &p6AttrReporter{}
			if err := New(deps).runExternalIDBackfill(context.Background(), tc.raw, rep); err != nil {
				t.Fatalf("runExternalIDBackfill: %v", err)
			}
			if len(deps.gotIfNeeded) != 1 || deps.gotIfNeeded[0] != tc.wantIfNeed {
				t.Errorf("BackfillExternalIDs ifNeeded calls = %v, want [%t]", deps.gotIfNeeded, tc.wantIfNeed)
			}
			if got := rep.noOpLines(); got != tc.wantNoOp {
				t.Errorf("no-op tagged lines = %d, want %d", got, tc.wantNoOp)
			}
		})
	}
}

func TestRunExternalIDBackfill_BadParamsFail(t *testing.T) {
	deps := &p6BootDeps{}
	if err := New(deps).runExternalIDBackfill(context.Background(), json.RawMessage(`{"if_needed":`), &p6AttrReporter{}); err == nil {
		t.Fatal("malformed params must fail the run")
	}
	if len(deps.gotIfNeeded) != 0 {
		t.Errorf("malformed params must not reach the backfill, got calls %v", deps.gotIfNeeded)
	}
}

func TestRunMovementAtomCleanup_NoOpTagOnlyWhenAlreadyDone(t *testing.T) {
	for _, done := range []bool{true, false} {
		rep := &p6AttrReporter{}
		if err := New(&p6BootDeps{skipped: done}).runMovementAtomCleanup(context.Background(), json.RawMessage(`{}`), rep); err != nil {
			t.Fatalf("alreadyDone=%t: %v", done, err)
		}
		want := 0
		if done {
			want = 1
		}
		if got := rep.noOpLines(); got != want {
			t.Errorf("alreadyDone=%t: no-op tagged lines = %d, want %d", done, got, want)
		}
	}
}

func TestRunMalformedM4BRemux_NoOpTagOnlyWhenAlreadyDone(t *testing.T) {
	for _, done := range []bool{true, false} {
		rep := &p6AttrReporter{}
		if err := New(&p6BootDeps{skipped: done}).runMalformedM4BRemux(context.Background(), json.RawMessage(`{}`), rep); err != nil {
			t.Fatalf("alreadyDone=%t: %v", done, err)
		}
		want := 0
		if done {
			want = 1
		}
		if got := rep.noOpLines(); got != want {
			t.Errorf("alreadyDone=%t: no-op tagged lines = %d, want %d", done, got, want)
		}
	}
}

func TestRunMalformedM4BTranscode_QuarantineAfter(t *testing.T) {
	cases := []struct {
		name            string
		raw             json.RawMessage
		skipped         bool
		quarantined     int
		wantQuarantines int
		wantNoOp        int
	}{
		// An operator's run: transcode only, as before.
		{name: "absent params do not quarantine", raw: nil, wantQuarantines: 0},
		{name: "quarantine_after runs the quarantine", raw: p6Params(t, map[string]bool{"quarantine_after": true}), quarantined: 2, wantQuarantines: 1},
		// Boot on a library where both steps already ran: a tagged no-op.
		{name: "both already done is a tagged no-op", raw: p6Params(t, map[string]bool{"quarantine_after": true}), skipped: true, wantQuarantines: 1, wantNoOp: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := &p6BootDeps{skipped: tc.skipped, quarantined: tc.quarantined}
			rep := &p6AttrReporter{}
			if err := New(deps).runMalformedM4BTranscode(context.Background(), tc.raw, rep); err != nil {
				t.Fatalf("runMalformedM4BTranscode: %v", err)
			}
			if deps.quarantineRuns != tc.wantQuarantines {
				t.Errorf("quarantine runs = %d, want %d", deps.quarantineRuns, tc.wantQuarantines)
			}
			if got := rep.noOpLines(); got != tc.wantNoOp {
				t.Errorf("no-op tagged lines = %d, want %d", got, tc.wantNoOp)
			}
		})
	}
}

// As in the old boot goroutine, the quarantine still runs after a failed
// transcode (it acts on files earlier runs marked), and the failure is returned.
func TestRunMalformedM4BTranscode_QuarantinesAfterFailedTranscode(t *testing.T) {
	wantErr := errors.New("synthetic transcode failure")
	deps := &p6BootDeps{transcodeErr: wantErr}
	err := New(deps).runMalformedM4BTranscode(context.Background(), p6Params(t, map[string]bool{"quarantine_after": true}), &p6AttrReporter{})
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if deps.quarantineRuns != 1 {
		t.Errorf("quarantine runs = %d, want 1", deps.quarantineRuns)
	}
}

// Shutdown: Registry.Shutdown cancels the run's ctx and waits for the run to
// return before the store closes. These two steps used to ignore that ctx
// (the external-id backfill watched bgCtx, which is cancelled only after the
// registry drain; the quarantine took no ctx), so a cancel mid-walk must now
// end the op promptly.
func TestBootOps_ReturnPromptlyWhenCancelledMidRun(t *testing.T) {
	runs := map[string]func(p *Plugin, ctx context.Context) error{
		"external-id-backfill": func(p *Plugin, ctx context.Context) error {
			return p.runExternalIDBackfill(ctx, p6Params(t, map[string]bool{"if_needed": true}), &p6AttrReporter{})
		},
		"transcode quarantine step": func(p *Plugin, ctx context.Context) error {
			return p.runMalformedM4BTranscode(ctx, p6Params(t, map[string]bool{"quarantine_after": true}), &p6AttrReporter{})
		},
	}
	for name, run := range runs {
		t.Run(name, func(t *testing.T) {
			deps := &p6BootDeps{block: true, started: make(chan struct{})}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- run(New(deps), ctx) }()
			select {
			case <-deps.started:
			case <-time.After(5 * time.Second):
				t.Fatal("the op never reached its library walk")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Errorf("err = %v, want context.Canceled", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("the op did not return within 2s of its ctx being cancelled")
			}
		})
	}
}

// The registry's warmup gate (D44) is what makes these ops wait for the memdb
// warmup now that boot enqueues them; a def that opted out would read a cold
// store at boot. Their policies are pinned too: 04-P6 changes none of them.
func TestBootOpDefs_WaitForWarmupAndKeepTheirPolicies(t *testing.T) {
	p := New(fakeDeps{})
	for _, tc := range []struct {
		def    sdk.OperationDef
		resume sdk.ResumePolicy
	}{
		{p.externalIDBackfillDef(), sdk.ResumeDrop},
		{p.movementAtomCleanupDef(), sdk.ResumeDrop},
		{p.malformedM4BRemuxDef(), sdk.ResumeDrop},
		{p.malformedM4BTranscodeDef(), sdk.ResumeAsk},
		// 04-P7: boot enqueues it with {"ensure": true}. Its warmup wait
		// lives here, in the dispatcher, since the goroutine's own wait was
		// deleted.
		{p.opChangeBookIndexRebuildDef(), sdk.ResumeDrop},
	} {
		if tc.def.NoWarmupWait {
			t.Errorf("%s: NoWarmupWait must be false so the dispatcher holds it until warmup", tc.def.ID)
		}
		if tc.def.ResumePolicy != tc.resume {
			t.Errorf("%s: ResumePolicy = %v, want %v", tc.def.ID, tc.def.ResumePolicy, tc.resume)
		}
		if !tc.def.Cancellable {
			t.Errorf("%s: Cancellable = false, but the run honours ctx and boot enqueues it; it must be cancellable", tc.def.ID)
		}
		if tc.def.DefaultPriority != sdk.PriorityLow {
			t.Errorf("%s: DefaultPriority = %v, want PriorityLow so boot runs never get ahead of a user's op", tc.def.ID, tc.def.DefaultPriority)
		}
	}
}

// SF-1: a boot run with nothing to do must add no line to the default activity
// feed: every Info-or-above line it logs carries the no-op tag (the "Starting"
// lines are Debug, which the mirror files in the hidden debug tier).
func TestBootOps_NoOpRunLogsNoUntaggedInfoLine(t *testing.T) {
	ifNeeded := p6Params(t, map[string]bool{"if_needed": true})
	quarantineAfter := p6Params(t, map[string]bool{"quarantine_after": true})
	runs := map[string]func(p *Plugin, rep *p6AttrReporter) error{
		"external-id": func(p *Plugin, rep *p6AttrReporter) error {
			return p.runExternalIDBackfill(context.Background(), ifNeeded, rep)
		},
		"movement-atom": func(p *Plugin, rep *p6AttrReporter) error {
			return p.runMovementAtomCleanup(context.Background(), json.RawMessage(`{}`), rep)
		},
		"remux": func(p *Plugin, rep *p6AttrReporter) error {
			return p.runMalformedM4BRemux(context.Background(), json.RawMessage(`{}`), rep)
		},
		"transcode": func(p *Plugin, rep *p6AttrReporter) error {
			return p.runMalformedM4BTranscode(context.Background(), quarantineAfter, rep)
		},
		"opchange-ensure": func(p *Plugin, rep *p6AttrReporter) error {
			return p.runOpChangeBookIndexRebuild(context.Background(), json.RawMessage(`{"ensure":true}`), rep)
		},
	}
	for name, run := range runs {
		t.Run(name, func(t *testing.T) {
			rep := &p6AttrReporter{}
			deps := &p6BootDeps{skipped: true}
			deps.store = opChangeFakeStore{ensure: trustedNoOpEnsure}
			if err := run(New(deps), rep); err != nil {
				t.Fatal(err)
			}
			for _, l := range rep.lines {
				if l.level >= slog.LevelInfo && !l.noOp {
					t.Errorf("no-op run logged an untagged %s line %q", l.level, l.msg)
				}
			}
		})
	}
}

// A run cancelled mid-walk returns ctx's error and never logs "complete".
func TestBootOps_CancelledRunDoesNotLogComplete(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runs := map[string]func(p *Plugin, rep *p6AttrReporter) error{
		"movement-atom": func(p *Plugin, rep *p6AttrReporter) error {
			return p.runMovementAtomCleanup(ctx, nil, rep)
		},
		"remux": func(p *Plugin, rep *p6AttrReporter) error { return p.runMalformedM4BRemux(ctx, nil, rep) },
		"transcode": func(p *Plugin, rep *p6AttrReporter) error {
			return p.runMalformedM4BTranscode(ctx, nil, rep)
		},
	}
	for name, run := range runs {
		t.Run(name, func(t *testing.T) {
			rep := &p6AttrReporter{}
			if err := run(New(&p6BootDeps{}), rep); !errors.Is(err, context.Canceled) {
				t.Fatalf("err = %v, want context.Canceled", err)
			}
			for _, l := range rep.lines {
				if strings.Contains(l.msg, "complete") {
					t.Errorf("cancelled run logged %q", l.msg)
				}
			}
		})
	}
}
