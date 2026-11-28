// file: internal/books/tasklists_test.go
// version: 0.1.0
// guid: 5b1e8c34-d902-47a6-8f13-6e0a2c9d7b45
// last-edited: 2026-11-22

package books

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	"github.com/falkcorp/audiobook-organizer/internal/plugins/acoustid"
	"github.com/falkcorp/audiobook-organizer/internal/plugins/deluge"
	"github.com/falkcorp/audiobook-organizer/internal/plugins/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/plugins/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/scheduler"
	"github.com/falkcorp/audiobook-organizer/internal/versions"
	"github.com/falkcorp/audiobook-organizer/internal/walkresume"
)

// A real store under every list's inputs, and recording-free fakes for the
// host-side services (the server's boot passes, its configuration reads and the
// auxiliary stores): the lists are built, never run, so the fakes only have to
// exist.

type fakeWalkRun = walkresume.Run

type listsG2Host struct{}

func (listsG2Host) BackfillExternalIDs(context.Context, bool, func(int, int, string), fakeWalkRun) (bool, error) {
	return false, nil
}

func (listsG2Host) StripMovementAtoms(context.Context, fakeWalkRun) (bool, error) { return false, nil }
func (listsG2Host) RemuxMalformedM4BFiles(context.Context, func(int, int, string), fakeWalkRun) (bool, error) {
	return false, nil
}

func (listsG2Host) TranscodeMalformedM4BFiles(context.Context, func(int, int, string), fakeWalkRun) (bool, error) {
	return false, nil
}

func (listsG2Host) QuarantineKnownBadFiles(context.Context) (int, bool, error) { return 0, false, nil }

type listsG3Host struct{ root string }

func (h listsG3Host) RootDir() string                                     { return h.root }
func (listsG3Host) BackupRetentionDays() int                              { return 7 }
func (listsG3Host) ActivityLogNightlyCompactionEnabled() bool             { return false }
func (listsG3Host) ActivityLogCompactionDays() int                        { return 30 }
func (listsG3Host) ActivityLogRetentionChangeDays() int                   { return 90 }
func (listsG3Host) ActivityLogRetentionDebugDays() int                    { return 7 }
func (listsG3Host) OperationQueueStore() maintenance.OpQueueReader        { return nil }
func (listsG3Host) RunAutoPurgeSoftDeleted(context.Context, string) error { return nil }
func (listsG3Host) CleanupOrphanedTempFiles(string, string) int           { return 0 }
func (listsG3Host) CleanupTrashedVersions() (versions.CleanupResult, error) {
	return versions.CleanupResult{}, nil
}
func (listsG3Host) ActivityFlushOp(string) {}
func (listsG3Host) MaintainActivityLog(context.Context, int, int) (int, int, int64, error) {
	return 0, 0, 0, nil
}

func (listsG3Host) CompactActivityEntries(context.Context, time.Time, database.CompactProgress) (database.CompactResult, error) {
	return database.CompactResult{}, nil
}
func (listsG3Host) InvalidateAuthorsCache() {}

type listsG4Host struct{ root string }

func (h listsG4Host) RootDir() string                                 { return h.root }
func (listsG4Host) OperationQueueStore() maintenance.OpQueueReader    { return nil }
func (listsG4Host) HasDedupEngine() bool                              { return false }
func (listsG4Host) HasAIParsing() bool                                { return false }
func (listsG4Host) FileProvenanceStore() database.FileProvenanceStore { return nil }
func (listsG4Host) AIBatchSubmitter() maintenance.AIBatchSubmitter    { return nil }
func (listsG4Host) DedupTriageExactPending(context.Context, bool) (*maintenance.TriageReport, error) {
	return &maintenance.TriageReport{}, nil
}

type listsAux struct{}

func (listsAux) OptimizeAIScanStore(context.Context) error { return nil }
func (listsAux) OptimizeOLStore(context.Context) error     { return nil }
func (listsAux) AIScanStoreCompactionStats() (database.CompactionStats, bool) {
	return database.CompactionStats{}, false
}

func (listsAux) OLStoreCompactionStats() (database.CompactionStats, bool) {
	return database.CompactionStats{}, false
}

// listsRuns is the run store the credits backfill reads; bookTaskLists only
// builds the definitions, so no call reaches it.
type listsRuns struct{}

