// file: internal/server/candidate_refetch.go
// version: 1.1.0
// guid: 09c23622-3fae-4104-8edc-2fe8860f40fd
// last-edited: 2026-10-05

package server

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"golang.org/x/time/rate"

	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	maintenanceplugin "github.com/falkcorp/audiobook-organizer/internal/plugins/maintenance"
)

// This file implements maintenanceplugin.CandidateRefetcher for the
// lost-candidates fixer (maintenance.refetch-lost-candidates): books whose
// cached candidates the store deleted when an identifier was filled
// (2026-10-05) are refetched through the candidate-fetch op's own per-book
// path, so the result is exactly what that op would have cached.

// candidateRefetchGate is the shared provider gate of every refetch: one
// limiter per server, sized like the candidate-fetch op's (the sum of the
// enabled sources' budgets; each source's own token bucket still paces it,
// Audible's included).
type candidateRefetchGate struct {
	once sync.Once
	lim  *rate.Limiter
}

func (g *candidateRefetchGate) limiter() *rate.Limiter {
	g.once.Do(func() {
		budget := metafetch.EnabledSourcesBudget()
		g.lim = candidateFetchLimiter(budget.RPS, budget.Burst)
	})
	return g.lim
}

// LatestCandidateFetchOutcomes implements maintenanceplugin.CandidateRefetcher
// over the memoised metadata-results set.
func (s *Server) LatestCandidateFetchOutcomes() (map[string]maintenanceplugin.CandidateFetchOutcome, error) {
	latest, _, err := latestMetadataResultsByBookCached(s.Ops())
	if err != nil {
		return nil, err
	}
	out := make(map[string]maintenanceplugin.CandidateFetchOutcome, len(latest))
	for id, r := range latest {
		out[id] = maintenanceplugin.CandidateFetchOutcome{Status: r.Status, At: r.CreatedAt}
	}
	return out, nil
}

// LatestCandidateFetchOutcome implements maintenanceplugin.CandidateRefetcher
// for one book, from the same memoised set.
func (s *Server) LatestCandidateFetchOutcome(bookID string) (maintenanceplugin.CandidateFetchOutcome, bool, error) {
	latest, _, err := latestMetadataResultsByBookCached(s.Ops())
	if err != nil {
		return maintenanceplugin.CandidateFetchOutcome{}, false, err
	}
	r, ok := latest[bookID]
	if !ok {
		return maintenanceplugin.CandidateFetchOutcome{}, false, nil
	}
	return maintenanceplugin.CandidateFetchOutcome{Status: r.Status, At: r.CreatedAt}, true, nil
}

// errCandidateFetchInFlight refuses a refetch of a book another
// metadata.candidate-fetch run is fetching, or another refetch is: two fetches
// of one book race on its cache entry and pay the providers twice.
var errCandidateFetchInFlight = errors.New("the book is being fetched by a running metadata candidate fetch")

// bookFetchClaims is the per-book in-flight set of candidate fetches. The
// refetch used to check the running candidate-fetch ops' book lists and then
// fetch: an op started between the two, or a second refetch of the same book
// (two Repairs applies), fetched it at the same time. A claim is taken
// atomically under mu; the refetch refuses a held book (tryClaim) and the
// op's workers wait for it (claim). The zero value is ready to use.
type bookFetchClaims struct {
	mu   sync.Mutex
	held map[string]chan struct{}
}

// tryClaim claims bookID, or reports false when it is held.
func (c *bookFetchClaims) tryClaim(bookID string) (release func(), ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, busy := c.held[bookID]; busy {
		return nil, false
	}
	return c.takeLocked(bookID), true
}

// claim claims bookID, waiting while it is held; it fails only when ctx ends.
func (c *bookFetchClaims) claim(ctx context.Context, bookID string) (release func(), err error) {
	for {
		c.mu.Lock()
		done, busy := c.held[bookID]
		if !busy {
			release := c.takeLocked(bookID)
			c.mu.Unlock()
			return release, nil
		}
		c.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// takeLocked records the claim; c.mu is held. The release closes the claim's
// channel, waking every waiter, and is safe to call more than once.
func (c *bookFetchClaims) takeLocked(bookID string) func() {
	if c.held == nil {
		c.held = make(map[string]chan struct{})
	}
	done := make(chan struct{})
	c.held[bookID] = done
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			delete(c.held, bookID)
			c.mu.Unlock()
			close(done)
		})
	}
}

// RefetchMetadataCandidates implements maintenanceplugin.CandidateRefetcher:
// fetchCandidateForBook, unforced, with the server's shared gate. It writes
// the candidate cache only; nothing is applied.
func (s *Server) RefetchMetadataCandidates(ctx context.Context, bookID string) (maintenanceplugin.CandidateRefetchResult, error) {
	mfs := s.metadataFetchService
	if mfs == nil {
		return maintenanceplugin.CandidateRefetchResult{}, fmt.Errorf("metadata fetch service not initialized")
	}
	// The claim comes first and is atomic: a candidate-fetch op that starts
	// after the book-list check below waits on it in its worker, and a second
	// refetch of the book is refused here.
	release, ok := s.candidateFetchClaims.tryClaim(bookID)
	if !ok {
		return maintenanceplugin.CandidateRefetchResult{}, errCandidateFetchInFlight
	}
	defer release()
	// A queued or running op that lists the book will fetch it anyway (its
	// worker may not have reached it yet, so it holds no claim): leave it to
	// that op rather than fetch it twice.
	if s.opRegistry != nil {
		active, err := metabatch.ActiveCandidateFetchBookIDs(s.Ops(), s.opRegistry.IsRunning)
		if err != nil {
			return maintenanceplugin.CandidateRefetchResult{}, fmt.Errorf("check running candidate fetches: %w", err)
		}
		if active[bookID] {
			return maintenanceplugin.CandidateRefetchResult{}, errCandidateFetchInFlight
		}
	}
	store := s.storeForWiring()
	r := s.fetchCandidateForBook(ctx, mfs, store, s.candidateRefetchGate.limiter(), "", bookID, false, s.newFolderMemo(store))
	out := maintenanceplugin.CandidateRefetchResult{Status: r.Status, Detail: r.Error}
	entry, err := store.GetMetadataCache(bookID)
	if err != nil {
		return out, fmt.Errorf("read the refetched cache row: %w", err)
	}
	if entry != nil {
		out.Candidates = len(entry.Candidates)
	}
	return out, nil
}
