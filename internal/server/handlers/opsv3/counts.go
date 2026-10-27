// file: internal/server/handlers/opsv3/counts.go
// version: 1.2.0
// guid: 4029c887-479b-45cc-8f37-fc9839c4d0c9
// last-edited: 2026-10-25

package opsv3

import (
	"net/http"
	"sort"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
)

// CountGroup is one group of /counts. Href is the census list that returns
// exactly Count rows.
type CountGroup struct {
	Key   string `json:"key"`
	Count int64  `json:"count"`
	Href  string `json:"href"`
}

// Counts implements GET /api/v3/ops/counts?group_by=state|def|outcome. Counts
// are the store's maintained census (opv3:meta:counts), exact, and each group
// links to the listing that returns that many rows. Groups with no runs are
// left out. An outcome group is a settled state, so its href lists that state;
// runs still going or waiting on a person have no outcome and are in no group.
func (h *Handler) Counts(c *gin.Context) {
	rd, ok := h.runs(c)
	if !ok {
		return
	}
	by := c.DefaultQuery("group_by", "state")
	if by != "state" && by != "def" && by != "outcome" {
		httputil.RespondWithValidationError(c, "group_by", "must be state, def or outcome")
		return
	}
	oc, err := rd.OpCountsV3()
	if err != nil {
		httputil.InternalError(c, "counts", err)
		return
	}
	// The census holds every run; the caller's holds the runs they may read, so
	// each group gives up the hidden Repairs runs and a group that reaches zero is
	// left out. This keeps the invariant that a group's count is the length of its
	// href's list (ListRuns counts only the runs the caller may read).
	hidden, err := h.hiddenFor(c, rd)
	if err != nil {
		httputil.InternalError(c, "counts", err)
		return
	}
	if hidden != nil && hidden.total > 0 {
		// Copies: the store's maps are not ours to edit.
		byState := make(map[state.V3]int64, len(oc.ByState))
		for s, n := range oc.ByState {
			byState[s] = n - hidden.byState[s]
		}
		byDef := make(map[string]int64, len(oc.ByDef))
		for d, n := range oc.ByDef {
			byDef[d] = n - hidden.byDef[d]
		}
		oc.ByState, oc.ByDef = byState, byDef
	}
	groups := []CountGroup{}
	var total int64
	switch by {
	case "state":
		for _, s := range state.AllV3() {
			if n := oc.ByState[s]; n > 0 {
				groups = append(groups, CountGroup{Key: string(s), Count: n, Href: listHref("state", string(s))})
				total += n
			}
		}
	case "outcome":
		for _, s := range state.AllV3() {
			if outcomeOf(s) == "" {
				continue
			}
			if n := oc.ByState[s]; n > 0 {
				groups = append(groups, CountGroup{Key: outcomeOf(s), Count: n, Href: listHref("state", string(s))})
				total += n
			}
		}
	case "def":
		for def, n := range oc.ByDef {
			if n > 0 {
				groups = append(groups, CountGroup{Key: def, Count: n, Href: listHref("def_id", def)})
				total += n
			}
		}
		sort.Slice(groups, func(i, j int) bool { return groups[i].Key < groups[j].Key })
	}
	c.JSON(http.StatusOK, gin.H{"group_by": by, "groups": groups, "total": total})
}
