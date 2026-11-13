// file: core/health/registry_test.go
// version: 1.1.1
// guid: e0849cfa-c877-4bf8-98f6-53c0658eea4c
// last-edited: 2026-11-13

package health

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// The components below are test components of the batcave; the real
// books.memdb cell of TestRegistry_Wait_UnblocksOnUp reaches the registry
// through Deps.Health, which lands at F5.

const (
	cave  ComponentID = "wayne.batcave"
	alley ComponentID = "wayne.crimealley"
)

func mustRegister(t *testing.T, r *Registry, c Check) Reporter {
	t.Helper()
	rep, err := r.Register(c)
	if err != nil {
		t.Fatalf("Register(%s): %v", c.ID, err)
	}
	return rep
}

// TestRegistry_Wait_UnblocksOnUp: a component pushed Up through its Reporter
// releases a waiting Wait, Degraded releases it too, and a cancelled ctx
// returns context.Canceled (05-health 5H.9; COR4-8).
func TestRegistry_Wait_UnblocksOnUp(t *testing.T) {
	r := NewRegistry()
	rep := mustRegister(t, r, Check{ID: cave, Readiness: true})

	done := make(chan error, 1)
	go func() { done <- r.Wait(t.Context(), cave) }()

	select {
	case err := <-done:
		t.Fatalf("Wait returned %v before the component was Up", err)
	case <-time.After(50 * time.Millisecond):
	}

	rep.Report(Result{Status: StatusUp})
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Wait = %v, want nil once Up", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return after the component went Up")
	}

	// Degraded also passes, and an already-satisfied Wait returns at once.
	rep.Report(Result{Status: StatusDegraded, Reason: "alfred_on_leave"})
	if err := r.Wait(t.Context(), cave); err != nil {
		t.Fatalf("Wait on a Degraded component = %v, want nil", err)
	}

	// A component that goes Down is not waited through.
	rep.Report(Result{Status: StatusDown, Reason: "batmobile_stolen"})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := r.Wait(ctx, cave); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait on a cancelled ctx = %v, want context.Canceled", err)
	}
}

func TestRegistry_Wait_CancelWhileBlocked(t *testing.T) {
	r := NewRegistry()
	mustRegister(t, r, Check{ID: cave})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- r.Wait(ctx, cave) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Wait = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return after cancel")
	}
}

// Wait on every listed component, and on one registered after Wait began.
func TestRegistry_Wait_AllListedAndLateRegistered(t *testing.T) {
	r := NewRegistry()
	a := mustRegister(t, r, Check{ID: cave})
	done := make(chan error, 1)
	go func() { done <- r.Wait(t.Context(), cave, alley) }()

	a.Report(Result{Status: StatusUp})
	select {
	case err := <-done:
		t.Fatalf("Wait returned %v while %s is not registered", err, alley)
	case <-time.After(50 * time.Millisecond):
	}
	b := mustRegister(t, r, Check{ID: alley})
	select {
	case err := <-done:
		t.Fatalf("Wait returned %v while %s is Unknown", err, alley)
	case <-time.After(50 * time.Millisecond):
	}
	b.Report(Result{Status: StatusUp})
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Wait = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return")
	}
	if err := r.Wait(t.Context()); err != nil {
		t.Fatalf("Wait with no ids = %v, want nil", err)
	}
}

// Wait does not spin: a thousand Reports on another component leave a
// blocked Wait blocked, with no goroutine of its own beyond the caller's.
func TestRegistry_Wait_NoBusyPollAndRaceFree(t *testing.T) {
	r := NewRegistry()
	mustRegister(t, r, Check{ID: cave})
	other := mustRegister(t, r, Check{ID: alley})
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if err := r.Wait(ctx, cave); !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("Wait = %v, want DeadlineExceeded", err)
			}
		})
	}
	for range 1000 {
		other.Report(Result{Status: StatusUp})
		r.Ready()
	}
	wg.Wait()
}

func TestRegistry_RegisterDuplicateRefused(t *testing.T) {
	r := NewRegistry()
	mustRegister(t, r, Check{ID: cave})
	rep, err := r.Register(Check{ID: cave, Critical: true})
	if err == nil {
		t.Fatal("a duplicate ID was registered")
	}
	if rep != nil {
		t.Errorf("a refused Register returned a Reporter")
	}
	if !strings.Contains(err.Error(), string(cave)) {
		t.Errorf("error %q does not name the component", err)
	}
}

