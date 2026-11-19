// file: internal/server/kinds_t7_test.go
// version: 1.0.0
// guid: 5d8c1f3e-72ab-4c0e-9b64-3a1e6f0d27c8
// last-edited: 2026-11-18

package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/entities"
)

// t7Row is what a converted server definition must look like, written out by
// hand from HEAD's definition and the brief's rules, not derived from the
// native kind under test.
type t7Row struct {
	// id is HEAD's definition ID; the kind is "books." + id (rule R3).
	id string
	// readOnly: the body writes nothing, so the kind is declared ReadOnly (R7).
	readOnly bool
	// liveOnly: HEAD has no Preview and a write capability, so a Preview run
	// is refused (R7, "live-only writer").
	liveOnly bool
	// noParams: HEAD's body takes no params at all, so there is nothing to refuse.
	noParams bool
}

// t7ServerRows is every converted server definition. A conversion adds its row.
var t7ServerRows = []t7Row{
	{id: librarySizeRefreshOpID, readOnly: true, noParams: true},
	{id: batchApplyRefusalsResetOpID},
	{id: itunesImportOpID, liveOnly: true},
	{id: olDownloadOpID, liveOnly: true},
	{id: olImportOpID, liveOnly: true},
	{id: aiAuthorScanOpID},
	{id: collectionRefreshOpID},
	{id: entities.AuthorReclassifyOpID, liveOnly: true},
	{id: bulkWriteBackOpID, liveOnly: true},
	{id: purgeSoftDeletedOpID},
	{id: folderAutoScanOpID, liveOnly: true},
	{id: libraryAIParseOpID, liveOnly: true},
	{id: handlers.DiagnosticsAIDefID, readOnly: true},
	{id: handlers.AIAuthorReviewDefID, readOnly: true},
	{id: handlers.AIAuthorMergeApplyDefID},
	{id: handlers.AIScanApplyDefID},
	{id: catalogCoverageOpID, readOnly: true},
	{id: seriesRenameOpID},
	{id: candidateFetchOpID},
	{id: batchSaveOpID, liveOnly: true},
	{id: bulkApplyPreviewOpID},
	{id: applyWhenScannedOpID},
	{id: organizeWhenScannedOpID},
	{id: batchApplyCachedOpID, liveOnly: true},
	{id: authorMergeOpID, liveOnly: true},
	{id: resolveProductionAuthorOpID, liveOnly: true},
	{id: identificationAdvanceOpID},
	{id: bulkMetadataFetchOpID, liveOnly: true},
	{id: catalogHarvestOpID},
	{id: libraryScanOpID, liveOnly: true},
	{id: libraryImportOpID, liveOnly: true},
	{id: libraryOrganizeOpID, liveOnly: true},
	{id: libraryTranscodeOpID, liveOnly: true},
	{id: dedupBookScanOpID, readOnly: true},
	{id: dedupBookMergeOpID, liveOnly: true},
	{id: dedupBookCombineOpID, liveOnly: true},
	{id: dedupAuthorScanOpID, readOnly: true},
	{id: dedupSeriesScanOpID, readOnly: true},
	{id: dedupSeriesDedupOpID},
	{id: dedupSeriesPruneOpID},
	{id: dedupSeriesMergeOpID},
	{id: dedupSeriesNormalizeOpID},
}

// t7ScanFullKind is the one native kind with no HEAD definition.
const t7ScanFullKind = tasks.Kind("books.library.scan-full")

// t7FakeStore and t7FakeWriter satisfy the composed interfaces by embedding them
// nil: a kind that reaches a method no test supplies panics, which is the signal
// the row is wrong, not a silent pass.
type t7FakeStore struct{ t7Store }

type t7FakeWriter struct{ t7Writer }

// t7ReachedCore is what the recording NewWriter panics with once a native shell
// has handed the host's writer to the core's env: the point under test.
type t7ReachedCore struct{ w tasks.Writer }

func t7TestDeps(t *testing.T) t7Deps {
	t.Helper()
	return t7Deps{
		Store:        t7FakeStore{},
		StandDownKey: tasks.Key("books.library.scan"),
		NewWriter:    func(w tasks.Writer) t7Writer { return t7FakeWriter{} },
	}
}

