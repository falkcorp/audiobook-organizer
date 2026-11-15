// file: internal/plugins/maintenance/kinds_t6_g5_test.go
// version: 1.0.6
// guid: c0ba68fc-0752-49a5-9e51-187eca399e39
// last-edited: 2026-11-14

package maintenance

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- recording fakes of t6g5Writer and t6g5Store (rule R10) ---
//
// Each embeds the interface it fakes, so only the methods a test drives are
// written out; any other call panics on the nil embed, which fails the test
// loudly instead of answering with a made-up value.

// t6g5RecWriter records every write a core makes through it, by method name.
type t6g5RecWriter struct {
	t6g5Writer
	mu    sync.Mutex
	calls []string
}

func (w *t6g5RecWriter) note(name string) {
	w.mu.Lock()
	w.calls = append(w.calls, name)
	w.mu.Unlock()
}

func (w *t6g5RecWriter) called() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.calls)
}

func (w *t6g5RecWriter) RecordOperationChange(*database.OperationChange) error {
	w.note("RecordOperationChange")
	return nil
}
func (w *t6g5RecWriter) DeleteNarrator(id int) error { w.note("DeleteNarrator"); return nil }
func (w *t6g5RecWriter) InvalidateAuthorsCache()     { w.note("InvalidateAuthorsCache") }

// t6g5FakeStore answers the reads the driven kinds make.
type t6g5FakeStore struct {
	t6g5Store
	root      string
	narrators []database.Narrator
	refs      database.NarratorRefs
}

func (s *t6g5FakeStore) RootDir() string                             { return s.root }
func (s *t6g5FakeStore) ListNarrators() ([]database.Narrator, error) { return s.narrators, nil }
func (s *t6g5FakeStore) NarratorRefCounts() (database.NarratorRefs, error) {
	return s.refs, nil
}
func (s *t6g5FakeStore) NarratorLinkCount(int) (int, error) { return 0, nil }
func (s *t6g5FakeStore) GetAllBooksCoreComplete(int, int) ([]database.BookCore, error) {
	return nil, nil
}
func (s *t6g5FakeStore) GetAuthorByName(string) (*database.Author, error) { return nil, nil }

// t6g5Fake is what fakeT6G5Deps builds: the recording writer and store, and the
// tasks.Writer values NewWriter was handed.
type t6g5Fake struct {
	writer *t6g5RecWriter
	store  *t6g5FakeStore

	mu         sync.Mutex
	newWriters []tasks.Writer
}

func newT6G5Fake() *t6g5Fake {
	return &t6g5Fake{writer: &t6g5RecWriter{}, store: &t6g5FakeStore{root: "/lib"}}
}

func (f *t6g5Fake) deps() t6g5Deps {
	return t6g5Deps{
		Store: f.store,
		NewWriter: func(w tasks.Writer) t6g5Writer {
			f.mu.Lock()
			f.newWriters = append(f.newWriters, w)
			f.mu.Unlock()
			return f.writer
		},
	}
}

func (f *t6g5Fake) handed() []tasks.Writer {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.newWriters)
}

// fakeT6G5Deps is the owner's declared dependencies as a test fake.
func fakeT6G5Deps(*testing.T) t6g5Deps { return newT6G5Fake().deps() }

// t6g5Gate is a tasks.Writer the run hands NewWriter; identity is what the
// shell test compares.
type t6g5Gate struct{ id string }

func (*t6g5Gate) Do(context.Context, tasks.Write, func(context.Context) ([]tasks.Change, error)) error {
	return nil
}
func (*t6g5Gate) MarkRowApplied(context.Context, string)           {}
func (*t6g5Gate) RowApplied(context.Context, string) (bool, error) { return false, nil }

// t6g5Host is a FakeRun with a chosen mode and writer.
type t6g5Host struct {
	*taskstest.FakeRun
	mode  tasks.Mode
	w     tasks.Writer
	holds *t6g5Holds
	// touches counts the liveness stamps the run made (FakeRun's Touch is a
	// no-op).
	touches atomic.Int64
}