func (listsRuns) List(context.Context, tasks.RunQuery) iter.Seq2[tasks.RunRecord, error] {
	return func(func(tasks.RunRecord, error) bool) {}
}

func (listsRuns) ReplaceRows(context.Context, tasks.RunID, string, int, func(int) (tasks.Row, error)) error {
	return nil
}

func (listsRuns) Rows(context.Context, tasks.RunID) iter.Seq2[tasks.Row, error] {
	return func(func(tasks.Row, error) bool) {}
}

type listsUpgrader struct{}

func (listsUpgrader) RunUpgrade(context.Context, int, operations.ProgressReporter) (*metabatch.UpgradeResult, error) {
	return &metabatch.UpgradeResult{}, nil
}

type listsCandidates struct{}

func (listsCandidates) GetCachedCandidates(string) (*database.MetadataCandidateCache, bool, error) {
	return nil, false, nil
}

type listsProtectedCache struct{}

func (listsProtectedCache) Refresh() error          { return nil }
func (listsProtectedCache) IsProtected(string) bool { return false }

// listsFixture is the inputs of bookTaskLists over a real Pebble store.
func listsFixture(t *testing.T) taskListInputs {
	t.Helper()
	root := filepath.Join(t.TempDir(), "library")
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return taskListInputs{
		Schedules: NewSchedules(ScheduleDeps{}),
		Maintenance: maintenance.BooksTaskInputs{
			Jobs:      store,
			G2Source:  store,
			G2Host:    listsG2Host{},
			RootDir:   func() string { return root },
			G3Source:  store,
			G3Host:    listsG3Host{root: root},
			Runs:      listsRuns{},
			Ops:       store,
			G4Host:    listsG4Host{root: root},
			AuxStores: listsAux{},
			G7: maintenance.G7Inputs{
				Reads: store, Writes: store,
				InvalidateSeriesCache: func() {},
				SupportedExtensions:   func() []string { return []string{".m4b"} },
			},
			StandDownKey: tasks.Key("books.library.scan"),
		},
		Metafetch: metafetch.BooksTaskInputs{
			Source:     store,
			Candidates: listsCandidates{},
			Scoring:    func() config.MetadataScoringConfig { return config.MetadataScoringConfig{} },
			NewAudible: func() metafetch.AudibleSearcher { return nil },
		},
		Deluge: deluge.BooksTaskInputs{
			Source: store,
			Cache:  listsProtectedCache{},
			Config: &config.Config{},
		},
		Acoustid: acoustid.BooksTaskInputs{
			Plugin: &acoustid.Plugin{},
			Source: store,
		},
		Scheduler: scheduler.BooksTaskInputs{
			Upgrader:     listsUpgrader{},
			StandDownKey: tasks.Key("books.library.scan"),
		},
		Repairs: repairs.BooksTaskInputs{
			// The real fixers, so every one of their permission atoms must
			// resolve through books' own atom table.
			Fixers:   (&maintenance.Plugin{}).Repairs().List(),
			Guard:    store,
			Stores:   store,
			PlanRows: listsPlanRows{},
			BindStores: func(ctx context.Context) context.Context {
				return repairs.WithStores[maintenance.FixerStores](ctx, listsFixerStores{})
			},
			StandDownKey: tasks.Key("books.library.scan"),
		},
	}
}

// listsPlanRows is the kernel's plan-row reader, which this fixture has no run
// for: bookTaskLists only builds the definitions.
type listsPlanRows struct{}

func (listsPlanRows) PlanOf(context.Context, tasks.RunID) (string, error) { return "", nil }
func (listsPlanRows) PlannedRow(context.Context, tasks.RunID, string) (json.RawMessage, bool, error) {
	return nil, false, nil
}

