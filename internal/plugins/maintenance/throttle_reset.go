// file: internal/plugins/maintenance/throttle_reset.go
// version: 1.0.1
// guid: ca0151f9-fe5c-4afa-82b6-03c85e42bb08
// last-edited: 2026-10-30

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// throttleResetID is the op id. The DELETE /metadata/providers/throttles
// routes it replaces were retired in 01-P81f (no caller); a hold now ends on
// expiry, on a successful call, or here.
const throttleResetID = "maintenance.throttle-reset"

// throttleResetParams: provider names one hold to release (empty = every
// active hold), plus the standard dry_run/dryRun mode.
type throttleResetParams struct {
	opmode.DryRunParams
	// Provider is a provider id as the Throttle panel and
	// GET /metadata/providers/throttles show it. Empty releases all.
	Provider string `json:"provider,omitempty"`
}

// throttleResetRow is one hold, reported by a preview (Released=false) and an
// apply (Released=true once it was actually dropped).
type throttleResetRow struct {
	Provider string `json:"provider"`
	Kind     string `json:"kind"`
	Reason   string `json:"reason,omitempty"`
	Until    string `json:"until"`
	SetAt    string `json:"set_at"`
	Released bool   `json:"released"`
	Error    string `json:"error,omitempty"`
}

type throttleResetResult struct {
	DryRun   bool               `json:"dry_run"`
	Provider string             `json:"provider,omitempty"`
	Matched  int                `json:"matched"`
	Released int                `json:"released"`
	Errors   int                `json:"errors"`
	Rows     []throttleResetRow `json:"rows"`
}

// throttleResetDef releases metadata-provider holds by hand.
//
// A quota or auth hold survives restarts and normally ends only on expiry
// (up to 24h) or when a successful call releases it. This is the operator's
// way out when the cause is already fixed (a key replaced, a quota raised).
// Preview by default (owner rule 2026-09-25): lists the holds it would release
// and changes nothing; dry_run=false releases them.
//
// It touches only the throttle registry and its persisted store rows -- no
// book data or files -- and declares the store read/write capabilities every
// maintenance op must declare (TestMaintenancePlugin_AllOpsHaveCapabilities).
// Admin-only like every op start (the ops routes are admin-gated).
func (p *Plugin) throttleResetDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          throttleResetID,
		Preview:     sdk.PreviewSpec{Params: throttleResetParams{}},
		Liveness:    sdk.LivenessNone,
		Plugin:      "maintenance",
		DisplayName: "Release metadata provider holds",
		Description: "Lists, and with dry_run=false releases, the quota/auth/rate-limit holds on metadata providers. " +
			"Preview by default: shows each hold's provider, kind, expiry and reason and releases nothing. " +
			"Optional provider scopes it to one provider id (an id with no active hold is an error); empty releases every hold. " +
			"Use after fixing the cause (a replaced key, a raised quota); a hold otherwise ends on expiry or on a successful call.",
		ProgressTimeout: time.Minute,
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityHigh,
		ConcurrencyKey:  throttleResetID,
		Cancellable:     false,
		// The registry persists holds in the store (provider throttle rows), so it
		// reads and deletes store state, the same set clear-apply-rename-failures
		// declares for its preference rows.
		Capabilities: []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Timeout:      time.Minute,
		Run:          p.runThrottleReset,
	}
}

func (p *Plugin) runThrottleReset(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	return runThrottleResetOn(ctx, metadata.DefaultThrottleRegistry(), raw, reporter)
}

// runThrottleResetOn is the op body over an explicit registry so tests do not
// share the process-global one.
func runThrottleResetOn(ctx context.Context, reg *metadata.ThrottleRegistry, raw json.RawMessage, reporter sdk.Reporter) error {
	var params throttleResetParams
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("%s: decode params: %w", throttleResetID, err)
		}
	}
	dryRun, err := opmode.ResolveDryRun(throttleResetID, params.DryRun, params.DryRunCamel)
	if err != nil {
		return err
	}
	provider := strings.TrimSpace(params.Provider)

	holds := reg.List()
	if provider != "" {
		var match []metadata.ProviderThrottle
		for _, h := range holds {
			if h.ProviderID == provider {
				match = append(match, h)
			}
		}
		if len(match) == 0 {
			held := make([]string, 0, len(holds))
			for _, h := range holds {
				held = append(held, h.ProviderID)
			}
			return fmt.Errorf("%s: provider %q has no active hold (held: %s)",
				throttleResetID, provider, orNone(strings.Join(held, ", ")))
		}
		holds = match
	}

	res := throttleResetResult{DryRun: dryRun, Provider: provider, Matched: len(holds), Rows: []throttleResetRow{}}
	for _, h := range holds {
		row := throttleResetRow{
			Provider: h.ProviderID,
			Kind:     string(h.Reason),
			Reason:   h.Detail,
			Until:    h.Until.UTC().Format(time.RFC3339),
			SetAt:    h.SetAt.UTC().Format(time.RFC3339),
		}
		if !dryRun {
			if err := ctx.Err(); err != nil {
				return err
			}
			_, released, rerr := reg.ReleaseHold(h.ProviderID)
			switch {
			case rerr != nil:
				res.Errors++
				row.Error = rerr.Error()
				_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("could not release hold on %s: %v", h.ProviderID, rerr))
			case released:
				row.Released = true
				res.Released++
				_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("released %s hold on %s (until %s)", h.Reason, h.ProviderID, row.Until))
			}
			// !released with no error: the hold expired or a successful call
			// released it between the listing and here -- already gone, not an error.
		}
		res.Rows = append(res.Rows, row)
	}

	summary := fmt.Sprintf("Provider holds: %d matched, %d released (dry_run=%t)", res.Matched, res.Released, dryRun)
	_ = reporter.Log(slog.LevelInfo, summary)
	_ = reporter.UpdateProgress(1, 1, summary)
	if err := opsregistry.ReporterSetResult(reporter, res); err != nil {
		_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("could not persist result data: %v", err))
	}
	if res.Errors > 0 {
		return fmt.Errorf("%s: %d of %d holds could not be released", throttleResetID, res.Errors, res.Matched)
	}
	return nil
}
