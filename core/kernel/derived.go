// file: core/kernel/derived.go
// version: 0.1.0
// guid: 8f2a6d41-3c97-4b15-a0e8-d5b7c19e4f62
// last-edited: 2026-11-28

package kernel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// The kernel half of derived indexes (05-storage §5S.3.9 "When the hooks run",
// 06 §6.5.2 step (3a)): P7's validation of the declarations against the hooks
// and build tasks the modules registered, and Run's version/generation check,
// Load, record and rebuild enqueue.
//
// Dark: free functions over explicit inputs, in the shape of updater.go and
// outbox.go. Nothing in boot.go, kernel.go or the supervisor calls them until
// F7-09's flip adds these lines:
//
//   - P7, with the other registration checks: derivedProblems(specs, decls)
//     over the enabled modules' Manifest.Storage and their Registrar.DerivedIndex
//     calls; a returned Problem fails boot with the rest;
//   - Run step (3a), after module hooks and workers have started and before
//     Bus.Open (supervisor.go, "Step (3a) arrives with health"): runDerived with
//     the restore bit of this boot and a start function that queues the build
//     run through the executor (Executor.Start only queues; the run begins at
//     step 5); the Blocking names it returns hold readiness false;
//   - the build task's end: markDerivedBuilt, so the next boot does not queue
//     the build again.
//
// Why runDerived writes the record BEFORE it queues the build. The record's
// state is what makes the order crash safe. A build owed is recorded
// (storage.DerivedBuilding) first and the run queued second, so a crash between
// the two leaves a record that makes the next boot queue the run, on that state
// alone: not on the version (already the new one), not on Load (an old artifact
// of the same generation is accepted by it, since Load cannot see the version),
// and not on the restore bit (a later boot no longer carries it). The price is
// that a crash after the queue call queues a second run on the next boot, so the
// enqueue is at-least-once and start must tolerate a run of the kind already
// queued or running (the executor's concurrency key does, F5). Writing the run
// first would lose a build on a crash instead.

// labelDerivedEnqueue is the crash-matrix region of the rebuild enqueue, the
// run record the tasks store writes for step (3a) (06 §6.5.1 writer table). The
// tasks store has no label of its own for a run insert (its one label is
// tasks.def.upsert), so the region wraps the start call.
const labelDerivedEnqueue = "kernel.derived.enqueue"

// DerivedLabels are the crash-matrix labels of step (3a)'s two durable
// operations, in the order they run: the record, then the enqueue. The rows go
// in core/coretest/crashmatrix; no boot reaches them until F7-09 wires step
// (3a).
func DerivedLabels() []string {
	return append(storage.DerivedLabels(), labelDerivedEnqueue)
}

// derivedDecl is one registered derived index: a Registrar.DerivedIndex call.
type derivedDecl struct {
	Module string
	Name   string
	Hooks  storage.DerivedHooks
	Build  tasks.Definition
}

// derivedBuildKind is the one kind a derived index's build may have
// (05-storage §5S.3.9: "<module>.derived.<name>.build").
func derivedBuildKind(module, name string) tasks.Kind {
	return tasks.Kind(module + ".derived." + name + ".build")
}