// notYetBuilt are the scheduled kinds bookTaskLists does not carry yet, each with
// the piece that will. An entry fails the test the moment its kind appears in the
// lists, so the table shrinks as the builders land and nothing is silently
// dropped. The held group-6 kinds stay here until TODO-F5-11-HOLD-RENEW.
var notYetBuilt = map[tasks.Kind]string{
	// internal/server t7Deps builder (kinds_t7.go).
	"books.catalog.harvest-authors":              "server t7",
	"books.collections.refresh-relative-dynamic": "server t7",
	"books.dedup.author-scan":                    "server t7",
	"books.dedup.series-normalize":               "server t7",
	"books.dedup.series-prune":                   "server t7",
	"books.identification.advance":               "server t7",
	"books.library.organize":                     "server t7",
	"books.library.scan":                         "server t7",
	"books.library.scan-full":                    "server t7",
	"books.library.size-refresh":                 "server t7",
	"books.metadata.candidate-fetch":             "server t7",
	"books.reconcile.scan":                       "server t7",
	// acoustid.scan is built by the acoustid list and withheld by bookTaskLists
	// (withheldKinds), not scheduled: HEAD runs it from the UI only.
	"books.acoustid.scan": "A6d gated dedup engine (F5-04 seam)",
	// the dedup plugin's KindDeps builder.
	"books.dedup.label-refinement": "dedup plugin",
	"books.dedup.llm-review":       "dedup plugin",
	// maintenance groups 1 and 5 builders; nightly-compact-activity-log is group 6
	// (held for TODO-F5-11-HOLD-RENEW).
	"books.maintenance.author-split-scan":            "maintenance g1/g5",
	"books.maintenance.metadata-refresh":             "maintenance g1/g5",
	"books.maintenance.orphan-book-files-cleanup":    "maintenance g1/g5",
	"books.maintenance.prune-ai-journal":             "maintenance g1/g5",
	"books.maintenance.resolve-production-authors":   "maintenance g1/g5",
	"books.maintenance.nightly-compact-activity-log": "maintenance g6 (held, TODO-F5-11-HOLD-RENEW)",
}

// scheduleSig is what a schedule reports through its accessors. tasks.CommonOf
// deep-copies the Schedule (a definition and its caller never share one), so
// pointer identity with the hook's schedule is not observable; the config key is
// unique per kind (TestF5_ScheduleKeysAreUniquePerKind), so it ties a definition
// to the hook's schedule for that kind, and the rest guards the copy.
type scheduleSig struct {
	key                                              string
	every                                            time.Duration
	onStart, startGate, intervalGate, paramsFn, live bool
	preview                                          bool
	missed                                           tasks.MissedPolicy
	params                                           string
	open, startOpen, intervalOn, windowOpen          bool
}

func sigOf(s *tasks.Schedule) scheduleSig {
	return scheduleSig{
		key: s.ConfigKey(), every: s.Interval(), onStart: s.RunsOnStart(),
		startGate: s.HasStartGate(), intervalGate: s.HasIntervalGate(), paramsFn: s.HasParamsFunc(),
		live: s.IsLive(), preview: s.IsPreview(), missed: s.OnMissed(), params: string(s.RunParams()),
		open: s.Open(), startOpen: s.StartOpen(), intervalOn: s.IntervalEnabled(), windowOpen: s.WindowOpen(),
	}
}

func TestF5_ScheduleKeysAreUniquePerKind(t *testing.T) {
	hook := NewSchedules(ScheduleDeps{})
	seen := map[string]tasks.Kind{}
	for _, k := range ScheduledKinds() {
		s := hook(k)
		require.NotNil(t, s, k)
		key := s.ConfigKey()
		if prev, dup := seen[key]; dup {
			t.Fatalf("kinds %s and %s share schedule key %q: a schedule comparison cannot tell them apart", prev, k, key)
		}
		seen[key] = k
	}
}

