// file: internal/seriesdenumber/decide.go
// version: 1.1.0
// guid: 0b7e5d3a-6c14-4f82-a9d0-3e81c75b2f46
// last-edited: 2026-11-16

package seriesdenumber

import (
	"fmt"
	"sort"
)

// Book is one member of a series as the decision needs it.
type Book struct {
	ID string
	// Seq is the book's series_sequence now; nil when it has none.
	Seq *int
	// Identity says which books are versions of one book: the version group id
	// when the book has one, "" otherwise (the book then stands for itself). A
	// book and its alternate rips hold one position in a series, not several,
	// and move together.
	Identity string
}

func (b Book) identity() string {
	if b.Identity != "" {
		return "g:" + b.Identity
	}
	return "b:" + b.ID
}

// Source is one numbered series folding into a Group's target.
type Source struct {
	FromID   int
	FromName string
	// Position is the number the name carries.
	Position int
	// Books are the source's members, every version included.
	Books []Book
}

// Group is every numbered series that folds into one base series: the same
// base name under the same author. The caller resolves the target through the
// store, so Existing is what the target holds now.
type Group struct {
	// TargetID is the base series, 0 when it does not exist yet.
	TargetID int
	BaseName string
	AuthorID int
	// Existing are the target's current members. Empty when TargetID is 0.
	Existing []Book
	Sources  []Source
	// TargetIsSource is true when the target is itself a numbered series another
	// plan of the run folds elsewhere (a base name that resolves, ignoring case,
	// to a numbered series). Books moved into it would be read by that plan only
	// if it ran later, and stranded if it ran first, so nothing moves into it
	// this run.
	TargetIsSource bool
}

// Move is one book that may move into the target.
type Move struct {
	BookID       string
	FromSeriesID int
	// OldSeq is the book's position before; nil when it had none.
	OldSeq *int
	// NewSeq is the position the book holds afterwards.
	NewSeq int
	// SetSeq is whether the move writes the position: true only when the book
	// had none. A position the book already holds is left as it is.
	SetSeq bool
}

// HoldCode names why a book was not moved.
type HoldCode string

const (
	// HoldOwnPosition: the book has a position of its own that differs from the
	// one in its series' name. A position somebody set outranks one parsed from
	// a name, so the book stays.
	HoldOwnPosition HoldCode = "own_position_differs"
	// HoldCollision: more than one book would sit at the same position of the
	// target, counting books already there.
	HoldCollision HoldCode = "position_collision"
	// HoldChainedTarget: the base series is itself a numbered series this run
	// folds into another base. It is merged first on a later run, then these
	// books fold into whatever its name resolves to then.
	HoldChainedTarget HoldCode = "base_is_a_numbered_series"
)

// Held is a book the decision refused to move.
type Held struct {
	BookID       string
	FromSeriesID int
	Code         HoldCode
	Reason       string
}

// SourceResult is the decision for one Source, in the order of Group.Sources.
type SourceResult struct {
	FromID int
	Moves  []Move
	Held   []Held
}

// GroupResult is the decision for a Group.
type GroupResult struct {
	// CreateTarget is true only when the target does not exist and at least one
	// book will move into it. A base series is never created for a plan that
	// moves nothing.
	CreateTarget bool
	Sources      []SourceResult
}

// MovedBooks is the number of books the group moves.
func (r GroupResult) MovedBooks() int {
	n := 0
	for _, s := range r.Sources {
		n += len(s.Moves)
	}
	return n
}

