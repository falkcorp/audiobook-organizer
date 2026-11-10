// file: internal/plugins/maintenance/repoint_probe_outcome_test.go
// version: 1.0.1
// guid: d3a16d65-e084-4a0a-8b8d-3eba7a91104c
// last-edited: 2026-11-09

package maintenance

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/bookfileaudio"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/mediainfo"
	"github.com/falkcorp/audiobook-organizer/internal/testutil/durtest"
)

// The plan-time probe is replayed at apply time without re-reading the file, and
// what it decided survives: a real duration is written, a failed read of the
// present file is one attempt, and a read that could not run (no ffprobe,
// absent file) is nothing. The last case is the one a bare
// MediaInfo{Duration, Codec} used to get wrong, recording a counted "0s".
func TestRfApplyProbe(t *testing.T) {
	cases := []struct {
		name         string
		probe        rfProbe
		wantDur      int
		wantAttempts int
		wantCodec    string
	}{
		{"measured", rfProbe{Info: mediainfo.MediaInfo{Duration: 5400, Codec: "AAC"}, Source: bookfileaudio.SourceCaller}, 5400, 0, "AAC"},
		{"failed on a present file", rfProbe{Info: mediainfo.MediaInfo{Codec: "AAC"}, Source: bookfileaudio.SourceProbeFailed, Failure: "exit status 1"}, 0, 1, "AAC"},
		{"could not run", rfProbe{Info: mediainfo.MediaInfo{Codec: "AAC"}, Source: bookfileaudio.SourceProbeUnavailable}, 0, 0, "AAC"},
		{"nothing tried", rfProbe{}, 0, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &database.BookFile{ID: "f1", FilePath: "/lib/Year One/01.m4b"}
			rfApplyProbe(f, tc.probe)
			require.Equal(t, tc.wantDur, durtest.Sec(f.Duration))
			require.Equal(t, tc.wantAttempts, f.Scan.DurationOK.Attempts)
			require.Equal(t, tc.wantCodec, f.Codec)
			if tc.wantAttempts > 0 {
				require.Equal(t, "exit status 1", f.Scan.DurationOK.LastError)
			}
		})
	}
}

// S6(b): the whole plan-then-apply path with a real ffprobe stub, not a hand-built
// rfProbe. The plan reads the target once and the apply replays its outcome, so
// what reaches the repointed row is decided by what planRFBook stores in rfProbe.
// A failed read of the present file is a COUNTED attempt carrying the plan's own
// failure text (mutation check: with planRFBook not filling rfProbe.Failure the
// attempt is recorded with the placeholder "probe failed (no detail)" text);
// a read that could not run is nothing.
func TestRepointFolderAudio_PlanThenApplyRecordsProbeOutcome(t *testing.T) {
	t.Run("ffprobe exits 1: one counted attempt with the plan's error", func(t *testing.T) {
		stubFFprobe(t, "exit 1")
		root := t.TempDir()
		store, target := seedConsolidated(t, root, 250)
		got := rfOnly(t, rfRun(t, store, root, false))
		require.Equal(t, rfOutcomeApplied, got.Outcome, got.Error)

		kept, ok := store.updateByID("fb")
		require.True(t, ok)
		require.Equal(t, target, kept.FilePath)
		require.Nil(t, kept.Duration)
		require.Equal(t, 1, kept.Scan.DurationOK.Attempts)
		require.Contains(t, kept.Scan.DurationOK.LastError, "exit status 1", "the plan's failure text must reach the row")
		require.Equal(t, database.DurationProbeFailed, kept.DurationState())
		require.Equal(t, "probe-failed", got.DurationSource)
	})
	t.Run("ffprobe not installed: nothing recorded", func(t *testing.T) {
		stubFFprobe(t, "")
		root := t.TempDir()
		store, _ := seedConsolidated(t, root, 250)
		got := rfOnly(t, rfRun(t, store, root, false))
		require.Equal(t, rfOutcomeApplied, got.Outcome, got.Error)
		kept, ok := store.updateByID("fb")
		require.True(t, ok)
		require.Equal(t, database.StageStatus{}, kept.Scan.DurationOK)
		require.Equal(t, "probe-unavailable", got.DurationSource)
	})
}

