// file: internal/operations/registry/register.go
// version: 1.5.2
// guid: c3d4e5f6-a7b8-9c0d-1e2f-3a4b5c6d7e8f
// last-edited: 2026-11-07

package registry

import (
	"context"
	"errors"
	"log/slog"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// RegistryWrapper wraps *Registry to adapt its Start(ctx) (void) method to
// the Starter interface's Start(ctx) error signature. NewService builds it;
// the server's container holds it as the "opregistry" Override.
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

// opRegistryStore is what the "opregistry" service needs from the store. Was
// database.Store (398 methods) until 2026-08-19, on a comment reading "resolve
// the wide database.Store so we get GetBookByID and all OpsV2Store methods from
// the same concrete *PebbleStore instance". The same-instance requirement is
// real and preserved -- this is still one resolution of KeyStore -- but it never
// needed the wide type to hold: an interface value carries its dynamic type
// either way.
//
// The ops v3 run state (05-PR4) is deliberately absent too: database.Store
// does not carry it, so this value -- the server's search-index decorator in
// production -- does not promote it. New resolves it from the same value as a
// capability, through the decorator's Unwrap (runStateV3).
//
// BookFiles is deliberately absent even though DepStore declares it:
// prodSchedulerStore supplies that method itself, and demanding it from the
// underlying store would be asking for a method *PebbleStore does not have.
type opRegistryStore interface {
	database.OpsV2Store // New, and 7 of the 8 DepStore/SchedulerStore methods

	// The one thing SchedulerStore needs that OpsV2Store does not carry.
	GetBookByID(id string) (*database.Book, error)
}

// prodSchedulerStore wraps opRegistryStore and adds the BookFiles method
// required by SchedulerStore (which embeds DepStore). BookFiles returns nil
// so AllFiles requirements are treated as unmet — a conservative stance that
// matches OpsV2DepAdapter. The dedup.check-book op only uses ReqFieldSet
// (not AllFiles), so this is correct, not a stub to remove later.
type prodSchedulerStore struct {
	opRegistryStore
	// full is the same registry value without the opRegistryStore narrowing, so
	// capability lookups (database.AsCapability) can walk past this wrapper.
	full database.Store
}

// Unwrap implements database.StoreUnwrapper: without it a capability lookup
// against the scheduler store would see only the narrow embed and report the
// backing Pebble store as unsupported (TODO.md L4703 / TASK-117).
func (p *prodSchedulerStore) Unwrap() database.Store {
	return p.full
}

// BookFiles satisfies DepStore.BookFiles. Returns nil so AllFiles requirements
// are treated as unmet when no per-file source is wired.
func (p *prodSchedulerStore) BookFiles(_ string) ([]string, error) {
	return nil, nil
}

// NewService builds the ops registry the server's service container holds
// under "opregistry": the registry over store with eight workers, publishing
// to hub, with the scan stand-down marker persisted when store can, the dep
// book store and the DepsScheduler wired, wrapped for the container's
// Start/Stop. hub is the container's serviceregistry.KeyOpHub value
// (NewEventHub).
//
// It replaces this file's init() registration (R1-15): the composition root
// constructs the registry explicitly and hands it to the container as an
// Override, so importing this package no longer registers anything. store is
// one value, so GetBookByID and the OpsV2Store methods come from the same
// concrete *PebbleStore instance.
func NewService(store database.Store, log *slog.Logger, hub *EventHub) (*RegistryWrapper, error) {
	if store == nil || hub == nil {
		return nil, errors.New("registry: NewService needs a store and an event hub")
	}
	if log == nil {
		log = slog.Default()
	}
	reg := New(store, log, 8, hub)

	// Persist the scan stand-down marker (reboot-safety). The concrete
	// store is *PebbleStore, which implements GetSetting/SetSetting; the
	// narrow opRegistryStore interface does not carry those methods, so
	// type-assert rather than widen it. Must be set BEFORE Start().
	if ss, ok := store.(standDownPersister); ok {
		reg.SetScanStandDownStore(ss)
	} else {
		log.Warn("registry: store does not implement SettingsStore; " +
			"scan stand-down marker will be in-memory only (NOT reboot-safe)")
	}

	// Wire the book store for dep evaluation (ReqFieldSet).
	// prodSchedulerStore adds BookFiles (nil shim).
	schedStore := &prodSchedulerStore{opRegistryStore: store, full: store}
	reg.SetDepBookStore(schedStore)

	// Wire the DepsScheduler so waiting_deps ops are re-evaluated after
	// op completions and on the periodic sweep tick.
	sched := NewDepsScheduler(reg, schedStore)
	reg.SetDepsScheduler(sched)

	return &RegistryWrapper{Registry: reg}, nil
}