func TestRegistry_RegisterIDGrammar(t *testing.T) {
	for _, id := range []ComponentID{"", "batcave", ".batcave", "wayne.", "Wayne.Cave", "wayne.cave now"} {
		if _, err := NewRegistry().Register(Check{ID: id}); err == nil {
			t.Errorf("Register(%q) = nil error, want a refusal", id)
		}
	}
	for _, id := range []ComponentID{"core.storage", "core.integrations.download.deluge-main", "books.memdb"} {
		if _, err := NewRegistry().Register(Check{ID: id}); err != nil {
			t.Errorf("Register(%q) = %v, want nil", id, err)
		}
	}
}

func TestRegistry_ReadyTokens(t *testing.T) {
	r := NewRegistry()
	if ok, tok := r.Ready(); !ok || len(tok) != 0 {
		t.Fatalf("an empty registry: Ready = %v %v, want true, none", ok, tok)
	}
	c := mustRegister(t, r, Check{ID: cave, Readiness: true})
	a := mustRegister(t, r, Check{ID: alley, Readiness: true})
	advisory := mustRegister(t, r, Check{ID: "wayne.library"}) // not Readiness, not Critical
	crit := mustRegister(t, r, Check{ID: "wayne.vault", Critical: true})

	// Unknown: Readiness components and the Critical one with no result yet.
	// The Critical one is Unknown, not Down, so it does not hold readiness.
	ok, tok := r.Ready()
	want := []string{"wayne.batcave:unknown", "wayne.crimealley:unknown"}
	if ok || !slices.Equal(tok, want) {
		t.Fatalf("Ready = %v %v, want false %v", ok, tok, want)
	}

	c.Report(Result{Status: StatusDown, Reason: "bat_signal_dark"})
	a.Report(Result{Status: StatusUp})
	ok, tok = r.Ready()
	want = []string{"wayne.batcave:bat_signal_dark"}
	if ok || !slices.Equal(tok, want) {
		t.Fatalf("Ready = %v %v, want false %v", ok, tok, want)
	}

	// Degraded passes readiness; a Down without a reason has a fixed token.
	c.Report(Result{Status: StatusDegraded, Reason: "alfred_on_leave"})
	if ok, tok = r.Ready(); !ok || len(tok) != 0 {
		t.Fatalf("Degraded must be ready: Ready = %v %v", ok, tok)
	}
	c.Report(Result{Status: StatusDown})
	if _, tok = r.Ready(); !slices.Equal(tok, []string{"wayne.batcave:down"}) {
		t.Fatalf("tokens = %v, want the fixed reason down", tok)
	}
	c.Report(Result{Status: StatusUp})

	// A non-Readiness, non-Critical component never holds readiness; a
	// Critical one does while Down.
	advisory.Report(Result{Status: StatusDown, Reason: "dust"})
	if ok, _ = r.Ready(); !ok {
		t.Fatal("a non-Readiness, non-Critical Down component held readiness")
	}
	crit.Report(Result{Status: StatusDown, Reason: "vault_open"})
	ok, tok = r.Ready()
	if ok || !slices.Equal(tok, []string{"wayne.vault:vault_open"}) {
		t.Fatalf("Ready = %v %v, want false [wayne.vault:vault_open]", ok, tok)
	}
}