// S5: the apply replays the plan's read without reading the file again, so a
// target that is not the file the plan read (a replacement dropped in between,
// even at the same size) is "changed since the plan" and nothing is written.
func TestRepointFolderAudio_TargetChangedSincePlanIsSkipped(t *testing.T) {
	cases := map[string]func(t *testing.T, target string){
		"different size": func(t *testing.T, target string) {
			require.NoError(t, os.WriteFile(target, make([]byte, 999), 0o644))
		},
		"same size, newer mtime": func(t *testing.T, target string) {
			later := time.Now().Add(time.Hour)
			require.NoError(t, os.Chtimes(target, later, later))
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			stubFFprobe(t, "echo 3600.0")
			root := t.TempDir()
			store, target := seedConsolidated(t, root, 250)
			env := rfEnv{store: store, rootDir: root, queue: &hookQueue{hook: func() { change(t, target) }}}
			got := rfOnly(t, rfRunEnv(t, env, false))
			require.Equal(t, rfOutcomeChanged, got.Outcome, got.Error)
			require.Contains(t, got.Error, "changed since the plan")
			require.Empty(t, store.updates)
		})
	}
	t.Run("untouched target applies (control)", func(t *testing.T) {
		stubFFprobe(t, "echo 3600.0")
		root := t.TempDir()
		store, _ := seedConsolidated(t, root, 250)
		got := rfOnly(t, rfRun(t, store, root, false))
		require.Equal(t, rfOutcomeApplied, got.Outcome, got.Error)
	})
}

// hookQueue is an OpQueueReader whose call (the apply's first act, after the
// plan has read every target) runs hook.
type hookQueue struct {
	hook func()
	once sync.Once
}

func (q *hookQueue) ListActiveOperationsV2() ([]database.OperationV2Row, error) {
	q.once.Do(q.hook)
	return nil, nil
}

// N5: the plan-time probe runs in a dry run too, and its failure line is Debug
// there, Warn in a real run (where it is the only record of the failure, the
// apply replaying it). (That a demoted line does not use up the once-per-cause
// warning is pinned in bookfileaudio.)
func TestRepointFolderAudio_PlanProbeFailureLogLevel(t *testing.T) {
	run := func(t *testing.T, script string, dry bool) *probeLogRec {
		stubFFprobe(t, script)
		root := t.TempDir()
		store, _ := seedConsolidated(t, root, 250)
		rec := &probeLogRec{}
		env := rfEnv{store: store, rootDir: root, queue: fsQueue{}, probeLog: rec}
		rfRunEnv(t, env, dry)
		return rec
	}
	t.Run("file failure, dry run: debug only", func(t *testing.T) {
		rec := run(t, "exit 1", true)
		require.Zero(t, rec.count(&rec.warns), "a dry run must not Warn per target")
		require.NotZero(t, rec.count(&rec.debugs))
	})
	t.Run("file failure, real run: warn", func(t *testing.T) {
		rec := run(t, "exit 1", false)
		require.NotZero(t, rec.count(&rec.warns))
	})
}

type probeLogRec struct {
	mu            sync.Mutex
	warns, debugs []string
}

func (l *probeLogRec) add(dst *[]string, m string, a []any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	*dst = append(*dst, strings.TrimSpace(fmt.Sprintf(m, a...)))
}
func (l *probeLogRec) count(p *[]string) int { l.mu.Lock(); defer l.mu.Unlock(); return len(*p) }
func (l *probeLogRec) Trace(string, ...any)  {}
func (l *probeLogRec) Info(string, ...any)   {}
func (l *probeLogRec) Error(string, ...any)  {}
func (l *probeLogRec) Debug(m string, a ...any) {
	l.add(&l.debugs, m, a)
}
func (l *probeLogRec) Warn(m string, a ...any) { l.add(&l.warns, m, a) }
