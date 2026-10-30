// file: internal/pathutil/protected_test.go
// version: 1.2.0
// guid: 5c0d7e92-3b84-4f1a-9a6e-2d18c4b7e063
// last-edited: 2026-10-29

package pathutil

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// protectedFixture is a library with an iTunes folder ("Gotham Music") holding
// one file, and a library folder ("Arkham") that is not protected. The base is
// t.TempDir() unresolved on purpose: on macOS it lives under /var, a link to
// /private/var, so every resolved spelling differs from the spelled one and a
// matcher that compared the wrong pair would show it.
type protectedFixture struct {
	base      string
	itunes    string // the protected folder
	hush      string // a real file inside it
	arkham    string // an unprotected folder
	unrelated string // a real file inside it
}

func newProtectedFixture(t *testing.T) *protectedFixture {
	t.Helper()
	base := t.TempDir()
	f := &protectedFixture{
		base:   base,
		itunes: filepath.Join(base, "lib", "Gotham Music"),
		arkham: filepath.Join(base, "lib", "Arkham"),
	}
	f.hush = filepath.Join(f.itunes, "Hush", "hush.m4b")
	f.unrelated = filepath.Join(f.arkham, "Joker", "killing-joke.m4b")
	for _, p := range []string{f.hush, f.unrelated} {
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o644))
	}
	return f
}

func (f *protectedFixture) link(t *testing.T, target, name string) string {
	t.Helper()
	l := filepath.Join(f.base, "lib", name)
	require.NoError(t, os.Symlink(target, l))
	return l
}

func TestUnderProtectedPath_PlainSpellingIsProtected(t *testing.T) {
	f := newProtectedFixture(t)
	got, err := UnderProtectedPath(f.hush, []string{f.itunes})
	require.NoError(t, err)
	assert.True(t, got)

	got, err = UnderProtectedPath(f.itunes, []string{f.itunes})
	require.NoError(t, err)
	assert.True(t, got, "the folder itself")

	// A file that does not exist yet under the folder.
	got, err = UnderProtectedPath(filepath.Join(f.itunes, "Hush", "new.m4b"), []string{f.itunes})
	require.NoError(t, err)
	assert.True(t, got)
}

func TestUnderProtectedPath_UnprotectedPathStaysUnprotected(t *testing.T) {
	f := newProtectedFixture(t)
	// A link that points somewhere harmless must not be taken for protected.
	harmless := f.link(t, f.arkham, "harmless")
	// A sibling that shares the protected folder's name as a prefix.
	sibling := filepath.Join(f.base, "lib", "Gotham Music 2", "x.m4b")
	require.NoError(t, os.MkdirAll(filepath.Dir(sibling), 0o755))
	require.NoError(t, os.WriteFile(sibling, []byte("x"), 0o644))

	for name, p := range map[string]string{
		"real file":                   f.unrelated,
		"through a harmless link":     filepath.Join(harmless, "Joker", "killing-joke.m4b"),
		"missing under the library":   filepath.Join(f.arkham, "Joker", "missing.m4b"),
		"sibling folder, same prefix": sibling,
	} {
		t.Run(name, func(t *testing.T) {
			got, err := UnderProtectedPath(p, []string{f.itunes})
			require.NoError(t, err)
			assert.False(t, got)
		})
	}
	got, err := UnderProtectedPath(f.unrelated, nil)
	require.NoError(t, err)
	assert.False(t, got, "no roots at all")
}

func TestUnderProtectedPath_SymlinkedFileIntoProtectedFolder(t *testing.T) {
	f := newProtectedFixture(t)
	l := filepath.Join(f.arkham, "link.m4b")
	require.NoError(t, os.Symlink(f.hush, l))
	got, err := UnderProtectedPath(l, []string{f.itunes})
	require.NoError(t, err)
	assert.True(t, got)
}

func TestUnderProtectedPath_SymlinkedParentDirectory(t *testing.T) {
	f := newProtectedFixture(t)
	l := f.link(t, filepath.Join(f.itunes, "Hush"), "link")
	got, err := UnderProtectedPath(filepath.Join(l, "hush.m4b"), []string{f.itunes})
	require.NoError(t, err)
	assert.True(t, got)
}

