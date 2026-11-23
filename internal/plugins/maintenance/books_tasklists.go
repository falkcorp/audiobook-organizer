// file: internal/plugins/maintenance/books_tasklists.go
// version: 0.1.0
// guid: 44996765-6630-4d30-9d92-103b7d071370
// last-edited: 2026-11-22

package maintenance

import (
	"fmt"

	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/internal/maintenance/jobs"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// This file is the one exported door of the maintenance package's converted
// kinds (F5-18 A6): BooksTaskLists builds every list the books module passes to
// Registrar.Tasks over the dependency values F5-18c built (deps_*.go), and
// nothing else in the package is reachable from outside. It is dark: no code
// outside tests calls it until the flip's registration commit.
//
// The lists, and what the flip must still decide about each:
//
//   - jobs.Kinds over newJobsDeps (the maintenance jobs).
//   - t6g2Kinds, t6g3Kinds, t6g4Kinds, t6g7Kinds over newT6G2Deps,
//     newT6G3Deps, newT6G4Deps, newT6G7Deps.
//   - auxStoreCompactKindList, the books half of db_optimize.
//
// Group 6 is NOT in BooksTaskLists. Its 14 kinds hold a library-scan
// stand-down for the length of an apply and must not register before core has
// Hold.Renew (TODO-F5-11-HOLD-RENEW, slice F5-18e): BooksHeldTaskLists builds
// them for the day slice e has merged, and the books module does not call it.

// The source and host interfaces of the builders, named so the books module can
// hand its store and server to them. Every one is the group's own narrow
// interface (the store satisfies them at the call site; none is
// database.Store), kept as aliases so the builders' signatures do not move.
type (
	// G2Source is the store the group-2 (shape and index repairs) builder reads
	// and writes through.
	G2Source = t6g2Source
	// G2BootHost is the boot-time library walks the host runs for group 2.
	G2BootHost = t6g2BootHost
	// G3Source is the store the group-3 (cleanup, census, cover text) builder
	// reads and writes through.
	G3Source = t6g3Source
	// G3BootHost is what the host serves group 3 that is not a store row.
	G3BootHost = t6g3BootHost
	// G4BootHost is what the host serves group 4 (file intros, transcription).
	G4BootHost = t6g4BootHost
	// AIBatchSubmitter is the one call group 4 makes on the AI parser; the
	// alias lets a host outside this package name the result type of
	// G4BootHost.AIBatchSubmitter.
	AIBatchSubmitter = t6g4AIBatchSubmitter
	// G6BootHost is the activity compactions the host runs for group 6.
	G6BootHost = t6g6BootHost
)

// BooksTaskInputs is everything BooksTaskLists builds the maintenance lists
// from. A nil field a list needs makes BooksTaskLists return an error naming
// it: the kind would otherwise register and fail at its first run with a
// refusal nobody reads at boot.
type BooksTaskInputs struct {
	// Jobs is the store the maintenance jobs read and write through.
	Jobs jobs.Store
	// G2Source and G2Host serve group 2; RootDir confines its report path.
	G2Source G2Source
	G2Host   G2BootHost
	RootDir  func() string
	// G3Source and G3Host serve group 3; GuardTags is the credits backfill's
	// franchise-tag reader (nil skips the tag check, as the guard documents).
	G3Source  G3Source
	G3Host    G3BootHost
	GuardTags repairs.GuardTagReader
	// Runs is the tasks run store (the kernel's NativeStores.Runs): the native
	// credits backfill keeps its class lists, ledger and judgements in it.
	Runs CreditsRunStore
	// Ops is the ops store group 4 reads and writes through; G4Host and Scan
	// serve it. Scan nil is HEAD's degraded caller (no scanner to stand down).
	Ops    OpsStore
	G4Host G4BootHost
	Scan   ScanController
	// AuxStores compacts books' separate AI-scan and OpenLibrary stores.
	AuxStores StoreOptimizer
	// G7 serves group 7 (the repair, report and user-merge kinds). Its RootDir
	// and StandDownKey are the shared ones above, whatever it carries.
	G7 G7Inputs
	// StandDownKey is the concurrency key of the library scan kinds, which a
	// live write parks (rc.StandDown). Empty makes a live write that needs the
	// stand-down refuse, so it is required here.
	StandDownKey tasks.Key
	// Schedules is books' schedule hook (books.NewSchedules).
	Schedules func(tasks.Kind) *tasks.Schedule
}

// CreditsRunStore is what the native credits backfill needs of the tasks run
// store; tasks.RunStore satisfies it.
type CreditsRunStore = cbRunStore

// tasks.RunStore, the store core hands the host, must satisfy CreditsRunStore: the
// flip passes one for the other, and a method dropped from either is caught here
// at compile time instead of at the wiring.
var _ CreditsRunStore = tasks.RunStore(nil)

// need collects the names of the missing inputs.
type need []string

func (n *need) check(name string, set bool) {
	if !set {
		*n = append(*n, name)
	}
}

func (n need) err(what string) error {
	if len(n) == 0 {
		return nil
	}
	return fmt.Errorf("maintenance: %s: missing input(s): %v", what, []string(n))
}

// BooksTaskLists returns every maintenance kind the books module registers,
// each with its schedule from in.Schedules, over the books module's own
// dependency values.
func BooksTaskLists(in BooksTaskInputs) ([]tasks.Definition, error) {
	var n need
	n.check("Jobs", in.Jobs != nil)
	n.check("G2Source", in.G2Source != nil)
	n.check("G2Host", in.G2Host != nil)
	n.check("RootDir", in.RootDir != nil)
	n.check("G3Source", in.G3Source != nil)
	n.check("G3Host", in.G3Host != nil)
	n.check("Runs", in.Runs != nil)
	n.check("Ops", in.Ops != nil)
	n.check("G4Host", in.G4Host != nil)
	n.check("AuxStores", in.AuxStores != nil)
	n.check("StandDownKey", in.StandDownKey != "")
	n.check("Schedules", in.Schedules != nil)
	n.check("G7.Reads", isSet(in.G7.Reads))
	n.check("G7.Writes", isSet(in.G7.Writes))
	n.check("G7.InvalidateSeriesCache", in.G7.InvalidateSeriesCache != nil)
	n.check("G7.SupportedExtensions", in.G7.SupportedExtensions != nil)
	if err := n.err("BooksTaskLists"); err != nil {
		return nil, err
	}
	p := &Plugin{}
	var out []tasks.Definition

	jd, err := newJobsDeps(in.Jobs)
	if err != nil {
		return nil, fmt.Errorf("maintenance jobs: %w", err)
	}
	jl, err := jobs.Kinds(jd, in.Schedules)
	if err != nil {
		return nil, fmt.Errorf("maintenance jobs: %w", err)
	}
	out = append(out, jl...)

	out = append(out, p.t6g2Kinds(newT6G2Deps(in.G2Source, in.G2Host, in.RootDir), in.Schedules)...)
	out = append(out, p.t6g3Kinds(newT6G3Deps(in.G3Source, in.G3Host, in.GuardTags, in.StandDownKey, in.Runs), in.Schedules)...)

	g4, err := t6g4Kinds(newT6G4Deps(in.Ops, in.G4Host, in.Scan), in.Schedules)
	if err != nil {
		return nil, fmt.Errorf("maintenance group 4: %w", err)
	}
	out = append(out, g4...)

	g7 := in.G7
	g7.RootDir, g7.StandDownKey = in.RootDir, in.StandDownKey
	out = append(out, p.t6g7Kinds(newT6G7Deps(g7), in.Schedules)...)

	out = append(out, auxStoreCompactKindList(in.AuxStores, in.Schedules(auxStoreCompactKind))...)
	return out, nil
}

// HeldTaskInputs is what BooksHeldTaskLists builds the group-6 kinds from.
type HeldTaskInputs struct {
	// Ops is the ops store the group reads and writes through; Host runs the
	// activity compactions.
	Ops  OpsStore
	Host G6BootHost
	// ActivityFullDetailDays is the configured activity_log_full_detail_days.
	ActivityFullDetailDays func() int
	// StandDownKey is the concurrency key of the library scan kinds.
	StandDownKey tasks.Key
	// RootDir is the library root; the report path is confined under it.
	RootDir func() string
	// ITunesRoots resolves the protected iTunes roots.
	ITunesRoots func() ([]string, error)
	// Schedules is books' schedule hook.
	Schedules func(tasks.Kind) *tasks.Schedule
}

// BooksHeldTaskLists returns the 14 group-6 kinds. They are held back from the
// books registration until core has Hold.Renew (TODO-F5-11-HOLD-RENEW, slice
// F5-18e): a live apply of these parks the scan for as long as it runs, and
// without a renewing hold the lease lapses mid-run. The books module does not
// call this; the flip does, once slice e has merged.
func BooksHeldTaskLists(in HeldTaskInputs) ([]tasks.Definition, error) {
	var n need
	n.check("Ops", in.Ops != nil)
	n.check("Host", in.Host != nil)
	n.check("ActivityFullDetailDays", in.ActivityFullDetailDays != nil)
	n.check("StandDownKey", in.StandDownKey != "")
	n.check("RootDir", in.RootDir != nil)
	n.check("ITunesRoots", in.ITunesRoots != nil)
	n.check("Schedules", in.Schedules != nil)
	if err := n.err("BooksHeldTaskLists"); err != nil {
		return nil, err
	}
	deps := newT6G6Deps(in.Ops, in.Host, in.ActivityFullDetailDays, in.StandDownKey, in.RootDir, in.ITunesRoots)
	return (&Plugin{}).t6g6Kinds(deps, in.Schedules), nil
}
