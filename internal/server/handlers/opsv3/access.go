// file: internal/server/handlers/opsv3/access.go
// version: 1.1.0
// guid: 2f6c1b9e-7a43-4d85-b0e1-93c5a8d47f20
// last-edited: 2026-10-25

package opsv3

import (
	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
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

// hiddenRuns counts the repairs runs one request may not read, per state and
// definition: what each census total must give up for them. It is a snapshot,
// used for the census only; a run listing leaves hidden runs out inside the
// store's own pass (RunQuery.Keep).
type hiddenRuns struct {
	byState map[state.V3]int64
	byDef   map[string]int64
	total   int64
}

// accessLog reports runs the access check had to skip.
var accessLog = logger.New("handlers.opsv3.access")

// hiddenIDs is the id of every run of the given definitions (the Repairs ones
// when defID is "" or names one, see runDefs) the caller may not read, from the
// store's by-definition index. A run listing hands it to RunQuery.SkipID so those
// runs are skipped before their records are read: a corrupt record of a hidden
// run then cannot fail a list the caller is allowed to see, and a run listing
// does not decode records only to refuse them. The set is a snapshot; the
// listing's own Keep still judges each record it reads, so a run created since
// is not served by mistake.
//
// A record that cannot be decoded cannot be judged (its fixer is unknown), so it
// is hidden, as a run that states no fixer is, and is logged once per pass.
func (h *Handler) hiddenIDs(reader *opaccess.Reader, rd RunReader, defID string) (map[string]struct{}, error) {
	hidden := map[string]struct{}{}
	var current string
	census := func(r *database.RunRecord) bool {
		if !reader.MayReadRun(r) {
			hidden[r.ID] = struct{}{}
		}
		return false
	}
	for _, def := range runDefs(defID) {
		current = def
		_, err := rd.ListRunsV3(database.RunQuery{
			DefID: def, Limit: 1, Keep: census,
			OnUndecodable: func(id string, err error) {
				hidden[id] = struct{}{}
				accessLog.Warn("run %s of %s cannot be read and is left out of listings: %v",
					logger.SanitizeLogValue(id), logger.SanitizeLogValue(current), err)
			},
		})
		if err != nil {
			return nil, err
		}
	}
	return hidden, nil
}

// runDefs is the Repairs definitions a listing for defID has to census: both
// when defID is empty, the one it names otherwise.
func runDefs(defID string) []string {
	if defID == "" {
		return repairsDefs
	}
	return []string{defID}
}

// hiddenFor counts every repairs run (both definitions, through the store's
// by-definition index) the caller may not read. It is exhaustive on purpose: the
// census totals are exact, so a sampled answer would make them lie. An admin
// still has a non-empty set (a run that states no fixer is refused to everyone),
// so there is no shortcut for a privileged caller.
//
// It is one pass per definition: the census rides RunQuery.Keep, which the store
// calls once per record and which here refuses every run, so no page is built and
// no record is read twice.
func (h *Handler) hiddenFor(c *gin.Context, rd RunReader) (*hiddenRuns, error) {
	reader := h.d.RepairsAccess.For(c)
	out := &hiddenRuns{byState: map[state.V3]int64{}, byDef: map[string]int64{}}
	census := func(r *database.RunRecord) bool {
		if !reader.MayReadRun(r) {
			out.byState[r.State]++
			out.byDef[r.DefID]++
			out.total++
		}
		return false
	}
	for _, def := range repairsDefs {
		if _, err := rd.ListRunsV3(database.RunQuery{DefID: def, Limit: 1, Keep: census}); err != nil {
			return nil, err
		}
	}
	return out, nil
}
