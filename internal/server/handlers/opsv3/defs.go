// file: internal/server/handlers/opsv3/defs.go
// version: 1.3.0
// guid: a701dc58-da75-41f2-b165-03a957c6e540
// last-edited: 2026-10-18

package opsv3

import (
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/sync/singleflight"

	"github.com/falkcorp/audiobook-organizer/internal/httputil"
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
	// HasPreview says whether a "preview" run of a writing definition really
	// writes nothing; false means only mode "live" starts it (POST runs
	// answers 409 otherwise). It is the op's own declaration of how it
	// previews (OperationDef.Preview), never inferred. null for a read-only
	// definition, where the mode changes nothing.
	HasPreview *bool `json:"has_preview"`
	// PreviewKeys are every params key the mode is written into, read off
	// the params type the op decodes: dry_run and dryRun (true = preview),
	// apply and live (false = preview). An op that decodes two (apply and
	// dry_run) writes only when both say so, so both are written. null when
	// the definition has no preview or is read-only; [] when its preview is
	// LiveOneOf alone.
	PreviewKeys []string `json:"preview_keys"`
	// LiveOneOf are write switches of which a live run must set exactly one
	// true in params (itunes.clone-into-library: apply, rollback, repair). A
	// preview writes them all false. Omitted when the definition has none.
	LiveOneOf []string `json:"live_one_of,omitempty"`
}

// lastRunTTL is how long a definition's looked-up last run is served before
// it is looked up again.
const lastRunTTL = 5 * time.Second

// lastRunCache holds each definition's last finished run, as LastRunFinder
// found it, for lastRunTTL. The lock guards the map only; lookups run outside
// it, and concurrent requests for the same definition share one lookup.
type lastRunCache struct {
	mu   sync.Mutex
	byID map[string]lastRunEntry
	sf   singleflight.Group
}

type lastRunEntry struct {
	at time.Time
	lr *LastRunView // nil: the definition has no finished run
}

// lastRunOf is the definition's newest finished run (LastRunFinder), or nil
// when it has none or no finder is wired. A lookup that fails is not cached
// and reads as nil for this request.
func (h *Handler) lastRunOf(defID string) *LastRunView {
	if h.d.LastRuns == nil {
		return nil
	}
	now := h.d.Now()
	h.lastCache.mu.Lock()
	if e, ok := h.lastCache.byID[defID]; ok && now.Sub(e.at) < lastRunTTL {
		h.lastCache.mu.Unlock()
		return e.lr
	}
	h.lastCache.mu.Unlock()
	v, err, _ := h.lastCache.sf.Do(defID, func() (any, error) {
		rec, err := h.d.LastRuns.LatestSettledRunV3(defID)
		if err != nil {
			return nil, err
		}
		var lr *LastRunView
		if rec != nil {
			lr = &LastRunView{ID: rec.ID, State: string(rec.State), Outcome: outcomeOf(rec.State), FinishedAt: rec.CompletedAt}
		}
		h.lastCache.mu.Lock()
		if h.lastCache.byID == nil {
			h.lastCache.byID = map[string]lastRunEntry{}
		}
		h.lastCache.byID[defID] = lastRunEntry{at: now, lr: lr}
		h.lastCache.mu.Unlock()
		return lr, nil
	})
	if err != nil {
		return nil
	}
	lr, _ := v.(*LastRunView)
	return lr
}

func (h *Handler) defView(d DefInfo, scheds map[string]ScheduleInfo) DefView {
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
		LastRun:  h.lastRunOf(d.ID),
	}
	if len(d.ParamsSchema) > 0 {
		v.ParamsSchema = d.ParamsSchema
	}
	if d.Writer {
		hp := d.HasPreview()
		v.HasPreview = &hp
		if hp {
			v.PreviewKeys = nonNil(d.Preview.ModeKeys())
			v.LiveOneOf = d.Preview.LiveOneOf
		}
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
	scheds := h.schedules()
	items := make([]DefView, 0, len(defs))
	for _, d := range defs {
		if kind != "" && d.Kind != kind {
			continue
		}
		items = append(items, h.defView(d, scheds))
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
	c.JSON(http.StatusOK, h.defView(d, h.schedules()))
}