func TestF5_ScheduledKindsRegistered(t *testing.T) {
	// The real-module subtest runs this test again in a child process (a boot with
	// the predicate on needs the process's ops recorder untouched, and the lists
	// fixture below makes it). The child runs the subtest alone.
	if os.Getenv(freshProcessEnv) != "" {
		t.Run("real module", registeredByTheRealModule)
		return
	}
	in := listsFixture(t)
	defs, err := bookTaskLists(in)
	require.NoError(t, err)

	byKind := map[tasks.Kind]tasks.Definition{}
	for _, d := range defs {
		require.NotContains(t, byKind, d.Kind(), "kind %s is in the lists twice", d.Kind())
		byKind[d.Kind()] = d
		require.Empty(t, tasks.Problems(d), d.Kind())
	}

	scheduled := map[tasks.Kind]bool{}
	for _, k := range ScheduledKinds() {
		scheduled[k] = true
		if _, ok := byKind[k]; !ok {
			require.Contains(t, notYetBuilt, k, "scheduled kind %s is in no list and not in notYetBuilt", k)
		}
	}
	for k, owner := range notYetBuilt {
		require.NotContains(t, byKind, k, "%s is built now: drop it from notYetBuilt (was waiting on %s)", k, owner)
		_, withheld := withheldKinds()[k]
		require.True(t, scheduled[k] || withheld, "notYetBuilt names %s, which is neither a scheduled kind nor withheld by bookTaskLists", k)
	}
	for k, why := range withheldKinds() {
		require.Contains(t, notYetBuilt, k, "bookTaskLists withholds %s (%s) and notYetBuilt does not say so", k, why)
		require.NotContains(t, byKind, k, "%s is withheld and still in the lists", k)
	}

	// The hook the lists were built with is the one books ships.
	hook := in.Schedules
	for k, d := range byKind {
		got := tasks.CommonOf(d).Schedule
		if !scheduled[k] {
			require.Nil(t, got, "%s is outside ScheduledKinds but carries a schedule", k)
			continue
		}
		require.NotNil(t, got, "%s is scheduled but its definition has no schedule", k)
		require.Equal(t, sigOf(hook(k)), sigOf(got), "%s carries another kind's schedule", k)
	}

	// The same holds for what the real module registers: the lists reach
	// Registrar.Tasks from Register with the predicate on, every scheduled kind
	// the lists carry is registered with its schedule, and the schedule's live
	// gates answer from the booted server, not from absent hooks.
	t.Run("real module", registeredByTheRealModule)
}

// registeredByTheRealModule is TestF5_ScheduledKindsRegistered's real-module
// subtest: it boots the composition with the predicate on, in a child process.
func registeredByTheRealModule(t *testing.T) {
	if parent, _ := inFreshProcess(t); parent {
		return
	}
	t.Cleanup(SetTasksFlipForTest(true))
	var rec module.Registrations
	c := bootComposedWith(t, false, "", nil, nil, nil, func(m module.Module) module.Module {
		return registrationTap{Module: m, rec: &rec}
	})

	reg := map[tasks.Kind]tasks.Definition{}
	for _, d := range rec.Tasks {
		require.NotContains(t, reg, d.Kind(), "kind %s registered twice", d.Kind())
		reg[d.Kind()] = d
		require.Empty(t, tasks.Problems(d), d.Kind())
	}
	require.NotEmpty(t, reg, "Register recorded no task definition with the predicate on")
	for k := range reg {
		require.Contains(t, taskLedger(), k, "%s is registered but not in the manifest ledger", k)
	}
	for _, k := range taskLedger() {
		require.Contains(t, reg, k, "%s is in the manifest ledger but Register did not register it", k)
	}

	hook := NewSchedules(ScheduleDeps{})
	for _, k := range ScheduledKinds() {
		d, ok := reg[k]
		if !ok {
			require.Contains(t, notYetBuilt, k, "scheduled kind %s is neither registered nor in notYetBuilt", k)
			continue
		}
		got := tasks.CommonOf(d).Schedule
		require.NotNil(t, got, "%s is scheduled but registered without its schedule", k)
		want := hook(k)
		require.Equal(t, want.ConfigKey(), got.ConfigKey(), "%s registered with another kind's schedule", k)
		require.Equal(t, want.Interval(), got.Interval(), k)
		require.Equal(t, want.IsLive(), got.IsLive(), k)
	}
	for k := range notYetBuilt {
		require.NotContains(t, reg, k, "%s is registered: drop it from notYetBuilt", k)
	}

	// The presence gates read the server's services through ScheduleDeps. A
	// hook left nil reads as the service being absent and closes the
	// schedule for good, which is what these kinds would do if the wiring
	// dropped it.
	hooks := c.srv.ScheduleHooks()
	require.True(t, hooks.HasActivitySvc(), "the booted server has no activity service: the check below proves nothing")
	require.True(t, hooks.HasMetadataFetchSvc(), "the booted server has no metadata fetch service: the check below proves nothing")
	for k, present := range map[tasks.Kind]bool{
		"books.maintenance.cleanup-activity-log": hooks.HasActivitySvc(),
		"books.scheduler.metadata-upgrade":       hooks.HasMetadataFetchSvc(),
	} {
		got := tasks.CommonOf(reg[k]).Schedule
		require.NotNil(t, got, k)
		require.Equal(t, present, got.IntervalEnabled(), "%s: its schedule gate does not follow the server's service", k)
		require.Equal(t, present, got.WindowOpen(), "%s: its window gate does not follow the server's service", k)
	}
}

