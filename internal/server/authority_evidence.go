// file: internal/server/authority_evidence.go
// version: 1.3.1
// guid: 3c15fa27-276b-44c2-a6e0-a9c9604af632
// last-edited: 2026-10-05

package server

import (
	"context"
	"fmt"
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
// for a load, on top of the caller's own context. It is the default of
// authorityEvidence.awaitMax.
const authorityAwaitMax = 2 * time.Minute

// authorityAwaitLogAfter is how long Await waits before a wait that ends
// with a snapshot is logged (at Info, with its length and why it ended). It
// is the default of authorityEvidence.awaitLogAfter. A wait that ends
// without one is always logged, once, at Warn.
const authorityAwaitLogAfter = time.Second

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
	// awaitMax and awaitLogAfter are authorityAwaitMax and
	// authorityAwaitLogAfter; fields so a test can shorten them.
	awaitMax      time.Duration
	awaitLogAfter time.Duration

	mu       sync.Mutex
	snap     *authority.Snapshot
	nextLoad time.Time // zero: load on the first flag-on call
	loading  bool
	loadDone chan struct{} // closed when the in-flight load ends (tests)
}

func newAuthorityEvidence(ctx context.Context, kv authority.Scanner, enabled func() bool,
	spawn func(name string, fn func())) *authorityEvidence {
	return &authorityEvidence{ctx: ctx, kv: kv, enabled: enabled, spawn: spawn, ttl: authorityRefreshTTL,
		awaitMax: authorityAwaitMax, awaitLogAfter: authorityAwaitLogAfter}
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
// most awaitMax, and never past ctx). ready reports whether a real
// snapshot is returned. Turning authority_evidence_enabled on by PUT /config
// starts no load (Start's Prime ran with the flag off), and Lookup never
// blocks, so without this a plan run right after the flip read Empty for
// every name (prod 2026-10-05).
//
// Each Await logs at most one line. With the flag on and no snapshot
// afterwards, one Warn, carrying the wait's length and why it ended when it
// waited, so a plan without authority lines is never silent. With a snapshot
// after a wait longer than awaitLogAfter, one Info with the same two facts.
func (a *authorityEvidence) Await(ctx context.Context) (authority.Lookup, bool) {
	if a == nil || a.enabled == nil || !a.enabled() {
		return authority.Empty(), false
	}
	_ = a.Lookup() // starts the load when one is due
	a.mu.Lock()
	snap, loading, done := a.snap, a.loading, a.loadDone
	a.mu.Unlock()
	var waited time.Duration
	var ended awaitEnd
	if snap == nil && loading && done != nil {
		// Every repairs.plan op shares one ConcurrencyKey
		// (repairs.PlanOpID, internal/plugins/maintenance/repairs_ops.go),
		// so plans run one at a time: while this plan waits here (up to
		// awaitMax), other fixers' plans queued behind it wait too. The wait
		// is logged with its length and why it ended, so a slow plan queue
		// can be traced to the snapshot load.
		start := time.Now()
		wait := time.NewTimer(a.awaitMax)
		defer wait.Stop()
		select {
		case <-done:
			ended = awaitLoadEnded
		case <-ctx.Done():
			ended = awaitCtxDone
		case <-wait.C:
			ended = awaitTimedOut
		}
		// select picks at random among ready cases: a load that ended at the
		// same moment the caller gave up still counts as ended.
		if ended != awaitLoadEnded {
			select {
			case <-done:
				ended = awaitLoadEnded
			default:
			}
		}
		waited = time.Since(start)
	}
	l := a.Lookup()
	if s, ok := l.(*authority.Snapshot); ok && s != nil {
		if ended != awaitNone && waited > a.awaitLogAfter {
			logger.New("authority").Info("authority evidence: a plan waited %s for the authority snapshot load (%s; repairs.plan ops queued behind it waited too)",
				waited.Round(time.Millisecond), ended.reason(a.awaitMax))
		}
		return s, true
	}
	if ended == awaitNone {
		logger.New("authority").Warn("authority evidence: enabled but no snapshot is loaded and no load is in flight (the last load failed or was discarded, or the server is shutting down); this plan reads no authority lists")
	} else {
		logger.New("authority").Warn("authority evidence: enabled but no snapshot after waiting %s (%s); this plan reads no authority lists",
			waited.Round(time.Millisecond), ended.reason(a.awaitMax))
	}
	return authority.Empty(), false
}

// awaitEnd is why Await's wait for a load ended.
type awaitEnd int

const (
	awaitNone      awaitEnd = iota // no wait: a snapshot was held, or no load was in flight
	awaitLoadEnded                 // the load finished (with or without a snapshot)
	awaitCtxDone                   // the caller's context ended first
	awaitTimedOut                  // awaitMax elapsed first
)

func (e awaitEnd) reason(limit time.Duration) string {
	switch e {
	case awaitLoadEnded:
		return "the snapshot load finished"
	case awaitCtxDone:
		return "the caller's context ended first"
	case awaitTimedOut:
		return fmt.Sprintf("gave up after the %s wait limit", limit)
	}
	return "no wait"
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
