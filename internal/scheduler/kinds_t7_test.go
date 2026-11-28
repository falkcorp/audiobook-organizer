// file: internal/scheduler/kinds_t7_test.go
// version: 1.0.0
// guid: 6e80510a-91e5-407b-a8cf-c873596cb2e4
// last-edited: 2026-11-18

package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// t7SchedulerRow is what a converted scheduler definition must look like,
// written out by hand from HEAD's definition and the brief's rules, not derived
// from the native kind under test.
type t7SchedulerRow struct {
	// id is HEAD's definition ID; the kind is "books." + id (rule R3).
	id string
	// liveOnly: HEAD has no Preview and a write capability, so a Preview run is
	// refused (R7, "live-only writer").
	liveOnly bool
}

var t7SchedulerRows = []t7SchedulerRow{
	{id: metadataUpgradeOpID, liveOnly: true},
}

// t7FakeWriter embeds the nil interface: a method the shell calls before the
// test expects it panics.
type t7FakeWriter struct{ t7Writer }

// t7FakeStore is the same for the read side.
type t7FakeStore struct{ t7Store }

func t7TestDeps() t7Deps {
	return t7Deps{
		Store:        t7FakeStore{},
		StandDownKey: tasks.Key("books.library.scan"),
		NewWriter:    func(tasks.Writer) t7Writer { return t7FakeWriter{} },
	}
}

func t7SchedulerDefs(deps t7Deps) []tasks.Definition {
	return (&ExtraOpsRegistrar{}).t7Kinds(deps, nil)
}

func t7SchedulerByKind(t *testing.T, defs []tasks.Definition, kind tasks.Kind) tasks.Definition {
	t.Helper()
	for _, d := range defs {
		if d.Kind() == kind {
			return d
		}
	}
	t.Fatalf("no definition of kind %s", kind)
	return nil
}

// t7ReachedCore is what the recording NewWriter panics with once a native shell
// has handed the host's writer to the core's env: the point under test.
type t7ReachedCore struct{ w tasks.Writer }

// t7Host is a taskstest.FakeRun with the mode and writer a test chooses.
type t7Host struct {
	*taskstest.FakeRun
	mode tasks.Mode
	w    tasks.Writer
	// hold, when set, is what the run's StandDown hands back; standKey records
	// the key it was asked for.
	hold     tasks.Hold
	standKey tasks.Key
}

func (h *t7Host) StandDown(k tasks.Key) (tasks.Hold, error) {
	if h.hold == nil {
		return h.FakeRun.StandDown(k)
	}
	h.standKey = k
	return h.hold, nil
}

func (h *t7Host) Mode() tasks.Mode     { return h.mode }
func (h *t7Host) Writer() tasks.Writer { return h.w }
func (h *t7Host) Log() *slog.Logger    { return slog.New(slog.DiscardHandler) }

// t7GateWriter is a distinguishable tasks.Writer: identity is the assertion.
type t7GateWriter struct{ name string }

func (*t7GateWriter) Do(context.Context, tasks.Write, func(context.Context) ([]tasks.Change, error)) error {
	return nil
}
func (*t7GateWriter) MarkRowApplied(context.Context, string)           {}
func (*t7GateWriter) RowApplied(context.Context, string) (bool, error) { return false, nil }

// t7RunKind runs d's own Run, the function the executor would call. A panic of
// t7ReachedCore is returned as reached, so a kind that gets as far as its core
// can be told from one that refused first.
func t7RunKind(t *testing.T, d tasks.Definition, mode tasks.Mode, w tasks.Writer, params string, opts ...func(*t7Host)) (reached *t7ReachedCore, err error) {
	t.Helper()
	r, ok := d.(interface {
		ExecutorRunners() (func(json.RawMessage) (any, error), *tasks.TaskSpec[any, json.RawMessage], *tasks.BatchSpec[any, any, json.RawMessage], *tasks.FixerSpec[any, any])
	})
	require.True(t, ok, "%T has no ExecutorRunners", d)
	decode, task, _, _ := r.ExecutorRunners()
	require.NotNil(t, task, "%s is not a Task", d.Kind())

	var raw json.RawMessage
	if params != "" {
		raw = json.RawMessage(params)
	}
	p, derr := decode(raw)
	if derr != nil {
		return nil, derr
	}
	h := &t7Host{FakeRun: taskstest.NewFakeRun(context.Background(), d.Kind()), mode: mode, w: w}
	for _, o := range opts {
		o(h)
	}
	defer func() {
		if rec := recover(); rec != nil {
			rc, ok := rec.(t7ReachedCore)
			if !ok {
				panic(rec)
			}
			reached = &rc
		}
	}()
	_, err = task.Run(tasks.NewRC(h), p)
	return nil, err
}