// TestRegistry_ReasonGrammar: a Reason outside [a-z0-9_]{1,40} is clamped to
// the fixed token invalid_reason, never stored, so a path or secret in a
// Reason cannot reach a probe. Detail is never copied into the token.
func TestRegistry_ReasonGrammar(t *testing.T) {
	good := []string{"a", "storage_quarantine", "secrets_keyring_not_exported", strings.Repeat("x", 40), "oracle"}
	bad := []string{"Upper", "has space", "dash-ed", "../vault/key", "tok\nen", strings.Repeat("x", 41), "bärman", "tok=secret"}
	for _, reason := range good {
		if !validReason(reason) {
			t.Errorf("validReason(%q) = false", reason)
		}
	}
	for _, reason := range append(bad, "") {
		if validReason(reason) {
			t.Errorf("validReason(%q) = true", reason)
		}
	}

	r := NewRegistry()
	rep := mustRegister(t, r, Check{ID: cave, Readiness: true})
	for _, reason := range bad {
		rep.Report(Result{Status: StatusDown, Reason: reason, Detail: "gordon says " + reason})
		got := r.Snapshot().Components[0].Result
		if got.Reason != InvalidReason {
			t.Errorf("Report(Reason %q) stored %q, want %q", reason, got.Reason, InvalidReason)
		}
		if got.Status != StatusDown {
			t.Errorf("Report(Reason %q) changed the status to %v", reason, got.Status)
		}
		if _, tok := r.Ready(); !slices.Equal(tok, []string{"wayne.batcave:" + InvalidReason}) {
			t.Errorf("Ready tokens for Reason %q = %v", reason, tok)
		}
	}
	// An empty Reason is legal: Up with nothing to say.
	rep.Report(Result{Status: StatusUp})
	if got := r.Snapshot().Components[0].Result; got.Reason != "" {
		t.Errorf("an empty Reason became %q", got.Reason)
	}
	if !validReason(InvalidReason) {
		t.Errorf("InvalidReason %q is itself outside the grammar", InvalidReason)
	}
}

func TestRegistry_ReportStampsAt(t *testing.T) {
	r := NewRegistry()
	t0 := time.Date(2026, 11, 10, 3, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return t0 }
	rep := mustRegister(t, r, Check{ID: cave})
	if got := r.Snapshot().Components[0].Result; got.Status != StatusUnknown || !got.At.IsZero() {
		t.Errorf("a registered component with no probe = %+v, want Unknown with no time", got)
	}
	rep.Report(Result{Status: StatusUp})
	if got := r.Snapshot().Components[0].Result.At; !got.Equal(t0) {
		t.Errorf("At = %v, want the registry clock %v", got, t0)
	}
	t1 := t0.Add(time.Hour)
	rep.Report(Result{Status: StatusUp, At: t1})
	if got := r.Snapshot().Components[0].Result.At; !got.Equal(t1) {
		t.Errorf("an explicit At was replaced: %v", got)
	}
}

// A Probe runs once, synchronously, at Register and is never scheduled.
func TestRegistry_ProbeRunsOnceAtRegister(t *testing.T) {
	r := NewRegistry()
	calls := 0
	rep := mustRegister(t, r, Check{ID: cave, Readiness: true, Probe: func(context.Context) Result {
		calls++
		return Result{Status: StatusUp}
	}})
	if calls != 1 {
		t.Fatalf("Probe ran %d times at Register, want 1", calls)
	}
	if got := r.Snapshot().Components[0].Result.Status; got != StatusUp {
		t.Fatalf("initial status = %v, want the probe's Up", got)
	}
	time.Sleep(20 * time.Millisecond)
	rep.Report(Result{Status: StatusDegraded})
	if calls != 1 {
		t.Fatalf("Probe ran %d times, want 1: nothing starts the loop but StartChecks", calls)
	}

	// A probe that panics or returns a bad Reason is contained.
	mustRegister(t, r, Check{ID: alley, Readiness: true, Probe: func(context.Context) Result { panic("joker") }})
	var alleyState ComponentState
	for _, c := range r.Snapshot().Components {
		if c.ID == alley {
			alleyState = c
		}
	}
	if alleyState.Result.Status != StatusDown || alleyState.Result.Reason != "check_panic" {
		t.Errorf("a panicking probe = %+v, want Down check_panic", alleyState.Result)
	}
	if strings.Contains(alleyState.Result.Detail, "joker") == false {
		t.Errorf("the panic value is missing from Detail: %q", alleyState.Result.Detail)
	}
}

// TestRegistry_ProbeTimeoutIsItsContext: a probe still running at its deadline
// is Down check_timeout. (A Register-time probe that answers after the deadline
// is later accepted, until StartChecks runs: TestRegister_LateAnswerBeforeStartChecksIsAccepted.
// This probe does not answer until the test ends, so the timeout stands.)
func TestRegistry_ProbeTimeoutIsItsContext(t *testing.T) {
	r := NewRegistry()
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	mustRegister(t, r, Check{ID: cave, Timeout: 30 * time.Millisecond, Probe: func(ctx context.Context) Result {
		<-ctx.Done()
		<-release
		return Result{Status: StatusUp, Reason: "answered_late"}
	}})
	if got := r.Snapshot().Components[0].Result; got.Status != StatusDown || got.Reason != "check_timeout" {
		t.Errorf("result = %+v: a probe past its deadline must be Down check_timeout", got)
	}
}