func TestUnderProtectedPath_ProtectedFolderConfiguredThroughASymlink(t *testing.T) {
	f := newProtectedFixture(t)
	// The configured folder is the link; the file is spelled by its real path,
	// and by the link's.
	root := f.link(t, f.itunes, "music")
	for name, p := range map[string]string{
		"real spelling":   f.hush,
		"link spelling":   filepath.Join(root, "Hush", "hush.m4b"),
		"missing, real":   filepath.Join(f.itunes, "Hush", "new.m4b"),
		"missing, linked": filepath.Join(root, "Hush", "new.m4b"),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := UnderProtectedPath(p, []string{root})
			require.NoError(t, err)
			assert.True(t, got)
		})
	}
	got, err := UnderProtectedPath(f.unrelated, []string{root})
	require.NoError(t, err)
	assert.False(t, got, "control: the link root does not protect an unrelated folder")
}

func TestUnderProtectedPath_DanglingLink(t *testing.T) {
	f := newProtectedFixture(t)
	missing := filepath.Join(f.itunes, "Hush", "gone.m4b")
	abs := filepath.Join(f.arkham, "dangling-abs.m4b")
	require.NoError(t, os.Symlink(missing, abs))
	rel := filepath.Join(f.arkham, "dangling-rel.m4b")
	require.NoError(t, os.Symlink(filepath.Join("..", "Gotham Music", "Hush", "gone.m4b"), rel))
	harmless := filepath.Join(f.arkham, "dangling-harmless.m4b")
	require.NoError(t, os.Symlink(filepath.Join(f.arkham, "Joker", "gone.m4b"), harmless))

	for name, p := range map[string]string{"absolute target": abs, "relative target": rel} {
		t.Run(name, func(t *testing.T) {
			got, err := UnderProtectedPath(p, []string{f.itunes})
			require.NoError(t, err)
			assert.True(t, got)
		})
	}
	got, err := UnderProtectedPath(harmless, []string{f.itunes})
	require.NoError(t, err)
	assert.False(t, got, "a dangling link to an unprotected place is unprotected")
}

func TestUnderProtectedPath_MissingFinalElementUnderASymlinkedDirectory(t *testing.T) {
	f := newProtectedFixture(t)
	l := f.link(t, filepath.Join(f.itunes, "Hush"), "link")
	got, err := UnderProtectedPath(filepath.Join(l, "not-yet", "new.m4b"), []string{f.itunes})
	require.NoError(t, err)
	assert.True(t, got)

	harmless := f.link(t, filepath.Join(f.arkham, "Joker"), "harmless")
	got, err = UnderProtectedPath(filepath.Join(harmless, "not-yet", "new.m4b"), []string{f.itunes})
	require.NoError(t, err)
	assert.False(t, got)
}

func TestUnderProtectedPath_ITunesMediaAndFailedFoldersReachedThroughALink(t *testing.T) {
	f := newProtectedFixture(t)
	media := filepath.Join(f.arkham, "iTunes Media", "Audiobooks")
	failed := filepath.Join(f.arkham, ".failed", "Knightfall")
	for _, d := range []string{media, failed} {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}
	mediaLink := f.link(t, media, "media-link")
	failedLink := f.link(t, failed, "failed-link")

	got, err := UnderProtectedPath(filepath.Join(mediaLink, "hush.m4b"), nil)
	require.NoError(t, err)
	assert.True(t, got, "iTunes Media only in the resolved spelling")
	got, err = UnderProtectedPath(filepath.Join(failedLink, "knightfall.m4b"), nil)
	require.NoError(t, err)
	assert.True(t, got, ".failed only in the resolved spelling")

	// And in the spelled one, with no filesystem at all.
	got, err = UnderProtectedPath(filepath.Join(f.base, "nowhere", "iTunes Media", "x.m4b"), nil)
	require.NoError(t, err)
	assert.True(t, got)
	got, err = UnderProtectedPath(filepath.Join(f.base, "nowhere", ".failed", "x.m4b"), nil)
	require.NoError(t, err)
	assert.True(t, got)
}

// A path that cannot be resolved for a reason other than not-exist is protected,
// and the error says why. A link cycle is the portable way to get one (a
// permission error does not happen when the tests run as root).
func TestUnderProtectedPath_ResolutionErrorFailsClosed(t *testing.T) {
	f := newProtectedFixture(t)
	a := filepath.Join(f.arkham, "a")
	b := filepath.Join(f.arkham, "b")
	require.NoError(t, os.Symlink(b, a))
	require.NoError(t, os.Symlink(a, b))

	t.Run("the path", func(t *testing.T) {
		got, err := UnderProtectedPath(filepath.Join(a, "x.m4b"), []string{f.itunes})
		assert.True(t, got)
		assert.Error(t, err)
	})
	t.Run("a protected folder", func(t *testing.T) {
		got, err := UnderProtectedPath(f.unrelated, []string{a})
		assert.True(t, got)
		assert.Error(t, err)
	})
	t.Run("the plain spelling still wins without an error", func(t *testing.T) {
		// Under the protected folder as spelled, so nothing is resolved.
		loopRoot := filepath.Join(f.itunes, "loop")
		require.NoError(t, os.Symlink(loopRoot, loopRoot))
		got, err := UnderProtectedPath(filepath.Join(loopRoot, "x.m4b"), []string{f.itunes})
		assert.True(t, got)
		assert.NoError(t, err)
	})
}

