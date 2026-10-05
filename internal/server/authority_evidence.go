// file: internal/server/authority_evidence.go
// version: 1.2.0
// guid: 3c15fa27-276b-44c2-a6e0-a9c9604af632
// last-edited: 2026-10-05

package server

import (
	"context"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/authorcredit"
	"github.com/falkcorp/audiobook-organizer/internal/authority"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// authorityRefreshTTL is how long one authority Snapshot serves before a
// background reload, so an authority-build apply reaches credit resolution
// without a restart.
const authorityRefreshTTL = 10 * time.Minute

// authorityRetryAfter is how long a failed load waits before the next try.
const authorityRetryAfter = time.Minute

// authorityAwaitMax bounds how long Await (a plan reading the lists) waits
// for a load, on top of the caller's own context.
const authorityAwaitMax = 2 * time.Minute

// authorityEvidence hands authorcredit the authority lists (the
// AuthoritySource store capability, offered by indexedStore). It holds one
// in-memory authority.Snapshot, loaded in the background: a credit resolve
// never waits on, or triggers, a per-call store read. It answers
// authority.Empty() while the flag is off, before the first load finishes,
// and after a first load fails; that is exactly the behaviour with no
// authority lists at all. A failed reload keeps the previous snapshot.
// Turning the flag off releases the snapshot (and discards a load that
// finishes after it), so a later flag-on starts from a fresh load.
type authorityEvidence struct {
	ctx     context.Context
	kv      authority.Scanner
	enabled func() bool
	// spawn starts a load goroutine. The server passes bgWG.Go, so Shutdown
	// waits for an in-flight load before the store closes; nil uses a bare
	// goroutine (tests).
	spawn func(name string, fn func())
	ttl   time.Duration

	mu       sync.Mutex
	snap     *authority.Snapshot
	nextLoad time.Time // zero: load on the first flag-on call
	loading  bool
	loadDone chan struct{} // closed when the in-flight load ends (tests)
}

func newAuthorityEvidence(ctx context.Context, kv authority.Scanner, enabled func() bool,
	spawn func(name string, fn func())) *authorityEvidence {
	return &authorityEvidence{ctx: ctx, kv: kv, enabled: enabled, spawn: spawn, ttl: authorityRefreshTTL}
}

// Prime starts the first load when the flag is on (Server.Start), so the
// lists are ready before the first resolve asks. A no-op with the flag off.
func (a *authorityEvidence) Prime() { _ = a.Lookup() }

// Lookup returns the current snapshot, or authority.Empty(). The flag is read
// on every call, so turning it on or off needs no restart. A due load starts
// in the background and never blocks the caller. With the flag off it never
// starts a load and releases any snapshot it holds.
func (a *authorityEvidence) Lookup() authority.Lookup {
	if a == nil || a.enabled == nil {
		return authority.Empty()
	}
	if !a.enabled() {
		a.mu.Lock()
		if a.snap != nil {
			a.snap, a.nextLoad = nil, time.Time{}
		}
		a.mu.Unlock()
		return authority.Empty()
	}
	a.mu.Lock()
	snap := a.snap
	// No new load once the server is shutting down: bgWG may already be in
	// Wait.
	if !a.loading && !time.Now().Before(a.nextLoad) && a.ctx.Err() == nil {
		a.loading = true
		done := make(chan struct{})
		a.loadDone = done
		run := func() { a.load(done) }
		if a.spawn != nil {
			a.spawn("authority-evidence-load", run)
		} else {
			go run()
		}
	}
	a.mu.Unlock()
	if snap == nil {
		return authority.Empty()
	}
	return snap
}

// Await is Lookup for a caller that must not read an empty answer as "no
// list knows this name" (a repairs plan): it starts a due load like Lookup
// and, while no snapshot is held and a load is in flight, waits for it (at
// most authorityAwaitMax, and never past ctx). ready reports whether a real
// snapshot is returned. Turning authority_evidence_enabled on by PUT /config
// starts no load (Start's Prime ran with the flag off), and Lookup never
// blocks, so without this a plan run right after the flip read Empty for
// every name (prod 2026-10-05). The flag on with no snapshot afterwards is
// logged, so a plan without authority lines is never silent.
func (a *authorityEvidence) Await(ctx context.Context) (authority.Lookup, bool) {
	if a == nil || a.enabled == nil || !a.enabled() {
		return authority.Empty(), false
	}
	_ = a.Lookup() // starts the load when one is due
	a.mu.Lock()
	snap, loading, done := a.snap, a.loading, a.loadDone
	a.mu.Unlock()
	if snap == nil && loading && done != nil {
		wait := time.NewTimer(authorityAwaitMax)
		defer wait.Stop()
		select {
		case <-done:
		case <-ctx.Done():
		case <-wait.C:
		}
	}
	l := a.Lookup()
	if s, ok := l.(*authority.Snapshot); ok && s != nil {
		return s, true
	}
	logger.New("authority").Warn("authority evidence: enabled but no snapshot is loaded (load in flight, failed, or the caller gave up); this plan reads no authority lists")
	return authority.Empty(), false
}

func (a *authorityEvidence) load(done chan struct{}) {
	defer close(done)
	start := time.Now()
	logger.New("authority").Info("authority evidence: loading the authority snapshot")
	snap, err := authority.LoadSnapshot(a.ctx, a.kv)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.loading = false
	if err != nil {
		a.nextLoad = time.Now().Add(authorityRetryAfter)
		logger.New("authority").Warn("authority evidence: snapshot load failed, keeping the previous one: %v", err)
		return
	}
	if !a.enabled() {
		// Turned off while loading: do not hold a snapshot nobody reads.
		a.nextLoad = time.Time{}
		logger.New("authority").Info("authority evidence: authority_evidence_enabled turned off during the load; snapshot discarded")
		return
	}
	a.snap = snap
	a.nextLoad = time.Now().Add(a.ttl)
	logger.New("authority").Info("authority evidence: loaded %d persons, %d publishers in %s",
		snap.Persons(), snap.Publishers(), time.Since(start).Round(time.Millisecond))
}

// AuthorityLookup implements authorcredit.AuthoritySource for the server's
// one store, so every credit resolve (importer, scanner, iTunes, metafetch,
// metadata apply, the audiobooks service and the refetch job) sees the
// authority lists once authority_evidence_enabled is on.
func (s *indexedStore) AuthorityLookup() authority.Lookup {
	return s.authority.Lookup()
}

// AwaitAuthorityLookup implements authorcredit.AuthorityWaiter: the lists for
// a plan, waiting for a load in flight (authorityEvidence.Await).
func (s *indexedStore) AwaitAuthorityLookup(ctx context.Context) (authority.Lookup, bool) {
	return s.authority.Await(ctx)
}

var (
	_ authorcredit.AuthoritySource = (*indexedStore)(nil)
	_ authorcredit.AuthorityWaiter = (*indexedStore)(nil)
)
