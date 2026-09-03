// file: internal/server/provider_throttle_wire.go
// version: 1.0.0
// guid: c730dc9f-e858-44d0-b00e-4a9f1aa4d722
// last-edited: 2026-09-03

package server

import (
	"fmt"
	"log/slog"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// AttachProviderThrottleStore restores the metadata-provider throttles a previous
// process left behind, and makes new holds durable.
//
// 🔴 CALL THIS ONLY FROM A PROCESS-SCOPED ENTRY POINT (the serve path), NEVER
// FROM NewServer.
//
// The registry is process-global by design -- the user asked for one hold per
// provider that every operation respects -- so whatever store it is handed is
// held for the life of the PROCESS. NewServer is called once in production but
// roughly twenty times in `go test ./internal/server`, each with its own
// *PebbleStore that the test closes in t.Cleanup. Attaching there left the
// registry holding a closed store, and Pebble PANICS on use-after-close rather
// than returning an error: `panic: pebble: closed` out of SaveProviderThrottle,
// deterministically, in any full-package run. Caught by CI on PR #3066.
//
// Attaching here instead means the store's lifetime and the registry's agree.
// Tests get a store-less registry, which is the correct behaviour for them:
// throttles still work, they just do not persist.
//
// The capability assertion is deliberate: the three persistence methods live on
// *PebbleStore only, so nothing widens the 398-method database.Store or forces a
// mock regeneration. A store without them degrades to in-memory holds, loudly.
func AttachProviderThrottleStore(store database.Store) {
	ts, ok := store.(metadata.ThrottleStore)
	if !ok {
		slog.Warn("store does not persist provider throttles; a provider hold will be lost on restart",
			"store_type", fmt.Sprintf("%T", store))
		return
	}
	restored, err := metadata.DefaultThrottleRegistry().AttachStore(ts)
	if err != nil {
		// The store is attached anyway (AttachStore does that before it can
		// fail on the read), so new holds still persist. Say exactly what was
		// lost, because "could not load" reads like "persistence is off".
		slog.Error("provider throttles: could not restore previous holds; NEW holds will still be persisted", "err", err)
		return
	}
	if restored > 0 {
		slog.Info("restored provider throttles from a previous run", "count", restored)
	}
}