func TestT7SchedulerKinds_Conformance(t *testing.T) {
	taskstest.Conformance(t, t7SchedulerDefs(t7TestDeps())...)
}

func TestT7SchedulerKinds_MatchHEADDefs(t *testing.T) {
	defs := t7SchedulerDefs(t7TestDeps())
	require.Len(t, defs, len(t7SchedulerRows), "every native kind has a row and every row a kind")

	reg := &ExtraOpsRegistrar{}
	heads := map[string]opsregistry.OperationDef{metadataUpgradeOpID: reg.metadataUpgradeDef()}

	for _, r := range t7SchedulerRows {
		head, ok := heads[r.id]
		require.True(t, ok, "no HEAD definition %s", r.id)
		c := tasks.CommonOf(t7SchedulerByKind(t, defs, tasks.Kind("books."+r.id)))

		require.Equal(t, head.DisplayName, c.Title, "%s title", r.id)
		require.Equal(t, head.Description, c.Help, "%s help", r.id)
		require.Equal(t, min(head.Timeout, tasks.MaxTimeout), c.Timeout, "%s timeout", r.id)
		require.Equal(t, head.ProgressTimeout, c.ProgressTimeout, "%s progress timeout", r.id)
		require.Equal(t, tasks.Key("books."+head.ConcurrencyKey), c.ConcurrencyKey, "%s concurrency key", r.id)
		require.Equal(t, tasks.ResumeDrop, c.Resume, "%s resume (HEAD ResumeDrop)", r.id)
		require.Equal(t, tasks.PriorityLow, c.Priority, "%s priority (HEAD PriorityLow)", r.id)
		require.Empty(t, c.NotWhile, "%s NotWhile (HEAD DependsOn is empty)", r.id)

		// Written out by hand, not through t7StartRequirement: HEAD declares
		// settings.manage alone, whose scopes are 05-rbac 6.1's row.
		require.Equal(t, rbac.AllOf("core:settings.manage", "books:settings.manage", "core:tasks.manage",
			"core:system.view", "core:system.manage", "core:library.manage", "core:library.browse",
			"core:integrations.manage"), c.Perms.Start, "%s start requirement", r.id)

		// A write set exactly when the body writes (R7); HEAD declares none, so
		// this is the "Writes found" row.
		require.False(t, c.Effects.IsReadOnly(), "%s read-only", r.id)
		// HEAD declares none; the sweep applies metadata to books (with their
		// authors, series, field states, segment titles, the metadata cache and
		// the ident rows) and saves its cursor as operation state. The trace,
		// call by call, is in extra_ops.go (metadataUpgradeNative). Written out
		// by hand.
		require.ElementsMatch(t, []entity.Ref{
			tasks.Res("books", "books"), tasks.Res("books", "authors"), tasks.Res("books", "series"),
			tasks.Res("books", "metadata_cache"), tasks.Res("books", "operations"),
			tasks.Res("books", "book_files"), tasks.Res("books", "field_states"), tasks.Res("books", "indexes"),
		}, c.Effects.WriteSet(), "%s write set", r.id)
		require.Empty(t, head.Writes, "%s: HEAD now declares Writes, re-derive the row", r.id)

		if !head.NoWarmupWait {
			require.Contains(t, c.Ready, t7SignalMemDB, "%s readiness", r.id)
		}
	}
}

func TestT7SchedulerKinds_WritesThroughShell(t *testing.T) {
	var got tasks.Writer
	deps := t7TestDeps()
	deps.NewWriter = func(w tasks.Writer) t7Writer {
		got = w
		panic(t7ReachedCore{w: w})
	}
	defs := t7SchedulerDefs(deps)

	for _, r := range t7SchedulerRows {
		d := t7SchedulerByKind(t, defs, tasks.Kind("books."+r.id))

		// A mode key in params is refused natively: the mode is the start
		// request's (05-tasks 5T.3.7 item 3).
		_, err := t7RunKind(t, d, tasks.ModeLive, &t7GateWriter{name: "mode"}, `{"dry_run":true}`)
		require.Error(t, err, "%s: a dry_run param must be refused", r.id)
		require.Contains(t, err.Error(), "mode", "%s", r.id)

		// A live run hands the host's own writer to the core's env.
		w := &t7GateWriter{name: r.id}
		got = nil
		reached, err := t7RunKind(t, d, tasks.ModeLive, w, `{}`)
		require.NoError(t, err, "%s", r.id)
		require.NotNil(t, reached, "%s: a live run must reach the writer", r.id)
		require.Same(t, w, got, "%s: the core's writer must be the run's rc.Writer()", r.id)
		require.Same(t, w, reached.w)

		if r.liveOnly {
			got = nil
			reached, err = t7RunKind(t, d, tasks.ModePreview, &t7GateWriter{name: "preview"}, `{}`)
			require.Nil(t, reached, "%s: a Preview run must not reach the core", r.id)
			require.Nil(t, got, "%s: a Preview run must not build a writer", r.id)
			require.Error(t, err, "%s", r.id)
			require.Contains(t, err.Error(), "preview not supported; start with mode live", "%s", r.id)
		}
	}
}

