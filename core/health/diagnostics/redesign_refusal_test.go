// file: core/health/diagnostics/redesign_refusal_test.go
// version: 1.0.0
// guid: 6b0e2d94-3a71-4c58-8f1d-9e5a7c2b4d63
// last-edited: 2026-11-15

package diagnostics

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/falkcorp/mediacore/tasks"
)

func asProduction() bool { return false }

func refusalConfig(t *testing.T) (Config, string) {
	t.Helper()
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return Config{Root: root, ViewerFor: func(context.Context, tasks.Actor) (Viewer, error) { return Viewer{}, nil }}, dir
}

// TestRedesignRefusal_Table (round 9, M-1): the refusal applies only while the
// redesign is pending and the process is not a test binary.
func TestRedesignRefusal_Table(t *testing.T) {
	for _, c := range []struct {
		name    string
		pending bool
		isTest  bool
		refused bool
	}{
		{"pending, production binary", true, false, true},
		{"pending, test binary", true, true, false},
		{"done, production binary", false, false, false},
		{"done, test binary", false, true, false},
	} {
		err := redesignRefusal(c.pending, func() bool { return c.isTest })
		if c.refused != errors.Is(err, ErrRedesignPendingF8_03b) || (!c.refused && err != nil) {
			t.Errorf("%s: err = %v, want refused = %v", c.name, err, c.refused)
		}
	}
	// The marker is what New and Build pass: while it stands, a production
	// binary is refused. F8-03b deletes this test with the marker.
	if err := redesignRefusal(RedesignPendingF8_03b, asProduction); !errors.Is(err, ErrRedesignPendingF8_03b) {
		t.Errorf("the marker is %v and a production binary got err = %v, want ErrRedesignPendingF8_03b", RedesignPendingF8_03b, err)
	}
}

// TestNew_RefusedInAProductionBinary (round 9, M-1): New refuses before it
// looks at the config, so no module, wrapper or constructor can build the
// Exporter while the path redactor is the one F8-03b replaces. This is the
// non-test call path: the test passes the probe a production binary would give.
func TestNew_RefusedInAProductionBinary(t *testing.T) {
	cfg, _ := refusalConfig(t)
	if e, err := newExporter(cfg, asProduction); e != nil || !errors.Is(err, ErrRedesignPendingF8_03b) {
		t.Errorf("newExporter(valid config, production) = %v, %v; want nil, ErrRedesignPendingF8_03b", e, err)
	}
	// An invalid config gets the same refusal: it is the first thing New does.
	if _, err := newExporter(Config{}, asProduction); !errors.Is(err, ErrRedesignPendingF8_03b) {
		t.Errorf("newExporter(zero config, production) = %v, want ErrRedesignPendingF8_03b", err)
	}
	// In this test binary New itself is allowed.
	if e, err := New(cfg); e == nil || err != nil {
		t.Errorf("New in a test binary = %v, %v; want an Exporter", e, err)
	}
}

// TestBuild_RefusedInAProductionBinary (round 9, M-1): Build is the choke point
// every bundle passes through (Export calls it), so an Exporter that got built
// any way at all still writes nothing in a production binary: no error other
// than the refusal, no file, no counter.
func TestBuild_RefusedInAProductionBinary(t *testing.T) {
	cfg, dir := refusalConfig(t)
	e, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	e.isTestBinary = asProduction
	actor := tasks.Actor{UserID: "bruce", AuthMethod: "session"}
	res, err := e.Build(context.Background(), "run-refused", actor, ExportParams{ActorID: "bruce"}, nil)
	if !errors.Is(err, ErrRedesignPendingF8_03b) || res.File != "" {
		t.Errorf("Build in a production binary = %+v, %v; want an empty File and ErrRedesignPendingF8_03b", res, err)
	}
	// The refusal comes before the actor check and the run id check, so a bad
	// call gets the same answer and never reaches the filesystem.
	if _, err := e.Build(context.Background(), "run-refused", actor, ExportParams{ActorID: "alfred"}, nil); !errors.Is(err, ErrRedesignPendingF8_03b) {
		t.Errorf("Build with a mismatched actor = %v, want ErrRedesignPendingF8_03b", err)
	}
	if _, err := e.Build(context.Background(), "../escape", actor, ExportParams{ActorID: "bruce"}, nil); !errors.Is(err, ErrRedesignPendingF8_03b) {
		t.Errorf("Build with a bad run id = %v, want ErrRedesignPendingF8_03b", err)
	}
	if ents, err := os.ReadDir(dir); err != nil || len(ents) != 0 {
		t.Errorf("the bundle directory holds %d entries after refused builds (err %v), want none", len(ents), err)
	}

	// A zero Exporter (no New) has no probe and takes the real one: a test
	// binary, so Build gets as far as its own validation.
	var zero Exporter
	if !zero.testBinary() {
		t.Error("a zero Exporter must use testing.Testing")
	}
}
