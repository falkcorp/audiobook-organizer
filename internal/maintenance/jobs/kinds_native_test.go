// file: internal/maintenance/jobs/kinds_native_test.go
// version: 1.0.1
// guid: e2745473-88af-4f8a-a81d-54e5e9a3b2d2
// last-edited: 2026-11-12

package jobs_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/books"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	dbmocks "github.com/falkcorp/audiobook-organizer/internal/books/store/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance/jobs"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/server"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// nativeKind returns the native definition of job id built by jobs.Kinds.
func nativeKind(t *testing.T, defs []tasks.Definition, id string) tasks.Definition {
	t.Helper()
	for _, d := range defs {
		if d.Kind() == tasks.Kind("books.maintenance."+id) {
			return d
		}
	}
	t.Fatalf("no native kind for %s", id)
	return nil
}

// runNative runs d's own Run with the params the executor would decode.
func runNative(t *testing.T, d tasks.Definition, rc *tasks.RC, params string) (json.RawMessage, error) {
	t.Helper()
	r, ok := d.(interface {
		ExecutorRunners() (func(json.RawMessage) (any, error), *tasks.TaskSpec[any, json.RawMessage], *tasks.BatchSpec[any, any, json.RawMessage], *tasks.FixerSpec[any, any])
	})
	require.True(t, ok, "%T has no ExecutorRunners", d)
	decode, task, _, _ := r.ExecutorRunners()
	require.NotNil(t, task)
	var raw json.RawMessage
	if params != "" {
		raw = json.RawMessage(params)
	}
	p, err := decode(raw)
	require.NoError(t, err)
	return task.Run(rc, p)
}

// logHost is a FakeRun that records the run log.
type logHost struct {
	*taskstest.FakeRun
	mu   sync.Mutex
	logs []string
}

type logHandler struct{ h *logHost }

func (l logHandler) Enabled(context.Context, slog.Level) bool { return true }
func (l logHandler) Handle(_ context.Context, r slog.Record) error {
	l.h.mu.Lock()
	l.h.logs = append(l.h.logs, r.Message)
	l.h.mu.Unlock()
	return nil
}
func (l logHandler) WithAttrs([]slog.Attr) slog.Handler { return l }
func (l logHandler) WithGroup(string) slog.Handler      { return l }

func (h *logHost) Log() *slog.Logger { return slog.New(logHandler{h}) }

func newLogHost(kind tasks.Kind) (*logHost, *tasks.RC) {
	h := &logHost{FakeRun: taskstest.NewFakeRun(context.Background(), kind)}
	return h, tasks.NewRC(h)
}

func (h *logHost) joined() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return strings.Join(h.logs, "\n")
}

// stateBytes is what HEAD's operations.SaveCheckpoint writes: the bytes the
// native Restore must decode.
type stateBytes struct{ data []byte }

func (s *stateBytes) SaveOperationState(_ string, data []byte) error { s.data = data; return nil }

func headCheckpoint(t *testing.T, opType, phase string, idx, total int) []byte {
	t.Helper()
	var w stateBytes
	require.NoError(t, operations.SaveCheckpoint(&w, "run-1", opType, phase, idx, total))
	return w.data
}

// markerStore is a MockStore that also carries recompute-book-aggregates'
// backfill sentinel.
type markerStore struct {
	*database.MockStore
}

func (markerStore) IsBookAggregatesBackfillDone() bool    { return false }
func (markerStore) MarkBookAggregatesBackfillDone() error { return nil }

func newRecomputeStore(ids []string) (markerStore, *[]string, *[]string) {
	var mu sync.Mutex
	var recomputed, saves []string
	var state []byte
	m := &database.MockStore{
		ListBookIDsFunc: func() ([]string, error) { return ids, nil },
		RecomputeBookAggregatesFunc: func(id string) error {
			mu.Lock()
			recomputed = append(recomputed, id)
			mu.Unlock()
			return nil
		},
		SaveOperationSummaryLogFunc: func(*database.OperationSummaryLog) error { return nil },
		SaveOperationStateFunc: func(opID string, data []byte) error {
			mu.Lock()
			defer mu.Unlock()
			state = data
			var st operations.OperationState
			_ = json.Unmarshal(data, &st)
			saves = append(saves, st.Phase+"@"+string(rune('0'+st.PhaseIndex/100)))
			return nil
		},
		GetOperationStateFunc: func(string) ([]byte, error) {
			mu.Lock()
			defer mu.Unlock()
			return state, nil
		},
		DeleteOperationStateFunc: func(string) error { return nil },
	}
	return markerStore{m}, &recomputed, &saves
}