func TestResolveExistingPrefix(t *testing.T) {
	f := newProtectedFixture(t)
	real, err := filepath.EvalSymlinks(f.itunes)
	require.NoError(t, err)

	l := f.link(t, f.itunes, "music")
	got, err := ResolveExistingPrefix(filepath.Join(l, "Hush", "new", "x.m4b"))
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(real, "Hush", "new", "x.m4b"), got)

	a := filepath.Join(f.arkham, "a")
	b := filepath.Join(f.arkham, "b")
	require.NoError(t, os.Symlink(b, a))
	require.NoError(t, os.Symlink(a, b))
	_, err = ResolveExistingPrefix(a)
	assert.Error(t, err)
}

// A link cycle is ELOOP, so a purge can tell a path that will never resolve from
// a filesystem that failed to answer. This also pins the message
// evalSymlinksCycle matches: if a Go release rewords it, this fails.
func TestResolveExistingPrefix_LinkCycleIsELOOP(t *testing.T) {
	f := newProtectedFixture(t)
	a := filepath.Join(f.arkham, "a")
	b := filepath.Join(f.arkham, "b")
	require.NoError(t, os.Symlink(b, a))
	require.NoError(t, os.Symlink(a, b))

	_, err := ResolveExistingPrefix(filepath.Join(a, "x.m4b"))
	require.Error(t, err)
	assert.ErrorIs(t, err, syscall.ELOOP)

	// c and d point at each other: a cycle, which filepath.EvalSymlinks itself
	// reports. (The hand-followed dangling-link bounds, maxSymlinkHops and
	// maxResolveDepth, are covered by TestResolveExistingPrefix_HopAndDepthLimitsAreELOOP.)
	c := filepath.Join(f.arkham, "c")
	d := filepath.Join(f.arkham, "d")
	require.NoError(t, os.Symlink("d", c))
	require.NoError(t, os.Symlink("c", d))
	_, err = ResolveExistingPrefix(c)
	require.Error(t, err)
	assert.ErrorIs(t, err, syscall.ELOOP)
}

// The hand-followed bounds report ELOOP too. EvalSymlinks stops at 255 links on
// its own, so on a real filesystem these bounds are only reached by lowering them.
func TestResolveExistingPrefix_HopAndDepthLimitsAreELOOP(t *testing.T) {
	f := newProtectedFixture(t)
	missing := filepath.Join(f.arkham, "nowhere", "gone.m4b")

	t.Run("hops", func(t *testing.T) {
		// l0 -> l1 -> l2 -> missing: each link is dangling, so each is one hop.
		l := []string{filepath.Join(f.arkham, "l0"), filepath.Join(f.arkham, "l1"), filepath.Join(f.arkham, "l2")}
		require.NoError(t, os.Symlink(l[1], l[0]))
		require.NoError(t, os.Symlink(l[2], l[1]))
		require.NoError(t, os.Symlink(missing, l[2]))

		got, err := ResolveExistingPrefix(l[0])
		require.NoError(t, err, "within the bound a dangling chain resolves")
		assert.Equal(t, filepath.Join(mustEval(t, f.arkham), "nowhere", "gone.m4b"), got)

		prev := maxSymlinkHops
		maxSymlinkHops = 1
		t.Cleanup(func() { maxSymlinkHops = prev })
		_, err = ResolveExistingPrefix(l[0])
		require.Error(t, err)
		assert.ErrorIs(t, err, syscall.ELOOP)
	})

	t.Run("depth", func(t *testing.T) {
		// A dangling link with a relative target resolves its folder first, one
		// level of nesting.
		rel := filepath.Join(f.arkham, "rel.m4b")
		require.NoError(t, os.Symlink(filepath.Join("nowhere", "gone.m4b"), rel))
		_, err := ResolveExistingPrefix(rel)
		require.NoError(t, err)

		prev := maxResolveDepth
		maxResolveDepth = 0
		t.Cleanup(func() { maxResolveDepth = prev })
		_, err = ResolveExistingPrefix(rel)
		require.Error(t, err)
		assert.ErrorIs(t, err, syscall.ELOOP)
	})
}

