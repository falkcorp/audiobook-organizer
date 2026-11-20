// file: internal/plugins/maintenance/kinds_shims_test.go
// version: 1.0.0
// guid: 1a8ad02b-275a-4af1-9576-4fee91f72c79
// last-edited: 2026-11-19

// Test-only shims. The three HEAD-named entry points below used to be the
// production path; F5-12 moved the work into the *Core functions both the HEAD
// shell and the native kind call, which left these as entry points only HEAD's
// tests call. They live here so production keeps no dead code (and no
// scripts/deadcode-baseline.txt lines), and the older tests keep exercising the
// live path unchanged (the F5-05 precedent, internal/plugins/acoustid).

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// repairReviewStatusIndex decodes the params and runs the core on HEAD's store,
// returning the tally. The v2 shell and the HEAD tests read the tally through it
// rather than by parsing a log line back out of a fake reporter.
func (p *Plugin) repairReviewStatusIndex(ctx context.Context, rawParams json.RawMessage) (reviewStatusIndexRepairResult, error) {
	var params reviewStatusIndexRepairParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return reviewStatusIndexRepairResult{}, fmt.Errorf("review-status-index-repair: decode params: %w", err)
		}
	}
	return reviewStatusIndexRepairCore(ctx, params.Apply, p.reviewStatusIndexRepairHeadEnv(nil, params.Apply))
}

// buildUnknownAuthorAudit is HEAD's sweep entry: the core over HEAD's store and reporter.
func buildUnknownAuthorAudit(ctx context.Context, store unknownAuthorAuditStore, params unknownAuthorAuditParams, reporter sdk.Reporter) (unknownAuthorAuditReport, error) {
	return unknownAuthorAuditCore(ctx, params, unknownAuthorAuditEnv{t6g7Env: t6g7HeadEnv(reporter, false), Store: store})
}

// planRewritePathPrefix is HEAD's planner entry: the core over HEAD's store,
// stand-down and reporter.
func planRewritePathPrefix(
	ctx context.Context,
	store rewritePathPrefixStore,
	scan ScanController,
	params rewritePathPrefixParams,
	reporter sdk.Reporter,
) (rewritePathPrefixPlan, error) {
	return rewritePathPrefixPlanCore(ctx, params, rewritePathPrefixHeadEnv(store, scan, !params.dryRun(), reporter))
}