// ---- the shared core ---------------------------------------------------------

type fakeUpgradeWriter struct {
	readyErr error
	res      *metabatch.UpgradeResult
	runErr   error
	calls    []string
	limit    int
	ctx      context.Context
}

func (f *fakeUpgradeWriter) Ready() error {
	f.calls = append(f.calls, "Ready")
	return f.readyErr
}

func (f *fakeUpgradeWriter) RunUpgrade(ctx context.Context, limit int, progress operations.ProgressReporter) (*metabatch.UpgradeResult, error) {
	f.calls = append(f.calls, "RunUpgrade")
	f.limit = limit
	f.ctx = ctx
	_ = progress.UpdateProgress(1, 2, "sweep")
	return f.res, f.runErr
}

type upgradeHoldKey struct{}

type upgradeCoreHarness struct {
	w        *fakeUpgradeWriter
	acquired []string
	finished []error
	standErr error
	finishFn func(error) error
	notes    []string
}

func (h *upgradeCoreHarness) env() metadataUpgradeEnv {
	return metadataUpgradeEnv{
		Log:      func(_ slog.Level, msg string, _ ...slog.Attr) { h.notes = append(h.notes, msg) },
		Progress: func(_, _ int, msg string) { h.notes = append(h.notes, "progress: "+msg) },
		Store:    h.w,
		Writer:   h.w,
		StandDown: func(ctx context.Context, reason string) (t7Hold, error) {
			h.acquired = append(h.acquired, reason)
			if h.standErr != nil {
				return t7Hold{}, h.standErr
			}
			return t7Hold{
				Ctx:      context.WithValue(ctx, upgradeHoldKey{}, true),
				Progress: func(_, _ int, msg string) { h.notes = append(h.notes, "hold: "+msg) },
				Finish: func(runErr error) error {
					h.finished = append(h.finished, runErr)
					if h.finishFn != nil {
						return h.finishFn(runErr)
					}
					return runErr
				}}, nil
		},
	}
}

func TestMetadataUpgradeCore(t *testing.T) {
	t.Run("a refused stand-down fails the run before any write", func(t *testing.T) {
		h := &upgradeCoreHarness{w: &fakeUpgradeWriter{}, standErr: errors.New("scan did not park")}
		res, err := metadataUpgradeCore(context.Background(), h.env())
		require.ErrorContains(t, err, "scan did not park")
		require.Nil(t, res)
		require.Empty(t, h.w.calls, "nothing may be touched without the stand-down")
		require.Empty(t, h.finished, "a hold that was never taken is not finished")
		require.Equal(t, []string{"scheduler.metadata-upgrade apply"}, h.acquired)
	})

	t.Run("a missing fetch service fails after the hold and releases it", func(t *testing.T) {
		h := &upgradeCoreHarness{w: &fakeUpgradeWriter{readyErr: errors.New("metadata fetch service not initialized")}}
		_, err := metadataUpgradeCore(context.Background(), h.env())
		require.ErrorContains(t, err, "metadata fetch service not initialized")
		require.Equal(t, []string{"Ready"}, h.w.calls)
		require.Len(t, h.finished, 1)
		require.NotContains(t, h.notes, "Scanning for books with upgradeable metadata sources...",
			"HEAD checked the service before the second progress line")
	})

	t.Run("a sweep is capped at 200 books and its result returned", func(t *testing.T) {
		want := &metabatch.UpgradeResult{Checked: 9, Upgraded: 4, Skipped: 3, OwnerManualOnly: 1, Errors: 1}
		h := &upgradeCoreHarness{w: &fakeUpgradeWriter{res: want}}
		res, err := metadataUpgradeCore(context.Background(), h.env())
		require.NoError(t, err)
		require.Same(t, want, res)
		require.Equal(t, 200, h.w.limit)
		require.Equal(t, []string{"Ready", "RunUpgrade"}, h.w.calls)
		require.Equal(t, []error{nil}, h.finished)
	})

	t.Run("a sweep error is returned and the hold sees it", func(t *testing.T) {
		boom := errors.New("bulk-apply cap exceeded")
		h := &upgradeCoreHarness{w: &fakeUpgradeWriter{runErr: boom}}
		res, err := metadataUpgradeCore(context.Background(), h.env())
		require.ErrorIs(t, err, boom)
		require.Nil(t, res)
		require.Equal(t, []error{boom}, h.finished)
	})

	t.Run("a lost lease turns a clean sweep into an error", func(t *testing.T) {
		lost := errors.New("scan stand-down lost")
		h := &upgradeCoreHarness{
			w:        &fakeUpgradeWriter{res: &metabatch.UpgradeResult{}},
			finishFn: func(error) error { return lost },
		}
		_, err := metadataUpgradeCore(context.Background(), h.env())
		require.ErrorIs(t, err, lost, "the hold's Finish decides the run's outcome")
	})
}