// registrationTap is a module whose Register also reads back what it recorded,
// through the idempotent Registrar.Close. A module that calls Close inside
// Register loses nothing (core/module/registrar.go).
type registrationTap struct {
	module.Module
	rec *module.Registrations
}

func (m registrationTap) Register(r *module.Registrar) error {
	err := m.Module.Register(r)
	*m.rec = r.Close()
	return err
}

// The hook reaches every list. A kind outside ScheduledKinds never shows a
// dropped hook (maintenance group 2 has none), so build the lists with a hook
// that schedules every kind and require every definition to carry it.
func TestF5_EveryKindTakesTheHook(t *testing.T) {
	probe := NewSchedules(ScheduleDeps{})("books.maintenance.trash-cleanup")
	require.NotNil(t, probe)
	in := listsFixture(t)
	in.Schedules = func(tasks.Kind) *tasks.Schedule { return probe }
	defs, err := bookTaskLists(in)
	require.NoError(t, err)
	var unscheduled []string
	for _, d := range defs {
		// The repairs kinds are operator-started fixers: repairs.KindDeps has no
		// schedule hook, so they carry none whatever books ships.
		if strings.HasPrefix(string(d.Kind()), "books.repairs.") {
			continue
		}
		// Batch children are started only by their parent, never on a schedule.
		switch d.Kind() {
		case "books.maintenance.extract-wav-clips.batch", "books.maintenance.transcribe-file-intros.batch", "books.maintenance.intro-migrate-single-file.batch":
			require.Nil(t, tasks.CommonOf(d).Schedule, "%s must remain parent-started", d.Kind())
			continue
		}
		if tasks.CommonOf(d).Schedule == nil {
			unscheduled = append(unscheduled, string(d.Kind()))
		}
	}
	require.Empty(t, unscheduled, "kinds that did not take the schedule hook")
}

// Every list bookTaskLists is meant to carry is in it. The schedule check cannot
// see a dropped list whose kinds are unscheduled (all of deluge and itunes), so
// each list is pinned by one kind it owns.
func TestF5_EveryBuiltListIsCarried(t *testing.T) {
	defs, err := bookTaskLists(listsFixture(t))
	require.NoError(t, err)
	have := map[tasks.Kind]bool{}
	for _, d := range defs {
		have[d.Kind()] = true
	}
	for list, sentinel := range map[string]tasks.Kind{
		"maintenance jobs":  "books.maintenance.fix-file-modes",
		"maintenance g2":    "books.maintenance.external-id-backfill",
		"maintenance g3":    "books.maintenance.purge-deleted",
		"maintenance g4":    "books.maintenance.dedupe-book-file-rows",
		"aux-store-compact": "books.maintenance.aux-store-compact",
		"metafetch":         "books.metafetch.asin-backfill",
		"deluge":            "books.deluge.centralize",
		"itunes":            "books.itunes.position-sync",
		"acoustid":          "books.acoustid.reset-all",
		"scheduler":         "books.scheduler.metadata-upgrade",
		"repairs":           "books.repairs.folder-books",
		"maintenance g7":    "books.maintenance.reports-prune",
	} {
		require.True(t, have[sentinel], "the %s list is not in bookTaskLists (no %s)", list, sentinel)
	}
}

// The Repairs list carries one kind per fixer the maintenance plugin offers, none
// dropped and none invented.
func TestF5_RepairsListCarriesEveryFixer(t *testing.T) {
	in := listsFixture(t)
	defs, err := bookTaskLists(in)
	require.NoError(t, err)
	have := map[tasks.Kind]bool{}
	for _, d := range defs {
		have[d.Kind()] = true
	}
	for _, f := range in.Repairs.Fixers {
		require.True(t, have[tasks.Kind(repairs.KindPrefix+f.ID())], "fixer %s has no kind", f.ID())
	}
	n := 0
	for k := range have {
		if strings.HasPrefix(string(k), repairs.KindPrefix) {
			n++
		}
	}
	require.Equal(t, len(in.Repairs.Fixers), n)
}

func TestF5_BookTaskListsBuild(t *testing.T) {
	defs, err := bookTaskLists(listsFixture(t))
	require.NoError(t, err)
	t.Logf("%d definitions", len(defs))
}