// derivedProblems is P7's check of the declarations against the registrations.
// specs holds the Manifest.Storage of every ENABLED module by ID: a disabled
// module registers no hooks, so its declarations are not checked and its
// indexes are not run. A returned problem is ProblemDerivedIndex, sorted by
// module and index name, for:
//
//   - a declared index with no registration, or whose hooks lack Load (Load is
//     what step (3a) calls; Snapshot alone is optional);
//   - a registration for an index the module did not declare;
//   - a registration twice;
//   - a registration with no build task, or whose kind is not exactly
//     "<module>.derived.<name>.build".
func derivedProblems(specs map[string]storage.Spec, decls []derivedDecl) []module.Problem {
	type ref struct{ module, name string }
	var out []module.Problem
	add := func(mod, format string, args ...any) {
		out = append(out, module.Problem{Module: module.ID(mod), Kind: module.ProblemDerivedIndex, Detail: fmt.Sprintf(format, args...)})
	}
	declared := func(mod, name string) bool {
		return slices.ContainsFunc(specs[mod].Derived, func(d storage.DerivedIndex) bool { return d.Name == name })
	}
	registered := map[ref]bool{}
	for _, d := range decls {
		r := ref{d.Module, d.Name}
		switch {
		case !declared(d.Module, d.Name):
			add(d.Module, "Registrar.DerivedIndex(%q) registers an index that Manifest.Storage.Derived does not declare; declare it there (or drop the registration)", d.Name)
			continue
		case registered[r]:
			add(d.Module, "derived index %q is registered twice", d.Name)
			continue
		}
		registered[r] = true
		if d.Hooks.Load == nil {
			add(d.Module, "derived index %q has no Load hook: Registrar.DerivedIndex must be given hooks with Load (Snapshot is optional)", d.Name)
		}
		if want := derivedBuildKind(d.Module, d.Name); d.Build == nil {
			add(d.Module, "derived index %q has no build task: pass the rebuild as a tasks.Definition of kind %q", d.Name, want)
		} else if got := d.Build.Kind(); got != want {
			add(d.Module, "derived index %q has a build task of kind %q, want exactly %q", d.Name, got, want)
		}
	}
	for _, mod := range slices.Sorted(maps.Keys(specs)) {
		for _, di := range specs[mod].Derived {
			if !registered[ref{mod, di.Name}] {
				add(mod, "derived index %q is declared in Manifest.Storage.Derived and was never registered: call Registrar.DerivedIndex(%q, hooks, build) in Register", di.Name, di.Name)
			}
		}
	}
	slices.SortStableFunc(out, func(a, b module.Problem) int {
		if c := strings.Compare(string(a.Module), string(b.Module)); c != 0 {
			return c
		}
		return strings.Compare(a.Detail, b.Detail)
	})
	return out
}

type derivedConfig struct{ log *slog.Logger }

// derivedOption configures runDerived. The one option is the logger, which the
// call's fixed arguments carry no place for.
type derivedOption func(*derivedConfig)

// derivedLog is where runDerived reports what it does not return: a Load error
// (treated as a refusal, so the index is rebuilt) and an unreadable record
// (treated as absent). Without it they are dropped.
func derivedLog(l *slog.Logger) derivedOption { return func(c *derivedConfig) { c.log = l } }

// runDerived is Run step (3a) for the derived indexes (§5S.3.9): for each
// registered index, in the order given (the caller orders them: modules in ID
// order, an index's registrations in order),
//
//  1. read the declaration (Version, Blocking) and the record written last;
//  2. read Derived.Current, the store generation and last touch;
//  3. call hooks.Load over dir/<module>/<name> (dir is storage.DerivedRoot's
//     value): an error is a refusal, never acceptance;
//  4. write the record: DerivedBuilding when a build is owed, else DerivedReady
//     with built_at = now();
//  5. when a build is owed, call start with the build kind.
//
// A build is owed when the record is absent or unreadable, its version is not
// the declared one, its state is not DerivedReady (a crash left the build
// unfinished), Load did not accept, or restored (this boot follows a restore,
// 05-backup §5B.3.4). The check is on the record and Load, never on one alone.
//
// It returns the "<module>/<name>" of every Blocking index whose build it
// queued: readiness stays false for them until markDerivedBuilt (unbuiltBlocking
// reads it again). An error stops the loop at the failing index, with the names
// found so far: a record that cannot be written, or a start that failed, leaves
// the record DerivedBuilding, so the next boot queues the build.
//
// The indexes run one after another: a handful of declarations (not a library
// scale loop), whose Loads may use objects a start hook built, and whose two
// writes the crash cells need in a fixed order. The package's own state is
// untouched, so it runs beside started hooks and workers, which may Touch the
// same indexes.
func runDerived(ctx context.Context, db *storage.DB, dir string, decls []derivedDecl, restored bool,
	start func(ctx context.Context, kind tasks.Kind) error, now func() time.Time, opts ...derivedOption) (blocking []string, err error) {
	cfg := derivedConfig{log: slog.New(slog.DiscardHandler)}
	for _, o := range opts {
		o(&cfg)
	}
	if now == nil {
		now = time.Now
	}
	for _, d := range decls {
		owed, block, err := runDerivedOne(ctx, db, dir, d, restored, start, now, cfg.log)
		if err != nil {
			return blocking, err
		}
		if owed && block {
			blocking = append(blocking, d.Module+"/"+d.Name)
		}
	}
	return blocking, nil
}

