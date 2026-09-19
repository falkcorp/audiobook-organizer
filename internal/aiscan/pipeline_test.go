// file: internal/aiscan/pipeline_test.go
// version: 1.1.0
// guid: c9d5e1f3-6a7b-8c9d-0e1f-2a3b4c5d6e7f
// last-edited: 2026-09-19

package aiscan

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPipelinePhaseTransitions(t *testing.T) {
	pm := &PipelineManager{}

	// Groups scan complete → should trigger groups_enrich
	next := pm.nextPhases("groups_scan", "complete", map[string]string{
		"groups_scan": "complete",
		"full_scan":   "processing",
	})
	require.Contains(t, next, "groups_enrich")
	require.NotContains(t, next, "cross_validate")

	// Full scan complete → should trigger full_enrich
	next = pm.nextPhases("full_scan", "complete", map[string]string{
		"groups_scan": "complete",
		"full_scan":   "complete",
	})
	require.Contains(t, next, "full_enrich")
	require.NotContains(t, next, "cross_validate")

	// Failed phase → should trigger nothing
	next = pm.nextPhases("groups_scan", "failed", map[string]string{
		"groups_scan": "failed",
	})
	require.Empty(t, next)
}

func TestPipelineCrossValidateReady(t *testing.T) {
	pm := &PipelineManager{}

	// Both enrichments complete → cross_validate
	next := pm.nextPhases("full_enrich", "complete", map[string]string{
		"groups_scan":   "complete",
		"full_scan":     "complete",
		"groups_enrich": "complete",
		"full_enrich":   "complete",
	})
	require.Contains(t, next, "cross_validate")
}

func TestPipelineCrossValidateNotReady(t *testing.T) {
	pm := &PipelineManager{}

	// Only groups_enrich done, full_enrich still running → no cross_validate
	next := pm.nextPhases("groups_enrich", "complete", map[string]string{
		"groups_scan":   "complete",
		"full_scan":     "complete",
		"groups_enrich": "complete",
		"full_enrich":   "processing",
	})
	require.NotContains(t, next, "cross_validate")
}

// TestPipelineCrossValidateWaitsForBothEnrichRows: an ABSENT enrichment row is
// not a finished one. runEnrichment is launched with `go` and always creates
// its row (even when nothing is uncertain), so an absent row means it has not
// started yet. Counting it as done ran cross-validation on un-enriched
// suggestions and then ran it again when the enrichment finished.
func TestPipelineCrossValidateWaitsForBothEnrichRows(t *testing.T) {
	pm := &PipelineManager{}

	next := pm.nextPhases("groups_enrich", "complete", map[string]string{
		"groups_scan":   "complete",
		"full_scan":     "complete",
		"groups_enrich": "complete",
		// full_enrich not created yet
	})
	require.NotContains(t, next, "cross_validate")
}