// t7Host is a taskstest.FakeRun with the mode and writer a test chooses.
type t7Host struct {
	*taskstest.FakeRun
	mode tasks.Mode
	w    tasks.Writer
}

func (h *t7Host) Mode() tasks.Mode     { return h.mode }
func (h *t7Host) Writer() tasks.Writer { return h.w }
func (h *t7Host) Log() *slog.Logger    { return slog.New(slog.DiscardHandler) }

// t7Writer is a distinguishable tasks.Writer: identity is the assertion.
type t7GateWriter struct{ name string }

func (*t7GateWriter) Do(context.Context, tasks.Write, func(context.Context) ([]tasks.Change, error)) error {
	return nil
}
func (*t7GateWriter) MarkRowApplied(context.Context, string)           {}
func (*t7GateWriter) RowApplied(context.Context, string) (bool, error) { return false, nil }

// t7RunKind runs d's own Run, the function the executor would call. A panic of
// t7ReachedCore is returned as reached, so a kind that gets as far as its core
// can be told from one that refused first.
func t7RunKind(t *testing.T, d tasks.Definition, mode tasks.Mode, w tasks.Writer, params string) (reached *t7ReachedCore, err error) {
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

func t7ServerDefs(t *testing.T, deps t7Deps) []tasks.Definition {
	t.Helper()
	return (&Server{}).t7Kinds(deps, nil)
}

func t7ByKind(t *testing.T, defs []tasks.Definition, kind tasks.Kind) tasks.Definition {
	t.Helper()
	for _, d := range defs {
		if d.Kind() == kind {
			return d
		}
	}
	t.Fatalf("no definition of kind %s", kind)
	return nil
}

func TestT7ServerKinds_Conformance(t *testing.T) {
	taskstest.Conformance(t, t7ServerDefs(t, t7TestDeps(t))...)
}

func TestT7ServerKinds_MatchHEADDefs(t *testing.T) {
	srv, _, _ := bootRegisteredOpIDs(t)
	defs := t7ServerDefs(t, t7TestDeps(t))

	wantKinds := map[tasks.Kind]bool{}
	for _, r := range t7ServerRows {
		wantKinds[tasks.Kind("books."+r.id)] = true
	}
	for _, d := range defs {
		k := d.Kind()
		if k == t7ScanFullKind {
			continue // the one kind with no HEAD def (see TestT7ServerKinds_ScanFullBesideScan)
		}
		require.True(t, wantKinds[k], "native kind %s has no row in t7ServerRows", k)
	}

	for _, r := range t7ServerRows {
		head, ok := srv.opRegistry.Def(r.id)
		require.True(t, ok, "HEAD has no definition %s", r.id)
		c := tasks.CommonOf(t7ByKind(t, defs, tasks.Kind("books."+r.id)))

		require.Equal(t, head.DisplayName, c.Title, "%s title", r.id)
		require.Equal(t, head.Description, c.Help, "%s help", r.id)
		require.Equal(t, min(head.Timeout, tasks.MaxTimeout), c.Timeout, "%s timeout", r.id)
		require.Equal(t, head.ProgressTimeout, c.ProgressTimeout, "%s progress timeout", r.id)
		wantKey := tasks.Key("")
		if head.ConcurrencyKey != "" {
			wantKey = tasks.Key("books." + head.ConcurrencyKey)
		}
		require.Equal(t, wantKey, c.ConcurrencyKey, "%s concurrency key", r.id)

		wantPrio := tasks.PriorityNormal
		switch head.DefaultPriority {
		case opsregistry.PriorityLow:
			wantPrio = tasks.PriorityLow
		case opsregistry.PriorityHigh:
			wantPrio = tasks.PriorityHigh
		}
		require.Equal(t, wantPrio, c.Priority, "%s priority", r.id)

		var wantNotWhile []tasks.Kind
		for _, dep := range head.DependsOn {
			wantNotWhile = append(wantNotWhile, tasks.Kind("books."+dep))
		}
		require.Equal(t, wantNotWhile, c.NotWhile, "%s NotWhile", r.id)

		// A write set exactly when the body writes (R7).
		require.Equal(t, r.readOnly, c.Effects.IsReadOnly(), "%s read-only", r.id)
		if !r.readOnly {
			require.NotEmpty(t, c.Effects.WriteSet(), "%s declares no write", r.id)
		}
		// HEAD with an explicit Writes list is never declared ReadOnly.
		if len(head.Writes) > 0 {
			require.False(t, c.Effects.IsReadOnly(), "%s: HEAD declares Writes", r.id)
		}

		if !head.NoWarmupWait {
			require.Contains(t, c.Ready, t7SignalMemDB, "%s readiness", r.id)
		}
	}
}

func TestT7ServerKinds_WritesThroughShell(t *testing.T) {
	var got tasks.Writer
	deps := t7TestDeps(t)
	deps.NewWriter = func(w tasks.Writer) t7Writer {
		got = w
		panic(t7ReachedCore{w: w})
	}
	defs := t7ServerDefs(t, deps)

	for _, r := range t7ServerRows {
		d := t7ByKind(t, defs, tasks.Kind("books."+r.id))

		// A mode key in params is refused natively: the mode is the start
		// request's (05-tasks 5T.3.7 item 3).
		if !r.noParams {
			_, err := t7RunKind(t, d, tasks.ModeLive, &t7GateWriter{name: "mode"}, `{"dry_run":true}`)
			require.Error(t, err, "%s: a dry_run param must be refused", r.id)
			require.Contains(t, err.Error(), "mode", "%s", r.id)
		}

		if r.readOnly {
			continue
		}

		// A live run hands the host's own writer to the core's env.
		w := &t7GateWriter{name: r.id}
		got = nil
		reached, err := t7RunKind(t, d, tasks.ModeLive, w, `{}`)
		if reached != nil {
			require.Same(t, w, got, "%s: the core's writer must be the run's rc.Writer()", r.id)
			require.Same(t, w, reached.w)
		} else if err != nil {
			// A kind may refuse `{}` for a required param before it needs a
			// writer; that is not a writer bypass, but it must not be the
			// live-only refusal.
			require.NotContains(t, err.Error(), "preview not supported", "%s", r.id)
		}

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

// t7ScanCapture is a writer whose PerformScan records the request the shared
// scan core built, and whose every other method panics (embedded nil).
type t7ScanCapture struct {
	t7FakeWriter
	req *scanner.ScanRequest
}

func (c *t7ScanCapture) PerformScan(_ context.Context, req *scanner.ScanRequest, _ logger.Logger) error {
	c.req = req
	return nil
}

func TestT7ServerKinds_ScanFullBesideScan(t *testing.T) {
	cap := &t7ScanCapture{}
	deps := t7TestDeps(t)
	deps.NewWriter = func(tasks.Writer) t7Writer { return cap }
	defs := t7ServerDefs(t, deps)
	scan := t7ByKind(t, defs, tasks.Kind("books."+libraryScanOpID))
	full := t7ByKind(t, defs, t7ScanFullKind)

	taskstest.Conformance(t, scan, full)

	// One key, the scan stand-down key: the two never run at once.
	cs, cf := tasks.CommonOf(scan), tasks.CommonOf(full)
	require.Equal(t, tasks.Key("books.library.scan"), cf.ConcurrencyKey)
	require.Equal(t, cs.ConcurrencyKey, cf.ConcurrencyKey)
	require.Equal(t, deps.StandDownKey, cf.ConcurrencyKey)

	// A full run hands the shared core force_update and include_root_dir true,
	// whatever the request carried; a plain scan hands it what it was given.
	_, err := t7RunKind(t, full, tasks.ModeLive, &t7GateWriter{}, `{}`)
	require.NoError(t, err)
	require.NotNil(t, cap.req)
	require.NotNil(t, cap.req.ForceUpdate)
	require.True(t, *cap.req.ForceUpdate)
	require.NotNil(t, cap.req.IncludeRootDir)
	require.True(t, *cap.req.IncludeRootDir)

	cap.req = nil
	_, err = t7RunKind(t, scan, tasks.ModeLive, &t7GateWriter{}, `{"folder_path":"/batcave/import"}`)
	require.NoError(t, err)
	require.NotNil(t, cap.req)
	require.Nil(t, cap.req.ForceUpdate)
	require.Nil(t, cap.req.IncludeRootDir)
	require.Equal(t, "/batcave/import", *cap.req.FolderPath)
}
