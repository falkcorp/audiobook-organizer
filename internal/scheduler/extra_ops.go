// file: internal/scheduler/extra_ops.go
// version: 1.30.0
// guid: a9b8c7d6-e5f4-3210-fedc-ba9876543210
// last-edited: 2026-10-13

// extra_ops registers OperationDefs for 2 scheduler tasks that previously
// used the legacy triggerOperation / triggerOperationWithID helpers.  Each def
// extracts the closure logic into a typed Run func so the UOS v2 dispatcher
// can manage lifecycle, cancellation, and concurrency without the old
// BridgeQueue.
//
// Extracted from internal/server/scheduler_extra_ops.go (SERVER-THIN-RESIDUAL).
// Receiver changed from *Server to *ExtraOpsRegistrar.

package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// ExtraOpsDeps holds the typed dependencies needed by ExtraOpsRegistrar.
// All fields are optional (nil-safe) to match the server's lazy-init pattern.
type ExtraOpsDeps struct {
	DedupEngine          *dedup.Engine
	MetadataFetchService *metafetch.Service
	// ScanStandDown is the scan stand-down gate the metadata upgrade op
	// holds while it applies (metadata is never applied during a library scan).
	// Nil = ungated (tests).
	ScanStandDown opsregistry.ScanStandDownGate
}

// ExtraOpsRegistrar holds a Store reference and typed Deps so the 2 scheduler
// OperationDefs can be registered without a *Server pointer.
type ExtraOpsRegistrar struct {
	Deps  ExtraOpsDeps
	Store metabatch.Store
}

// NewExtraOpsRegistrar constructs an ExtraOpsRegistrar.
func NewExtraOpsRegistrar(store metabatch.Store, deps ExtraOpsDeps) *ExtraOpsRegistrar {
	return &ExtraOpsRegistrar{Store: store, Deps: deps}
}

// extraOpsProgressAdapter bridges opsregistry.Reporter → operations.ProgressReporter
// so the run* helpers can be called from a v2 op Run function without changes.
type extraOpsProgressAdapter struct{ r opsregistry.Reporter }

func (a extraOpsProgressAdapter) UpdateProgress(current, total int, message string) error {
	return a.r.UpdateProgress(current, total, message)
}
func (a extraOpsProgressAdapter) Log(level, message string, details *string) error {
	l := slog.LevelInfo
	switch level {
	case "warn", "warning":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	case "debug":
		l = slog.LevelDebug
	}
	var attrs []slog.Attr
	if details != nil {
		attrs = append(attrs, slog.String("details", *details))
	}
	return a.r.Log(l, message, attrs...)
}
func (a extraOpsProgressAdapter) IsCanceled() bool { return a.r.IsCanceled() }

// --- dedup-llm-review ---

// RegisterDedupLLMReviewOp registers the scheduler.dedup-llm-review OperationDef.
func (r *ExtraOpsRegistrar) RegisterDedupLLMReviewOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "scheduler.dedup-llm-review",
		Liveness:        opsregistry.LivenessNone,
		ProgressTimeout: 2 * time.Hour, // LivenessNone requires an explicit budget
		Plugin:          "scheduler",
		DisplayName:     "Dedup LLM Review",
		Description:     "Run LLM review on ambiguous dedup candidates.",
		DefaultPriority: opsregistry.PriorityLow,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         2 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "scheduler.dedup-llm-review",
		Permissions:     []auth.Permission{auth.PermSettingsManage},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			progress := extraOpsProgressAdapter{r: reporter}
			if r.Deps.DedupEngine == nil {
				_ = progress.Log("info", "Dedup engine not initialized, skipping LLM review", nil)
				return nil
			}
			_ = progress.Log("info", "Starting LLM review of ambiguous dedup candidates", nil)
			return r.Deps.DedupEngine.RunLLMReview(ctx)
		},
	})
}

// --- metadata-upgrade ---

// RegisterMetadataUpgradeOp registers the scheduler.metadata-upgrade OperationDef.
//
// It is the ONLY registration of the metadata upgrade (2026-09-27). Until then
// the same job was also registered as maintenance.metadata-upgrade by the
// maintenance plugin, a second def with its own ConcurrencyKey, so the two
// could run at once over the same books. Nothing in web/src or the handlers
// named the maintenance ID; it stays reachable as a FormerID, so an old
// operations_v2 row or an API enqueue by that ID resolves to this def.
func (r *ExtraOpsRegistrar) RegisterMetadataUpgradeOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "scheduler.metadata-upgrade",
		FormerIDs:       []string{"maintenance.metadata-upgrade"},
		Liveness:        opsregistry.LivenessManual,
		Plugin:          "scheduler",
		DisplayName:     "Metadata Upgrade",
		Description:     "Replace metadata from lower-ranked sources (Open Library, Google Books, Wikipedia) with a higher-ranked source's (Hardcover, Audible) when its match passes the bulk-apply gate. User-locked fields are kept.",
		DefaultPriority: opsregistry.PriorityLow,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         4 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "scheduler.metadata-upgrade",
		Permissions:     []auth.Permission{auth.PermSettingsManage},
		// Audible and generic network: the search queries every metadata
		// provider. OpenAI: the search may LLM-rerank its candidates.
		Capabilities: []opsregistry.Capability{
			opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite,
			opsregistry.CapNetworkAudible, opsregistry.CapNetworkGeneric, opsregistry.CapNetworkOpenAI,
		},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) (retErr error) {
			// Metadata is never applied during a library scan: hold the scan
			// stand-down before the first write. A running scan does not make
			// the op refuse: the acquire quiesces it and waits for it to park
			// (bounded by the lease), and the op fails only if it never parks.
			hold, sdErr := opsregistry.HoldScanStandDown(ctx, r.Deps.ScanStandDown, reporter, "scheduler.metadata-upgrade apply")
			if sdErr != nil {
				return sdErr
			}
			defer func() { retErr = hold.Finish(retErr) }()
			ctx, reporter = hold.Context(), hold.Reporter()

			progress := extraOpsProgressAdapter{r: reporter}
			p := sdk.NewProgress(reporter, 0)
			p.Start("Scanning for books with upgradeable metadata sources...")
			if r.Deps.MetadataFetchService == nil {
				return fmt.Errorf("metadata fetch service not initialized")
			}
			svc := metabatch.NewMetadataUpgradeService(r.Store, r.Deps.MetadataFetchService)
			_ = progress.Log("info", "Scanning for books with upgradeable metadata sources...", nil)
			// M7 (2026-07 error-correction sweep): thread progress through so
			// this 4-hour-timeout op reports books processed/total every 25
			// books instead of going silent between start and result.
			result, err := svc.RunUpgrade(ctx, 200, progress)
			if err != nil {
				return err
			}
			msg := fmt.Sprintf("Metadata upgrade complete: checked %d, upgraded %d, skipped %d, "+
				"owner-manual-only %d (Doctor Who / Big Finish / Torchwood), errors %d; sweep after %q -> %q (wrapped %v)",
				result.Checked, result.Upgraded, result.Skipped, result.OwnerManualOnly, result.Errors,
				result.CursorStart, result.CursorEnd, result.Wrapped)
			_ = progress.Log("info", msg, nil)
			p.Done(msg)
			return nil
		},
	})
}
