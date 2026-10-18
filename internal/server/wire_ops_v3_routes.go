// file: internal/server/wire_ops_v3_routes.go
// version: 1.4.0
// guid: 3393014a-b734-46f4-9b8d-64fb73c1a547
// last-edited: 2026-10-18

package server

import (
	"encoding/json"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/scheduler"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/opsv3"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// wireOpsV3Routes registers /api/v3/ops/* on the protected /api/v3 group.
//
// Reads, cancel and approve are library.view at the route; the two start
// routes are scan.trigger (v1's POST /operations/v2 floor). Starting,
// cancelling and approving are then checked inside the handler against the
// definition's own permission (see opsv3). The handlers take narrow interfaces: the run reader
// is resolved from the store with AsCapability (it is not part of
// database.Store), the catalog and enqueuer are the registry, the schedules are
// the TaskScheduler's.
func (s *Server) wireOpsV3Routes(protected *gin.RouterGroup) {
	d := opsv3.Deps{Authz: s.opsV3Authz(), Closing: s.opsV3Closing}
	if runs, ok := database.AsCapability[opsv3.RunReader](s.storeForWiring()); ok {
		d.Runs = runs
	}
	// The per-definition last-run lookup is a PebbleStore method outside
	// database.Store; a store without it leaves /defs last_run null.
	if lr, ok := database.AsCapability[opsv3.LastRunFinder](s.storeForWiring()); ok {
		d.LastRuns = lr
	}
	if s.opRegistry != nil {
		d.Defs = &opsV3Defs{reg: s.opRegistry, warm: warmupSource(s.storeForWiring())}
		d.Enqueuer = s.opRegistry
		d.Zombies = s.opRegistry
	}
	// Lazy: s.scheduler is assigned in Start(), after setupRoutes has run
	// this, so a value captured here would always be nil and every def would
	// read manual (the operations handler's provider in wire_handlers.go has
	// the same shape).
	d.Sched = opsV3Sched{ts: func() *scheduler.TaskScheduler { return s.scheduler }}
	// No Approver: nothing in the runner records an approval against a run yet
	// (05-PR9 report), so POST .../approve answers 501 after its checks.
	if d.Runs != nil && s.opHub != nil {
		d.Broker = opsv3.NewBroker(d.Runs, s.opHub, d.Zombies, opsv3.BrokerOptions{})
		// Started by its first subscriber, so a server (or a test server)
		// nobody streams from runs no loop.
		d.Broker.StartOnFirstSubscriber(s.bgCtx, func(fn func()) { s.bgWG.Go("ops-v3-events", fn) })
	}
	opsv3.New(d).Register(protected, s.perm)
}

// closeOpsV3Streams ends every open /api/v3/ops/events stream. Idempotent.
func (s *Server) closeOpsV3Streams() {
	if s.opsV3Closing == nil {
		return
	}
	s.closeOpsV3Once.Do(func() { close(s.opsV3Closing) })
}

// opsV3FirstRunKey caches opsV3Authz's first-run check on the request.
const opsV3FirstRunKey = "opsv3.first_run"

// opsV3Authz is the server's permission semantics (auth disabled and the
// first-run bypass hold every permission) as the handlers' two checks.
func (s *Server) opsV3Authz() opsv3.Authz {
	store := s.storeForWiring()
	// firstRun is "no users yet", read once per request: Holds runs for
	// every permission of a definition (and of the start floor), and
	// CountUsers is a store read each time.
	firstRun := func(c *gin.Context) bool {
		if v, ok := c.Get(opsV3FirstRunKey); ok {
			b, _ := v.(bool)
			return b
		}
		fr := false
		if store != nil {
			if n, err := store.CountUsers(); err == nil && n == 0 {
				fr = true
			}
		}
		c.Set(opsV3FirstRunKey, fr)
		return fr
	}
	holds := func(c *gin.Context, p auth.Permission) bool {
		if !config.AppConfig.EnableAuth {
			return true
		}
		if firstRun(c) {
			return true
		}
		return auth.Can(c.Request.Context(), p)
	}
	return opsv3.Authz{Require: s.permCheck(), Holds: holds}
}

// warmupSource is the store's warm-up signal, nil when it has none. store is
// taken as any: AsCapability needs nothing more, and naming database.Store
// here would add a reference to the full interface for no method call.
func warmupSource(store any) opsregistry.WarmupStatuser {
	w, _ := database.AsCapability[opsregistry.WarmupStatuser](store)
	return w
}

// opsV3Defs is the registry catalog as opsv3.Defs.
type opsV3Defs struct {
	reg  *opsregistry.Registry
	warm opsregistry.WarmupStatuser
}

func (d *opsV3Defs) info(def ops.Definition, od opsregistry.OperationDef) opsv3.DefInfo {
	compiled := ops.Compile(def)
	c := compiled.Common
	info := opsv3.DefInfo{
		ID: def.ID(), Title: c.Title, Help: c.Help, Kind: def.Kind().String(),
		Permissions: od.Permissions, ReadOnly: c.Effects.IsReadOnly(),
	}
	for _, r := range c.Effects.ReadSet() {
		info.Reads = append(info.Reads, string(r))
	}
	for _, r := range c.Effects.WriteSet() {
		info.Writes = append(info.Writes, string(r))
	}
	for _, r := range c.Effects.DeleteSet() {
		info.Deletes = append(info.Deletes, string(r))
	}
	if od.ParamsSchema != nil {
		info.ParamsSchema = json.RawMessage(*od.ParamsSchema)
	}
	if compiled.Kind == ops.KindAdapted {
		// FromV2 declares no effects, so the v2 op's own declarations say
		// whether it writes, and its own Preview declaration how it previews.
		_, forcedLive := writesWithoutPreview[od.ID]
		info.Writer = canWrite(od) || forcedLive
		if info.Writer && !forcedLive {
			info.Preview = adaptedPreview(od.Preview)
		}
	} else {
		// A native definition's mode is resolved by the runner from dry_run
		// (opmode.ParseDryRun, both spellings).
		info.Writer = c.Effects.IsWriter()
		if info.Writer {
			info.Preview = opsregistry.PreviewSpec{Params: opmode.DryRunParams{}}
		}
	}
	return info
}

// writesWithoutPreview lists adapted ops whose v2 declarations read as
// read-only (no write capability, no Writes) but which write something on
// every run and have no mode key to turn that off. v3 treats each as a writer
// with no preview: a start is refused (409) unless it says mode "live", and
// params are passed through untouched. Without this list v3 would run them in
// either mode, so a "preview" start would upsert candidate rows or spend a
// paid AI batch.
//
// This is a v3-only classification. canWrite is unchanged, so the ops get no
// write_op_modes.golden line and the dispatcher's write-set gate is not
// engaged for them. Whether each should instead declare what it writes, gain
// a real preview, or be accepted as a read is the owner's call
// (todo.d/2026-10-17-ops-v3-approve-and-schema-checker.md). An entry is
// refused by TestOpsV3_WritesWithoutPreviewAreLiveOnly once its op declares
// writes or a preview of its own, so the list cannot go stale silently.
var writesWithoutPreview = map[string]string{
	"acoustid.scan":               "upserts dedup candidate rows every run",
	"ai.author-scan":              "writes AI scan state and suggestions; paid OpenAI calls",
	"dedup.split-book-scan":       "saves split-book candidates every run",
	"diagnostics.ai-analyze":      "paid OpenAI batch on every run",
	"library.bulk-metadata-fetch": "writes metadata fetch caches every run",
}

// canWrite: declares a write capability, declares a resource it writes
// (OperationDef.Writes), or declares no capabilities at all (undeclared is
// not evidence of read-only). Writes counts on its own because an op can
// declare the rows it writes and still list only read capabilities, as
// maintenance.transcribe-book-intros did; capabilities alone made v3 treat it
// as read-only and ignore the mode.
func canWrite(d opsregistry.OperationDef) bool {
	if len(d.Capabilities) == 0 || len(d.Writes) > 0 {
		return true
	}
	for _, c := range d.Capabilities {
		switch c {
		case opsregistry.CapLibraryWrite, opsregistry.CapFilesWrite, opsregistry.CapDBMigrate:
			return true
		}
	}
	return false
}

// adaptedPreview is a v2 op's Preview declaration as v3 uses it. Nothing
// declared, or a declaration that does not check, is no preview: fail closed,
// so the op starts only in mode live with params untouched. ValidateOpDef
// refuses to register a declaration that does not check, so the second case
// is a guard, not a path.
func adaptedPreview(p opsregistry.PreviewSpec) opsregistry.PreviewSpec {
	if p.IsZero() || p.Check() != nil {
		return opsregistry.PreviewSpec{}
	}
	return p
}

func (d *opsV3Defs) ListDefs() []opsv3.DefInfo {
	cat := d.reg.Catalog()
	out := make([]opsv3.DefInfo, 0, len(cat))
	for _, def := range cat {
		od, ok := d.reg.Def(def.ID())
		if !ok {
			continue
		}
		out = append(out, d.info(def, od))
	}
	return out
}

func (d *opsV3Defs) GetDef(id string) (opsv3.DefInfo, bool) {
	od, ok := d.reg.Def(id)
	if !ok {
		return opsv3.DefInfo{}, false
	}
	for _, def := range d.reg.Catalog() {
		if def.ID() == od.ID {
			return d.info(def, od), true
		}
	}
	return opsv3.DefInfo{}, false
}

// Ready is D44: a definition that reads the store waits for the store's
// warm-up. A definition that declares it needs nothing (NoWarmupWait) is
// always ready.
func (d *opsV3Defs) Ready(id string) (bool, string) {
	od, ok := d.reg.Def(id)
	if !ok || od.NoWarmupWait || d.warm == nil {
		return true, ""
	}
	if _, done, _ := d.warm.WarmupStatus(); !done {
		return false, opsregistry.WarmupStatusMessage
	}
	return true, ""
}

// opsV3Sched is the TaskScheduler's schedules as opsv3.Sched. ts is resolved
// per request; before the scheduler exists there are no schedules.
type opsV3Sched struct {
	ts func() *scheduler.TaskScheduler
}

func (o opsV3Sched) Schedules() map[string]opsv3.ScheduleInfo {
	ts := o.ts()
	if ts == nil {
		return nil
	}
	sts := ts.ScheduleStatuses()
	out := make(map[string]opsv3.ScheduleInfo, len(sts))
	for _, st := range sts {
		out[st.DefID] = opsv3.ScheduleInfo{Kind: st.Kind, Active: st.Active, Live: st.Live, NextFire: st.NextFire}
	}
	return out
}
