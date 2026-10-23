// file: internal/masterlist/collapse.go
// version: 1.0.0
// guid: c98332e8-00a2-48b9-b729-87f0dbd569f2
// last-edited: 2026-10-23

package masterlist

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Group is the input rows that name one book, joined before matching: the
// same book listed by Audible and by iTunes is one book, matched once.
type Group struct {
	// Members are the indexes (into the rows given to Collapse) of the rows
	// in the group, in input order. The first is the representative: the plan
	// row that carries the match; the others are duplicates of it.
	Members []int
	// Row is the members combined (see Combine).
	Row Row
}

// Collapse joins rows that share an ASIN, an ISBN-13 (an ISBN-10 counts as the
// ISBN-13 it converts to) or a (source, source id) pair, transitively
// (union-find), and combines each group into one Row. Rows that share only a
// folded title and author are NOT joined: that is how different editions of
// one title look. Groups come back in the input order of their first member.
func Collapse(rows []Row) []Group {
	parent := make([]int, len(rows))
	for i := range parent {
		parent[i] = i
	}
	find := func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		switch {
		case ra == rb:
		case ra < rb:
			parent[rb] = ra
		default:
			parent[ra] = rb
		}
	}
	owner := map[string]int{}
	for i := range rows {
		for _, k := range joinKeys(rows[i]) {
			if j, ok := owner[k]; ok {
				union(i, j)
			} else {
				owner[k] = i
			}
		}
	}
	byRoot := map[int][]int{}
	var order []int
	for i := range rows {
		root := find(i)
		if _, ok := byRoot[root]; !ok {
			order = append(order, root)
		}
		byRoot[root] = append(byRoot[root], i)
	}
	out := make([]Group, 0, len(order))
	for _, root := range order {
		members := byRoot[root]
		group := make([]Row, len(members))
		for k, idx := range members {
			group[k] = rows[idx]
		}
		out = append(out, Group{Members: members, Row: Combine(group)})
	}
	return out
}

// joinKeys are the identifiers two rows must not share without being one book.
func joinKeys(r Row) []string {
	var keys []string
	if r.ASIN != "" {
		keys = append(keys, "asin:"+r.ASIN)
	}
	if r.ISBN13 != "" {
		keys = append(keys, "isbn13:"+r.ISBN13)
	}
	if r.ISBN10 != "" {
		if d, ok := ISBN10To13(r.ISBN10); ok && d != r.ISBN13 {
			keys = append(keys, "isbn13:"+d)
		}
	}
	for _, s := range r.Sources {
		if s.SourceID != "" {
			keys = append(keys, "src:"+s.Source+":"+s.SourceID)
		}
	}
	return keys
}

// Combine merges rows that are one book into one Row, given in input order.
//
//   - Descriptive fields (title, credits, series, runtime) are the first
//     non-empty value in input order.
//   - Identifiers likewise; two different ASINs, or two different ISBN-13s,
//     in one group add a list_conflict issue (the join went through a third
//     identifier, so one of the rows is wrong).
//   - Every source is kept, as provenance.
//   - Status uses the sticky rules: finished beats everything (the earliest
//     finished_at is the finish); otherwise the latest last_listened_at
//     decides, and with no times the further-along status does; progress and
//     position are the maximum.
//   - Every member's issues are kept, each with its own line.
func Combine(rows []Row) Row {
	if len(rows) == 0 {
		return Row{}
	}
	out := Row{Line: rows[0].Line}
	asins, isbns := map[string]bool{}, map[string]bool{}
	for _, r := range rows {
		out.Line = min(out.Line, r.Line)
		setFirst(&out.Title, r.Title)
		setFirst(&out.Subtitle, r.Subtitle)
		setFirst(&out.Note, r.Note)
		setFirst(&out.ASIN, r.ASIN)
		setFirst(&out.ISBN10, r.ISBN10)
		setFirst(&out.ISBN13, r.ISBN13)
		if len(out.Authors) == 0 {
			out.Authors = slices.Clone(r.Authors)
		}
		if len(out.Narrators) == 0 {
			out.Narrators = slices.Clone(r.Narrators)
		}
		if len(out.Series) == 0 {
			out.Series = slices.Clone(r.Series)
		}
		if out.RuntimeMin == 0 {
			out.RuntimeMin = r.RuntimeMin
		}
		if r.ASIN != "" {
			asins[r.ASIN] = true
		}
		for _, k := range joinKeys(r) {
			if v, ok := strings.CutPrefix(k, "isbn13:"); ok {
				isbns[v] = true
			}
		}
		for _, s := range r.Sources {
			if !slices.Contains(out.Sources, s) {
				out.Sources = append(out.Sources, s)
			}
		}
		out.Issues = append(out.Issues, r.Issues...)
	}
	if len(asins) > 1 || len(isbns) > 1 {
		out.Issues = append(out.Issues, Issue{Code: IssueListConflict, Line: out.Line,
			Detail: fmt.Sprintf("rows of one group carry %d ASINs and %d ISBN-13s; one of them is wrong", len(asins), len(isbns))})
	}
	combineStatus(&out, rows)
	return out
}

func setFirst(dst *string, v string) {
	if *dst == "" {
		*dst = v
	}
}

// statusRank orders statuses when no time decides: further along is higher.
func statusRank(s string) int {
	switch s {
	case StatusFinished:
		return 4
	case StatusInProgress:
		return 3
	case StatusAbandoned:
		return 2
	case StatusUnstarted:
		return 1
	}
	return 0
}

// lastListened is the newest last_listened_at over the row's sources.
func lastListened(r Row) time.Time {
	var t time.Time
	for _, s := range r.Sources {
		if v, ok := ParseTime(s.LastListenedAt); ok && v.After(t) {
			t = v
		}
	}
	return t
}

func combineStatus(out *Row, rows []Row) {
	var winner *Row
	for i := range rows {
		r := &rows[i]
		out.ProgressPct = max(out.ProgressPct, r.ProgressPct)
		out.PositionSec = max(out.PositionSec, r.PositionSec)
		if r.Status == "" {
			continue
		}
		if winner == nil {
			winner = r
			continue
		}
		wf, rf := winner.Status == StatusFinished, r.Status == StatusFinished
		switch {
		case wf != rf:
			if rf {
				winner = r
			}
		case wf && rf:
			// Two finishes: the earliest finished_at is the finish.
			wt, wok := ParseTime(winner.FinishedAt)
			rt, rok := ParseTime(r.FinishedAt)
			if rok && (!wok || rt.Before(wt)) {
				winner = r
			}
		default:
			wt, rt := lastListened(*winner), lastListened(*r)
			if rt.After(wt) || (rt.Equal(wt) && statusRank(r.Status) > statusRank(winner.Status)) {
				winner = r
			}
		}
	}
	if winner == nil {
		return
	}
	out.Status = winner.Status
	if winner.Status == StatusFinished {
		out.FinishedAt = winner.FinishedAt
		if out.FinishedAt == "" {
			// A finished row with no finish time of its own may be dated by
			// another finished row of the group.
			for _, r := range rows {
				if r.Status == StatusFinished && r.FinishedAt != "" {
					out.FinishedAt = r.FinishedAt
					break
				}
			}
		}
	}
}
