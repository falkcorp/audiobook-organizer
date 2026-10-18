// file: internal/plugins/maintenance/ident_index.go
// version: 1.2.1
// guid: 90d81b9d-7102-4e21-a4ac-e55a95d3922c
// last-edited: 2026-10-18

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// --- ident: identification index rebuild op ---
//
// The index (ident:state:, ident:by:, the in-memory counters), its
// write-through and its boot build live in internal/database/ident_state.go.
// Every boot builds or reconciles it, and SuperviseIdentIndex retries a failed
// boot build with backoff; once those retries are spent it logs an ERROR
// naming this op. This op is the
// manual handle for the same rebuild: after that ERROR, or after a rollback to
// a build that predates the index (whose writes did not maintain it).
//
// Preview by default (owner rule 2026-09-25): `{}` runs VerifyIdentIndex --
// the rebuild's two passes in read-only form plus the counter drift -- and
// reports what a rebuild would change; `{"dry_run": false}` rebuilds. The
// rebuild writes ident: rows only, never book or cache rows.
//
// Liveness is manual: one progress stamp before and one after. A rebuild is a
// keys-and-small-rows pass (about 0.2 s per 5,000 books in testing), far
// inside the default progress timeout.
//
// It resolves the store method through database.AsCapability: OpsStore() can be
// a Store-embedding decorator (server.indexedStore, until 07-S3), which hides every
// *PebbleStore method outside database.Store.

type identIndexRebuilder interface {
	VerifyIdentIndex(ctx context.Context) (database.IdentIndexReport, error)
	RebuildIdentIndex(ctx context.Context) (database.IdentRebuildStats, error)
}

func (p *Plugin) identIndexRebuildDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          database.IdentIndexRebuildOpID,
		Preview:     sdk.PreviewSpec{Key: sdk.PreviewDryRun, Params: opmode.DryRunParams{}},
		Liveness:    sdk.LivenessManual,
		Plugin:      "maintenance",
		DisplayName: "Identification index rebuild",
		Description: "Manual rebuild of the book identification index (ident:state: and ident:by: rows and " +
			"their in-memory counts) that the Library's ident: filter and the review summary's ident_counts " +
			"read. Every boot already builds or reconciles it and retries a failure, so this is the manual " +
			"handle for the same repair. Preview by default: reports the books whose state is wrong, the " +
			"orphan rows and keys, and the counter drift, writes nothing. With dry_run=false it rebuilds; " +
			"ident: queries answer 503 while a from-scratch rebuild runs. Writes ident: rows only, never " +
			"book or metadata cache rows.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.ident-index",
		Cancellable:     true,
		Timeout:         30 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runIdentIndexRebuild,
	}
}

func (p *Plugin) runIdentIndexRebuild(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	var params opmode.DryRunParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("decode params: %w", err)
		}
	}
	dryRun, err := opmode.ResolveDryRun(database.IdentIndexRebuildOpID, params.DryRun, params.DryRunCamel)
	if err != nil {
		return err
	}
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	r, ok := database.AsCapability[identIndexRebuilder](store)
	if !ok {
		return fmt.Errorf("cannot run: store is %T, which has no identification index "+
			"(and no store in its decorator chain does)", store)
	}
	if dryRun {
		return previewIdentIndex(ctx, r, reporter)
	}
	return rebuildIdentIndex(ctx, r, reporter)
}

// previewIdentIndex is the read-only mode: it reports what a rebuild would
// change and writes nothing.
func previewIdentIndex(ctx context.Context, r identIndexRebuilder, reporter sdk.Reporter) error {
	_ = reporter.UpdateProgress(0, 1, "verifying the identification index (preview, writes nothing)")
	rep, err := r.VerifyIdentIndex(ctx)
	if err != nil {
		return fmt.Errorf("verify identification index: %w", err)
	}
	msg := fmt.Sprintf("preview: ready=%t books=%d rows_wrong=%d orphan_rows=%d bad_keys=%d "+
		"counter_drift=%d undecodable=%d; run with dry_run=false to rebuild",
		rep.Ready, rep.Books, rep.RowsWrong, rep.OrphanRows, rep.BadKeys, rep.CounterDrift, rep.Undecodable)
	if rep.Clean() {
		msg = fmt.Sprintf("preview: the identification index matches its %d books (undecodable=%d); nothing to rebuild",
			rep.Books, rep.Undecodable)
	}
	_ = reporter.Log(slog.LevelInfo, msg)
	_ = reporter.UpdateProgress(1, 1, msg)
	return nil
}

func rebuildIdentIndex(ctx context.Context, r identIndexRebuilder, reporter sdk.Reporter) error {
	_ = reporter.UpdateProgress(0, 1, "rebuilding the identification index")
	st, err := r.RebuildIdentIndex(ctx)
	if err != nil {
		return fmt.Errorf("rebuild identification index (scanned %d books before failing; a from-scratch "+
			"rebuild leaves the index not ready, so ident: queries answer 503 until a rebuild succeeds): %w",
			st.Books, err)
	}
	msg := fmt.Sprintf("rebuilt the identification index: books=%d rows_fixed=%d rows_removed=%d "+
		"keys_removed=%d counter_drift=%d undecodable=%d from_scratch=%t in %s",
		st.Books, st.RowsFixed, st.RowsRemoved, st.KeysRemoved, st.CounterDrift, st.Undecodable,
		st.FromScratch, st.Duration.Round(time.Millisecond))
	_ = reporter.UpdateProgress(1, 1, msg)
	if st.Undecodable > 0 {
		// Not green: those books are in no state, so the counts leave them
		// out until their rows are repaired.
		_ = reporter.Log(slog.LevelError, msg)
		return fmt.Errorf("identification index rebuilt, but %d book row(s) cannot be decoded and are "+
			"not counted in any state until they are repaired", st.Undecodable)
	}
	_ = reporter.Log(slog.LevelInfo, msg)
	return nil
}
