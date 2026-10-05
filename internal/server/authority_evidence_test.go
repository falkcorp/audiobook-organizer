// file: internal/server/authority_evidence_test.go
// version: 1.3.1
// guid: 4492804b-1186-4576-8833-2d1d7a405363
// last-edited: 2026-10-05

package server

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/authorcredit"
	"github.com/falkcorp/audiobook-organizer/internal/authority"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// waitAuthorityLoad waits for the in-flight snapshot load, if any.
func waitAuthorityLoad(t *testing.T, a *authorityEvidence) {
	t.Helper()
	a.mu.Lock()
	done := a.loadDone
	a.mu.Unlock()
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("authority snapshot load did not finish")
	}
}

// The production store (Server.store, the indexedStore decorator) offers
// authorcredit.AuthoritySource. With authority_evidence_enabled off it
// answers authority.Empty(); turned on (no restart) the first call starts a
// background load and answers Empty until it lands, then the snapshot. A
// credit resolved through it then puts a tier-O author named
// like a series first.
func TestServerStore_AuthorityEvidenceCapability(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()
	store := server.store
	src, ok := database.AsCapability[authorcredit.AuthoritySource](store)
	require.True(t, ok, "the server store must offer authorcredit.AuthoritySource")

	for _, n := range []string{"Michael Anderle", "Craig Martelle"} {
		_, err := store.CreateAuthor(n)
		require.NoError(t, err)
	}
	ma, err := store.GetAuthorByName("Michael Anderle")
	require.NoError(t, err)
	_, err = store.CreateSeries("Michael Anderle", &ma.ID)
	require.NoError(t, err)
	require.NoError(t, authority.PutPersonOverride(store, authority.PersonOverride{Name: "Michael Anderle",
		Roles: map[authority.Role]bool{authority.RoleAuthor: true}, SetAt: time.Now()}))
	authorcredit.ResetTitleCache()
	t.Cleanup(authorcredit.ResetTitleCache)

	ae := store.(*indexedStore).authority
	require.Same(t, server.authorityEvidence, ae)
	require.False(t, config.AuthorityEvidenceEnabled())
	require.False(t, src.AuthorityLookup().IsKnownPerson("Michael Anderle", authority.RoleAuthor), "flag off: Empty")
	got, err := authorcredit.Resolve(store, "Michael Anderle, Craig Martelle", authorcredit.PrepareGate)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "Craig Martelle", got[0].Name, "flag off: no evidence, the series-named part is dropped")
	// #3741 review N6: a flag-off resolve never starts a load.
	ae.mu.Lock()
	loading, done := ae.loading, ae.loadDone
	ae.mu.Unlock()
	require.False(t, loading, "flag off: no load started")
	require.Nil(t, done, "flag off: no load started")

	config.Mutate(func(c *config.Config) { c.AuthorityEvidenceEnabled = true })
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { c.AuthorityEvidenceEnabled = false }) })
	_ = src.AuthorityLookup() // starts the background load
	waitAuthorityLoad(t, ae)
	require.True(t, src.AuthorityLookup().IsKnownPerson("Michael Anderle", authority.RoleAuthor), "flag on: the snapshot")
	got, err = authorcredit.Resolve(store, "Michael Anderle, Craig Martelle", authorcredit.PrepareGate)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, []string{"Michael Anderle", "Craig Martelle"}, []string{got[0].Name, got[1].Name})

	// #3741 review N4: turning the flag off releases the snapshot.
	config.Mutate(func(c *config.Config) { c.AuthorityEvidenceEnabled = false })
	require.False(t, src.AuthorityLookup().IsKnownPerson("Michael Anderle", authority.RoleAuthor), "flag off again: Empty")
	ae.mu.Lock()
	snap := ae.snap
	ae.mu.Unlock()
	require.Nil(t, snap, "flag off: the snapshot is released")
}

// failingScanner fails every paged scan.
type failingScanner struct{}

func (failingScanner) GetRaw(string) ([]byte, error) { return nil, nil }
func (failingScanner) ScanPrefixPage(string, string, int) ([]database.KVPair, string, error) {
	return nil, "", errors.New("scan down")
}

