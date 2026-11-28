// file: internal/books/tasklists_test.go
// version: 0.1.0
// guid: 5b1e8c34-d902-47a6-8f13-6e0a2c9d7b45
// last-edited: 2026-11-22

package books

import (
	"context"
	"encoding/json"
	"iter"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	svcmetafetch "github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	"github.com/falkcorp/audiobook-organizer/internal/plugins/acoustid"
	"github.com/falkcorp/audiobook-organizer/internal/plugins/dedup"
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

// listsG5Host is group 5's host: the fixture's store serves every accessor.
type listsG5Host struct {
	s    *database.PebbleStore
	root string
}

func (h listsG5Host) RootDir() string { return h.root }
func (listsG5Host) RunMetadataRefreshScan(context.Context, operations.ProgressReporter) error {
	return nil
}
func (h listsG5Host) OperationQueueStore() maintenance.OpQueueReader { return h.s }
func (listsG5Host) PlanLibraryClone(*database.Book, []database.BookFile) ([]string, error) {
	return nil, nil
}
func (listsG5Host) LibraryITunesPath(p string) string                      { return p }
func (h listsG5Host) BookTagReader() maintenance.BookTagReader             { return h.s }
func (h listsG5Host) VersionPrimaryStore() maintenance.VersionPrimaryStore { return h.s }
func (h listsG5Host) PlaylistStore() database.UserPlaylistStore            { return h.s }
func (listsG5Host) MergeBooks(string, []string, string) (int, error)       { return 0, nil }
func (listsG5Host) CloneBookIntoLibrary(*database.Book, []database.BookFile, string) (string, error) {
	return "", nil
}
func (h listsG5Host) MergeUserStateStore() merge.UserStateRepairStore { return h.s }
func (h listsG5Host) MetadataCacheStore() database.MetadataCacheStore { return h.s }
func (listsG5Host) InvalidateAuthorsCache()                           {}

type listsAux struct{}

func (listsAux) OptimizeAIScanStore(context.Context) error { return nil }
func (listsAux) OptimizeOLStore(context.Context) error     { return nil }
func (listsAux) AIScanStoreCompactionStats() (database.CompactionStats, bool) {
	return database.CompactionStats{}, false
}

func (listsAux) OLStoreCompactionStats() (database.CompactionStats, bool) {
	return database.CompactionStats{}, false
}

// listsRuns is the run store the books module holds: it lists and has no Get,
// which is the shape of core's own-kinds view of the run store (module.TaskRows).
// recs is what it lists; bookTaskLists only builds the definitions, so the zero
// value is enough there.
type listsRuns struct{ recs []tasks.RunRecord }

func (l listsRuns) List(_ context.Context, q tasks.RunQuery) iter.Seq2[tasks.RunRecord, error] {
	return func(yield func(tasks.RunRecord, error) bool) {
		for _, r := range l.recs {
			if q.After != "" && r.ID <= q.After {
				continue
			}
			if len(q.Kinds) > 0 && !slices.Contains(q.Kinds, r.Kind) {
				continue
			}
			if !yield(r, nil) {
				return
			}
		}
	}
}

func (listsRuns) ReplaceRows(context.Context, tasks.RunID, string, int, func(int) (tasks.Row, error)) error {
	return nil
}

func (listsRuns) Rows(context.Context, tasks.RunID) iter.Seq2[tasks.Row, error] {
	return func(func(tasks.Row, error) bool) {}
}

// The module's run-store handle has no Get, so the authority-build apply reads
// its plan through an adapter over List. This fixture is the handle's shape: if it
// grew a Get the adapter would be unneeded, and if the adapter stopped accepting a
// List-only store this stops compiling.
var (
	_ maintenance.AuthorityRunLister = listsRuns{}
	_ maintenance.AuthorityRunReader = maintenance.NewAuthorityRunReader(listsRuns{})
)

// The reader made over the module's List-only handle finds a plan run among the
// listed runs and answers ErrRunNotFound for one that is not there.
func TestF5_AuthorityRunReaderOverTheModuleHandle(t *testing.T) {
	const kind = tasks.Kind("books.maintenance.authority-build")
	recs := []tasks.RunRecord{{ID: "run-1", Kind: kind}, {ID: "run-2", Kind: kind}}
	r := maintenance.NewAuthorityRunReader(listsRuns{recs: recs})
	got, err := r.Get(context.Background(), "run-2")
	require.NoError(t, err)
	require.Equal(t, tasks.RunID("run-2"), got.ID)
	_, err = r.Get(context.Background(), "run-3")
	require.ErrorIs(t, err, tasks.ErrRunNotFound)
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
	// The lists are built, never run: a zero engine is never called.
	emb := database.NewEmbeddingStore(store.DB())
	eng := &dedupengine.Engine{}
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
			G5Host:    listsG5Host{s: store, root: root},
			AuxStores: listsAux{},
			G1: maintenance.G1Inputs{
				FetchStore: store,
				NewFetchService: func(gated svcmetafetch.Store) *svcmetafetch.Service {
					return svcmetafetch.NewService(gated)
				},
				NewTranscriptionRunners: func(*svcmetafetch.Service) maintenance.TranscriptionRunners { return nil },
				ManualOnly:              applygate.ManualOnlyReaders{Files: store, Series: store, Authors: store, Tags: store},
				AIJournalRetentionDays:  func() int { return 0 },
				InvalidateAuthorsCache:  func() {},
				InvalidateDedupCache:    func() {},
			},
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
		DedupEnabled: true,
		Dedup: dedup.BooksTaskInputs{
			Source:     store,
			Indexes:    store,
			Embeddings: emb,
			NewEngine:  func(tasks.Writer) dedup.WriteEngine { return eng },
			Engine:     eng,
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
	// the two kinds below are group 6 (held for TODO-F5-11-HOLD-RENEW;
	// orphan-book-files-cleanup is one of its 14).
	"books.maintenance.orphan-book-files-cleanup":    "maintenance g6 (held, TODO-F5-11-HOLD-RENEW)",
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
		require.True(t, scheduled[k], "notYetBuilt names %s, which is not a scheduled kind", k)
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
		"acoustid":          "books.acoustid.scan",
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

// kindsOf is the set of kinds bookTaskLists builds over in.
func kindsOf(t *testing.T, in taskListInputs) map[tasks.Kind]bool {
	t.Helper()
	defs, err := bookTaskLists(in)
	require.NoError(t, err)
	out := map[tasks.Kind]bool{}
	for _, d := range defs {
		out[d.Kind()] = true
	}
	return out
}

// A server with embedding disabled has no dedup engine and no embedding store, so
// HEAD builds no dedup plugin and registers none of its operations. The flip says
// so with DedupEnabled=false (never by leaving Dedup's fields nil): every
// non-dedup kind is built, the dedup kinds are exactly absent, and the zero Dedup
// inputs are not an error. Enabled with no NewEngine (until the gated engine) is
// the dedup list less its engine kinds; enabled with one is the whole list.
func TestF5_DedupListFollowsDedupEnabled(t *testing.T) {
	full := kindsOf(t, listsFixture(t))

	noEngine := listsFixture(t)
	noEngine.Dedup.NewEngine = nil
	withoutEngine := kindsOf(t, noEngine)

	off := listsFixture(t)
	off.DedupEnabled = false
	off.Dedup = dedup.BooksTaskInputs{} // embedding disabled: none of these exist
	disabled := kindsOf(t, off)

	var dedupKinds []tasks.Kind
	for k := range full {
		if !disabled[k] {
			dedupKinds = append(dedupKinds, k)
		}
	}
	require.Len(t, dedupKinds, 31, "the dedup list is the kinds only DedupEnabled adds")
	for k := range disabled {
		require.True(t, full[k], "%s is built only when dedup is off", k)
	}
	require.Len(t, full, len(disabled)+31)

	engine := map[tasks.Kind]bool{}
	for _, k := range dedup.EngineKinds() {
		engine[k] = true
	}
	for _, k := range dedupKinds {
		require.Equal(t, !engine[k], withoutEngine[k], "%s with no NewEngine", k)
	}
	require.Len(t, withoutEngine, len(disabled)+20)
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
		"Jobs":                      func(in *taskListInputs) { in.Maintenance.Jobs = nil },
		"G2Source":                  func(in *taskListInputs) { in.Maintenance.G2Source = nil },
		"G2Host":                    func(in *taskListInputs) { in.Maintenance.G2Host = nil },
		"RootDir":                   func(in *taskListInputs) { in.Maintenance.RootDir = nil },
		"G3Source":                  func(in *taskListInputs) { in.Maintenance.G3Source = nil },
		"G3Host":                    func(in *taskListInputs) { in.Maintenance.G3Host = nil },
		"Runs":                      func(in *taskListInputs) { in.Maintenance.Runs = nil },
		"Ops":                       func(in *taskListInputs) { in.Maintenance.Ops = nil },
		"G4Host":                    func(in *taskListInputs) { in.Maintenance.G4Host = nil },
		"AuxStores":                 func(in *taskListInputs) { in.Maintenance.AuxStores = nil },
		"StandDownKey":              func(in *taskListInputs) { in.Maintenance.StandDownKey = "" },
		"G5Host":                    func(in *taskListInputs) { in.Maintenance.G5Host = nil },
		"G1.ManualOnly.Files":       func(in *taskListInputs) { in.Maintenance.G1.ManualOnly.Files = nil },
		"G1.ManualOnly.Series":      func(in *taskListInputs) { in.Maintenance.G1.ManualOnly.Series = nil },
		"G1.ManualOnly.Authors":     func(in *taskListInputs) { in.Maintenance.G1.ManualOnly.Authors = nil },
		"G1.ManualOnly.Tags":        func(in *taskListInputs) { in.Maintenance.G1.ManualOnly.Tags = nil },
		"G1.AIJournalRetentionDays": func(in *taskListInputs) { in.Maintenance.G1.AIJournalRetentionDays = nil },
		"G1.InvalidateAuthorsCache": func(in *taskListInputs) { in.Maintenance.G1.InvalidateAuthorsCache = nil },
		"G1.InvalidateDedupCache":   func(in *taskListInputs) { in.Maintenance.G1.InvalidateDedupCache = nil },
		"G7.Reads":                  func(in *taskListInputs) { in.Maintenance.G7.Reads = nil },
		"G7.Writes":                 func(in *taskListInputs) { in.Maintenance.G7.Writes = nil },
		"G7.InvalidateSeriesCache":  func(in *taskListInputs) { in.Maintenance.G7.InvalidateSeriesCache = nil },
		"G7.SupportedExtensions":    func(in *taskListInputs) { in.Maintenance.G7.SupportedExtensions = nil },
		"Dedup.Source":              func(in *taskListInputs) { in.Dedup.Source = nil },
		"Dedup.Indexes":             func(in *taskListInputs) { in.Dedup.Indexes = nil },
		"Dedup.Embeddings":          func(in *taskListInputs) { in.Dedup.Embeddings = nil },
		"Dedup.Engine":              func(in *taskListInputs) { in.Dedup.Engine = nil },
		"Schedules":                 func(in *taskListInputs) { in.Schedules = nil },
	} {
		t.Run(name, func(t *testing.T) {
			in := listsFixture(t)
			mutate(&in)
			_, err := bookTaskLists(in)
			require.Error(t, err)
			if field, ok := strings.CutPrefix(name, "Dedup."); ok {
				// the dedup package names its own inputs, in its own error
				require.Contains(t, err.Error(), "dedup: BooksTaskLists: missing input(s): ["+field+"]")
				return
			}
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
