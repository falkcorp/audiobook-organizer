// file: internal/remux/resume_test.go
// version: 1.0.0
// guid: da8f14cf-0bcb-4c90-af10-93aad72994d6
// last-edited: 2026-10-30

package remux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/walkresume"
)

// countingChecker is a ProtectedChecker that protects nothing and records every
// path it is asked about. Both passes ask exactly once per candidate they
// process, before any probe, so it is the per-item call counter.
type countingChecker struct {
	mu    sync.Mutex
	paths []string
	// cancelAt, when > 0, calls cancel on the cancelAt-th question.
	cancelAt int
	cancel   context.CancelFunc
}

func (c *countingChecker) IsProtected(path string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.paths = append(c.paths, path)
	if c.cancelAt > 0 && len(c.paths) == c.cancelAt {
		c.cancel()
	}
	return false
}

func (c *countingChecker) seen() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.paths...)
}

// resumeFixture writes n unreadable .m4b files (taglib fails and so does
// ffmpeg, so each is counted failed: no fixture needs a real container) under a
// layout that includes a directory and a sibling whose names straddle '/' in
// string order ("Batman" vs "Batman-Begins"), the case a path cursor over a raw
// WalkDir order gets wrong.
func resumeFixture(t *testing.T, n int) (root string, files []string) {
	t.Helper()
	root = t.TempDir()
	withRemuxAppDirs(t, root, true)
	dirs := []string{"Batman", "Batman-Begins", "Robin", "Batman/Year One"}
	for i := 0; i < n; i++ {
		f := filepath.Join(root, dirs[i%len(dirs)], fmt.Sprintf("book-%03d.m4b", i))
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte("not a real m4b"), 0o644); err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	return root, files
}

type resumePass struct {
	name string
	key  string
	run  func(ctx context.Context, s *MockStore, c ProtectedChecker, opts ...walkresume.Option) error
}

var resumePasses = []resumePass{
	{"remux", RemuxKey, func(ctx context.Context, s *MockStore, c ProtectedChecker, opts ...walkresume.Option) error {
		r := New(s)
		r.SetProtectedChecker(c)
		return r.RemuxMalformedFiles(ctx, nil, opts...)
	}},
	{"transcode", TranscodeKey, func(ctx context.Context, s *MockStore, c ProtectedChecker, opts ...walkresume.Option) error {
		tr := NewTranscoder(s)
		tr.SetProtectedChecker(c)
		return tr.TranscodeMalformedFiles(ctx, nil, opts...)
	}},
}

// A run cancelled mid-walk persists a cursor, and the resumed run continues
// after it: it asks about no file the cursor covers, every remaining file is
// asked about exactly once, the earlier attempts' counters carry, and only the
// completed run sets the done flag.
func TestMalformedPasses_CancelledRunCheckpointsAndResumeSkipsDoneItems(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available, skipping")
	}
	const n = 40
	for _, p := range resumePasses {
		t.Run(p.name, func(t *testing.T) {
			_, files := resumeFixture(t, n)
			store := &MockStore{}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			first := &countingChecker{cancelAt: 25, cancel: cancel}
			var states []walkresume.State
			run := walkresume.Run{Checkpoint: func(st walkresume.State) { states = append(states, st) }}
			if err := p.run(ctx, store, first, walkresume.WithRun(run)); err != nil {
				t.Fatalf("cancelled leg: %v", err)
			}
			if doneFlagSet(store, p.key) {
				t.Fatal("a cancelled walk set the done flag")
			}
			if len(states) == 0 {
				t.Fatal("no checkpoint was written before the cancel")
			}
			last := states[len(states)-1]
			if last.Cursor == "" {
				t.Fatalf("checkpoint has no cursor: %+v", last)
			}
			if last.Failed < walkresume.CheckpointEvery {
				t.Errorf("checkpoint carried Failed=%d, want the %d+ files already counted", last.Failed, walkresume.CheckpointEvery)
			}
			firstSeen := first.seen()
			if len(firstSeen) != 25 {
				t.Fatalf("the cancelled leg asked about %d files, want 25 (stop right at the cancel)", len(firstSeen))
			}

			// Resume: the checkpoint is the next attempt's Resume, as the registry
			// merges it back into params.
			second := &countingChecker{}
			if err := p.run(context.Background(), store, second, walkresume.WithRun(walkresume.Run{Resume: last})); err != nil {
				t.Fatalf("resumed leg: %v", err)
			}
			covered := map[string]bool{}
			for _, f := range files {
				if f <= last.Cursor {
					covered[f] = true
				}
			}
			resumed := second.seen()
			seen := map[string]int{}
			for _, f := range resumed {
				if f <= last.Cursor {
					t.Errorf("resumed leg reprocessed %s, at or before the cursor %s", f, last.Cursor)
				}
				seen[f]++
			}
			if want := n - len(covered); len(resumed) != want {
				t.Errorf("resumed leg asked about %d files, want %d (the %d past the cursor)", len(resumed), want, want)
			}
			for _, f := range files {
				if !covered[f] && seen[f] != 1 {
					t.Errorf("%s processed %d times by the resumed leg, want exactly 1", f, seen[f])
				}
			}
			if !doneFlagSet(store, p.key) {
				t.Error("the completed resumed walk did not set the done flag")
			}
		})
	}
}

