// file: internal/plugins/maintenance/merge_cleanup_chain.go
// version: 1.0.0
// guid: 0b5cec7d-63b6-41db-ac42-7e4f46aea8ed
// last-edited: 2026-11-18

package maintenance

import (
	"fmt"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// How the merge chain of a loser ends.
type mcChainEnd string

const (
	// mcEndLive: the chain ends at a book with no pointer that is not in the
	// trash.
	mcEndLive mcChainEnd = "live"
	// mcEndDead: it ends at a trashed book with no pointer, or at a book that
	// does not exist.
	mcEndDead mcChainEnd = "dead"
	// mcEndCycle: a book is reached twice.
	mcEndCycle mcChainEnd = "cycle"
	// mcEndOverCap: more than mcMaxHops targets and still no end.
	mcEndOverCap mcChainEnd = "over_cap"
)

// mcMaxHops is the cap of merge.resolveForwardPath (its `for range 10`), so
// this walker and the existing resolver agree on what is too long.
const mcMaxHops = 10

// A hop's state.
const (
	mcHopLive    = "live"    // no pointer, not in the trash
	mcHopMerged  = "merged"  // not in the trash, but itself merged into another book
	mcHopTrashed = "trashed" // in the trash (it may still carry a pointer)
	mcHopMissing = "missing" // no such book
)

// mcHop is one book on a chain after the loser.
type mcHop struct {
	ID    string
	State string
	// Book is nil for a missing hop.
	Book *database.Book
}

// mcChain is where a loser's merge pointer leads.
type mcChain struct {
	Hops []mcHop
	End  mcChainEnd
	// EndID is the live book the chain ends at (End == mcEndLive).
	EndID string
	// CycleMembers are the books on the cycle, in walk order from the first
	// book reached twice (End == mcEndCycle). The loser is among them only when
	// the cycle runs through it.
	CycleMembers []string
	// ThroughTrashed: a trashed book sits on the path to the live end (set only
	// when End is mcEndLive), which is then reached only by following the
	// pointer THROUGH the trash.
	ThroughTrashed bool
}

// HopIDs lists the ids of the hops, in order.
func (c mcChain) HopIDs() []string {
	out := make([]string, len(c.Hops))
	for i := range c.Hops {
		out[i] = c.Hops[i].ID
	}
	return out
}

// LastID is the id of the last hop read ("" for no hop).
func (c mcChain) LastID() string {
	if len(c.Hops) == 0 {
		return ""
	}
	return c.Hops[len(c.Hops)-1].ID
}

// mcWalkChain follows start's MergedIntoBookID pointer, hop by hop.
//
// It is its own walker because merge.ResolveMergeSurvivor answers a different
// question: it returns the first book that is not trashed, so for L -> B -> C
// with B merged but not trashed it reports B as the survivor, and a verdict
// made against B would compare the loser with another loser. This walker
// follows the POINTER only, through trashed hops (a trashed book keeps its
// pointer), and says how the chain ends: a live book, a dead end, a cycle, or
// past the cap. It does not follow an Audiobookshelf sync redirect (a
// different mechanism, read separately).
//
// read returns (nil, nil) for a book that does not exist; any other error is
// returned and never read as a dead end.
func mcWalkChain(start *database.Book, read func(id string) (*database.Book, error)) (mcChain, error) {
	var c mcChain
	if start == nil || start.MergedIntoBookID == nil || *start.MergedIntoBookID == "" {
		return c, fmt.Errorf("merge chain: the book has no merge pointer")
	}
	// seq[0] is the loser; position records where each id first appeared so a
	// repeat names exactly the cycle.
	seq := []string{start.ID}
	pos := map[string]int{start.ID: 0}
	next := *start.MergedIntoBookID
	for range mcMaxHops {
		if at, seen := pos[next]; seen {
			c.End = mcEndCycle
			c.CycleMembers = append([]string(nil), seq[at:]...)
			return c, nil
		}
		b, err := read(next)
		if err != nil {
			return c, fmt.Errorf("merge chain: read %s: %w", next, err)
		}
		pos[next] = len(seq)
		seq = append(seq, next)
		if b == nil {
			c.Hops = append(c.Hops, mcHop{ID: next, State: mcHopMissing})
			c.End = mcEndDead
			return c, nil
		}
		hasPtr := b.MergedIntoBookID != nil && *b.MergedIntoBookID != ""
		trashed := database.IsInTrash(b)
		h := mcHop{ID: next, Book: b}
		switch {
		case trashed:
			h.State = mcHopTrashed
		case hasPtr:
			h.State = mcHopMerged
		default:
			h.State = mcHopLive
		}
		c.Hops = append(c.Hops, h)
		if !hasPtr {
			if trashed {
				c.End = mcEndDead
				return c, nil
			}
			c.End, c.EndID = mcEndLive, next
			for _, earlier := range c.Hops[:len(c.Hops)-1] {
				if earlier.State == mcHopTrashed {
					c.ThroughTrashed = true
				}
			}
			return c, nil
		}
		next = *b.MergedIntoBookID
	}
	// Ten targets read and the last still points on. A pointer that loops
	// back into what was read is a cycle, not a long chain.
	if at, seen := pos[next]; seen {
		c.End = mcEndCycle
		c.CycleMembers = append([]string(nil), seq[at:]...)
		return c, nil
	}
	c.End = mcEndOverCap
	return c, nil
}