// #3741 review S2: the load runs through the spawn hook (the server passes
// bgWG.Go, so Shutdown waits for it), and once the server context is
// cancelled no new load starts.
func TestAuthorityEvidence_LoadRunsThroughSpawnAndStopsAtShutdown(t *testing.T) {
	inner, err := database.NewPebbleStoreInMemory(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = inner.Close() })
	var wg namedWaitGroup
	var names []string
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := newAuthorityEvidence(ctx, inner, func() bool { return true }, func(name string, fn func()) {
		names = append(names, name)
		wg.Go(name, fn)
	})
	a.Prime()
	wg.Wait()
	require.Equal(t, []string{"authority-evidence-load"}, names)
	a.mu.Lock()
	require.NotNil(t, a.snap)
	a.nextLoad = time.Time{} // due again
	a.mu.Unlock()
	cancel()
	_ = a.Lookup()
	require.Len(t, names, 1, "no load after the server context is cancelled")
}

// A failed load answers Empty (no snapshot yet) and backs off instead of
// retrying on every call.
func TestAuthorityEvidence_LoadFailureAnswersEmpty(t *testing.T) {
	a := newAuthorityEvidence(context.Background(), failingScanner{}, func() bool { return true }, nil)
	require.Nil(t, a.Lookup().Person("anyone"))
	waitAuthorityLoad(t, a)
	a.mu.Lock()
	next, snap := a.nextLoad, a.snap
	a.mu.Unlock()
	require.Nil(t, snap)
	require.True(t, next.After(time.Now()), "a failed load backs off")
	var nilA *authorityEvidence
	require.Nil(t, nilA.Lookup().Person("anyone"), "a nil source answers Empty")
}

// Await (a plan reading the lists): flag off answers Empty, not ready, and
// starts no load; flag on waits for the first load and answers the snapshot,
// ready; a failed load answers Empty, not ready; a cancelled caller context
// stops the wait.
func TestAuthorityEvidence_Await(t *testing.T) {
	inner, err := database.NewPebbleStoreInMemory(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = inner.Close() })
	require.NoError(t, authority.PutPersonOverride(inner, authority.PersonOverride{Name: "Zed Newperson",
		Roles: map[authority.Role]bool{authority.RoleAuthor: true}, SetAt: time.Now()}))

	var on bool
	var mu sync.Mutex
	enabled := func() bool { mu.Lock(); defer mu.Unlock(); return on }
	a := newAuthorityEvidence(context.Background(), inner, enabled, nil)
	l, ready := a.Await(context.Background())
	require.False(t, ready, "flag off")
	require.Nil(t, l.Person("Zed Newperson"))
	a.mu.Lock()
	require.Nil(t, a.loadDone, "flag off: no load started")
	a.mu.Unlock()

	mu.Lock()
	on = true
	mu.Unlock()
	l, ready = a.Await(context.Background())
	require.True(t, ready, "flag on: the first load is waited for")
	require.True(t, l.IsKnownPerson("Zed Newperson", authority.RoleAuthor))

	failed := newAuthorityEvidence(context.Background(), failingScanner{}, func() bool { return true }, nil)
	l, ready = failed.Await(context.Background())
	require.False(t, ready, "a failed load is not ready")
	require.Nil(t, l.Person("anyone"))

	// A load that never finishes: the caller's context ends the wait.
	stuck := newAuthorityEvidence(context.Background(), inner, func() bool { return true },
		func(string, func()) {}) // the spawn hook never runs the load
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, ready = stuck.Await(ctx)
	require.False(t, ready, "the caller's context bounds the wait")

	var nilA *authorityEvidence
	_, ready = nilA.Await(context.Background())
	require.False(t, ready)
}

// lockedBuffer is a bytes.Buffer safe for the load goroutine and the test to
// share.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// lines returns the captured lines holding substr.
func (b *lockedBuffer) lines(substr string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, l := range strings.Split(b.buf.String(), "\n") {
		if strings.Contains(l, substr) {
			out = append(out, l)
		}
	}
	return out
}

