// file: internal/plugins/itunes/plugin_test.go
// version: 1.1.1
// guid: a7b8c9d0-e1f2-3456-ghij-567890123456
// last-edited: 2026-07-17

package itunes

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// TestPlugin_NilGuard tests that the plugin handles nil service gracefully.
// This is critical for server initialization when iTunes is disabled.
func TestPlugin_NilGuard(t *testing.T) {
	p := New(nil, nil)
	if err := p.Register(nil); err != nil {
		t.Fatalf("nil guard: %v", err)
	}
}

// TestStubOps_NoCronSchedule locks in the C1 fix: unimplemented stub ops
// must NOT carry a cron schedule (they were burning a green no-op
// op-history row every 10/30 minutes). Restore schedules only alongside a
// real implementation.
func TestStubOps_NoCronSchedule(t *testing.T) {
	p := New(nil, nil)
	if s := p.syncDef().Schedule; s != nil {
		t.Errorf("itunes.sync is a stub but has schedule %q — remove it or implement the op", *s)
	}
	if s := p.positionSyncDef().Schedule; s != nil {
		t.Errorf("itunes.position-sync is a stub but has schedule %q — remove it or implement the op", *s)
	}
	if s := p.pathReconciledDef().Schedule; s != nil {
		t.Errorf("itunes.path-reconcile is a stub but has schedule %q — remove it or implement the op", *s)
	}
}

// stubWarnReporter captures Log calls so the not-implemented warning can be
// asserted. Other Reporter methods are no-ops.
type stubWarnReporter struct {
	logged bytes.Buffer
}

var _ sdk.Reporter = (*stubWarnReporter)(nil)

func (r *stubWarnReporter) UpdateProgress(current, total int, message string) error { return nil }
func (r *stubWarnReporter) Log(level slog.Level, message string, attrs ...slog.Attr) error {
	r.logged.WriteString(level.String() + " " + message)
	for _, a := range attrs {
		r.logged.WriteString(" " + a.String())
	}
	r.logged.WriteString("\n")
	return nil
}
func (r *stubWarnReporter) Logger() *slog.Logger       { return slog.Default() }
func (r *stubWarnReporter) Checkpoint(state any) error { return nil }
func (r *stubWarnReporter) IsCanceled() bool           { return false }
func (r *stubWarnReporter) RunPhase(ctx context.Context, name string, fn func(context.Context, sdk.Reporter) error) error {
	return fn(ctx, r)
}
func (r *stubWarnReporter) Trigger(ctx context.Context, eventName string, payload any) error {
	return nil
}
func (r *stubWarnReporter) SetCurrentItem(label string) {}

// TestStubRuns_LogNotImplementedWarn proves every stub Run emits an honest
// "op not implemented" warning instead of silently returning nil.
func TestStubRuns_LogNotImplementedWarn(t *testing.T) {
	p := New(nil, nil)
	runs := map[string]func(context.Context) error{}
	rep := &stubWarnReporter{}
	runs["itunes.sync"] = func(ctx context.Context) error { return p.runSync(ctx, nil, rep) }
	runs["itunes.position-sync"] = func(ctx context.Context) error { return p.runPositionSync(ctx, nil, rep) }
	runs["itunes.import"] = func(ctx context.Context) error { return p.runImport(ctx, nil, rep) }
	runs["itunes.path-reconcile"] = func(ctx context.Context) error { return p.runPathReconcile(ctx, nil, rep) }
	runs["itunes.path-repair"] = func(ctx context.Context) error { return p.runPathRepair(ctx, nil, rep) }

	for id, run := range runs {
		rep.logged.Reset()
		if err := run(context.Background()); err != nil {
			t.Fatalf("%s: stub run returned error: %v", id, err)
		}
		out := rep.logged.String()
		if !strings.Contains(out, "op not implemented") {
			t.Errorf("%s: stub run did not log a not-implemented warning; got %q", id, out)
		}
		if !strings.Contains(out, id) {
			t.Errorf("%s: warning does not name the def id; got %q", id, out)
		}
	}
}
