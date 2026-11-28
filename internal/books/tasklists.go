// file: internal/books/tasklists.go
// version: 0.1.0
// guid: 7d3a9e52-1c64-4b08-a5f7-92e0d4b6c183
// last-edited: 2026-11-22

package books

import (
	"context"
	"fmt"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/plugins/acoustid"
	"github.com/falkcorp/audiobook-organizer/internal/plugins/deluge"
	"github.com/falkcorp/audiobook-organizer/internal/plugins/itunes"
	"github.com/falkcorp/audiobook-organizer/internal/plugins/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/plugins/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/scheduler"
)

// taskListInputs is everything bookTaskLists builds the module's task lists
// from: the books services, each as the narrow interface its package declares
// (never database.Store). Schedules is books' one schedule hook (NewSchedules);
// it is copied into every package's inputs so the lists cannot disagree on it.
type taskListInputs struct {
	Schedules   func(tasks.Kind) *tasks.Schedule
	Maintenance maintenance.BooksTaskInputs
	Metafetch   metafetch.BooksTaskInputs
	Deluge      deluge.BooksTaskInputs
	ITunes      itunes.BooksTaskInputs
	Acoustid    acoustid.BooksTaskInputs
	Scheduler   scheduler.BooksTaskInputs
	Repairs     repairs.BooksTaskInputs
}

// bookTaskLists is every task definition the books module registers through
// Registrar.Tasks at the F5-18 flip, built over in. It is NOT called by
// Module(): until the flip's registration commit nothing reaches it, so no
// native kind is registered by it (F5-18 A6, dark).
//
// Not built yet (F5-18 A6 carries them, each its own slice of work, named in
// notYetBuilt in tasklists_test.go with the kinds it will bring):
//
//   - TODO-F5-18-A6-MAINT-G15: maintenance groups 1 and 5 deps builders
//     (t6g1Deps, t6g5Deps; #5003 has merged, so Inputs.Runs
//     is the store their run-plan reads can take)
//     and their entries in maintenance.BooksTaskLists.
//   - TODO-F5-18-A6-DEDUP: the dedup plugin's KindDeps builder (five groups).
//   - TODO-F5-18-A6-SERVER-T7: internal/server's t7Deps builder, in a new file.
//
// Left out on purpose: the 14 group-6 maintenance kinds. They hold a
// library-scan stand-down for an apply's whole length and must not register
// before core has Hold.Renew. TODO-F5-11-HOLD-RENEW (slice F5-18e):
// once that merges, append maintenance.BooksHeldTaskLists here and delete
// the group-6 entries from notYetBuilt in the registration test.
func bookTaskLists(in taskListInputs) ([]tasks.Definition, error) {
	var out []tasks.Definition

	mi := in.Maintenance
	mi.Schedules = in.Schedules
	m, err := maintenance.BooksTaskLists(mi)
	if err != nil {
		return nil, fmt.Errorf("books task lists: %w", err)
	}
	out = append(out, m...)

	mf := in.Metafetch
	mf.Schedules = in.Schedules
	l, err := metafetch.BooksTaskLists(mf)
	if err != nil {
		return nil, fmt.Errorf("books task lists: %w", err)
	}
	out = append(out, l...)

	dl := in.Deluge
	dl.Schedules = in.Schedules
	l, err = deluge.BooksTaskLists(dl)
	if err != nil {
		return nil, fmt.Errorf("books task lists: %w", err)
	}
	out = append(out, l...)

	it := in.ITunes
	it.Schedules = in.Schedules
	l, err = itunes.BooksTaskLists(it)
	if err != nil {
		return nil, fmt.Errorf("books task lists: %w", err)
	}
	out = append(out, l...)

	ac := in.Acoustid
	ac.Schedules = in.Schedules
	l, err = acoustid.BooksTaskLists(ac)
	if err != nil {
		return nil, fmt.Errorf("books task lists: %w", err)
	}
	out = append(out, withoutWithheld(l)...)

	sc := in.Scheduler
	sc.Schedules = in.Schedules
	l, err = scheduler.BooksTaskLists(sc)
	if err != nil {
		return nil, fmt.Errorf("books task lists: %w", err)
	}
	out = append(out, l...)

	ri := in.Repairs
	ri.Scopes = atomScopes
	// A BindStores that is set but binds nothing (an identity func) passes the
	// repairs package's nil check and then every fixer refuses at its first
	// run. Probe it once here, by the type the fixers ask for.
	if ri.BindStores != nil {
		if _, ok := repairs.StoresFrom[maintenance.FixerStores](ri.BindStores(context.Background())); !ok {
			return nil, fmt.Errorf("books task lists: Repairs.BindStores binds no maintenance.FixerStores")
		}
	}
	l, err = repairs.BooksTaskLists(ri)
	if err != nil {
		return nil, fmt.Errorf("books task lists: %w", err)
	}
	out = append(out, l...)

	return out, nil
}

// withheldKinds are kinds a plugin's list builds that bookTaskLists leaves out
// on purpose, each with the reason, because registering them now would give a
// kind that refuses every run. They are not in the manifest ledger either
// (Manifest.TaskLedger is the kinds bookTaskLists returns). A fresh map per call.
//
//   - books.acoustid.scan: the scan runs the dedup engine, and the engine writes
//     its candidates through its own stores. Building one needs the wide
//     database.Store and would bypass the writer gate (the F5-04 seam), so the
//     kind waits for A6d's gated dedup engine; acoustid.BooksTaskInputs.NewEngine
//     is that hook.
func withheldKinds() map[tasks.Kind]string {
	return map[tasks.Kind]string{
		"books.acoustid.scan": "needs A6d's gated dedup engine (F5-04 seam)",
	}
}

// withoutWithheld is defs less the kinds withheldKinds names.
func withoutWithheld(defs []tasks.Definition) []tasks.Definition {
	withheld := withheldKinds()
	out := make([]tasks.Definition, 0, len(defs))
	for _, d := range defs {
		if _, skip := withheld[d.Kind()]; !skip {
			out = append(out, d)
		}
	}
	return out
}

// atomScopes resolves a HEAD permission atom to the native scopes books' atom
// table (AtomTable) writes for it, which is what a Repairs kind's Start
// requirement is made of. An atom the table does not hold resolves to false.
func atomScopes(atom auth.Permission) ([]rbac.Permission, bool) {
	for _, a := range AtomTable() {
		if a.Name == string(atom) {
			return a.Scopes, len(a.Scopes) > 0
		}
	}
	return nil, false
}