// the native shell keeps a time budget HEAD declared; an unrelated edit to the
// def must not silently change it.
func TestMetadataUpgradeDef_Budget(t *testing.T) {
	d := (&ExtraOpsRegistrar{}).metadataUpgradeDef()
	require.Equal(t, 4*time.Hour, d.Timeout)
	require.Equal(t, metadataUpgradeOpID, d.ID)
}

func TestMetadataUpgradeCore_SweepRunsUnderTheHold(t *testing.T) {
	h := &upgradeCoreHarness{w: &fakeUpgradeWriter{res: &metabatch.UpgradeResult{}}}
	_, err := metadataUpgradeCore(context.Background(), h.env())
	require.NoError(t, err)
	require.Equal(t, true, h.w.ctx.Value(upgradeHoldKey{}), "the sweep must run on the hold's context, canceled if the lease is lost")
	require.Contains(t, h.notes, "hold: sweep", "progress must go through the hold so every stamp renews the lease")
	require.NotContains(t, h.notes, "progress: sweep")
}

// An empty stand-down key refuses a native live run before the sweep, so the
// metadata is never applied without the scan interlock.
func TestMetadataUpgradeNative_RefusesWithoutStandDownKey(t *testing.T) {
	deps := t7TestDeps()
	deps.StandDownKey = ""
	d := t7SchedulerByKind(t, t7SchedulerDefs(deps), t7KindFor(metadataUpgradeOpID))
	_, err := t7RunKind(t, d, tasks.ModeLive, &t7GateWriter{name: "live"}, `{}`)
	require.ErrorContains(t, err, "no scan stand-down key configured")
}

// fakeUpgradeReporter is the slice of HEAD's Reporter the upgrade's shell uses.
type fakeUpgradeReporter struct{ opsregistry.Reporter }

func (fakeUpgradeReporter) UpdateProgress(int, int, string) error      { return nil }
func (fakeUpgradeReporter) Log(slog.Level, string, ...slog.Attr) error { return nil }
func (fakeUpgradeReporter) IsCanceled() bool                           { return false }

// HEAD's shell refuses a registrar with no metadata fetch service, after taking
// the (here absent) hold, and without touching the store.
func TestMetadataUpgradeDef_RunRefusesWithoutFetchService(t *testing.T) {
	def := NewExtraOpsRegistrar(nil, ExtraOpsDeps{}).metadataUpgradeDef()
	err := def.Run(context.Background(), nil, fakeUpgradeReporter{})
	require.ErrorContains(t, err, "metadata fetch service not initialized")
}

type recordingHold struct{ released int }

func (h *recordingHold) Release() { h.released++ }

// Renew succeeds: this hold is never lost.
func (h *recordingHold) Renew() error { return nil }

