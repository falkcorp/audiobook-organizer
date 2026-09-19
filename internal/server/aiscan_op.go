// file: internal/server/aiscan_op.go
// version: 1.3.0
// guid: 4f7a2c91-8d63-4e05-b1a7-9c3e5d80f624
// last-edited: 2026-09-19

// aiscan_op registers the ai.author-scan OperationDef. Before 2026-08-22 the
// multi-pass AI author dedup pipeline was the last subsystem with no
// OperationDef at all: it wrote a v1 operations row directly from
// internal/aiscan and mirrored progress onto it at six sites. That row reached
// no timeline and no UI, so the scan was invisible everywhere.

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/aiscan"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// aiAuthorScanParams holds the serializable parameters for the ai.author-scan op.
//
// It carries no LegacyOpID. Every other op in this package still stamps one to
// bridge its status back onto a v1 row; this op has no v1 row to bridge to,
// because the scan's state lives in the ai_scans store and its operation is the
// v2 row itself.
type aiAuthorScanParams struct {
	ScanID int `json:"scan_id"`
}

// RegisterAIAuthorScanOp registers the "ai.author-scan" v2 OperationDef.
func (s *Server) RegisterAIAuthorScanOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "ai.author-scan",
		Liveness:        opsregistry.LivenessManual,
		Plugin:          "ai",
		DisplayName:     "AI Author Scan",
		Description:     "Multi-pass AI scan of the author list that proposes duplicate-author merges.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Isolate:         false,

		// A batch-mode scan is held by OpenAI, which allows up to 24h. This is
		// the registry's ceiling, not a guess.
		Timeout: 24 * time.Hour,

		// ResumeRestart, not ResumeDrop like its ai.* siblings. Those siblings do
		// all their work inside Run, so an interrupted one has nothing left to
		// rejoin. A scan does: OpenAI is still holding a batch scan's jobs, and a
		// realtime scan has persisted every chunk it finished. RunScan decides
		// from PERSISTED phase state, per phase, what still has to run.
		//
		// RESUME AUDIT 2026-09-11 (c), revised 2026-09-19: kept, with no
		// reporter.Checkpoint, and that is correct here. The checkpoint this op
		// needs lives in the ai_scans store: drive (internal/aiscan/pipeline.go)
		// reads the phase rows — pending phases launch; a realtime full_scan
		// continues from its persisted chunks and re-requests none of them; a
		// submitted batch re-attaches to the job OpenAI holds; a batch phase that
		// died mid-submit or whose CreateBatch errored ("submitting") re-attaches
		// by batch metadata, resubmits only on a confirmed absence, and stays
		// "submitting" while the lookup is inconclusive; finished phases feed
		// enrichment and cross-validation, which replace rather than append
		// their results. The only param, scan_id, never changes.
		//
		// A restart must not look like a cancel. The registry cancels running
		// ops on shutdown with registry.ErrShutdown as the context cause, and
		// RunScan then returns WITHOUT CancelScan: the batches keep running at
		// OpenAI and the phases keep their state for the resumed run. Only an
		// operator cancel or the op timeout (a cause other than ErrShutdown)
		// cancels the batches and marks the scan canceled.
		ResumePolicy: opsregistry.ResumeRestart,

		// Serializes scans. Two could previously run at once; a second now
		// queues. Deliberate: this is a paid whole-library LLM pass, and two
		// concurrent ones are almost always a double-click, not an intent.
		ConcurrencyKey: "ai.author-scan",

		Permissions:  []auth.Permission{auth.PermLibraryEditMetadata},
		Capabilities: []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapNetworkOpenAI},

		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p aiAuthorScanParams
			if len(rawParams) > 0 {
				if err := json.Unmarshal(rawParams, &p); err != nil {
					return fmt.Errorf("ai.author-scan: decode params: %w", err)
				}
			}
			if p.ScanID == 0 {
				return fmt.Errorf("ai.author-scan: scan_id is required")
			}
			if s.pipelineManager == nil {
				return fmt.Errorf("ai.author-scan: pipeline manager not initialized")
			}
			return s.pipelineManager.RunScan(ctx, p.ScanID, reporter)
		},
	})
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterAIAuthorScanOp(reg) })
}

// aiscanSinkCheck asserts at compile time that a registry Reporter satisfies the
// pipeline's ProgressSink. The two are declared in packages that do not import
// each other — aiscan deliberately stays free of the operations layer — so
// nothing else would catch a drift in UpdateProgress's signature.
var _ aiscan.ProgressSink = (opsregistry.Reporter)(nil)
