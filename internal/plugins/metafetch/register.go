// file: internal/plugins/metafetch/register.go
// version: 1.2.0
// guid: 7a2f9c1d-4e63-4b80-9c15-6d0e1f2a3b40
// last-edited: 2026-10-02

// Service registry registration for the metafetch UOS plugin (INIT-3-T1).
//
// Mirrors internal/plugins/dedup/register.go: an init() that self-registers a
// ServiceDef whose Build returns the constructed *Plugin (or a typed-nil when a
// required dependency is unavailable), plus a PostInit that registers the
// plugin's op-defs against the container's opregistry after all services exist.

package metafetch

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"

	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

func init() {
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   "metafetchplugin",
		Needs:  []string{serviceregistry.KeyStore, serviceregistry.KeyMetaFetch},
		Groups: []string{"plugins"},
		Build: func(c *serviceregistry.Container) (any, error) {
			store := serviceregistry.Get[pluginStore](c, serviceregistry.KeyStore)
			mfs, _ := serviceregistry.TryGet[*metafetch.Service](c, serviceregistry.KeyMetaFetch)
			if store == nil || mfs == nil {
				return (*Plugin)(nil), nil
			}
			return New(store, mfs), nil
		},
	})
}

// PostInit self-registers this plugin's op-defs against the container's
// opregistry. Called by Container.PostInit() after all services are built.
// Safe to call when the plugin is nil — early-returns without error.
func (p *Plugin) PostInit(ctx context.Context, c *serviceregistry.Container) error {
	if p == nil {
		return nil
	}
	wrapper, ok := serviceregistry.TryGet[*opsregistry.RegistryWrapper](c, "opregistry")
	if !ok || wrapper == nil {
		slog.Warn("PostInit opregistry not available, skipping op-def registration")
		return nil
	}
	if err := p.Register(wrapper.Registry); err != nil {
		return err
	}
	// The metadata apply's per-book identifier step hands books to this
	// plugin's op (Audible only) instead of searching other sources itself.
	var status metafetch.ASINBackfillStatusFunc
	if st, ok := serviceregistry.TryGet[opStatusReader](c, serviceregistry.KeyStore); ok && st != nil {
		status = func(opID string) (string, error) {
			row, err := st.GetOperationV2(opID)
			if err != nil || row == nil {
				return "", err
			}
			return row.Status, nil
		}
	}
	p.mfs.SetASINBackfillQueue(metafetch.NewASINBackfillQueue(p.enqueueBookBackfill, status, 0))
	return nil
}

// opStatusReader reads one operation's row, for the backfill queue's "is the
// last run still waiting" check.
type opStatusReader interface {
	GetOperationV2(id string) (*database.OperationV2Row, error)
}

// enqueueBookBackfill enqueues one live metafetch.asin-backfill run for
// bookIDs. retry_after_days=0: these books' metadata just changed, so an
// older no-match marker must not skip them.
func (p *Plugin) enqueueBookBackfill(ctx context.Context, bookIDs []string) (string, error) {
	if p.registry == nil {
		return "", fmt.Errorf("%s: plugin not registered", asinBackfillOpID)
	}
	zero := 0
	return p.registry.EnqueueOp(ctx, asinBackfillOpID, asinBackfillParams{
		DryRun: opmode.Live(), BookIDs: bookIDs, RetryAfterDays: &zero,
	})
}
