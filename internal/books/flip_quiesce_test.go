// file: internal/books/flip_quiesce_test.go
// version: 1.0.0
// guid: 8b918de1-0796-40a3-ae28-2517600c7b46
// last-edited: 2026-11-21

package books

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// quiesceBackgroundForFlipPUT drains the booted composition's operations
// registry before a TestFlip test saves settings, so no registry worker or
// scheduler-started op is running while the save replaces the config.
//
// WHY this exists: a settings save replaces the whole process config struct
// under the config lock (config.Project here; on main, the PUT path's Mutate in
// internal/config/update_service.go does the same whole-struct write), while
// about 400 background readers across the tree still read config.AppConfig
// fields without the lock. That race is pre-existing on main and is not the
// flip's; the flip's tests are just the first to run ops and saves in one
// process. Its class fix (an atomic copy-on-write config, every reader taking
// an immutable snapshot) is its own lane, the config copy-on-write lane
// (config-cow), which lands on main right after the F3-07 stack.
//
// The config-cow lane deletes this helper and its one call site (runFlipped),
// and deleting them with TestFlip* still clean under -race -count=20 is that
// lane's acceptance test. Do not widen it, and do not copy it into other
// suites. The bootFlippedFresh tests need no quiesce: they boot without Run,
// so no registry worker or scheduler is started.
//
// Registry.Shutdown is the quiesce: it cancels running ops, waits for the
// workers to drain, and from then on no run starts (a dispatch cycle returns at
// once and a claimed run is left queued). That also makes the scheduler inert:
// every scheduled or startup task ends in a registry enqueue, which now only
// writes a queued row, and the scheduler's own reads take config.Snapshot.
// Shutdown is idempotent, so the composition's normal stop calling it again is
// safe.
func quiesceBackgroundForFlipPUT(t *testing.T, c *composed) {
	t.Helper()
	require.NotNil(t, c.srv, "the composition has no server to quiesce")
	reg := c.srv.OpRegistry()
	require.NotNil(t, reg, "the composition has no operations registry to quiesce")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	require.NoError(t, reg.Shutdown(ctx), "the registry did not drain before the settings save")
}