// Touch counts the stamp.
func (h *t6g5Host) Touch() { h.touches.Add(1) }

// StandDown records the key and the release when a test installed holds, and
// is FakeRun's refusal otherwise.
func (h *t6g5Host) StandDown(key tasks.Key) (tasks.Hold, error) {
	if h.holds == nil {
		return h.FakeRun.StandDown(key)
	}
	return h.holds.take(key)
}

// t6g5Holds records the scan stand-down keys a run took and how many of those
// holds were released.
type t6g5Holds struct {
	mu       sync.Mutex
	keys     []tasks.Key
	released int
	err      error
}

type t6g5HoldHandle struct{ h *t6g5Holds }

func (x t6g5HoldHandle) Release() {
	x.h.mu.Lock()
	x.h.released++
	x.h.mu.Unlock()
}

func (h *t6g5Holds) take(key tasks.Key) (tasks.Hold, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.err != nil {
		return nil, h.err
	}
	h.keys = append(h.keys, key)
	return t6g5HoldHandle{h}, nil
}

func (h *t6g5Holds) snapshot() ([]tasks.Key, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.keys), h.released
}

func (h *t6g5Host) Mode() tasks.Mode     { return h.mode }
func (h *t6g5Host) Writer() tasks.Writer { return h.w }
func (h *t6g5Host) Log() *slog.Logger    { return slog.New(slog.DiscardHandler) }

func newT6G5Host(kind tasks.Kind, mode tasks.Mode, w tasks.Writer) *tasks.RC {
	rc, _ := newT6G5HostCtx(context.Background(), kind, mode, w, nil)
	return rc
}

// newT6G5HostCtx is newT6G5Host over a chosen context and stand-down recorder.
func newT6G5HostCtx(ctx context.Context, kind tasks.Kind, mode tasks.Mode, w tasks.Writer, holds *t6g5Holds) (*tasks.RC, *taskstest.FakeRun) {
	rc, h := newT6G5HostFull(ctx, kind, mode, w, holds)
	return rc, h.FakeRun
}

// newT6G5HostFull is newT6G5HostCtx that also returns the host, for a test
// that reads what the run did to it.
func newT6G5HostFull(ctx context.Context, kind tasks.Kind, mode tasks.Mode, w tasks.Writer, holds *t6g5Holds) (*tasks.RC, *t6g5Host) {
	h := &t6g5Host{FakeRun: taskstest.NewFakeRun(ctx, kind), mode: mode, w: w, holds: holds}
	return tasks.NewRC(h), h
}

// t6g5Run runs d's own Run with the params the executor would decode.
func t6g5Run(t *testing.T, d tasks.Definition, rc *tasks.RC, params string) (json.RawMessage, error) {
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
	// A run that panics fails THIS test instead of killing the test binary: with
	// the binary dead, every test after it (in later files) never runs, and a
	// mutation pass would credit the kill to whichever test crashed first.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("%s panicked instead of returning an error: %v", d.Kind(), r)
		}
	}()
	return task.Run(rc, p)
}

func t6g5KindByName(t *testing.T, defs []tasks.Definition, headID string) tasks.Definition {
	t.Helper()
	for _, d := range defs {
		if d.Kind() == tasks.Kind("books."+headID) {
			return d
		}
	}
	t.Fatalf("no native kind for %s", headID)
	return nil
}

// t6g5ParString renders a definition's declared parallelism, "" for a Task.
func t6g5ParString(d tasks.Definition) string {
	p := tasks.ParallelismOf(d)
	if p == (tasks.Parallelism{}) {
		return ""
	}
	return p.String()
}

// t6g5WriteKinds lists the resource kinds an Effects writes, sorted, and fails
// the test when any of them is declared under a module other than books.
func t6g5WriteKinds(t t6g5TB, e tasks.Effects) []string {
	t.Helper()
	var out []string
	for _, r := range e.WriteSet() {
		assert.Equal(t, "books", r.Module, "every write of this group is declared under the books module (%s)", r.Kind)
		out = append(out, r.Kind)
	}
	slices.Sort(out)
	return out
}