// The done flag is gated on "no protected file was skipped". That count is part
// of the checkpoint: a protected file skipped by attempt 1, below the cursor, must
// still stop attempt 2 from marking the pass done.
func TestMalformedPasses_ProtectedSkipBeforeCursorStillWithholdsDoneFlag(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available, skipping")
	}
	for _, p := range resumePasses {
		t.Run(p.name, func(t *testing.T) {
			_, files := resumeFixture(t, 12)
			store := &MockStore{}
			resume := walkresume.State{Cursor: files[len(files)-1], SkippedProtected: 1}
			// The cursor is past every file: nothing is left to walk, and the
			// carried skip alone must keep the flag unset.
			if err := p.run(context.Background(), store, &countingChecker{}, walkresume.WithRun(walkresume.Run{Resume: resume})); err != nil {
				t.Fatal(err)
			}
			if doneFlagSet(store, p.key) {
				t.Error("done flag set although an earlier attempt skipped a protected file")
			}
		})
	}
}

// A run told to yield stops after a checkpoint with walkresume.ErrYield, leaves
// the done flag unset, and a resume from that checkpoint finishes the walk.
func TestMalformedPasses_YieldStopsAfterCheckpointAndResumeFinishes(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available, skipping")
	}
	for _, p := range resumePasses {
		t.Run(p.name, func(t *testing.T) {
			_, files := resumeFixture(t, 30)
			store := &MockStore{}
			var states []walkresume.State
			first := &countingChecker{}
			run := walkresume.Run{
				Checkpoint: func(st walkresume.State) { states = append(states, st) },
				YieldDue:   func() bool { return len(states) >= 2 },
			}
			err := p.run(context.Background(), store, first, walkresume.WithRun(run))
			if !errors.Is(err, walkresume.ErrYield) {
				t.Fatalf("err = %v, want walkresume.ErrYield", err)
			}
			if doneFlagSet(store, p.key) {
				t.Fatal("a yielded walk set the done flag")
			}
			if got := len(first.seen()); got != 2*walkresume.CheckpointEvery {
				t.Errorf("yielded leg processed %d files, want %d (stop at the second checkpoint)", got, 2*walkresume.CheckpointEvery)
			}
			last := states[len(states)-1]
			second := &countingChecker{}
			if err := p.run(context.Background(), store, second, walkresume.WithRun(walkresume.Run{Resume: last})); err != nil {
				t.Fatal(err)
			}
			if got := len(first.seen()) + len(second.seen()); got != len(files) {
				t.Errorf("the two legs asked about %d files in total, want %d (nothing twice, nothing missed)", got, len(files))
			}
			if !doneFlagSet(store, p.key) {
				t.Error("the resumed walk did not set the done flag")
			}
		})
	}
}