func mustEval(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	require.NoError(t, err)
	return r
}

// Only EvalSymlinks' own cycle error is a cycle. Any other error carries the
// path, and a folder called "too many links" must not turn it into ELOOP.
func TestResolveExistingPrefix_OnlyTheExactCycleMessageIsELOOP(t *testing.T) {
	assert.True(t, evalSymlinksCycle(errors.New("EvalSymlinks: too many links")))
	assert.False(t, evalSymlinksCycle(errors.New("lstat /lib/too many links/x: permission denied")))
	assert.False(t, evalSymlinksCycle(errors.New("EvalSymlinks: too many links: and more")))
	assert.False(t, evalSymlinksCycle(nil))

	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions, so the lstat cannot be made to fail")
	}
	f := newProtectedFixture(t)
	locked := filepath.Join(f.arkham, "too many links", "locked")
	require.NoError(t, os.MkdirAll(locked, 0o755))
	require.NoError(t, os.Chmod(locked, 0o000))
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	_, err := ResolveExistingPrefix(filepath.Join(locked, "x.m4b"))
	require.Error(t, err)
	assert.ErrorIs(t, err, fs.ErrPermission)
	assert.NotErrorIs(t, err, syscall.ELOOP, "a permission error under a folder named too many links is not a link cycle")
}

// withShortRootCache runs a test with the root cache TTL set to ttl and the cache
// empty, and restores both afterwards.
func withRootCacheTTL(t *testing.T, ttl time.Duration) {
	t.Helper()
	prev := protectedRootCacheTTL
	protectedRootCacheTTL = ttl
	reset := func() {
		protectedRootCacheMu.Lock()
		protectedRootCache = map[string]*protectedRoots{}
		protectedRootCacheMu.Unlock()
	}
	reset()
	t.Cleanup(func() { protectedRootCacheTTL = prev; reset() })
}

// The folders are resolved once per TTL, not once per call: a link retargeted
// inside the window is not seen until the window ends.
func TestUnderProtectedPath_ResolvedRootsAreCachedForTheTTL(t *testing.T) {
	withRootCacheTTL(t, time.Hour)
	f := newProtectedFixture(t)
	realA := f.itunes
	realB := filepath.Join(f.base, "lib", "Other Music")
	require.NoError(t, os.MkdirAll(filepath.Join(realB, "Hush"), 0o755))
	root := f.link(t, realA, "music")
	inB := filepath.Join(realB, "Hush", "hush.m4b")

	got, err := UnderProtectedPath(inB, []string{root})
	require.NoError(t, err)
	assert.False(t, got, "the link points at the other folder")

	require.NoError(t, os.Remove(root))
	require.NoError(t, os.Symlink(realB, root))
	got, err = UnderProtectedPath(inB, []string{root})
	require.NoError(t, err)
	assert.False(t, got, "inside the TTL the cached resolution is used")

	protectedRootCacheTTL = 0 // the window is over
	got, err = UnderProtectedPath(inB, []string{root})
	require.NoError(t, err)
	assert.True(t, got, "after the TTL the folder is resolved again")
}

// A folder that fails to resolve stays a refusal for the whole window (fail
// closed, cached as a failure), is reported once, not once per call, and is
// reported again if it recovers and fails again.
func TestUnderProtectedPath_RootFailureIsCachedFailsClosedAndLoggedOnce(t *testing.T) {
	withRootCacheTTL(t, time.Hour)
	f := newProtectedFixture(t)
	a := filepath.Join(f.arkham, "loop-a")
	b := filepath.Join(f.arkham, "loop-b")
	require.NoError(t, os.Symlink(b, a))
	require.NoError(t, os.Symlink(a, b))

	logs := 0
	for range 50 {
		got, err := UnderProtectedPath(f.unrelated, []string{a})
		assert.True(t, got, "a folder that cannot be resolved protects")
		require.ErrorIs(t, err, ErrProtectedFolderUnresolved)
		assert.ErrorIs(t, err, syscall.ELOOP)
		if ShouldLogProtectedPathError(err) {
			logs++
		}
	}
	assert.Equal(t, 1, logs, "one report per folder, not one per call")

	// A failure about one path is never swallowed.
	assert.True(t, ShouldLogProtectedPathError(errors.New("resolve symlinks in /x: boom")))

	// The folder is fixed: the next window resolves it, and a later failure is
	// reported again.
	require.NoError(t, os.Remove(a))
	require.NoError(t, os.Remove(b))
	protectedRootCacheTTL = 0
	got, err := UnderProtectedPath(f.unrelated, []string{a})
	require.NoError(t, err)
	assert.False(t, got)

	protectedRootCacheTTL = time.Hour
	require.NoError(t, os.Symlink(b, a))
	require.NoError(t, os.Symlink(a, b))
	protectedRootCacheTTL = 0
	_, err = UnderProtectedPath(f.unrelated, []string{a})
	require.ErrorIs(t, err, ErrProtectedFolderUnresolved)
	assert.True(t, ShouldLogProtectedPathError(err), "reported again after it recovered")
}

