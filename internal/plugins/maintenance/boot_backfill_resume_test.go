// file: internal/plugins/maintenance/boot_backfill_resume_test.go
// version: 1.0.0
// guid: 579d9a65-c4dd-4668-8bd0-c8fb6914b259
// last-edited: 2026-10-30

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/remux"
	"github.com/falkcorp/audiobook-organizer/internal/walkresume"
)

// ckptReporter records Checkpoint states. It is safe for the pool's workers,
// which call UpdateProgress concurrently.
type ckptReporter struct {
	fakeReporter
	pmu    sync.Mutex
	states []bootBackfillParams
}

func (r *ckptReporter) UpdateProgress(_, _ int, _ string) error {
	r.pmu.Lock()
	defer r.pmu.Unlock()
	return nil
}

func (r *ckptReporter) Checkpoint(state any) error {
	r.pmu.Lock()
	defer r.pmu.Unlock()
	if p, ok := state.(bootBackfillParams); ok {
		r.states = append(r.states, p)
	}
	return nil
}

func (r *ckptReporter) last(t *testing.T) bootBackfillParams {
	t.Helper()
	r.pmu.Lock()
	defer r.pmu.Unlock()
	if len(r.states) == 0 {
		t.Fatal("no checkpoint was written; a ResumeRestart op that never checkpoints restarts from zero")
	}
	return r.states[len(r.states)-1]
}

// resumeDeps captures the walkresume.Run each dep method is handed and returns
// the configured error.
type resumeDeps struct {
	fakeDeps
	mu   sync.Mutex
	runs map[string]walkresume.Run
	err  error
}

func (d *resumeDeps) capture(name string, run walkresume.Run) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.runs == nil {
		d.runs = map[string]walkresume.Run{}
	}
	d.runs[name] = run
	return d.err
}

func (d *resumeDeps) BackfillExternalIDs(_ context.Context, _ bool, _ func(int, int, string), run walkresume.Run) (bool, error) {
	return false, d.capture("external-id", run)
}

func (d *resumeDeps) StripMovementAtoms(_ context.Context, run walkresume.Run) (bool, error) {
	return false, d.capture("movement", run)
}

func (d *resumeDeps) RemuxMalformedM4BFiles(_ context.Context, _ func(int, int, string), run walkresume.Run) (bool, error) {
	return false, d.capture("remux", run)
}

func (d *resumeDeps) TranscodeMalformedM4BFiles(_ context.Context, _ func(int, int, string), run walkresume.Run) (bool, error) {
	return false, d.capture("transcode", run)
}

var _ ServerDeps = (*resumeDeps)(nil)

type bootRunner struct {
	name string
	run  func(p *Plugin, ctx context.Context, raw json.RawMessage, rep *ckptReporter) error
	// canYield: the op is ResumeRestart, so the registry honours a yield.
	canYield bool
}

var bootRunners = []bootRunner{
	{"external-id", func(p *Plugin, ctx context.Context, raw json.RawMessage, rep *ckptReporter) error {
		return p.runExternalIDBackfill(ctx, raw, rep)
	}, true},
	{"movement", func(p *Plugin, ctx context.Context, raw json.RawMessage, rep *ckptReporter) error {
		return p.runMovementAtomCleanup(ctx, raw, rep)
	}, true},
	{"remux", func(p *Plugin, ctx context.Context, raw json.RawMessage, rep *ckptReporter) error {
		return p.runMalformedM4BRemux(ctx, raw, rep)
	}, true},
	{"transcode", func(p *Plugin, ctx context.Context, raw json.RawMessage, rep *ckptReporter) error {
		return p.runMalformedM4BTranscode(ctx, raw, rep)
	}, false},
}

// The checkpoint an op writes is its own params with Resume updated, so the
// registry's merge (checkpoint over params) hands the next attempt both the
// position and the mode flags; and a resumed attempt's deps see that position.
func TestBootOps_CheckpointIsTheOpsOwnParamsAndResumeIsHandedToTheDep(t *testing.T) {
	for _, tc := range bootRunners {
		t.Run(tc.name, func(t *testing.T) {
			deps := &resumeDeps{}
			p := New(deps)
			rep := &ckptReporter{}
			raw := p6Params(t, map[string]any{"if_needed": true, "quarantine_after": true,
				"resume": map[string]any{"cursor": "/lib/Batman/book-010.m4b", "failed": 7}})
			require := func(ok bool, format string, args ...any) {
				t.Helper()
				if !ok {
					t.Fatalf(format, args...)
				}
			}
			if err := tc.run(p, context.Background(), raw, rep); err != nil {
				t.Fatal(err)
			}
			run := deps.runs[tc.name]
			require(run.Resume.Cursor == "/lib/Batman/book-010.m4b" && run.Resume.Failed == 7, "the dep got Resume %+v, want the cursor and counters from params", run.Resume)
			require(run.Checkpoint != nil, "the dep got no Checkpoint func")

			run.Checkpoint(walkresume.State{Cursor: "/lib/Robin/book-020.m4b", Failed: 9})
			got := rep.last(t)
			require(got.Resume.Cursor == "/lib/Robin/book-020.m4b" && got.Resume.Failed == 9, "checkpoint = %+v", got)
			require(got.IfNeeded && got.QuarantineAfter, "checkpoint dropped the mode flags (a restart would change what the op does): %+v", got)

			require((run.YieldDue != nil) == tc.canYield, "YieldDue set = %t, want %t: only ResumeRestart ops can continue in place", run.YieldDue != nil, tc.canYield)
		})
	}
}

