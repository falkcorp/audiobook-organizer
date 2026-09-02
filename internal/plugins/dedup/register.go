// file: internal/plugins/dedup/register.go
// version: 1.4.0
// last-edited: 2026-09-02

// Service registry registration for the dedup UOS plugin (W5/W7).
//
// Build returns the constructed *Plugin when all required services are
// available, or nil when any dep is unavailable (no API key → no dedup
// engine → no plugin).
//
// PostInit (W7) self-registers the plugin's op-defs against the
// container's opregistry, replacing the inline `Register(server.opRegistry)`
// call that used to live in NewServer.

package dedup

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/falkcorp/audiobook-organizer/internal/config"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

func init() {
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   "dedupplugin",
		Needs:  []string{serviceregistry.KeyStore, serviceregistry.KeyDedup, serviceregistry.KeyEmbeddingStore, serviceregistry.KeyConfigUpdate},
		Groups: []string{"plugins"},
		Build: func(c *serviceregistry.Container) (any, error) {
			engine, _ := serviceregistry.TryGet[*dedupengine.Engine](c, serviceregistry.KeyDedup)
			embStore, _ := serviceregistry.TryGet[*database.EmbeddingStore](c, serviceregistry.KeyEmbeddingStore)
			if engine == nil || embStore == nil {
				return (*Plugin)(nil), nil
			}
			store := serviceregistry.Get[pluginStore](c, serviceregistry.KeyStore)
			return New(engine, store, embStore), nil
		},
	})
}

// PostInit self-registers this plugin's op-defs against the container's
// opregistry. Called by Container.PostInit() after all services are built.
//
// Safe to call when the plugin is nil — early-returns without error.
func (p *Plugin) PostInit(ctx context.Context, c *serviceregistry.Container) error {
	if p == nil {
		return nil
	}
	// The dedup-score sink lives here, not in the dedup ServiceDef in
	// internal/server/registry_wire.go, because it needs BOTH the engine and
	// the ops registry: a PUT /api/v1/config that changes dedup.signals swaps
	// the ladder into the live engine and queues dedup.rescore to re-band the
	// stored rows (see rescore_op.go). It is installed unconditionally on the
	// registry being present, so a missing ops registry surfaces as a sink
	// error naming the manual remedy rather than as a silently un-re-banded
	// backlog.
	//
	// A MISSING update service is a hard error, not a warning. KeyConfigUpdate
	// is a declared Needs of this plugin's ServiceDef (see the Needs list
	// above), so its absence is a wiring bug, not a deployment shape. Warning
	// and continuing would leave a server that starts clean and looks healthy
	// while dedup.signals silently never reaches the live engine — the whole
	// feature disabled by one log line nobody reads. Fail the startup instead.
	updateSvc, ok := serviceregistry.TryGet[*config.UpdateService](c, serviceregistry.KeyConfigUpdate)
	if !ok || updateSvc == nil {
		return fmt.Errorf("dedup PostInit: the config update service (%s) is a declared Needs but was not in the container; dedup.signals changes would never reach the live engine", serviceregistry.KeyConfigUpdate)
	}
	updateSvc.SetDedupScoreConfigSink(p.dedupScoreSink)

	// Unlike the update service above, a missing ops registry stays non-fatal:
	// it is not a declared Needs, and the sink reports it per-request with the
	// manual remedy (rescore_op.go's nil-registry branch). But say plainly that
	// ZERO op defs were registered — including dedup.full-scan — rather than
	// logging "skipping op-def registration" and returning success.
	wrapper, ok := serviceregistry.TryGet[*opsregistry.RegistryWrapper](c, "opregistry")
	if !ok || wrapper == nil {
		slog.Warn("PostInit: no ops registry — the dedup plugin registered ZERO operations (dedup.full-scan and dedup.rescore are both unavailable); config PUTs will report the manual re-band remedy")
		return nil
	}
	return p.Register(wrapper.Registry)
}