// The callers run in worker pools. Many goroutines asking about the same and
// different folder lists while the cache entries expire under them: run with
// -race. Every answer must be the one a single caller gets.
func TestUnderProtectedPath_ConcurrentCallsAgree(t *testing.T) {
	withRootCacheTTL(t, time.Millisecond)
	f := newProtectedFixture(t)
	good := []string{f.itunes}
	linked := []string{f.link(t, f.itunes, "music")}
	a := filepath.Join(f.arkham, "loop-a")
	b := filepath.Join(f.arkham, "loop-b")
	require.NoError(t, os.Symlink(b, a))
	require.NoError(t, os.Symlink(a, b))
	bad := []string{a}
	viaLink := f.link(t, filepath.Join(f.itunes, "Hush"), "hush-link")

	var wg sync.WaitGroup
	const workers, calls = 16, 300
	// Sized for every call failing, so a broken implementation fails the test
	// instead of blocking the senders.
	errs := make(chan string, workers*calls)
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range calls {
				switch (w + i) % 4 {
				case 0:
					if got, err := UnderProtectedPath(f.hush, good); !got || err != nil {
						errs <- "plain: want protected without error"
					}
				case 1:
					if got, err := UnderProtectedPath(filepath.Join(viaLink, "hush.m4b"), linked); !got || err != nil {
						errs <- "linked: want protected without error"
					}
				case 2:
					if got, err := UnderProtectedPath(f.unrelated, good); got || err != nil {
						errs <- "unrelated: want unprotected without error"
					}
				default:
					got, err := UnderProtectedPath(f.unrelated, bad)
					if !got || !errors.Is(err, ErrProtectedFolderUnresolved) {
						errs <- "bad folder: want protected with a folder error"
					}
					_ = ShouldLogProtectedPathError(err)
				}
				if i%50 == 0 {
					time.Sleep(2 * time.Millisecond) // let entries expire mid-run
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

// withFailureLog resets the report throttle and drives its clock and interval,
// restoring everything afterwards. The returned func moves the clock.
func withFailureLog(t *testing.T, interval time.Duration) (advance func(time.Duration)) {
	t.Helper()
	prevClock, prevInterval := clock, rootFailureRelogInterval
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	clock = func() time.Time { return now }
	rootFailureRelogInterval = interval
	reset := func() {
		reportedRootFailuresMu.Lock()
		reportedRootFailures = map[string]time.Time{}
		reportedRootFailuresMu.Unlock()
	}
	reset()
	t.Cleanup(func() { clock, rootFailureRelogInterval = prevClock, prevInterval; reset() })
	return func(d time.Duration) { now = now.Add(d) }
}

// A link re-pointed inside the cache window: the cached answer is the stale
// one, UnderProtectedPathUncached is the fresh one, and it keeps nothing.
func TestUnderProtectedPathUncached_SeesALinkRepointedInsideTheCacheWindow(t *testing.T) {
	withRootCacheTTL(t, time.Hour)
	f := newProtectedFixture(t)
	elsewhere := filepath.Join(f.base, "lib", "Other Music")
	require.NoError(t, os.MkdirAll(elsewhere, 0o755))
	root := f.link(t, elsewhere, "music") // not the folder holding hush yet

	got, err := UnderProtectedPath(f.hush, []string{root})
	require.NoError(t, err)
	require.False(t, got, "the link points away from the file")

	require.NoError(t, os.Remove(root))
	require.NoError(t, os.Symlink(f.itunes, root)) // now it points at the folder holding hush

	cached, err := UnderProtectedPath(f.hush, []string{root})
	require.NoError(t, err)
	assert.False(t, cached, "inside the TTL the cached resolution is stale")

	fresh, err := UnderProtectedPathUncached(f.hush, []string{root})
	require.NoError(t, err)
	assert.True(t, fresh, "a fresh look sees the link's new target")

	protectedRootCacheMu.Lock()
	defer protectedRootCacheMu.Unlock()
	assert.Len(t, protectedRootCache, 1, "the uncached call left nothing in the cache (only the first call's entry)")
}

// The uncached check fails closed on a folder that cannot be resolved, with the
// same error as the cached one.
func TestUnderProtectedPathUncached_FailsClosedOnAFolderThatCannotBeResolved(t *testing.T) {
	withFailureLog(t, time.Hour)
	f := newProtectedFixture(t)
	a := filepath.Join(f.arkham, "loop-a")
	b := filepath.Join(f.arkham, "loop-b")
	require.NoError(t, os.Symlink(b, a))
	require.NoError(t, os.Symlink(a, b))

	got, err := UnderProtectedPathUncached(f.unrelated, []string{a})
	assert.True(t, got)
	require.ErrorIs(t, err, ErrProtectedFolderUnresolved)
	assert.ErrorIs(t, err, syscall.ELOOP)
}

// A folder that keeps failing is reported again once the interval has passed,
// and only then.
func TestShouldLogProtectedPathError_RelogsAFolderThatKeepsFailing(t *testing.T) {
	advance := withFailureLog(t, time.Hour)
	err := &RootError{Root: "Gotham Music", Err: syscall.ELOOP}

	assert.True(t, ShouldLogProtectedPathError(err), "first report")
	assert.False(t, ShouldLogProtectedPathError(err))
	advance(59 * time.Minute)
	assert.False(t, ShouldLogProtectedPathError(err), "inside the interval")
	advance(2 * time.Minute)
	assert.True(t, ShouldLogProtectedPathError(err), "an hour on, still failing: reported again")
	assert.False(t, ShouldLogProtectedPathError(err), "and the interval starts over")

	other := &RootError{Root: "Arkham Music", Err: syscall.ELOOP}
	assert.True(t, ShouldLogProtectedPathError(other), "another folder has its own throttle")
}

// A root configured relative is reported under the spelling it was configured
// with, whichever step failed.
func TestUnderProtectedPath_RootErrorNamesTheRootAsConfigured(t *testing.T) {
	withRootCacheTTL(t, 0)
	withFailureLog(t, time.Hour)
	f := newProtectedFixture(t)
	require.NoError(t, os.Symlink("loop-b", filepath.Join(f.arkham, "loop-a")))
	require.NoError(t, os.Symlink("loop-a", filepath.Join(f.arkham, "loop-b")))
	t.Chdir(f.arkham)

	_, err := UnderProtectedPath(f.unrelated, []string{"loop-a"})
	var re *RootError
	require.ErrorAs(t, err, &re)
	assert.Equal(t, "loop-a", re.Root)
}

// A folder that cannot be made absolute is recorded under its configured
// spelling, and is cleared by the same spelling once it resolves, so a later
// failure is reported at once. Under the old code the resolve step cleared the
// absolute spelling, which the make-absolute failure never used.
func TestUnderProtectedPath_AbsoluteFailureIsClearedWhenTheFolderResolves(t *testing.T) {
	withRootCacheTTL(t, 0)
	withFailureLog(t, time.Hour)
	f := newProtectedFixture(t)
	t.Chdir(f.base)
	prev := absPath
	t.Cleanup(func() { absPath = prev })
	roots := []string{filepath.Join("lib", "Gotham Music")} // not an ancestor of f.unrelated, so the check reaches the resolve step

	absPath = func(string) (string, error) { return "", errors.New("getwd: the working directory is gone") }
	_, err := UnderProtectedPath(f.unrelated, roots)
	var re *RootError
	require.ErrorAs(t, err, &re)
	assert.Equal(t, roots[0], re.Root)
	assert.True(t, ShouldLogProtectedPathError(err), "first report")
	assert.False(t, ShouldLogProtectedPathError(err), "throttled")

	absPath = prev
	_, err = UnderProtectedPath(f.unrelated, roots)
	require.NoError(t, err, "the folder resolves now")

	absPath = func(string) (string, error) { return "", errors.New("getwd: the working directory is gone") }
	_, err = UnderProtectedPath(f.unrelated, roots)
	require.ErrorAs(t, err, &re)
	assert.True(t, ShouldLogProtectedPathError(err), "failed again after resolving: reported at once")
}
