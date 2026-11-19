// file: internal/plugins/dedup/kinds_scoring_test.go
// version: 1.0.0
// guid: a002cdf7-aa0e-4499-b278-7610e743dccf
// last-edited: 2026-11-18

package dedup

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/pkg/ops"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// scoringSpy is the engine a test hands the scoring kinds: it records the
// writer its factory was handed and the engine calls that reach it. It
// implements every method of scoringEngine itself (the compiler checks that where
// scoringFakeDeps returns it), so a method added to the interface breaks this
// file until the spy answers it, instead of panicking on a nil embedded interface.
type scoringSpy struct {
	mu      sync.Mutex
	writers []tasks.Writer
	calls   []string

	purgeDeleted int
	purgeErr     error
}

func (s *scoringSpy) rec(call string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, call)
}

func (s *scoringSpy) PurgeStaleCandidates(context.Context) (int, error) {
	s.rec("PurgeStaleCandidates")
	return s.purgeDeleted, s.purgeErr
}

func (s *scoringSpy) factory(w tasks.Writer) scoringEngine {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writers = append(s.writers, w)
	return s
}

func (s *scoringSpy) seenWriters() []tasks.Writer {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]tasks.Writer(nil), s.writers...)
}

func (s *scoringSpy) recorded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

// newScoringSpy is a spy and the deps that hand it to every scoring kind.
func newScoringSpy() (*scoringSpy, ScoringKindDeps) {
	s := &scoringSpy{}
	return s, ScoringKindDeps{NewEngine: s.factory}
}

func scoringFakeDeps(t *testing.T) ScoringKindDeps {
	t.Helper()
	_, d := newScoringSpy()
	return d
}

func scoringRows() []kindRow {
	return []kindRow{
		{
			HeadID: "dedup.purge-stale", Title: "Cleanup stale dedup candidates",
			HeadResume: sdk.ResumeDrop, Resume: tasks.ResumeDrop,
			Priority: tasks.PriorityHigh, Timeout: 10 * time.Minute, NoWarmup: true,
			Writes: []ops.Resource{ops.ResEmbeddings}, HeadKey: "dedup.purge-stale",
		},
	}
}

func scoringWriteProbes(t *testing.T) []writeProbe {
	t.Helper()
	spy, d := newScoringSpy()
	spy.purgeDeleted = 3
	def := (&Plugin{}).purgeStaleNative(d, nil)
	return []writeProbe{{Kind: def.Kind(), Def: def, Writers: spy.seenWriters, Calls: spy.recorded}}
}

// --- group tests (not TestDedupKinds_: the brief counts exactly four of those) ---

// A native purge-stale runs the shared core: the engine's count comes back as the
// result, and the progress frames are HEAD's.
func TestDedupScoring_PurgeStaleNativeRunsTheCore(t *testing.T) {
	spy, d := newScoringSpy()
	spy.purgeDeleted = 7
	def := (&Plugin{}).purgeStaleNative(d, nil)

	h, err := runKind(t, def, tasks.ModeLive, &sentinelWriter{}, "")
	require.NoError(t, err)
	require.Equal(t, []string{"PurgeStaleCandidates"}, spy.recorded())
	last := h.lastNote()
	require.Equal(t, "Cleanup complete — 7 stale candidate(s) removed", last)
}

// HEAD runs purge-stale live only; a native Preview must not delete, so it is
// refused before the engine is built.
func TestDedupScoring_PurgeStalePreviewIsRefusedWithoutBuildingTheEngine(t *testing.T) {
	spy, d := newScoringSpy()
	def := (&Plugin{}).purgeStaleNative(d, nil)

	_, err := runKind(t, def, tasks.ModePreview, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "preview not supported; start with mode live")
	require.Empty(t, spy.seenWriters(), "a refused Preview builds no engine")
	require.Empty(t, spy.recorded())
}

// The run's mode comes from the start request: a mode key in params is refused,
// not dropped, so a dry-run start can never become a live one.
func TestDedupScoring_PurgeStaleRefusesModeKeysInParams(t *testing.T) {
	for _, params := range []string{`{"dry_run":true}`, `{"dryRun":false}`, `{"Apply":true}`} {
		spy, d := newScoringSpy()
		def := (&Plugin{}).purgeStaleNative(d, nil)
		_, err := runKind(t, def, tasks.ModeLive, &sentinelWriter{}, params)
		require.ErrorContains(t, err, "the run's mode comes from the start request", params)
		require.Empty(t, spy.recorded(), params)
	}
}

// An engine error fails the run with HEAD's wrapping.
func TestDedupScoring_PurgeStaleEngineErrorFailsTheRun(t *testing.T) {
	spy, d := newScoringSpy()
	spy.purgeErr = errors.New("pebble closed")
	def := (&Plugin{}).purgeStaleNative(d, nil)

	_, err := runKind(t, def, tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "purge stale candidates: pebble closed")
}

// A deps value with no engine factory is refused up front, not dereferenced.
func TestDedupScoring_PurgeStaleRefusesUnwiredDeps(t *testing.T) {
	def := (&Plugin{}).purgeStaleNative(ScoringKindDeps{}, nil)
	_, err := runKind(t, def, tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "Scoring.NewEngine is not set")
}

// The HEAD shell still refuses a plugin with no engine, as before.
func TestDedupScoring_PurgeStaleHeadShellNeedsAnEngine(t *testing.T) {
	err := (&Plugin{}).runPurgeStale(context.Background(), nil, &mockReporter{})
	require.ErrorContains(t, err, "dedup engine not available")
}
