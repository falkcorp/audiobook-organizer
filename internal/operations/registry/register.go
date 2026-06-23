// file: internal/operations/registry/register.go
// version: 1.1.1
// guid: c3d4e5f6-a7b8-9c0d-1e2f-3a4b5c6d7e8f
// last-edited: 2026-06-23

package registry

import (
	"context"
	"log/slog"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

// RegistryWrapper wraps *Registry to adapt its Start(ctx) (void) method to
// the Starter interface's Start(ctx) error signature. The wrapper is registered
// in the service registry and consumers can Get[*Registry] from the container.
type RegistryWrapper struct {
	*Registry
}

// Start satisfies serviceregistry.Starter by converting Registry.Start's void
// return to an error return.
func (w *RegistryWrapper) Start(ctx context.Context) error {
	w.Registry.Start(ctx)
	return nil
}

// Stop satisfies serviceregistry.Stopper by delegating to Registry.Shutdown.
func (w *RegistryWrapper) Stop(ctx context.Context) error {
	return w.Registry.Shutdown(ctx)
}

// prodSchedulerStore wraps database.Store and adds the BookFiles method
// required by SchedulerStore (which embeds DepStore). BookFiles returns nil
// so AllFiles requirements are treated as unmet — a conservative stance that
// matches OpsV2DepAdapter. The dedup.check-book op only uses ReqFieldSet
// (not AllFiles), so this is correct, not a stub to remove later.
type prodSchedulerStore struct {
	database.Store
}

// BookFiles satisfies DepStore.BookFiles. Returns nil so AllFiles requirements
// are treated as unmet when no per-file source is wired.
func (p *prodSchedulerStore) BookFiles(_ string) ([]string, error) {
	return nil, nil
}

func init() {
	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   serviceregistry.KeyOpHub,
		Needs:  []string{},
		Groups: []string{"scheduler"},
		Build: func(c *serviceregistry.Container) (any, error) {
			return NewEventHub(), nil
		},
	})

	serviceregistry.Register(serviceregistry.ServiceDef{
		Name:   "opregistry",
		Needs:  []string{serviceregistry.KeyStore, serviceregistry.KeyOpHub},
		Groups: []string{"scheduler"},
		Build: func(c *serviceregistry.Container) (any, error) {
			// Resolve the wide database.Store so we get GetBookByID and all
			// OpsV2Store methods from the same concrete *PebbleStore instance.
			store := serviceregistry.Get[database.Store](c, serviceregistry.KeyStore)
			hub := serviceregistry.Get[*EventHub](c, serviceregistry.KeyOpHub)
			reg := New(store, slog.Default(), 8, hub)

			// Wire the book store for dep evaluation (ReqFieldSet).
			// prodSchedulerStore wraps database.Store and adds BookFiles (nil shim).
			schedStore := &prodSchedulerStore{Store: store}
			reg.SetDepBookStore(schedStore)

			// Wire the DepsScheduler so waiting_deps ops are re-evaluated after
			// op completions and on the periodic sweep tick.
			sched := NewDepsScheduler(reg, schedStore)
			reg.SetDepsScheduler(sched)

			return &RegistryWrapper{Registry: reg}, nil
		},
	})
}
