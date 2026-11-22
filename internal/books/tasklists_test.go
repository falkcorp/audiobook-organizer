// file: internal/books/tasklists_test.go
// version: 0.1.0
// guid: 5b1e8c34-d902-47a6-8f13-6e0a2c9d7b45
// last-edited: 2026-11-22

package books

import (
	"context"
	"iter"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/plugins/maintenance"
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
			Jobs:         store,
			G2Source:     store,
			G2Host:       listsG2Host{},
			RootDir:      func() string { return root },
			G3Source:     store,
			G3Host:       listsG3Host{root: root},
			Runs:         listsRuns{},
			Ops:          store,
			G4Host:       listsG4Host{root: root},
			AuxStores:    listsAux{},
			StandDownKey: tasks.Key("books.library.scan"),
		},
	}
}

func TestF5_BookTaskListsBuild(t *testing.T) {
	defs, err := bookTaskLists(listsFixture(t))
	require.NoError(t, err)
	have := map[tasks.Kind]bool{}
	for _, d := range defs {
		have[d.Kind()] = true
	}
	for _, k := range ScheduledKinds() {
		if !have[k] {
			t.Logf("scheduled kind not in the lists: %s", k)
		}
	}
	t.Logf("%d definitions", len(defs))
}
