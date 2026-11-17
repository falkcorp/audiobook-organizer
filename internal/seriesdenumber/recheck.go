// file: internal/seriesdenumber/recheck.go
// version: 1.0.0
// guid: 7d2e94b1-3a68-4c0f-b5d7-1e9a40c6f382
// last-edited: 2026-11-17

package seriesdenumber

import "fmt"

// Recheck applies the target-side rules of Decide to moves decided earlier,
// against what the target holds NOW. Decide ran on the target as the planner read
// it; an apply writes minutes later, and a book may have been moved into the
// target, restored from the trash into it, or linked to it by a row the read
// cannot list. A move whose position is held by anyone now, or into a target that
// holds books nobody can place (unseen), is held, never made.
//
// existing is every book the target holds now, live and trashed, with the
// positions they hold. unseen is the number of rows that reference the target and
// are in neither list. The moves of one book (its versions share Identity) are
// kept or held together.
func Recheck(baseName string, moves []Move, existing []Book, unseen int) (keep []Move, held []Held) {
	if unseen > 0 {
		for _, m := range moves {
			held = append(held, Held{
				BookID: m.BookID, FromSeriesID: m.FromSeriesID, Code: HoldTargetUnseen,
				Reason: fmt.Sprintf("the base %q now holds %d book(s) the run cannot see (not listed live and not in the trash), so it cannot know position %d is free", baseName, unseen, m.NewSeq),
			})
		}
		return nil, held
	}
	taken := map[int]int{} // position -> how many distinct books hold it
	seen := map[int]map[string]bool{}
	for _, b := range existing {
		if b.Seq == nil {
			continue
		}
		if seen[*b.Seq] == nil {
			seen[*b.Seq] = map[string]bool{}
		}
		if !seen[*b.Seq][b.identity()] {
			seen[*b.Seq][b.identity()] = true
			taken[*b.Seq]++
		}
	}
	for _, m := range moves {
		if n := taken[m.NewSeq]; n > 0 {
			held = append(held, Held{
				BookID: m.BookID, FromSeriesID: m.FromSeriesID, Code: HoldCollision,
				Reason: fmt.Sprintf("position %d of %q is held by %d book(s) now, since the plan was made", m.NewSeq, baseName, n),
			})
			continue
		}
		keep = append(keep, m)
	}
	return keep, held
}