// TestJobKinds_CheckpointerFromRC: the checkpointing jobs use rc.Checkpoint and
// rc.Restore under the native shell, with HEAD's own checkpoint struct so the
// bytes HEAD's SaveCheckpoint wrote decode, and keep the v1 helpers when the
// shell is HEAD's. retention-and-hygiene has no checkpoint call at all (its
// policy comment says so; only its comment names SaveCheckpoint), so there are
// two jobs to cover, not the brief's three.
func TestJobKinds_CheckpointerFromRC(t *testing.T) {
	var gotStore database.Store
	defs := jobs.Kinds(jobs.KindDeps{NewStore: func(tasks.Writer) jobs.Store { return gotStore.(jobs.Store) }}, nil)

	t.Run("recompute-book-aggregates saves through rc and resumes from HEAD's bytes", func(t *testing.T) {
		ids := make([]string, 250)
		for i := range ids {
			ids[i] = "bruce-" + string(rune('a'+i/26)) + string(rune('a'+i%26))
		}
		def := nativeKind(t, defs, "recompute-book-aggregates")

		// Fresh run: checkpoints at books 0, 100 and 200 land on the run, none on the store.
		store, recomputed, saves := newRecomputeStore(ids)
		gotStore = store
		h, rc := newLogHost(def.Kind())
		_, err := runNative(t, def, rc, "")
		require.NoError(t, err)
		assert.Len(t, *recomputed, 250)
		assert.Empty(t, *saves, "the v1 helper must not be used under the native shell")
		raw, ok := h.CheckpointJSON("")
		require.True(t, ok, "no checkpoint was saved through rc.Checkpoint")
		var st operations.OperationState
		require.NoError(t, json.Unmarshal(raw, &st))
		assert.Equal(t, "maintenance:recompute-book-aggregates", st.Type)
		assert.Equal(t, "scanning", st.Phase)
		assert.Equal(t, 200, st.PhaseIndex)
		assert.Equal(t, 250, st.PhaseTotal)
		assert.Equal(t, string(h.RunID()), st.OperationID)

		// Resume: HEAD's SaveCheckpoint bytes in the run's checkpoint skip the first 100 books.
		store, recomputed, _ = newRecomputeStore(ids)
		gotStore = store
		h, rc = newLogHost(def.Kind())
		require.NoError(t, h.SetCheckpoint("", json.RawMessage(headCheckpoint(t, "maintenance:recompute-book-aggregates", "scanning", 100, 250))))
		_, err = runNative(t, def, rc, "")
		require.NoError(t, err)
		assert.Equal(t, ids[100:], *recomputed, "a resumed run redoes only the books after the checkpoint")

		// A checkpoint of another phase is not a resume point.
		store, recomputed, _ = newRecomputeStore(ids)
		gotStore = store
		h, rc = newLogHost(def.Kind())
		require.NoError(t, h.SetCheckpoint("", json.RawMessage(headCheckpoint(t, "maintenance:recompute-book-aggregates", "done", 100, 250))))
		_, err = runNative(t, def, rc, "")
		require.NoError(t, err)
		assert.Len(t, *recomputed, 250)
	})

	t.Run("recompute-book-aggregates under HEAD's shell still uses the v1 helpers", func(t *testing.T) {
		ids := make([]string, 150)
		for i := range ids {
			ids[i] = "selina-" + string(rune('a'+i/26)) + string(rune('a'+i%26))
		}
		store, recomputed, saves := newRecomputeStore(ids)
		job, err := maintenance.Get("recompute-book-aggregates")
		require.NoError(t, err)
		ctx := maintenance.WithOperationID(context.Background(), "run-1")
		require.NoError(t, job.Run(ctx, store, nopReporter{}, false))
		assert.Len(t, *recomputed, 150)
		assert.Equal(t, []string{"scanning@0", "scanning@1"}, *saves, "checkpoints at books 0 and 100 through SaveOperationState")
	})

	t.Run("backfill-file-hashes writes its started marker through rc and reads HEAD's", func(t *testing.T) {
		def := nativeKind(t, defs, "backfill-file-hashes")

		root := hbLibrary(t)
		h := newHBStore(t, database.BookFileCore{ID: "f1", BookID: "b1", FilePath: hbWrite(t, root, "cowl.m4b", "bytes")})
		gotStore = h.store()
		lh, rc := newLogHost(def.Kind())
		_, err := runNative(t, def, rc, "")
		require.NoError(t, err)
		assert.Empty(t, h.phases, "the started marker must not go through SaveOperationState under the native shell")
		raw, ok := lh.CheckpointJSON("")
		require.True(t, ok, "no started marker was saved through rc.Checkpoint")
		var st operations.OperationState
		require.NoError(t, json.Unmarshal(raw, &st))
		assert.Equal(t, "maintenance:backfill-file-hashes", st.Type)
		assert.Equal(t, "started", st.Phase)
		assert.Equal(t, 1, st.PhaseTotal)
		assert.NotContains(t, lh.joined(), "started before", "a fresh run is not a resumption")

		// A marker HEAD's SaveCheckpoint wrote is found: with no earlier report the
		// summary says the counts are partial.
		root = hbLibrary(t)
		h = newHBStore(t, database.BookFileCore{ID: "f1", BookID: "b1", FilePath: hbWrite(t, root, "cowl.m4b", "bytes")})
		gotStore = h.store()
		lh, rc = newLogHost(def.Kind())
		require.NoError(t, lh.SetCheckpoint("", json.RawMessage(headCheckpoint(t, "maintenance:backfill-file-hashes", "started", 0, 1))))
		_, err = runNative(t, def, rc, "")
		require.NoError(t, err)
		assert.Contains(t, lh.joined(), "started before", "HEAD's marker bytes were not decoded by rc.Restore")

		// And HEAD's own shell keeps the v1 path.
		root = hbLibrary(t)
		h = newHBStore(t, database.BookFileCore{ID: "f1", BookID: "b1", FilePath: hbWrite(t, root, "cowl.m4b", "bytes")})
		_, _, _ = runHB(t, h.store(), hbRun{opID: "run-1"})
		assert.Equal(t, []string{"started@0"}, h.phases, "HEAD's wrapper path must still save the marker with SaveOperationState")
	})
}