// Decide chooses which books of a Group may move, and holds the rest with a
// reason. It never moves a book it cannot place:
//
//   - a book whose own position differs from the one in its series' name is
//     held;
//   - when more than one book would sit at one position of the target -- movers
//     from one source or several, or a mover and a book already there -- every
//     mover at that position is held and the book already there is untouched.
//
// Versions of one book count once and are decided together: if any version
// fails a check, all are held.
func Decide(g Group) GroupResult {
	res := GroupResult{Sources: make([]SourceResult, len(g.Sources))}

	if g.TargetIsSource {
		for si, s := range g.Sources {
			res.Sources[si].FromID = s.FromID
			for _, b := range s.Books {
				res.Sources[si].Held = append(res.Sources[si].Held, Held{
					BookID: b.ID, FromSeriesID: s.FromID, Code: HoldChainedTarget,
					Reason: fmt.Sprintf("the base %q is itself a numbered series this run merges elsewhere; merge it first, then run again", g.BaseName),
				})
			}
		}
		return res
	}

	// identities already at each position of the target.
	existingAt := map[int]map[string]bool{}
	for _, b := range g.Existing {
		if b.Seq == nil {
			continue
		}
		if existingAt[*b.Seq] == nil {
			existingAt[*b.Seq] = map[string]bool{}
		}
		existingAt[*b.Seq][b.identity()] = true
	}

	type mover struct {
		src int
		ver []Book
	}
	var passing []mover
	atPosition := map[int][]int{} // position -> indices into passing

	for si, s := range g.Sources {
		res.Sources[si].FromID = s.FromID
		if s.FromID == g.TargetID && g.TargetID != 0 {
			continue // already the canonical row
		}
		for _, ident := range groupByIdentity(s.Books) {
			bad := firstDifferingSeq(ident, s.Position)
			if bad != nil {
				for _, b := range ident {
					res.Sources[si].Held = append(res.Sources[si].Held, Held{
						BookID: b.ID, FromSeriesID: s.FromID, Code: HoldOwnPosition,
						Reason: fmt.Sprintf("the book is already at position %d, but its series name says %d", *bad, s.Position),
					})
				}
				continue
			}
			atPosition[s.Position] = append(atPosition[s.Position], len(passing))
			passing = append(passing, mover{src: si, ver: ident})
		}
	}

	positions := make([]int, 0, len(atPosition))
	for p := range atPosition {
		positions = append(positions, p)
	}
	sort.Ints(positions)

	for _, p := range positions {
		idx := atPosition[p]
		there := len(existingAt[p])
		if len(idx)+there > 1 {
			for _, i := range idx {
				m := passing[i]
				for _, b := range m.ver {
					res.Sources[m.src].Held = append(res.Sources[m.src].Held, Held{
						BookID: b.ID, FromSeriesID: g.Sources[m.src].FromID, Code: HoldCollision,
						Reason: fmt.Sprintf("%d books would share position %d of %q (%d already there, %d arriving)",
							len(idx)+there, p, g.BaseName, there, len(idx)),
					})
				}
			}
			continue
		}
		for _, i := range idx {
			m := passing[i]
			for _, b := range m.ver {
				res.Sources[m.src].Moves = append(res.Sources[m.src].Moves, Move{
					BookID: b.ID, FromSeriesID: g.Sources[m.src].FromID,
					OldSeq: copyInt(b.Seq), NewSeq: p, SetSeq: b.Seq == nil,
				})
			}
		}
	}

	res.CreateTarget = g.TargetID == 0 && res.MovedBooks() > 0
	return res
}

// groupByIdentity splits books into version sets, in order of first appearance.
func groupByIdentity(books []Book) [][]Book {
	var order []string
	byID := map[string][]Book{}
	for _, b := range books {
		k := b.identity()
		if _, seen := byID[k]; !seen {
			order = append(order, k)
		}
		byID[k] = append(byID[k], b)
	}
	out := make([][]Book, 0, len(order))
	for _, k := range order {
		out = append(out, byID[k])
	}
	return out
}

// firstDifferingSeq returns the position of the first version whose own
// position is set and is not want, or nil when every version has none or want.
func firstDifferingSeq(versions []Book, want int) *int {
	for _, b := range versions {
		if b.Seq != nil && *b.Seq != want {
			return b.Seq
		}
	}
	return nil
}

func copyInt(p *int) *int {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