// captureInfoLogs swaps the default slog handler for one writing INFO+ into
// a locked buffer, restored at cleanup.
//
// It swaps slog.Default because there is no other seam: authorityEvidence
// logs through logger.New("authority"), which writes to slog.Default, and
// neither internal/logger nor this package has a shared capture helper. The
// nearest one, captureWarnLogs (vector_backend_warn_test.go), captures WARN
// only, which loses the Info lines asserted here, and writes to an unlocked
// bytes.Buffer, which races with the load goroutine under -race. The swap is
// process-global, so the tests using it must not call t.Parallel.
func captureInfoLogs(t *testing.T) *lockedBuffer {
	t.Helper()
	buf := &lockedBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// Await logs one line per call: a wait past awaitLogAfter that ends with a
// snapshot is one Info naming the wait and why it ended (the load finished);
// a wait that ends without one is one Warn naming why (the wait limit, or the
// caller's context), never an Info as well; a wait under awaitLogAfter logs
// nothing. awaitLogAfter (1s in production) and awaitMax are shortened so
// the test does not sleep a second; the code path is the same.
func TestAuthorityEvidence_AwaitLogsWhyTheWaitEnded(t *testing.T) {
	inner, err := database.NewPebbleStoreInMemory(t.TempDir())
	require.NoError(t, err)
	var loads sync.WaitGroup
	t.Cleanup(func() { loads.Wait(); _ = inner.Close() }) // loads end before the store closes
	logs := captureInfoLogs(t)
	const (
		waitInfo = "a plan waited"
		noSnap   = "enabled but no snapshot"
	)
	count := func() (info, warn []string) {
		for _, l := range logs.lines("authority evidence: ") {
			switch {
			case strings.Contains(l, waitInfo):
				require.Contains(t, l, "level=INFO", l)
				info = append(info, l)
			case strings.Contains(l, noSnap):
				require.Contains(t, l, "level=WARN", l)
				warn = append(warn, l)
			}
		}
		return info, warn
	}
	reset := func() {
		logs.mu.Lock()
		logs.buf.Reset()
		logs.mu.Unlock()
	}
	// delayed runs each load after d, tracked by loads.
	delayed := func(d time.Duration) func(string, func()) {
		return func(_ string, fn func()) {
			loads.Add(1)
			go func() {
				defer loads.Done()
				time.Sleep(d)
				fn()
			}()
		}
	}
	on := func() bool { return true }

	// Slow load, ready: one Info naming the finished load, no Warn.
	a := newAuthorityEvidence(context.Background(), inner, on, delayed(60*time.Millisecond))
	a.awaitLogAfter = 10 * time.Millisecond
	_, ready := a.Await(context.Background())
	require.True(t, ready)
	info, warn := count()
	require.Len(t, info, 1, "logs: %v", logs.lines(""))
	require.Contains(t, info[0], "the snapshot load finished")
	require.Empty(t, warn)

	// Fast load, ready: under awaitLogAfter, no wait line.
	reset()
	a = newAuthorityEvidence(context.Background(), inner, on, delayed(0))
	a.awaitLogAfter = time.Minute
	_, ready = a.Await(context.Background())
	require.True(t, ready)
	info, warn = count()
	require.Empty(t, info)
	require.Empty(t, warn)

	// A load that never ends, bounded by awaitMax: one Warn naming the
	// limit, and no Info for the same wait.
	reset()
	a = newAuthorityEvidence(context.Background(), inner, on, func(string, func()) {})
	a.awaitLogAfter, a.awaitMax = time.Millisecond, 30*time.Millisecond
	_, ready = a.Await(context.Background())
	require.False(t, ready)
	info, warn = count()
	require.Empty(t, info, "a wait that ends without a snapshot is not also logged at Info")
	require.Len(t, warn, 1, "logs: %v", logs.lines(""))
	require.Contains(t, warn[0], "wait limit")

	// The same, bounded by the caller's context: one Warn naming it.
	reset()
	a = newAuthorityEvidence(context.Background(), inner, on, func(string, func()) {})
	a.awaitLogAfter = time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, ready = a.Await(ctx)
	require.False(t, ready)
	info, warn = count()
	require.Empty(t, info)
	require.Len(t, warn, 1, "logs: %v", logs.lines(""))
	require.Contains(t, warn[0], "the caller's context ended first")
}