// A whole native run: the writer the factory builds does the sweep, the scan
// stand-down is taken on the configured key and given back, and the sweep's result
// is the Task's result.
func TestMetadataUpgradeNative_RunsTheSweepUnderTheScanStandDown(t *testing.T) {
	for _, tc := range []struct {
		name   string
		runErr error
	}{{"success", nil}, {"sweep error", errors.New("bulk-apply cap exceeded")}} {
		t.Run(tc.name, func(t *testing.T) {
			w := &fakeUpgradeWriter{res: &metabatch.UpgradeResult{Checked: 3, Upgraded: 1}, runErr: tc.runErr}
			deps := t7TestDeps()
			deps.Store = w
			deps.NewWriter = func(tasks.Writer) t7Writer { return w }
			d := t7SchedulerByKind(t, t7SchedulerDefs(deps), t7KindFor(metadataUpgradeOpID))

			hold := &recordingHold{}
			var host *t7Host
			_, err := t7RunKind(t, d, tasks.ModeLive, &t7GateWriter{name: "live"}, `{}`, func(h *t7Host) { h.hold = hold; host = h })

			require.Equal(t, []string{"Ready", "RunUpgrade"}, w.calls)
			require.Equal(t, 200, w.limit)
			require.Equal(t, tasks.Key("books.library.scan"), host.standKey)
			require.Equal(t, 1, hold.released, "the scan stand-down must be given back, also after an error")
			if tc.runErr != nil {
				require.ErrorIs(t, err, tc.runErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

// fakeScanGate is the registry's scan stand-down gate.
type fakeScanGate struct {
	holders  []string
	reasons  []string
	released int
}

func (g *fakeScanGate) AcquireScanStandDown(_ context.Context, holder, reason string) (func(), error) {
	g.holders = append(g.holders, holder)
	g.reasons = append(g.reasons, reason)
	return func() { g.released++ }, nil
}

func (g *fakeScanGate) RenewScanStandDown(string) bool { return true }

type opIDReporter struct {
	fakeUpgradeReporter
	id string
}

func (r opIDReporter) OpID() string { return r.id }

// HEAD's shell holds the registrar's scan gate keyed by the run's own id, and
// releases it when the run ends.
func TestMetadataUpgradeDef_RunHoldsTheScanGate(t *testing.T) {
	gate := &fakeScanGate{}
	def := NewExtraOpsRegistrar(nil, ExtraOpsDeps{ScanStandDown: gate}).metadataUpgradeDef()
	err := def.Run(context.Background(), nil, opIDReporter{id: "op-7"})
	require.ErrorContains(t, err, "metadata fetch service not initialized")
	require.Equal(t, []string{"op-7"}, gate.holders)
	require.Equal(t, []string{"scheduler.metadata-upgrade apply"}, gate.reasons)
	require.Equal(t, 1, gate.released)
}

// Rule R6, stated by hand rather than derived from the helper.
func TestStartRequirement_Rules(t *testing.T) {
	settings := rbac.AllOf("core:settings.manage", "books:settings.manage", "core:tasks.manage",
		"core:system.view", "core:system.manage", "core:library.manage", "core:library.browse",
		"core:integrations.manage")
	require.Equal(t, settings, t7StartRequirement(nil), "no atom is HEAD's default, settings.manage")
	require.Equal(t, rbac.AllOf("books:scan", "books:view"), t7StartRequirement([]auth.Permission{auth.PermLibraryView}),
		"library.view alone keeps the scan floor")
	require.Equal(t, rbac.AllOf("books:view", "core:tasks.view", "books:edit"),
		t7StartRequirement([]auth.Permission{auth.PermLibraryView, auth.PermLibraryEditMetadata}),
		"every atom is required: the union of their scopes, in order, without repeats")
	require.True(t, t7StartRequirement([]auth.Permission{"gotham.unknown"}).IsZero(), "an atom with no row fails closed")
}

// The kernel refuses a Timeout over tasks.MaxTimeout, so a longer HEAD budget is
// clamped to it.
func TestCommon_ClampsTheTimeout(t *testing.T) {
	c := t7Common(opsregistry.OperationDef{ID: "gotham.vigil", Timeout: 25 * time.Hour}, nil)
	require.Equal(t, tasks.MaxTimeout, c.Timeout)
}

// The native shell refuses a run whose owner left the store unset, before the
// core dereferences it and before a writer is built.
func TestMetadataUpgradeNative_RefusesWithoutAStore(t *testing.T) {
	deps := t7TestDeps()
	deps.Store = nil
	built := false
	deps.NewWriter = func(tasks.Writer) t7Writer { built = true; return t7FakeWriter{} }
	d := t7SchedulerByKind(t, t7SchedulerDefs(deps), t7KindFor(metadataUpgradeOpID))
	_, err := t7RunKind(t, d, tasks.ModeLive, &t7GateWriter{name: "live"}, `{}`)
	require.ErrorContains(t, err, "t7Deps.Store is not set")
	require.False(t, built)
}

// HEAD's body only touches its Reporter when it first needs it, so building the
// env over a nil one must not panic.
func TestHeadEnv_BuildsOverANilReporter(t *testing.T) {
	require.NotPanics(t, func() {
		e := t7HeadEnv(nil)
		require.NotNil(t, e.Canceled)
	})
}