// runDerivedOne is runDerived for one index: whether a build was owed and
// queued, and whether the index is Blocking.
func runDerivedOne(ctx context.Context, db *storage.DB, dir string, d derivedDecl, restored bool,
	start func(ctx context.Context, kind tasks.Kind) error, now func() time.Time, log *slog.Logger) (owed, blocking bool, err error) {
	name := d.Module + "/" + d.Name
	owner := storage.Owner(d.Module)
	if d.Build == nil {
		return false, false, fmt.Errorf("kernel: derived index %s has no build task (P7 should have refused it)", name)
	}
	decl, err := db.DerivedDeclaration(owner, d.Name)
	if err != nil {
		return false, false, fmt.Errorf("kernel: derived index %s: %w", name, err)
	}
	handle, err := db.Derived(owner, d.Name)
	if err != nil {
		return false, false, fmt.Errorf("kernel: derived index %s: %w", name, err)
	}
	prev, found, err := db.DerivedRecord(ctx, owner, d.Name)
	if errors.Is(err, storage.ErrDerivedRecordUnreadable) {
		log.WarnContext(ctx, "kernel: the derived-index record is unreadable; the index is rebuilt", "index", name, "err", err)
		prev, found, err = storage.DerivedRecord{}, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("kernel: derived index %s: read its record: %w", name, err)
	}
	cur, err := handle.Current(ctx)
	if err != nil {
		return false, false, fmt.Errorf("kernel: derived index %s: its generation: %w", name, err)
	}

	accepted := false
	if d.Hooks.Load != nil {
		ok, lerr := d.Hooks.Load(ctx, storage.DerivedArtifactDir(dir, owner, d.Name), cur)
		if lerr != nil {
			log.WarnContext(ctx, "kernel: a derived index's Load failed; treated as a refusal, the index is rebuilt", "index", name, "err", lerr)
		} else {
			accepted = ok
		}
	}

	owed = restored || !found || prev.Version != decl.Version || prev.State != storage.DerivedReady || !accepted
	rec := storage.DerivedRecord{Version: decl.Version, State: storage.DerivedReady, BuiltAt: now().UTC()}
	if owed {
		rec.State, rec.BuiltAt = storage.DerivedBuilding, prev.BuiltAt
	}
	if err := db.PutDerivedRecord(ctx, owner, d.Name, rec); err != nil {
		return false, false, fmt.Errorf("kernel: derived index %s: %w", name, err)
	}
	if !owed {
		return false, decl.Blocking, nil
	}
	exit := bootseam.Region(ctx, labelDerivedEnqueue)
	err = start(ctx, d.Build.Kind())
	exit()
	if err != nil {
		return true, decl.Blocking, fmt.Errorf("kernel: derived index %s: queue its build %s: %w", name, d.Build.Kind(), err)
	}
	return true, decl.Blocking, nil
}

// markDerivedBuilt records that module's index name finished a build at the
// version this build declares: state DerivedReady, built_at now. The build task
// calls it as its last step (F7-09 wires it); without it the record stays
// DerivedBuilding and every boot queues the build again.
func markDerivedBuilt(ctx context.Context, db *storage.DB, module, name string, now func() time.Time) error {
	if now == nil {
		now = time.Now
	}
	decl, err := db.DerivedDeclaration(storage.Owner(module), name)
	if err != nil {
		return fmt.Errorf("kernel: derived index %s/%s: %w", module, name, err)
	}
	rec := storage.DerivedRecord{Version: decl.Version, State: storage.DerivedReady, BuiltAt: now().UTC()}
	if err := db.PutDerivedRecord(ctx, storage.Owner(module), name, rec); err != nil {
		return fmt.Errorf("kernel: derived index %s/%s: %w", module, name, err)
	}
	return nil
}

// unbuiltBlocking is the "<module>/<name>" of every Blocking index among decls
// whose record is not DerivedReady at the declared version: what readiness
// waits for (05-health). It reads committed records only.
func unbuiltBlocking(ctx context.Context, db *storage.DB, decls []derivedDecl) ([]string, error) {
	var out []string
	for _, d := range decls {
		owner := storage.Owner(d.Module)
		decl, err := db.DerivedDeclaration(owner, d.Name)
		if err != nil {
			return nil, fmt.Errorf("kernel: derived index %s/%s: %w", d.Module, d.Name, err)
		}
		if !decl.Blocking {
			continue
		}
		rec, found, err := db.DerivedRecord(ctx, owner, d.Name)
		if err != nil && !errors.Is(err, storage.ErrDerivedRecordUnreadable) {
			return nil, fmt.Errorf("kernel: derived index %s/%s: %w", d.Module, d.Name, err)
		}
		if !found || rec.Version != decl.Version || rec.State != storage.DerivedReady {
			out = append(out, d.Module+"/"+d.Name)
		}
	}
	return out, nil
}