// A walk that yields returns registry.ErrContinueInPlace, the sentinel the
// registry turns into a same-operation requeue with a fresh timeout.
func TestBootOps_YieldBecomesContinueInPlace(t *testing.T) {
	for _, tc := range bootRunners {
		if !tc.canYield {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			p := New(&resumeDeps{err: fmt.Errorf("walk: %w", walkresume.ErrYield)})
			err := tc.run(p, context.Background(), nil, &ckptReporter{})
			if !errors.Is(err, opsregistry.ErrContinueInPlace) {
				t.Fatalf("err = %v, want one wrapping registry.ErrContinueInPlace", err)
			}
		})
	}
}

// The yield fires only when the deadline is closer than the margin or twice
// the longest gap between checkpoints, and never without a deadline.
func TestNewYieldDue(t *testing.T) {
	if newYieldDue(context.Background(), time.Hour)() {
		t.Error("yield due on a context with no deadline")
	}
	far, cancelFar := context.WithTimeout(context.Background(), time.Hour)
	defer cancelFar()
	if newYieldDue(far, time.Minute)() {
		t.Error("yield due with an hour left and a one-minute margin")
	}
	near, cancelNear := context.WithTimeout(context.Background(), time.Minute)
	defer cancelNear()
	if !newYieldDue(near, 5*time.Minute)() {
		t.Error("yield not due with one minute left and a five-minute margin")
	}
	// A slow gap widens the margin: an 80ms gap between checkpoints doubles to a 160ms margin against ~70ms left.
	slow, cancelSlow := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancelSlow()
	due := newYieldDue(slow, time.Millisecond)
	if due() {
		t.Fatal("yield due on the first call with 150ms left and a 1ms margin")
	}
	time.Sleep(80 * time.Millisecond)
	if !due() {
		t.Error("yield not due: an 80ms gap between checkpoints needs a 160ms margin and ~70ms is left")
	}
}

// The real worker pool: the remux walk driven by bootWalkRun (registry.RunItems
// at the op's pool size) writes checkpoints through reporter.Checkpoint, and a
// resume from the last one asks about no file the cursor covers. Out of order
// completion is what the contiguous watermark exists for, so the assertions
// hold for any interleaving: nothing at or before the cursor is repeated, and
// every file is asked about at least once across the two legs.
func TestBootOps_RemuxResumesOnTheRealPoolWithoutReprocessingBeforeTheCursor(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available, skipping")
	}
	root := t.TempDir()
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })
	const n = 60
	var files []string
	dirs := []string{"Batman", "Batman-Begins", "Robin", "Batman/Year One"}
	for i := 0; i < n; i++ {
		f := filepath.Join(root, dirs[i%len(dirs)], fmt.Sprintf("book-%03d.m4b", i))
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte("not a real m4b"), 0o644); err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	store := &remuxMemStore{settings: map[string]string{}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := &askRecorder{cancelAt: 45, cancel: cancel}
	rep := &ckptReporter{}
	rm := remux.New(store)
	rm.SetProtectedChecker(first)
	run := bootWalkRun(ctx, rep, bootBackfillParams{}, bootWalkWorkers, false)
	if err := rm.RemuxMalformedFiles(ctx, nil, walkresume.WithRun(run)); err != nil {
		t.Fatal(err)
	}
	if store.settings[remux.RemuxKey] == "true" {
		t.Fatal("a cancelled walk set the done flag")
	}
	cursor := rep.last(t).Resume.Cursor
	if cursor == "" {
		t.Fatal("checkpoint has no cursor")
	}

	second := &askRecorder{}
	rm2 := remux.New(store)
	rm2.SetProtectedChecker(second)
	run2 := bootWalkRun(context.Background(), &ckptReporter{}, rep.last(t), bootWalkWorkers, false)
	if err := rm2.RemuxMalformedFiles(context.Background(), nil, walkresume.WithRun(run2)); err != nil {
		t.Fatal(err)
	}
	seenAny := map[string]bool{}
	for _, f := range first.seen() {
		seenAny[f] = true
	}
	for _, f := range second.seen() {
		if f <= cursor {
			t.Errorf("resumed leg reprocessed %s, at or before the cursor %s", f, cursor)
		}
		seenAny[f] = true
	}
	for _, f := range files {
		if !seenAny[f] {
			t.Errorf("%s was never processed by either leg", f)
		}
	}
	if store.settings[remux.RemuxKey] != "true" {
		t.Error("the completed resumed walk did not set the done flag")
	}
}

// askRecorder is a remux.ProtectedChecker that protects nothing and records the
// paths it is asked about (once per processed file, before any probe).
type askRecorder struct {
	mu       sync.Mutex
	paths    []string
	cancelAt int
	cancel   context.CancelFunc
}

func (a *askRecorder) IsProtected(path string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.paths = append(a.paths, path)
	if a.cancelAt > 0 && len(a.paths) == a.cancelAt {
		a.cancel()
	}
	return false
}

func (a *askRecorder) seen() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.paths...)
}

// remuxMemStore is remux.Store over a map. The pool's workers may read settings
// concurrently with the final done-flag write.
type remuxMemStore struct {
	mu       sync.Mutex
	settings map[string]string
}

func (s *remuxMemStore) GetSetting(key string) (*database.Setting, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.settings[key]; ok {
		return &database.Setting{Key: key, Value: v}, nil
	}
	return nil, nil
}

func (s *remuxMemStore) SetSetting(key, value, _ string, _ bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings[key] = value
	return nil
}