// --- the three tests the brief names ---

func TestT6G5Kinds_Conformance(t *testing.T) {
	defs := (&Plugin{}).t6g5Kinds(fakeT6G5Deps(t), nil)
	taskstest.Conformance(t, defs...)
}

// t6g5Want is one converted definition's expected native shape. The write sets
// and resume policies are literals, never derived from the code under test;
// title, timeouts and the concurrency key are compared with HEAD's own def.
type t6g5Want struct {
	headID   string
	def      func(*Plugin) sdk.OperationDef
	resume   tasks.ResumePolicy
	readOnly bool
	writes   []string
}

func t6g5Wants() []t6g5Want {
	return []t6g5Want{
		{"itunes.clone-into-library", (*Plugin).itunesCloneIntoLibraryDef, tasks.ResumeDrop, false, []string{string(ops.ResAuthors), string(ops.ResBookFiles), string(ops.ResBooks), string(ops.ResFiles), string(ops.ResUserState)}},
		{"itunes.playlist-import", (*Plugin).itunesPlaylistImportDef, tasks.ResumeDrop, false, []string{string(ops.ResUserState)}},
		{"itunes.regroup", (*Plugin).itunesRegroupDef, tasks.ResumeDrop, false, []string{string(ops.ResBookFiles), string(ops.ResBooks), string(ops.ResExternalIDs), string(ops.ResUserState)}},
		{"maintenance.repair-library-state", (*Plugin).repairLibraryStateDef, tasks.ResumeDrop, false, []string{string(ops.ResBooks)}},
		{"maintenance.mark-missing-files", (*Plugin).markMissingFilesDef, tasks.ResumeDrop, false, []string{string(ops.ResBookFiles), string(ops.ResBooks)}},
		{"maintenance.merge-same-path-dupes", (*Plugin).mergeSamePathDupesDef, tasks.ResumeDrop, false, []string{string(ops.ResBookFiles), string(ops.ResBooks), string(ops.ResExternalIDs), string(ops.ResOperations), string(ops.ResUserState)}},
		{"maintenance.metadata-refresh", (*Plugin).metadataRefreshDef, tasks.ResumeFromZero, true, nil},
		{"maintenance.metadata-cache-reap", (*Plugin).metadataCacheReapDef, tasks.ResumeDrop, false, []string{string(ops.ResMetadataCache)}},
		{"maintenance.missing-file-audit", (*Plugin).missingFileAuditDef, tasks.ResumeDrop, true, nil},
		{"maintenance.missing-file-repair", (*Plugin).missingFileRepairDef, tasks.ResumeDrop, true, nil},
		{"maintenance.missing-file-repoint", (*Plugin).missingFileRepointDef, tasks.ResumeDrop, false, []string{string(ops.ResBookFiles), string(ops.ResBooks)}},
		{"maintenance.move-book-file-rows", (*Plugin).moveBookFileRowsDef, tasks.ResumeDrop, false, []string{string(ops.ResBookFiles), string(ops.ResBooks)}},
		{"maintenance.purge-empty-narrators", (*Plugin).purgeEmptyNarratorsDef, tasks.ResumeDrop, false, []string{string(ops.ResBooks), string(ops.ResOperations)}},
		{"maintenance.split-joined-narrators", (*Plugin).splitJoinedNarratorsDef, tasks.ResumeDrop, false, []string{string(ops.ResBooks), string(ops.ResOperations)}},
	}
}

