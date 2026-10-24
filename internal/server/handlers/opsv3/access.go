// file: internal/server/handlers/opsv3/access.go
// version: 1.0.0
// guid: 2f6c1b9e-7a43-4d85-b0e1-93c5a8d47f20
// last-edited: 2026-10-24

package opsv3

import (
	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/opaccess"
)

// A Repairs run (repairs.plan, repairs.apply) names the fixer it belongs to, its
// state, who started it and its error, and a guest holds library.view. So every
// read of runs here asks Deps.RepairsAccess (opaccess.Repairs): a run the caller
// may not read is left out of lists, the timeline, the census and the event
// stream, and a read by id answers 403. Deps.RepairsAccess unset refuses every
// repairs run (fail closed); TestWireHandlers_OpsV3RepairsAccessIsInstalled pins
// the server's wiring.

// repairsDefs are the definitions whose runs the check applies to.
var repairsDefs = []string{repairs.PlanOpID, repairs.ApplyOpID}

// mayReadRun is the by-id check: false means the 403 is written.
func (h *Handler) mayReadRun(c *gin.Context, rec *database.RunRecord) bool {
	if h.d.RepairsAccess.For(c).MayReadRun(rec) {
		return true
	}
	httputil.RespondWithForbidden(c, "permission denied: this run belongs to a Repairs fixer you may not use")
	return false
}

// hiddenRuns is the repairs runs one request may not read, with what each
// census total must give up for them.
type hiddenRuns struct {
	ids     map[string]struct{}
	byState map[state.V3]int64
	byDef   map[string]int64
	total   int64
	runs    []hiddenRun
}

type hiddenRun struct {
	defID string
	state state.V3
}

func (hr *hiddenRuns) hides(id string) bool {
	if hr == nil {
		return false
	}
	_, ok := hr.ids[id]
	return ok
}

// matching is how many hidden runs satisfy a listing filter (either may be
// empty), the number to take off that listing's total.
func (hr *hiddenRuns) matching(defID string, st state.V3) int64 {
	if hr == nil {
		return 0
	}
	var n int64
	for _, r := range hr.runs {
		if (defID == "" || r.defID == defID) && (st == "" || r.state == st) {
			n++
		}
	}
	return n
}

// hiddenFor reads every repairs run (both definitions, through the store's
// by-definition index) and returns the ones the caller may not read. It is
// exhaustive on purpose: the census totals and a listing's total are exact, so
// a sampled answer would make them lie. An admin still has a non-empty set (a
// run that states no fixer is refused to everyone), so there is no shortcut for
// a privileged caller. onlyDef, when set, limits the read to that definition
// (nothing to hide for any other).
func (h *Handler) hiddenFor(c *gin.Context, rd RunReader, onlyDef string) (*hiddenRuns, error) {
	if onlyDef != "" && !opaccess.IsRepairsOp(onlyDef) {
		return nil, nil
	}
	reader := h.d.RepairsAccess.For(c)
	out := &hiddenRuns{ids: map[string]struct{}{}, byState: map[state.V3]int64{}, byDef: map[string]int64{}}
	for _, def := range repairsDefs {
		if onlyDef != "" && onlyDef != def {
			continue
		}
		cursor := ""
		for {
			page, err := rd.ListRunsV3(database.RunQuery{DefID: def, Cursor: cursor, Limit: MaxLimit})
			if err != nil {
				return nil, err
			}
			for i := range page.Runs {
				r := &page.Runs[i]
				if reader.MayReadRun(r) {
					continue
				}
				out.ids[r.ID] = struct{}{}
				out.byState[r.State]++
				out.byDef[r.DefID]++
				out.total++
				out.runs = append(out.runs, hiddenRun{defID: r.DefID, state: r.State})
			}
			if page.Next == "" {
				break
			}
			cursor = page.Next
		}
	}
	return out, nil
}

// visible drops the hidden runs of recs.
func (hr *hiddenRuns) visible(recs []database.RunRecord) []database.RunRecord {
	if hr == nil || len(hr.ids) == 0 {
		return recs
	}
	out := make([]database.RunRecord, 0, len(recs))
	for _, r := range recs {
		if !hr.hides(r.ID) {
			out = append(out, r)
		}
	}
	return out
}
