// file: internal/plugins/maintenance/db_census_exact.go
// version: 1.1.0
// guid: 7b09d3be-0012-432e-91e0-ea0f2491bea2
// last-edited: 2026-10-04

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// dbCensusExactOpID is the op that writes the exact census the
// GET /diagnostics/db-census endpoint returns as last_exact.
const dbCensusExactOpID = "maintenance.db-census-exact"

// dbCensusExactParams are the op's optional parameters.
type dbCensusExactParams struct {
	// Force runs even inside the cooldown after the last completed run, and
	// starts fresh: any saved progress of an unfinished run is discarded.
	Force bool `json:"force"`
	// Restart discards saved progress without overriding the cooldown.
	Restart bool `json:"restart"`
	// ReadMBPerSec overrides config db_census_exact_read_mb_per_sec for this
	// run (> 0 only).
	ReadMBPerSec int `json:"read_mb_per_sec"`
}

func (p *Plugin) dbCensusExactDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:     dbCensusExactOpID,
		Plugin: "maintenance",
		// LivenessManual: the pass reports after every family and at least
		// every ~5 s inside a large one (RunExactCensus's Progress).
		Liveness:    sdk.LivenessManual,
		DisplayName: "Exact database census",
		Description: "Counts every key family of the main database exactly with a rate-limited keys-only pass " +
			"(config db_census_exact_read_mb_per_sec, default 50 MB/s) and builds the per-book history distribution. " +
			"Resumes after a restart (restart=true discards saved progress); refuses to start again within db_census_exact_cooldown_hours (default 6) unless force=true. " +
			"The result is what /diagnostics/db-census shows as last_exact.",
		// ResumeRestart: a restarted run continues after the last family it
		// finished (progress is saved per family in the store).
		ResumePolicy:    sdk.ResumeRestart,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  dbCensusExactOpID,
		Cancellable:     true,
		Isolate:         false,
		// The budget counts UNCOMPRESSED key and value bytes, which came to
		// 2.4-6x the on-disk size on test stores; at 50 MB/s the production
		// store (~50 GB on disk) should take roughly 40-100 minutes. The
		// timeout leaves room for a much lower budget.
		Timeout: 12 * time.Hour,
		// No Schedule: an operator (or a release checklist) runs it.
		// CapLibraryWrite: it writes its result and progress keys.
		Capabilities: []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:          p.runDBCensusExact,
	}
}

func (p *Plugin) runDBCensusExact(ctx context.Context, params json.RawMessage, reporter sdk.Reporter) error {
	var args dbCensusExactParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &args); err != nil {
			return fmt.Errorf("invalid parameters: %w", err)
		}
	}
	store := p.deps.OpsStore()
	if store == nil {
		return errors.New("database not initialized")
	}
	runner, ok := database.AsCapability[database.DBCensusExactRunner](store)
	if !ok {
		return errors.New("exact db census requires the Pebble store")
	}
	cfg := config.Snapshot()
	return runDBCensusExactWith(ctx, runner, args, cfg.DBCensusExactReadMBPerSec, cfg.DBCensusExactCooldownHours, reporter, time.Now())
}

// runDBCensusExactWith is the op body, separated from config and deps for
// tests.
func runDBCensusExactWith(
	ctx context.Context, runner database.DBCensusExactRunner, args dbCensusExactParams,
	cfgMBPerSec, cfgCooldownHours int, reporter sdk.Reporter, now time.Time,
) error {
	mbps := cfgMBPerSec
	if mbps <= 0 {
		mbps = config.DefaultDBCensusExactReadMBPerSec
	}
	if args.ReadMBPerSec > 0 {
		mbps = args.ReadMBPerSec
	}
	cooldown := time.Duration(cfgCooldownHours) * time.Hour
	if cfgCooldownHours <= 0 {
		cooldown = time.Duration(config.DefaultDBCensusExactCooldownHours) * time.Hour
	}

	if !args.Force {
		last, err := runner.LastExactCensus()
		if err != nil {
			return fmt.Errorf("read last exact census: %w", err)
		}
		if last != nil && now.Sub(last.GeneratedAt) < cooldown {
			msg := fmt.Sprintf("Skipped: the last exact census finished %s ago, inside the %s cooldown "+
				"(db_census_exact_cooldown_hours). Rerun with force=true to run anyway.",
				now.Sub(last.GeneratedAt).Round(time.Second), cooldown)
			_ = reporter.Log(slog.LevelInfo, msg)
			_ = reporter.UpdateProgress(1, 1, msg)
			return nil
		}
	}

	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("Exact database census starting at %d MB/s read budget", mbps))
	_ = reporter.UpdateProgress(0, 1, "Starting exact database census...")
	c, err := runner.RunExactCensus(ctx, database.ExactCensusOptions{
		ReadBytesPerSec: int64(mbps) << 20,
		Restart:         args.Force || args.Restart,
		Progress: func(pr database.ExactCensusProgress, family string) {
			reporter.SetCurrentItem(family)
			_ = reporter.UpdateProgress(pr.FamiliesDone, pr.FamiliesAll, fmt.Sprintf(
				"Counting %s (%d/%d families, %d MB read)", family, pr.FamiliesDone, pr.FamiliesAll, pr.BytesRead>>20))
		},
	})
	if err != nil {
		return fmt.Errorf("exact db census: %w", err)
	}
	var keys, entries int64
	for _, f := range c.Families {
		keys += f.Keys
		entries += f.Entries
	}
	msg := fmt.Sprintf("Exact database census done in %s: %d families, %d live keys, %d entries, %d MB read",
		time.Duration(c.DurationMS)*time.Millisecond, len(c.Families), keys, entries, c.BytesRead>>20)
	_ = reporter.Log(slog.LevelInfo, msg)
	_ = reporter.UpdateProgress(len(c.Families), len(c.Families), msg)
	return nil
}
