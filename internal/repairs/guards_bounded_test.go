// file: internal/repairs/guards_bounded_test.go
// version: 1.0.1
// guid: 6d3b8e51-2a07-4c94-b1f8-5e9a0c7d2314
// last-edited: 2026-10-22

package repairs

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// hangingFS makes every Lstat under a folder block until released, counting
// the calls that reach it.
type hangingFS struct {
	hang    string
	calls   atomic.Int64
	release chan struct{}
	once    sync.Once
}

func newHangingFS(t *testing.T, hang string) *hangingFS {
	h := &hangingFS{hang: hang, release: make(chan struct{})}
	t.Cleanup(h.free)
	return h
}

func (h *hangingFS) free() { h.once.Do(func() { close(h.release) }) }

func (h *hangingFS) lstat(p string) (os.FileInfo, error) {
	if strings.HasPrefix(p, h.hang) {
		h.calls.Add(1)
		<-h.release
		return nil, os.ErrNotExist
	}
	return os.Lstat(p)
}

func boundedResolver(h *hangingFS, bound time.Duration, maxAbandoned int64) *PathResolver {
	r := NewPathResolver()
	r.lstat = h.lstat
	r.bound = bound
	r.maxAbandoned = maxAbandoned
	return r
}

// A path on a hung mount is doubt, not an indefinite wait: the guard skips the
// row (SkipGuardUnreadable) and a path elsewhere still resolves.
func TestPathResolver_HungMountIsDoubtNotAWait(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "ok", "a.mp3")
	require.NoError(t, os.MkdirAll(filepath.Dir(good), 0o755))
	require.NoError(t, os.WriteFile(good, []byte("x"), 0o644))
	h := newHangingFS(t, filepath.Join(dir, "hung"))
	r := boundedResolver(h, 30*time.Millisecond, 8)

	start := time.Now()
	kind, why := GuardBookPathsWith(r, "b1", []string{filepath.Join(dir, "hung", "a.mp3")}, "")
	require.Equal(t, SkipGuardUnreadable, kind, why)
	require.Contains(t, why, "could not tell")
	require.Less(t, time.Since(start), 5*time.Second)

	kind, why = GuardBookPathsWith(r, "b2", []string{good}, "")
	require.Empty(t, kind, why)
}

// A folder that timed out once is not tried again: every other path in it is
// doubt at once, so one dead folder costs one blocked call, not one per file.
func TestPathResolver_TimedOutFolderIsRememberedAsDoubt(t *testing.T) {
	dir := t.TempDir()
	h := newHangingFS(t, filepath.Join(dir, "hung"))
	r := boundedResolver(h, 20*time.Millisecond, 8)
	for _, name := range []string{"a.mp3", "b.mp3", "c.mp3", "d.mp3"} {
		kind, _ := GuardBookPathsWith(r, "b", []string{filepath.Join(dir, "hung", name)}, "")
		require.Equal(t, SkipGuardUnreadable, kind, name)
	}
	require.EqualValues(t, 1, h.calls.Load(), "the folder's later files must not reach the filesystem")
	require.EqualValues(t, 1, r.abandoned.Load())
}

// Past the resolver's cap of still-hung calls it stops trying: many dead
// folders cost at most the cap of blocked threads, and every later path is
// doubt without a filesystem call.
func TestPathResolver_StopsTryingPastTheAbandonedCap(t *testing.T) {
	dir := t.TempDir()
	h := newHangingFS(t, filepath.Join(dir, "hung"))
	const limit = 3
	r := boundedResolver(h, 15*time.Millisecond, limit)
	for i := 0; i < limit+5; i++ {
		p := filepath.Join(dir, "hung", string(rune('a'+i)), "f.mp3")
		kind, _ := GuardBookPathsWith(r, "b", []string{p}, "")
		require.Equal(t, SkipGuardUnreadable, kind, p)
	}
	require.EqualValues(t, limit, h.calls.Load(), "calls past the cap must not be made")
	require.EqualValues(t, limit, r.abandoned.Load())

	// The abandoned calls return once the mount answers, and the gauge falls.
	before := resolverAbandoned.Load()
	h.free()
	require.Eventually(t, func() bool { return r.abandoned.Load() == 0 }, 5*time.Second, 5*time.Millisecond)
	require.Eventually(t, func() bool { return resolverAbandoned.Load() <= before-limit }, 5*time.Second, 5*time.Millisecond)
}

// The process-wide cap protects against runs that each stay under their own.
func TestPathResolver_StopsTryingPastTheProcessWideCap(t *testing.T) {
	dir := t.TempDir()
	h := newHangingFS(t, dir)
	r := boundedResolver(h, time.Second, 8)
	resolverAbandoned.Add(ResolverMaxAbandonedTotal)
	t.Cleanup(func() { resolverAbandoned.Add(-ResolverMaxAbandonedTotal) })
	kind, why := GuardBookPathsWith(r, "b", []string{filepath.Join(dir, "x", "f.mp3")}, "")
	require.Equal(t, SkipGuardUnreadable, kind, why)
	require.Zero(t, h.calls.Load())
}
