// file: internal/operations/registry/live_tracker_test.go
// version: 1.0.0
// guid: 7a1c4e92-3b6f-4d58-a0e7-5c8d2f9b1a46
// last-edited: 2026-07-03

// live_tracker_test.go covers ShutdownAllForStore — the ordered-teardown seam
// internal/testutil's SetupIntegration cleanup uses to drain leaked op
// registries (Started, never Shutdown) before closing their PebbleStore.
package registry_test

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// TestShutdownAllForStore_DrainsLeakedRegistry simulates the exact
// internal/server leak: a registry Started against a real PebbleStore whose
// owning test never calls Shutdown. ShutdownAllForStore must find it (scoped
// by store identity), drain it, and leave nothing for a second call.
func TestShutdownAllForStore_DrainsLeakedRegistry(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	store, cleanup := openTestPebbleStore(t)
	defer cleanup()
	otherStore, otherCleanup := openTestPebbleStore(t)

	// Leaked registry on `store` — Started, never Shutdown by its "owner".
	leaked := registry.NewWithOptions(store, logger, 2, registry.Options{
		SweepInterval: 10 * time.Millisecond,
	})
	leaked.SetDepsScheduler(registry.NewDepsScheduler(leaked, &pebbleSchedulerStore{PebbleStore: store}))
	leaked.Start(context.Background())

	// A registry on a DIFFERENT store must NOT be drained (parallel tests each
	// own their own store).
	other := registry.NewWithOptions(otherStore, logger, 2, registry.Options{})
	other.Start(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if n := registry.ShutdownAllForStore(ctx, store); n != 1 {
		t.Fatalf("expected to drain exactly 1 leaked registry, drained %d", n)
	}
	// Idempotent: the drained registry deregistered itself.
	if n := registry.ShutdownAllForStore(ctx, store); n != 0 {
		t.Fatalf("expected 0 on second drain, got %d", n)
	}

	// The other store's registry is still live; its own drain finds it.
	if n := registry.ShutdownAllForStore(ctx, otherStore); n != 1 {
		t.Fatalf("expected to drain 1 registry on otherStore, drained %d", n)
	}
	otherCleanup()

	// A registry shut down by its owner is not double-drained.
	owned := registry.NewWithOptions(store, logger, 2, registry.Options{})
	owned.Start(context.Background())
	if err := owned.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if n := registry.ShutdownAllForStore(ctx, store); n != 0 {
		t.Fatalf("expected 0 after owner Shutdown, got %d", n)
	}
}