func TestT6G5Kinds_MatchHEADDefs(t *testing.T) {
	p := &Plugin{}
	defs := p.t6g5Kinds(fakeT6G5Deps(t), nil)
	wants := t6g5Wants()
	require.Len(t, defs, len(wants), "every converted definition has exactly one native kind")
	for i, w := range wants {
		d := defs[i]
		t.Run(w.headID, func(t *testing.T) {
			h := w.def(p)
			require.Equal(t, w.headID, h.ID)
			c := tasks.CommonOf(d)
			assert.Equal(t, tasks.Kind("books."+w.headID), d.Kind(), "R3: kind is books.<HEAD ID>")
			assert.Equal(t, h.DisplayName, c.Title)
			assert.Equal(t, w.resume, c.Resume, "R5 resume mapping")
			assert.Equal(t, h.Timeout, c.Timeout)
			assert.Equal(t, h.ProgressTimeout, c.ProgressTimeout)
			assert.Equal(t, tasks.Key("books."+h.ConcurrencyKey), c.ConcurrencyKey, "R5 concurrency key")
			assert.Equal(t, tasks.PriorityLow, c.Priority)
			assert.Equal(t, []tasks.ReadySignal{"books.memdb"}, c.Ready)
			assert.Nil(t, c.Schedule, "R9: nil sched means no schedule")
			assert.False(t, c.Perms.Start.IsZero(), "R6")
			assert.True(t, c.Effects.Declared())
			assert.Equal(t, w.readOnly, c.Effects.IsReadOnly(), "R7 effects class")
			if !w.readOnly {
				assert.Equal(t, w.writes, t6g5WriteKinds(t, c.Effects), "R7 write set")
			}
			assert.Equal(t, "", t6g5ParString(d), "a Task declares no Parallelism: its loops pass theirs to RunItems")
		})
	}
}

// t6g5Pools pins rule R4 for each in-body loop, as literals.
func TestT6G5_LoopParallelism(t *testing.T) {
	assert.True(t, strings.HasPrefix(missingFileStatPool().Par.String(), "fixed(24): "), missingFileStatPool().Par.String())
	assert.Equal(t, 24, missingFileStatPool().Workers, "HEAD: the stat sweeps' own pool")
	assert.Equal(t, icWorkers, 4)
	assert.True(t, strings.HasPrefix(t6g5FixedPool(icWorkers, "r").Par.String(), "fixed(4): "))
	assert.Equal(t, "cpu", t6g5CPUPool().Par.String())
}

