// file: internal/plugins/metafetch/register.go
// version: 1.4.0
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
	"github.com/falkcorp/audiobook-organizer/internal/logger"
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
	var clearMarkers metafetch.ASINBackfillClearFunc
	if st, ok := serviceregistry.TryGet[markerStore](c, serviceregistry.KeyStore); ok && st != nil {
		clearMarkers = func(bookID string) { clearBackfillMarkers(st, bookID) }
	} else {
		registerLog.Warn("store cannot delete raw keys; a metadata apply will not clear asin-backfill no-match markers")
	}
	p.mfs.SetASINBackfillQueue(metafetch.NewASINBackfillQueue(p.enqueueBookBackfill, status, clearMarkers, 0))
	return nil
}

var registerLog = logger.New("metafetch.plugin")

// Shutdown reaches the queue only through Container.Stop's Stopper check.
var _ serviceregistry.Stopper = (*Plugin)(nil)

// Stop stops the backfill queue on container shutdown. It is a backstop:
// the server stops the queue itself before draining the op registry
// (Server.stopASINBackfillQueue), because Container.Stop runs after the
// registry is already shut down. Queue.Stop is idempotent, so the second
// call is a no-op. Safe on a nil plugin (Build returns a typed nil when deps
// are missing).
func (p *Plugin) Stop(_ context.Context) error {
	if p == nil || p.mfs == nil {
		return nil
	}
	p.mfs.ASINBackfillQueue().Stop()
	return nil
}

// markerStore reads and deletes the op's raw no-match markers.
type markerStore interface {
	GetRaw(key string) ([]byte, error)
	DeleteRaw(key string) error
}

// clearBackfillMarkers deletes the book's ASIN and ISBN no-match markers.
// Called when a metadata apply queues the book: its metadata just changed, so
// an older "Audible has no match" verdict no longer holds, and with the
// markers gone the scheduled full walk re-checks the book even if the
// in-memory queue is lost to a restart. Only a marker that exists (or could
// not be read) is deleted, since the delete is a synced write. A failure is logged; the marker then just
// waits out its retry window.
func clearBackfillMarkers(st markerStore, bookID string) {
	for _, key := range []string{asinMissKey(bookID), isbnMissKey(bookID)} {
		// A read error falls through to the delete: absent is not proven.
		if raw, err := st.GetRaw(key); err == nil && len(raw) == 0 {
			continue
		}
		if err := st.DeleteRaw(key); err != nil {
			registerLog.Warn("clearing asin-backfill marker %s failed: %s",
				logger.SanitizeLogValue(key), logger.SanitizeLogValue(err.Error()))
		}
	}
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
