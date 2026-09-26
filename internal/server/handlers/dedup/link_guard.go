// file: internal/server/handlers/dedup/link_guard.go
// version: 1.1.0
// guid: 6a7c81b3-f45d-4156-a464-2cae0ff48561
// last-edited: 2026-09-25

package deduphandler

import (
	"fmt"
	"sort"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
)

// linkGuard enforces the review-queue-only rule (dedup/automated_guard.go) on
// the endpoints that link a whole filtered set or a union-find cluster with no
// per-pair human look: bulk-link and link-series.
//
// Checking each candidate pair alone is not enough on those endpoints. Linking
// is transitive: bulk-link links (A,C) and then (B,C), and link-series unions
// A–C and B–C into one cluster, and either way A and B end up in one version
// group although the A–B pair itself was refused. So the guard also checks
// whole book SETS: a set is refused when any two of its members are at the
// same cleaned path or have a pending manual candidate between them.
//
// Scope: the guard sees the manual candidates pending when it was built and
// the books involved in this request. It does not see members a book's
// existing version group already has; linking into a group can still join a
// same-path twin that was grouped earlier.
//
// link-cluster, reject-cluster and remove-from-cluster do not use this guard:
// they act on a book-ID list a human selected on a cluster card, which is a
// human verdict, not an automated pass.
type linkGuard struct {
	es    EmbeddingStore
	store DedupStore
	books map[string]*database.Book
	// manualPairs holds every pending manual book candidate's pair, keyed
	// with the lower ID first.
	manualPairs map[[2]string]struct{}
}

func newLinkGuard(es EmbeddingStore, store DedupStore) (*linkGuard, error) {
	manual, _, err := es.ListCandidates(database.CandidateFilter{
		EntityType: "book",
		Status:     "pending",
		Source:     database.CandidateSourceManual,
		Limit:      100000,
	})
	if err != nil {
		return nil, fmt.Errorf("list manual candidates: %w", err)
	}
	g := &linkGuard{
		es:          es,
		store:       store,
		books:       make(map[string]*database.Book),
		manualPairs: make(map[[2]string]struct{}, len(manual)),
	}
	for _, c := range manual {
		g.manualPairs[pairKey(c.EntityAID, c.EntityBID)] = struct{}{}
	}
	return g, nil
}

func pairKey(a, b string) [2]string {
	if a > b {
		a, b = b, a
	}
	return [2]string{a, b}
}

// book returns the book, cached. A book that cannot be loaded is nil, which
// SamePathPair treats as "not same-path"; the merge itself will fail on it.
func (g *linkGuard) book(id string) *database.Book {
	if b, ok := g.books[id]; ok {
		return b
	}
	b, err := g.store.GetBookByID(id)
	if err != nil {
		b = nil
	}
	g.books[id] = b
	return b
}

// pairRefusal applies the shared guard to one candidate, using the row as
// listed. Call recheck right before the merge as well.
func (g *linkGuard) pairRefusal(c database.DedupCandidate) (string, bool) {
	return dedup.AutomatedResolutionRefusal(c, g.book(c.EntityAID), g.book(c.EntityBID))
}

// recheck re-reads candidate c right before its merge (see
// dedup.RecheckAutomatedMerge). The row must still carry the status it was
// listed under, which is the request's status filter: bulk-link accepts a
// status other than pending (the Embedding tab sends its status filter), so
// pinning the check to "pending" would refuse every such row.
func (g *linkGuard) recheck(c database.DedupCandidate) (string, bool) {
	return dedup.RecheckAutomatedMerge(g.es, c.ID, c.Status, g.book(c.EntityAID), g.book(c.EntityBID))
}

// setRefusal reports whether linking every book in ids together would join a
// review-queue-only pair.
func (g *linkGuard) setRefusal(ids []string) (string, bool) {
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			a, b := sorted[i], sorted[j]
			if _, ok := g.manualPairs[pairKey(a, b)]; ok {
				return fmt.Sprintf("manual: %s and %s are pinned for human review; linking this set would link them", a, b), true
			}
			if dedup.SamePathPair(g.book(a), g.book(b)) {
				return fmt.Sprintf("same_path: %s and %s share a cleaned path; linking this set would link them", a, b), true
			}
		}
	}
	return "", false
}

// linkComponents tracks which books one bulk request has already linked
// together, so each new link can be checked against everything it would join.
type linkComponents struct {
	parent  map[string]string
	members map[string][]string
}

func newLinkComponents() *linkComponents {
	return &linkComponents{parent: map[string]string{}, members: map[string][]string{}}
}

func (lc *linkComponents) find(x string) string {
	if _, ok := lc.parent[x]; !ok {
		lc.parent[x] = x
		lc.members[x] = []string{x}
		return x
	}
	for lc.parent[x] != x {
		lc.parent[x] = lc.parent[lc.parent[x]]
		x = lc.parent[x]
	}
	return x
}

// joined returns the members the two books' components would have together.
func (lc *linkComponents) joined(a, b string) []string {
	ra, rb := lc.find(a), lc.find(b)
	if ra == rb {
		return append([]string(nil), lc.members[ra]...)
	}
	out := make([]string, 0, len(lc.members[ra])+len(lc.members[rb]))
	out = append(out, lc.members[ra]...)
	return append(out, lc.members[rb]...)
}

func (lc *linkComponents) union(a, b string) {
	ra, rb := lc.find(a), lc.find(b)
	if ra == rb {
		return
	}
	lc.parent[ra] = rb
	lc.members[rb] = append(lc.members[rb], lc.members[ra]...)
	delete(lc.members, ra)
}
