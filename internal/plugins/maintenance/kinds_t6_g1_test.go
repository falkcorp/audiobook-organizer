// file: internal/plugins/maintenance/kinds_t6_g1_test.go
// version: 1.0.0
// guid: 8b0c5c2e-6a54-4c27-9d0e-3f1a7d52e0b4
// last-edited: 2026-11-13

package maintenance

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/authormerge"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
)

// t6g1Recorder is the writer a test hands the kinds: it records the calls that
// reach it and the tasks.Writer NewWriter was given.
type t6g1Recorder struct {
	t6g1Writer // nil: a call the test did not expect panics

	mu      sync.Mutex
	renames []string
}

func (r *t6g1Recorder) RenameAuthorJournaled(opID string, a database.Author, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.renames = append(r.renames, opID+":"+a.Name+"->"+name)
	return nil
}

func (r *t6g1Recorder) MoveCredits(string, authormerge.CreditMove) (authormerge.MoveResult, error) {
	panic("MoveCredits is not expected in this test")
}

// t6g1JokerStore holds one stranded author, "& Joker", and no author "Joker".
type t6g1JokerStore struct{ t6g1Store }

func (t6g1JokerStore) GetAllAuthors() ([]database.Author, error) {
	return []database.Author{{ID: 7, Name: "& Joker"}, {ID: 8, Name: "Batman"}}, nil
}
func (t6g1JokerStore) GetAuthorByName(string) (*database.Author, error) { return nil, nil }

// t6g1Probe records how the native shells called deps.NewWriter.
type t6g1Probe struct {
	mu       sync.Mutex
	calls    int
	given    tasks.Writer
	recorder *t6g1Recorder
	caches   int
}

func (p *t6g1Probe) deps() t6g1Deps {
	return t6g1Deps{
		Store: t6g1JokerStore{},
		NewWriter: func(w tasks.Writer) t6g1Writer {
			p.mu.Lock()
			defer p.mu.Unlock()
			p.calls++
			p.given = w
			return p.recorder
		},
		AIJournalRetentionDays: func() int { return 30 },
		InvalidateAuthorsCache: func() { p.mu.Lock(); p.caches++; p.mu.Unlock() },
		InvalidateDedupCache:   func() { p.mu.Lock(); p.caches++; p.mu.Unlock() },
	}
}

// fakeT6G1Deps is a deps value that is enough to build the 14 definitions: the
// conformance checks read their declarations and never run a body.
func fakeT6G1Deps(t *testing.T) t6g1Deps {
	t.Helper()
	p := &t6g1Probe{recorder: &t6g1Recorder{}}
	return p.deps()
}

// gateWriter is a distinguishable tasks.Writer: identity is the assertion.
type gateWriter struct{ name string }

func (*gateWriter) Do(context.Context, tasks.Write, func(context.Context) ([]tasks.Change, error)) error {
	return nil
}
func (*gateWriter) MarkRowApplied(context.Context, string)           {}
func (*gateWriter) RowApplied(context.Context, string) (bool, error) { return false, nil }

// t6g1Host is a taskstest.FakeRun with the mode and writer a test chooses
// (FakeRun is pinned to ModeLive with no writer).
type t6g1Host struct {
	*taskstest.FakeRun
	mode tasks.Mode
	w    tasks.Writer
}

func (h *t6g1Host) Mode() tasks.Mode     { return h.mode }
func (h *t6g1Host) Writer() tasks.Writer { return h.w }
func (h *t6g1Host) Log() *slog.Logger    { return slog.New(slog.DiscardHandler) }

// runT6G1Kind runs d's own Run, the function the executor would call.
func runT6G1Kind(t *testing.T, d tasks.Definition, mode tasks.Mode, w tasks.Writer, params string) error {
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
	p, err := decode(raw)
	require.NoError(t, err)
	h := &t6g1Host{FakeRun: taskstest.NewFakeRun(context.Background(), d.Kind()), mode: mode, w: w}
	_, err = task.Run(tasks.NewRC(h), p)
	return err
}

func t6g1ByKind(t *testing.T, defs []tasks.Definition, kind tasks.Kind) tasks.Definition {
	t.Helper()
	for _, d := range defs {
		if d.Kind() == kind {
			return d
		}
	}
	t.Fatalf("no definition of kind %s", kind)
	return nil
}

func TestT6G1Kinds_Conformance(t *testing.T) {
	taskstest.Conformance(t, (&Plugin{}).t6g1Kinds(fakeT6G1Deps(t), nil)...)
}