func TestT6G5Kinds_WritesThroughShell(t *testing.T) {
	t.Run("purge-empty-narrators preview writes nothing", func(t *testing.T) {
		f := newT6G5Fake()
		f.store.narrators = []database.Narrator{{ID: 1, Name: "Selina Kyle"}}
		d := t6g5KindByName(t, (&Plugin{}).t6g5Kinds(f.deps(), nil), "maintenance.purge-empty-narrators")
		rc := newT6G5Host(d.Kind(), tasks.ModePreview, &t6g5Gate{})
		out, err := t6g5Run(t, d, rc, "")
		require.NoError(t, err)
		assert.Contains(t, string(out), `"eligible":1`)
		assert.Empty(t, f.writer.called(), "a preview deletes nothing")
	})

	t.Run("purge-empty-narrators live refuses without a scan stand-down key", func(t *testing.T) {
		f := newT6G5Fake()
		f.store.narrators = []database.Narrator{{ID: 1, Name: "Selina Kyle"}}
		d := t6g5KindByName(t, (&Plugin{}).t6g5Kinds(f.deps(), nil), "maintenance.purge-empty-narrators")
		gate := &t6g5Gate{id: "purge"}
		rc := newT6G5Host(d.Kind(), tasks.ModeLive, gate)
		_, err := t6g5Run(t, d, rc, "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no scan stand-down key configured")
		handed := f.handed()
		require.Len(t, handed, 1, "NewWriter is called once per run")
		assert.Same(t, gate, handed[0], "NewWriter is handed this run's rc.Writer()")
		assert.Empty(t, f.writer.called(), "no delete before the stand-down is held")
	})

	t.Run("read-only kinds get no writer", func(t *testing.T) {
		for _, mode := range []tasks.Mode{tasks.ModePreview, tasks.ModeLive} {
			s := newRepairPebble(t)
			dir := t.TempDir()
			const book = "01BATMAN00000000000000RO01"
			_, err := s.CreateBook(&database.Book{ID: book, Title: "Hush", FilePath: dir})
			require.NoError(t, err)
			require.NoError(t, s.CreateBookFile(&database.BookFile{ID: "01ROWRO0000000000000000001", BookID: book, FilePath: filepath.Join(dir, "gone.mp3")}))

			r := t6g5RunNativeOn(t, New(fakeDeps{store: s, root: dir}), mode, "maintenance.missing-file-audit", "", "")
			require.True(t, tasks.CommonOf(r.def).Effects.IsReadOnly())
			require.NoError(t, r.err, "mode %v", mode)
			var res missingFileAuditResult
			require.NoError(t, json.Unmarshal(r.out, &res))
			assert.Equal(t, 1, res.Rows, "the audit read the book_file table (mode %v)", mode)
			assert.Equal(t, 1, res.Missing, "mode %v", mode)
			assert.Empty(t, r.handed, "a ReadOnly run never asks for a writer (mode %v)", mode)
			assert.Empty(t, r.spy.calls, "mode %v", mode)
		}
	})

	t.Run("mode keys are refused in params", func(t *testing.T) {
		for _, tc := range []struct{ headID, params string }{
			{"itunes.playlist-import", `{"dryRun":false}`},
			{"itunes.playlist-import", `{"dry_run":false}`},
			{"itunes.regroup", `{"dryRun":false}`},
			{"maintenance.purge-empty-narrators", `{"apply":true}`},
			{"maintenance.split-joined-narrators", `{"apply":true}`},
		} {
			f := newT6G5Fake()
			d := t6g5KindByName(t, (&Plugin{}).t6g5Kinds(f.deps(), nil), tc.headID)
			rc := newT6G5Host(d.Kind(), tasks.ModeLive, &t6g5Gate{})
			_, err := t6g5Run(t, d, rc, tc.params)
			require.Error(t, err, tc.headID+tc.params)
			assert.Contains(t, err.Error(), "the run's mode comes from the start request", tc.headID+tc.params)
			assert.Empty(t, f.writer.called())
		}
	})

	t.Run("clone: a live run names its write, a preview clears them", func(t *testing.T) {
		f := newT6G5Fake()
		d := t6g5KindByName(t, (&Plugin{}).t6g5Kinds(f.deps(), nil), "itunes.clone-into-library")
		_, err := t6g5Run(t, d, newT6G5Host(d.Kind(), tasks.ModeLive, &t6g5Gate{}), "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "a live run must name one of apply, rollback or repair")

		out, err := t6g5Run(t, d, newT6G5Host(d.Kind(), tasks.ModePreview, &t6g5Gate{}), `{"apply":true}`)
		require.NoError(t, err)
		assert.Contains(t, string(out), `"dry_run":true`, "a preview forces the dry run whatever params say")
		assert.Empty(t, f.writer.called())
	})

	t.Run("a missing NewWriter fails the run before the core", func(t *testing.T) {
		deps := t6g5Deps{Store: newT6G5Fake().store}
		d := t6g5KindByName(t, (&Plugin{}).t6g5Kinds(deps, nil), "maintenance.purge-empty-narrators")
		_, err := t6g5Run(t, d, newT6G5Host(d.Kind(), tasks.ModeLive, &t6g5Gate{}), "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "NewWriter is not set")
	})
}

// The native base's Touch is rc.Progress().Tick(): the long per-item loops call
// it to stamp liveness, and a stall detector reads that stamp. A Touch that
// reverts to a no-op would let a healthy run be flagged.
func TestT6G5NativeBase_TouchStampsLiveness(t *testing.T) {
	deps := t6g5Deps{Store: newT6G5Fake().store}
	rc, host := newT6G5HostFull(context.Background(), "books.maintenance.missing-file-audit", tasks.ModeLive, &t6g5Gate{}, nil)
	b, err := t6g5NativeBase("books.maintenance.missing-file-audit", rc, deps, false)
	require.NoError(t, err)
	before := host.touches.Load()
	b.Touch()
	b.Touch()
	assert.Equal(t, before+2, host.touches.Load(), "each Touch reaches the host as a liveness stamp")
}