// A dependency the maintenance lists need and the caller left nil refuses at
// build time, by name: the kind would otherwise register and fail at its first
// run with a refusal nobody reads at boot. The hook is copied into every
// package's inputs, so a nil hook refuses there too.
func TestF5_MissingMaintenanceInputRefusesByName(t *testing.T) {
	for name, mutate := range map[string]func(*taskListInputs){
		"Jobs":                     func(in *taskListInputs) { in.Maintenance.Jobs = nil },
		"G2Source":                 func(in *taskListInputs) { in.Maintenance.G2Source = nil },
		"G2Host":                   func(in *taskListInputs) { in.Maintenance.G2Host = nil },
		"RootDir":                  func(in *taskListInputs) { in.Maintenance.RootDir = nil },
		"G3Source":                 func(in *taskListInputs) { in.Maintenance.G3Source = nil },
		"G3Host":                   func(in *taskListInputs) { in.Maintenance.G3Host = nil },
		"Runs":                     func(in *taskListInputs) { in.Maintenance.Runs = nil },
		"Ops":                      func(in *taskListInputs) { in.Maintenance.Ops = nil },
		"G4Host":                   func(in *taskListInputs) { in.Maintenance.G4Host = nil },
		"AuxStores":                func(in *taskListInputs) { in.Maintenance.AuxStores = nil },
		"StandDownKey":             func(in *taskListInputs) { in.Maintenance.StandDownKey = "" },
		"G7.Reads":                 func(in *taskListInputs) { in.Maintenance.G7.Reads = nil },
		"G7.Writes":                func(in *taskListInputs) { in.Maintenance.G7.Writes = nil },
		"G7.InvalidateSeriesCache": func(in *taskListInputs) { in.Maintenance.G7.InvalidateSeriesCache = nil },
		"G7.SupportedExtensions":   func(in *taskListInputs) { in.Maintenance.G7.SupportedExtensions = nil },
		"Schedules":                func(in *taskListInputs) { in.Schedules = nil },
	} {
		t.Run(name, func(t *testing.T) {
			in := listsFixture(t)
			mutate(&in)
			_, err := bookTaskLists(in)
			require.Error(t, err)
			require.Contains(t, err.Error(), name)
		})
	}
}

// listsFixerStores is a real (non-nil) fixer-stores value for the fixture's
// BindStores. No fixer runs here, so the embedded interface is never called.
type listsFixerStores struct{ maintenance.FixerStores }

// A BindStores that binds nothing is refused by name, not accepted because it is
// non-nil.
func TestF5_NoOpBindStoresRefusesByName(t *testing.T) {
	in := listsFixture(t)
	in.Repairs.BindStores = func(ctx context.Context) context.Context { return ctx }
	defs, err := bookTaskLists(in)
	require.ErrorContains(t, err, "Repairs.BindStores binds no maintenance.FixerStores")
	require.Nil(t, defs)
}

// TestF5_NoHEADScheduledKindIsNotYetBuilt is the flip's own gate on notYetBuilt:
// every kind in it is one HEAD runs today (a schedule, or an operation the UI
// starts) that the native lists do not carry yet, so the native registration
// cannot be the one production runs while any is left. It is skipped while the
// predicate is off, which is every build before the F5-18 switch commit; that
// commit turns tasksFlipOn on for good (it edits the function, it does not delete
// it), and from then on this test fails until the table is empty. It does not
// call SetTasksFlipForTest, which that commit deletes, and it does not set the
// predicate: it reads the shipped value.
func TestF5_NoHEADScheduledKindIsNotYetBuilt(t *testing.T) {
	if !tasksFlipOn() {
		t.Skip("the tasks flip is off: notYetBuilt may still hold kinds; this test bites when the switch commit turns it on")
	}
	scheduled := map[tasks.Kind]bool{}
	for _, k := range ScheduledKinds() {
		scheduled[k] = true
	}
	var left []string
	for k, owner := range notYetBuilt {
		kind := "operation HEAD runs on request"
		if scheduled[k] {
			kind = "HEAD schedules it"
		}
		left = append(left, fmt.Sprintf("%s (%s; waits on %s)", k, kind, owner))
	}
	slices.Sort(left)
	require.Empty(t, left, "the tasks flip is on and %d kind(s) HEAD runs today are not registered", len(left))
}