type nopReporter struct{}

func (nopReporter) SetTotal(int)                {}
func (nopReporter) Increment()                  {}
func (nopReporter) Log(string, string, *string) {}

// TestJobKinds_MatchHEADWrapper compares every kind with the definition HEAD's
// wrapper registers for the same job, read back from a real registry (the
// oracle is the wrapper's own code, so a drift in either fails here).
func TestJobKinds_MatchHEADWrapper(t *testing.T) {
	m := dbmocks.NewMockStore(t)
	m.EXPECT().UpsertOpDefinitionV2(mock.Anything).Return(nil).Maybe()
	reg := opsregistry.New(m, slog.New(slog.DiscardHandler), 1, nil)
	require.NoError(t, (&server.Server{}).RegisterMaintenanceJobOps(reg))

	var asked []tasks.Kind
	scheduled := tasks.Kind("books.maintenance.dedup-books")
	defs := jobs.Kinds(jobs.KindDeps{}, func(k tasks.Kind) *tasks.Schedule {
		asked = append(asked, k)
		if k == scheduled {
			return &tasks.Schedule{}
		}
		return nil
	})
	all := maintenance.All()
	require.Len(t, all, 34)
	require.Len(t, defs, len(all))

	atoms := map[string]books.Atom{}
	for _, a := range books.AtomTable() {
		atoms[a.Name] = a
	}
	resume := map[opsregistry.ResumePolicy]tasks.ResumePolicy{
		opsregistry.ResumeRestart: tasks.ResumeContinue, opsregistry.ResumeRequeue: tasks.ResumeFromZero,
		opsregistry.ResumeDrop: tasks.ResumeDrop, opsregistry.ResumeAsk: tasks.ResumeAsk,
	}
	checkpoints := map[string]bool{}
	for id, j := range oracleJobs(t) {
		checkpoints[id] = j.checkpoints
	}
	var readOnly []string
	for i, job := range all {
		id := "maintenance." + job.ID()
		head, ok := reg.Def(id)
		require.Truef(t, ok, "HEAD registers no def %s", id)
		d := defs[i]
		c := tasks.CommonOf(d)

		assert.Equal(t, tasks.Kind("books."+id), d.Kind(), id)
		assert.Equal(t, head.DisplayName, c.Title, id)
		assert.Equal(t, head.Description, c.Help, id)
		wantResume := resume[head.ResumePolicy]
		if head.ResumePolicy == opsregistry.ResumeRestart && !checkpoints[job.ID()] {
			// HEAD re-enters a restarted job that never checkpoints from the
			// top; that is ResumeFromZero, not "restore a checkpoint".
			wantResume = tasks.ResumeFromZero
		}
		assert.Equal(t, wantResume, c.Resume, "%s: resume", id)
		assert.Equal(t, head.Timeout, c.Timeout, "%s: timeout", id)
		assert.Equal(t, tasks.PriorityNormal, c.Priority, "%s: HEAD's priority is Normal", id)
		assert.Equal(t, []tasks.ReadySignal{"books.memdb"}, c.Ready, "%s: HEAD waits for warmup", id)
		assert.Empty(t, c.Perms.StartableByViewers, id)

		// HEAD derives the key from the def ID when the job declares none; the
		// native zero key is Key(Kind), the same domain.
		if job.Policy().ConcurrencyKey == "" {
			assert.Equal(t, head.ID, head.ConcurrencyKey, "%s: HEAD's derived key", id)
			assert.Equal(t, tasks.Key(""), c.ConcurrencyKey, id)
		} else {
			assert.Equal(t, tasks.Key("books."+head.ConcurrencyKey), c.ConcurrencyKey, id)
		}

		// ReadOnly exactly when HEAD declares no preview (the jobs that advertise
		// dry_run=false); a previewable job is a declared writer.
		noPreview := reflect.DeepEqual(head.Preview, opsregistry.PreviewSpec{})
		assert.Equal(t, noPreview, c.Effects.IsReadOnly(), "%s: ReadOnly <=> no preview", id)
		assert.True(t, c.Effects.Declared(), id)
		if c.Effects.IsReadOnly() {
			readOnly = append(readOnly, job.ID())
		}

		// Start requirement: the atom table's route requirement for HEAD's atom.
		require.Len(t, head.Permissions, 1, id)
		atom, ok := atoms[string(head.Permissions[0])]
		require.Truef(t, ok, "%s: HEAD permission %s is in no atom row", id, head.Permissions[0])
		assert.Equal(t, rbac.KindAllOf, c.Perms.Start.Kind(), id)
		assert.Equal(t, atom.Route.Permissions(), c.Perms.Start.Permissions(), "%s: start requirement drifted from the atom table", id)

		if k := d.Kind(); k == scheduled {
			assert.NotNil(t, c.Schedule, "%s: schedule hook result was dropped", id)
		} else {
			assert.Nil(t, c.Schedule, id)
		}
	}
	slices.Sort(readOnly)
	assert.Equal(t, []string{"relink-report", "scan-chapter-groups", "scan-duplicate-files", "scan-duration-mismatch", "scan-metadata-hash-dups"}, readOnly)
	assert.Len(t, asked, 34, "the schedule hook is asked once per kind")
	assert.Equal(t, auth.PermSettingsManage, auth.Permission("settings.manage"))
}
