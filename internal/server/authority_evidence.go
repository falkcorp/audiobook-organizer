// file: internal/server/authority_evidence.go
// version: 1.0.0
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

// authorityEvidence hands authorcredit the authority lists (the
// AuthoritySource store capability, offered by indexedStore). It holds one
// in-memory authority.Snapshot, loaded in the background: a credit resolve
// never waits on, or triggers, a per-call store read. It answers
// authority.Empty() while the flag is off, before the first load finishes,
// and after a first load fails; that is exactly the behaviour with no
// authority lists at all. A failed reload keeps the previous snapshot.
type authorityEvidence struct {
	ctx     context.Context
	kv      authority.Scanner
	enabled func() bool
	ttl     time.Duration

	mu       sync.Mutex
	snap     *authority.Snapshot
	nextLoad time.Time // zero: load on the first flag-on call
	loading  bool
	loadDone chan struct{} // closed when the in-flight load ends (tests)
}

func newAuthorityEvidence(ctx context.Context, kv authority.Scanner, enabled func() bool) *authorityEvidence {
	return &authorityEvidence{ctx: ctx, kv: kv, enabled: enabled, ttl: authorityRefreshTTL}
}

// Lookup returns the current snapshot, or authority.Empty(). The flag is read
// on every call, so turning it on or off needs no restart. A due load starts
// in the background and never blocks the caller.
func (a *authorityEvidence) Lookup() authority.Lookup {
	if a == nil || a.enabled == nil || !a.enabled() {
		return authority.Empty()
	}
	a.mu.Lock()
	snap := a.snap
	if !a.loading && !time.Now().Before(a.nextLoad) {
		a.loading = true
		a.loadDone = make(chan struct{})
		go a.load(a.loadDone)
	}
	a.mu.Unlock()
	if snap == nil {
		return authority.Empty()
	}
	return snap
}

func (a *authorityEvidence) load(done chan struct{}) {
	defer close(done)
	start := time.Now()
	snap, err := authority.LoadSnapshot(a.ctx, a.kv)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.loading = false
	if err != nil {
		a.nextLoad = time.Now().Add(authorityRetryAfter)
		logger.New("authority").Warn("authority evidence: snapshot load failed, keeping the previous one: %v", err)
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

var _ authorcredit.AuthoritySource = (*indexedStore)(nil)
