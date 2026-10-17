// file: internal/server/handlers/opsv3/defs.go
// version: 1.0.0
// guid: a701dc58-da75-41f2-b165-03a957c6e540
// last-edited: 2026-10-17

package opsv3

import (
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
)

// EffectsView is a definition's declared effects.
type EffectsView struct {
	ReadOnly bool     `json:"read_only"`
	Reads    []string `json:"reads"`
	Writes   []string `json:"writes"`
	Deletes  []string `json:"deletes"`
}

// ScheduleView is a definition's timer. kind "manual" means none.
type ScheduleView struct {
	Kind     string     `json:"kind"`
	Active   bool       `json:"active"`
	NextFire *time.Time `json:"next_fire"`
	Live     bool       `json:"live"`
}

// ReadyView is D44's readiness: whether a run could start now.
type ReadyView struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason"`
}

// LastRunView is the most recent finished run of a definition.
type LastRunView struct {
	ID         string     `json:"id"`
	State      string     `json:"state"`
	Outcome    string     `json:"outcome"`
	FinishedAt *time.Time `json:"finished_at"`
}

// DefView is one definition as GET /api/v3/ops/defs lists it.
type DefView struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Help       string `json:"help"`
	Kind       string `json:"kind"`
	Permission string `json:"permission"`
	// Permissions is the full AND set; Permission is its first entry.
	Permissions  []string        `json:"permissions"`
	Approver     string          `json:"approver"`
	Effects      EffectsView     `json:"effects"`
	Schedule     ScheduleView    `json:"schedule"`
	Ready        ReadyView       `json:"ready"`
	LastRun      *LastRunView    `json:"last_run"`
	ParamsSchema json.RawMessage `json:"params_schema,omitempty"`
}

// lastRunWindow and lastRunTTL bound the work of finding every definition's
// latest finished run: one pass over the last day's finished runs, kept for a
// few seconds, plus a lookup of the scheduler's recorded last run for a
// definition that did not run in the window.
const (
	lastRunWindow = 24 * time.Hour
	lastRunTTL    = 5 * time.Second
	lastRunScan   = 2000
)

type lastRunCache struct {
	mu   sync.Mutex
	at   time.Time
	byID map[string]LastRunView
}

// lastRuns returns the latest finished run per definition within the window.
func (h *Handler) lastRuns() map[string]LastRunView {
	h.lastCache.mu.Lock()
	defer h.lastCache.mu.Unlock()
	now := h.d.Now()
	if h.lastCache.byID != nil && now.Sub(h.lastCache.at) < lastRunTTL {
		return h.lastCache.byID
	}
	out := map[string]LastRunView{}
	if h.d.Runs != nil {
		recs, _, err := h.d.Runs.ListDoneRunsSinceV3(now.Add(-lastRunWindow), lastRunScan)
		if err == nil {
			// Oldest first, so a later run of the same definition replaces an
			// earlier one.
			for _, r := range recs {
				out[r.DefID] = LastRunView{ID: r.ID, State: string(r.State), Outcome: outcomeOf(r.State), FinishedAt: r.CompletedAt}
			}
		}
	}
	h.lastCache.byID, h.lastCache.at = out, now
	return out
}

// lastRunOf is the definition's last finished run: the window's, else the one
// its schedule state recorded.
func (h *Handler) lastRunOf(defID string, window map[string]LastRunView) *LastRunView {
	if lr, ok := window[defID]; ok {
		return &lr
	}
	if h.d.Runs == nil {
		return nil
	}
	st, err := h.d.Runs.GetSchedV3(defID)
	if err != nil || st == nil || st.LastRunID == "" {
		return nil
	}
	rec, err := h.d.Runs.GetRunV3(st.LastRunID)
	if err != nil || rec == nil || !state.ClassifyV3(rec.State).Settled {
		return nil
	}
	return &LastRunView{ID: rec.ID, State: string(rec.State), Outcome: outcomeOf(rec.State), FinishedAt: rec.CompletedAt}
}

func (h *Handler) defView(d DefInfo, scheds map[string]ScheduleInfo, window map[string]LastRunView) DefView {
	perms := requiredPerms(d, true)
	ps := append([]string(nil), perms...)
	approver := d.Approver
	if approver == "" {
		approver = "settings.manage"
	}
	v := DefView{
		ID: d.ID, Title: d.Title, Help: d.Help, Kind: d.Kind,
		Permission: ps[0], Permissions: ps, Approver: approver,
		Effects:  EffectsView{ReadOnly: d.ReadOnly, Reads: nonNil(d.Reads), Writes: nonNil(d.Writes), Deletes: nonNil(d.Deletes)},
		Schedule: ScheduleView{Kind: "manual"},
		LastRun:  h.lastRunOf(d.ID, window),
	}
	if len(d.ParamsSchema) > 0 {
		v.ParamsSchema = d.ParamsSchema
	}
	if s, ok := scheds[d.ID]; ok {
		v.Schedule = ScheduleView{Kind: s.Kind, Active: s.Active, NextFire: s.NextFire, Live: s.Live}
	}
	v.Ready.OK, v.Ready.Reason = true, ""
	if h.d.Defs != nil {
		v.Ready.OK, v.Ready.Reason = h.d.Defs.Ready(d.ID)
	}
	return v
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (h *Handler) schedules() map[string]ScheduleInfo {
	if h.d.Sched == nil {
		return nil
	}
	return h.d.Sched.Schedules()
}

// ListDefs implements GET /api/v3/ops/defs[?kind=]: every definition with its
// effects, schedule and next fire, readiness and last finished run.
func (h *Handler) ListDefs(c *gin.Context) {
	if h.d.Defs == nil {
		httputil.RespondWithServiceUnavailable(c, "operations registry not initialized")
		return
	}
	kind := c.Query("kind")
	defs := h.d.Defs.ListDefs()
	sort.Slice(defs, func(i, j int) bool { return defs[i].ID < defs[j].ID })
	scheds, window := h.schedules(), h.lastRuns()
	items := make([]DefView, 0, len(defs))
	for _, d := range defs {
		if kind != "" && d.Kind != kind {
			continue
		}
		items = append(items, h.defView(d, scheds, window))
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": len(items)})
}

// GetDef implements GET /api/v3/ops/defs/:id.
func (h *Handler) GetDef(c *gin.Context) {
	if h.d.Defs == nil {
		httputil.RespondWithServiceUnavailable(c, "operations registry not initialized")
		return
	}
	d, ok := h.d.Defs.GetDef(c.Param("id"))
	if !ok {
		httputil.RespondWithNotFound(c, "definition", c.Param("id"))
		return
	}
	c.JSON(http.StatusOK, h.defView(d, h.schedules(), h.lastRuns()))
}
