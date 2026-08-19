// file: internal/plugins/itunes/plugin_test.go
// version: 1.3.0
// guid: a7b8c9d0-e1f2-3456-ghij-567890123456
// last-edited: 2026-08-19

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
	p := New(nil)
	if err := p.Register(nil); err != nil {
		t.Fatalf("nil guard: %v", err)
	}
}

// TestStubOps_NoCronSchedule locks in the C1 fix: unimplemented stub ops
// must NOT carry a cron schedule (they were burning a green no-op
// op-history row every 10/30 minutes). Restore schedules only alongside a
// real implementation.
func TestStubOps_NoCronSchedule(t *testing.T) {
	p := New(nil)
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

// TestStubRuns_FailInsteadOfReportingSuccess requires every stub Run to return
// an error naming its def.
//
// This test previously asserted the exact opposite -- `if err != nil {
// t.Fatalf(...) }` -- and so locked in the defect it looked like it was
// guarding. A WARN log plus `return nil` is not an honest stub: the registry
// keys operation status off the returned error, so the op was recorded
// "completed" and the UI showed a green row while nothing ran. Between
// 2026-07-17 and 2026-08-16 that applied to itunes.sync,
// itunes.path-reconcile and itunes.path-repair in production, all three of
// which were also shadowing working implementations in internal/server.
//
// The log line is gone because the returned error carries the same
// information to the same place, and cannot be mistaken for success.
func TestStubRuns_FailInsteadOfReportingSuccess(t *testing.T) {
	p := New(nil)
	rep := &stubWarnReporter{}
	runs := map[string]func(context.Context) error{
		"itunes.sync":           func(ctx context.Context) error { return p.runSync(ctx, nil, rep) },
		"itunes.position-sync":  func(ctx context.Context) error { return p.runPositionSync(ctx, nil, rep) },
		"itunes.import":         func(ctx context.Context) error { return p.runImport(ctx, nil, rep) },
		"itunes.path-reconcile": func(ctx context.Context) error { return p.runPathReconcile(ctx, nil, rep) },
		"itunes.path-repair":    func(ctx context.Context) error { return p.runPathRepair(ctx, nil, rep) },
	}

	for id, run := range runs {
		err := run(context.Background())
		if err == nil {
			t.Errorf("%s: stub Run returned nil — the registry will record this op as completed", id)
			continue
		}
		if !strings.Contains(err.Error(), id) {
			t.Errorf("%s: error does not name the def id, so the op history will not say which op lied; got %q", id, err)
		}
	}
}

// TestRegister_OnlyRegistersDefsWithARealRun pins which of this package's defs
// reach the registry.
//
// Registering a stub does not add an operation, it REPLACES whichever
// registration would otherwise claim that ID. Container.PostInit runs before
// NewServer's opRegistrars loop (server.go:567 vs :625), so a plugin def always
// wins and the server's real implementation is the one rejected -- with a
// single swallowed WARN as the only evidence.
//
// This is a whitelist on purpose. Adding a def here must be a deliberate edit
// with a real Run behind it, not something that happens by appending a line to
// the defs slice.
func TestRegister_OnlyRegistersDefsWithARealRun(t *testing.T) {
	// These IDs have working implementations registered from internal/server.
	// A plugin stub claiming any of them silently disables the real op.
	shadowed := map[string]string{
		"itunes.sync":           "server.RegisterITunesSyncOp (Importer.Sync)",
		"itunes.import":         "server.RegisterITunesImportOp (Importer.Execute)",
		"itunes.path-reconcile": "server.RegisterITunesPathReconcileOp (Paths.Reconcile)",
		"itunes.path-repair":    "server.RegisterITunesPathRepairOp (Repair.Repair)",
	}

	p := New(nil)
	var got []string
	for _, def := range p.registeredDefs() {
		got = append(got, def.ID)
	}

	for _, id := range got {
		if owner, bad := shadowed[id]; bad {
			t.Errorf("plugin registered %q, which shadows the real implementation in %s; "+
				"the plugin def wins the collision and the working op becomes unreachable", id, owner)
		}
	}

	want := []string{"itunes.position-sync"}
	if len(got) != len(want) {
		t.Fatalf("registered ops = %v, want %v", got, want)
	}
	for i, id := range want {
		if got[i] != id {
			t.Errorf("registered op %d = %q, want %q", i, got[i], id)
		}
	}
}
