// file: internal/server/series_writers_writeset_test.go
// version: 1.0.0
// guid: 6c0d8f3e-2b71-4a95-9e4d-73a1c5f20b86
// last-edited: 2026-10-11

package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// TestSeriesWriters_WriteSetKeepsPruneAndApplyAwayFromNormalize runs the REAL
// registered defs of repairs.apply (whose series prune fixer repoints books
// and deletes series rows) and dedup.series-prune through a dispatcher, each
// beside the real dedup.series-normalize: while one runs, the other must stay
// queued, and it must run once the first ends. Only Run is swapped (a blocker
// and a counter); everything the dispatcher's gates read -- Writes,
// ConcurrencyKey, Plugin -- is the registered def's. The two ops have
// different ConcurrencyKeys, so the write-set gate is the only thing that can
// keep them apart (mutation-checked: without ResSeries on the blocker the
// contender runs at once and this fails).
func TestSeriesWriters_WriteSetKeepsPruneAndApplyAwayFromNormalize(t *testing.T) {
	srv, _, _ := bootRegisteredOpIDs(t)
	realDef := func(id string) opsregistry.OperationDef {
		d, ok := srv.opRegistry.Def(id)
		require.True(t, ok, "%s is registered", id)
		return d
	}
	for _, tc := range []struct{ blocker, contender string }{
		{repairs.ApplyOpID, "dedup.series-normalize"},
		{"dedup.series-prune", "dedup.series-normalize"},
		{"dedup.series-normalize", repairs.ApplyOpID},
		{"dedup.series-normalize", "dedup.series-prune"},
	} {
		t.Run(tc.blocker+"_then_"+tc.contender, func(t *testing.T) {
			ctx := t.Context()
			reg := opsregistry.New(newOpsFake(t), slog.New(slog.DiscardHandler), 4, nil)

			started, release := make(chan struct{}), make(chan struct{})
			blocker := realDef(tc.blocker)
			blocker.Run = func(runCtx context.Context, _ json.RawMessage, _ opsregistry.Reporter) error {
				close(started)
				select {
				case <-release:
				case <-runCtx.Done():
				}
				return nil
			}
			var contenderRan atomic.Bool
			contender := realDef(tc.contender)
			contender.Run = func(context.Context, json.RawMessage, opsregistry.Reporter) error {
				contenderRan.Store(true)
				return nil
			}
			require.NotEqual(t, blocker.ConcurrencyKey, contender.ConcurrencyKey,
				"precondition: a shared ConcurrencyKey would serialize them without the write-set gate")
			require.NoError(t, reg.RegisterOp(blocker))
			require.NoError(t, reg.RegisterOp(contender))
			reg.Start(ctx)

			_, err := reg.EnqueueOp(ctx, blocker.ID, nil)
			require.NoError(t, err)
			<-started
			_, err = reg.EnqueueOp(ctx, contender.ID, nil)
			require.NoError(t, err)

			require.Never(t, contenderRan.Load, 500*time.Millisecond, 15*time.Millisecond,
				"%s ran while %s held the series write-set", tc.contender, tc.blocker)
			close(release)
			require.Eventually(t, contenderRan.Load, 10*time.Second, 10*time.Millisecond,
				"%s never ran after %s ended", tc.contender, tc.blocker)
		})
	}
}