func TestT6G1Kinds_MatchHEADDefs(t *testing.T) {
	p := &Plugin{}
	defs := p.t6g1Kinds(fakeT6G1Deps(t), nil)
	require.Len(t, defs, 14)

	type row struct {
		head sdk.OperationDef
		par  tasks.Parallelism
		// checkpoints: HEAD's ResumeRestart is ResumeContinue only for a body
		// that checkpoints.
		checkpoints bool
		// readOnly: the two reports write nothing.
		readOnly bool
	}
	rows := []row{
		{head: p.activityFilterIndexBackfillDef(), par: activityFilterIndexBackfillParallelism(), checkpoints: true},
		{head: p.pruneAIJournalDef(), par: pruneAIJournalParallelism()},
		{head: p.authorSplitScanDef(), par: authorSplitScanParallelism()},
		{head: p.resolveProductionAuthorsDef(), par: resolveProductionAuthorsParallelism()},
		{head: p.authorConjunctionRepairDef(), par: authorConjunctionRepairParallelism()},
		{head: p.authorDuplicateMergeDef(), par: authorDuplicateMergeParallelism()},
		{head: p.authorIDRepairDef(), par: authorIDRepairParallelism()},
		{head: p.authorPathLinkDef(), par: authorPathLinkParallelism()},
		{head: p.purgeEmptyAuthorsDef(), par: purgeEmptyAuthorsParallelism()},
		{head: p.authorStripMergeDef(), par: authorStripMergeParallelism()},
		{head: p.authorTitleFragmentScanDef(), par: authorTitleFragmentParallelism(), readOnly: true},
		{head: p.authorWhitespaceCollisionReportDef(), par: authorWhitespaceCollisionParallelism(), readOnly: true},
		{head: p.authorityBuildDef(), par: authorityBuildParallelism()},
		{head: p.autoMatchTranscribedDef(), par: autoMatchTranscribedParallelism(), checkpoints: true},
	}

	// Parallelism, as each core's own function declares it. HEAD's pools: a
	// fixed worker count where HEAD set one, cpu where it sized to NumCPU, and
	// sequential (with its reason) otherwise.
	wantPar := map[string]string{
		"maintenance.author-split-scan":              "fixed(4): ",
		"maintenance.resolve-production-authors":     "fixed(",
		"maintenance.auto-match-transcribed":         "fixed(1): ",
		"maintenance.author-id-repair":               "cpu",
		"maintenance.author-path-link":               "cpu",
		"maintenance.authority-build":                "cpu",
		"maintenance.activity-filter-index-backfill": "cpu",
	}

	seen := map[tasks.Kind]bool{}
	for _, r := range rows {
		id := r.head.ID
		kind := tasks.Kind("books." + id)
		seen[kind] = true
		def := t6g1ByKind(t, defs, kind)
		c := tasks.CommonOf(def)

		require.Equal(t, r.head.DisplayName, c.Title, "%s title", id)
		require.Equal(t, r.head.Description, c.Help, "%s help", id)
		wantPrio := tasks.PriorityNormal
		switch r.head.DefaultPriority {
		case sdk.PriorityLow:
			wantPrio = tasks.PriorityLow
		case sdk.PriorityHigh:
			wantPrio = tasks.PriorityHigh
		}
		require.Equal(t, wantPrio, c.Priority, "%s priority", id)
		require.Equal(t, r.head.Timeout, c.Timeout, "%s timeout", id)
		require.Equal(t, tasks.Key("books."+r.head.ConcurrencyKey), c.ConcurrencyKey, "%s concurrency key", id)
		if r.head.ProgressTimeout > 0 {
			require.Equal(t, r.head.ProgressTimeout, c.ProgressTimeout, "%s progress timeout", id)
		}

		var wantResume tasks.ResumePolicy
		switch r.head.ResumePolicy {
		case sdk.ResumeRestart:
			wantResume = tasks.ResumeFromZero
			if r.checkpoints {
				wantResume = tasks.ResumeContinue
			}
		case sdk.ResumeRequeue:
			wantResume = tasks.ResumeFromZero
		case sdk.ResumeDrop:
			wantResume = tasks.ResumeDrop
		case sdk.ResumeAsk:
			wantResume = tasks.ResumeAsk
		default:
			t.Fatalf("%s: HEAD resume policy %v has no mapping", id, r.head.ResumePolicy)
		}
		require.Equal(t, wantResume, c.Resume, "%s resume", id)

		switch {
		case r.readOnly:
			require.True(t, c.Effects.IsReadOnly(), "%s must be declared ReadOnly", id)
		default:
			require.False(t, c.Effects.IsReadOnly(), "%s writes", id)
			require.NotEmpty(t, c.Effects.WriteSet(), "%s write set", id)
		}

		ps := r.par.String()
		if want, ok := wantPar[id]; ok {
			require.True(t, strings.HasPrefix(ps, want), "%s parallelism %q, want prefix %q", id, ps, want)
		} else {
			require.True(t, strings.HasPrefix(ps, "sequential: "), "%s parallelism %q, want sequential", id, ps)
		}
		require.Contains(t, c.Ready, t6g1SignalMemDB, "%s readiness", id)
	}
	require.Len(t, seen, 14, "14 distinct kinds")
}

func TestT6G1Kinds_WritesThroughShell(t *testing.T) {
	probe := &t6g1Probe{recorder: &t6g1Recorder{}}
	defs := (&Plugin{}).t6g1Kinds(probe.deps(), nil)
	conj := t6g1ByKind(t, defs, authorConjunctionRepairKind)
	gate := &gateWriter{name: "gotham"}

	// Preview reports and writes nothing.
	require.NoError(t, runT6G1Kind(t, conj, tasks.ModePreview, gate, ""))
	require.Empty(t, probe.recorder.renames, "a preview wrote")
	require.Zero(t, probe.caches, "a preview dropped the caches")

	// Live renames through the writer NewWriter built over the run's writer.
	probe.calls = 0
	require.NoError(t, runT6G1Kind(t, conj, tasks.ModeLive, gate, ""))
	require.Equal(t, 1, probe.calls)
	require.Same(t, gate, probe.given, "NewWriter must be given the run's writer")
	require.Len(t, probe.recorder.renames, 1)
	require.Contains(t, probe.recorder.renames[0], "& Joker->Joker")
	require.Positive(t, probe.caches, "a live write drops the caches")

	// A live-only kind refuses Preview before it builds a writer.
	probe.calls = 0
	for _, kind := range []tasks.Kind{pruneAIJournalKind, activityFilterIndexBackfillKind} {
		err := runT6G1Kind(t, t6g1ByKind(t, defs, kind), tasks.ModePreview, gate, "")
		require.Error(t, err, "%s", kind)
		require.Contains(t, err.Error(), "preview not supported", "%s", kind)
	}
	require.Zero(t, probe.calls, "a refused preview built a writer")
}