func TestRegistry_SnapshotSortedAndLive(t *testing.T) {
	r := NewRegistry()
	mustRegister(t, r, Check{ID: "wayne.cave", Critical: true})
	mustRegister(t, r, Check{ID: "arkham.cell"})
	mustRegister(t, r, Check{ID: "core.zone"})
	s := r.Snapshot()
	var ids []ComponentID
	for _, c := range s.Components {
		ids = append(ids, c.ID)
	}
	if !slices.Equal(ids, []ComponentID{"arkham.cell", "core.zone", "wayne.cave"}) {
		t.Errorf("components = %v, want sorted by ID", ids)
	}
	if !s.Components[2].Critical || s.Components[0].Critical {
		t.Errorf("Critical not carried: %+v", s.Components)
	}
	if !r.Live() || !s.Live {
		t.Error("Live = false: there is no heartbeat before F8, so a built registry is live")
	}
	if !s.Ready {
		t.Error("Snapshot.Ready disagrees with Ready")
	}
}

// Reporters are cut per component and several goroutines may push at once.
func TestRegistry_ConcurrentReports(t *testing.T) {
	r := NewRegistry()
	a := mustRegister(t, r, Check{ID: cave, Readiness: true})
	b := mustRegister(t, r, Check{ID: alley, Readiness: true})
	var wg sync.WaitGroup
	for i := range 8 {
		rep := []Reporter{a, b}[i%2]
		wg.Go(func() {
			for range 200 {
				rep.Report(Result{Status: StatusUp})
				rep.Report(Result{Status: StatusDown, Reason: "dark"})
				r.Ready()
				r.Snapshot()
			}
		})
	}
	wg.Wait()
	a.Report(Result{Status: StatusUp})
	b.Report(Result{Status: StatusUp})
	if ok, tok := r.Ready(); !ok {
		t.Fatalf("Ready = false %v after both went Up", tok)
	}
}

func TestErrNotOwnedIsASentinel(t *testing.T) {
	if !errors.Is(ErrNotOwned, ErrNotOwned) || ErrNotOwned.Error() == "" {
		t.Fatal("ErrNotOwned is not a usable sentinel")
	}
}

// TestNoGlobals_Health is 03 R5 for core/health: no non-test file of the
// package holds package-level mutable state, so one Registry per App stays
// one. A package-level var may only be a sentinel error built by errors.New
// (ErrNotOwned, ErrUnknownStep); consts, types and funcs are not state. The
// app package's TestNoGlobals scans every core file for the process globals
// (default registry, slog default); this one scans every file of this package
// for any package-level var at all.
func TestNoGlobals_Health(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	scanned := 0
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		scanned++
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				if !sentinelError(vs) {
					t.Errorf("%s: package-level var %v is state; keep it on the Registry (03 R5)", file, vs.Names)
				}
			}
		}
	}
	if scanned < 6 {
		t.Fatalf("scanned %d files, want every non-test file of the package (registry, reporter, snapshot, checks, probes, startup, ...)", scanned)
	}
}

// sentinelError reports a var whose every value is an errors.New(...) call.
func sentinelError(vs *ast.ValueSpec) bool {
	if len(vs.Values) == 0 {
		return false
	}
	for _, v := range vs.Values {
		call, ok := v.(*ast.CallExpr)
		if !ok {
			return false
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		if id, ok := sel.X.(*ast.Ident); !ok || id.Name != "errors" || sel.Sel.Name != "New" {
			return false
		}
	}
	return true
}

// The scan is not blind: it flags a mutable var and passes a sentinel.
func TestNoGlobals_HealthScanFindsAVar(t *testing.T) {
	const src = `package x
import "errors"
var Err = errors.New("ok")
var counter int
var global = map[string]int{}
`
	f, err := parser.ParseFile(token.NewFileSet(), "x.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var flagged []string
	for _, d := range f.Decls {
		if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.VAR {
			for _, spec := range gd.Specs {
				if vs := spec.(*ast.ValueSpec); !sentinelError(vs) {
					flagged = append(flagged, vs.Names[0].Name)
				}
			}
		}
	}
	if !slices.Equal(flagged, []string{"counter", "global"}) {
		t.Fatalf("flagged %v, want [counter global]", flagged)
	}
}
