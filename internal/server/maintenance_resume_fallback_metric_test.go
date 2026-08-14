// file: internal/server/maintenance_resume_fallback_metric_test.go
// version: 1.0.0
// guid: 7c2e9a41-3f58-4b6d-9e0a-8d1c5b2f7a93
// last-edited: 2026-08-14

package server

import (
	"testing"

	"github.com/oklog/ulid/v2"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/falkcorp/audiobook-organizer/internal/metrics"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
)

// gatherResumeFallbackCount reads the C511 counter for one (job_id, reason)
// pair from the DEFAULT registry — the same registry /metrics serves — so a
// nonzero value here proves both registration and the increment. A counter vec
// with no children is absent from the gather entirely; that reads as 0.
func gatherResumeFallbackCount(t *testing.T, jobID, reason string) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, mf := range families {
		if mf.GetName() != "audiobook_organizer_maintenance_resume_params_fallback_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			labels := map[string]string{}
			for _, lp := range m.GetLabel() {
				labels[lp.GetName()] = lp.GetValue()
			}
			if labels["job_id"] == jobID && labels["reason"] == reason {
				return m.GetCounter().GetValue()
			}
		}
	}
	return 0
}

// metricFamilyPresent reports whether the named family appears in the default
// registry's gather output at all.
func metricFamilyPresent(t *testing.T, name string) bool {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, mf := range families {
		if mf.GetName() == name {
			return true
		}
	}
	return false
}

// TestResumeLegacyOp_NoSavedParamsIncrementsFallbackCounter pins C511: the
// resume fallback (interrupted maintenance job with no saved params) must be
// countable from Prometheus, not just visible in a log line that ages out of
// journald. Post-#2419 every enqueue persists params, so a production fire of
// this counter means a SaveParams silently failed.
func TestResumeLegacyOp_NoSavedParamsIncrementsFallbackCounter(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()
	if server.opRegistry == nil {
		t.Skip("ops registry not wired in this build")
	}
	metrics.Register()

	jobID := probeAdvertisesTrue.ID()

	// No saved params → the fallback branch must fire and count.
	opID := ulid.Make().String()
	opType := "maintenance:" + jobID
	if _, err := server.Store().CreateOperation(opID, opType, nil); err != nil {
		t.Fatalf("CreateOperation: %v", err)
	}

	before := gatherResumeFallbackCount(t, jobID, "no_saved_params")
	server.resumeLegacyOp(opID, opType)
	after := gatherResumeFallbackCount(t, jobID, "no_saved_params")

	if after != before+1 {
		t.Fatalf("no_saved_params fallback counter went %v -> %v, want +1: the C511 metric is not wired to the fallback branch", before, after)
	}
	if !metricFamilyPresent(t, "audiobook_organizer_maintenance_resume_params_fallback_total") {
		t.Fatal("metric family absent from the default registry — Register() does not include the C511 counter")
	}

	// Control: a resume WITH saved params must NOT count — the metric measures
	// fallbacks, not resumes. Without this, an increment misplaced onto the
	// happy path would still pass the assertion above.
	opID2 := ulid.Make().String()
	if _, err := server.Store().CreateOperation(opID2, opType, nil); err != nil {
		t.Fatalf("CreateOperation: %v", err)
	}
	if err := operations.SaveParams(server.Store(), opID2, maintenanceJobOpParams{
		LegacyOpID: opID2,
		JobID:      jobID,
		DryRun:     true,
	}); err != nil {
		t.Fatalf("SaveParams: %v", err)
	}
	mid := gatherResumeFallbackCount(t, jobID, "no_saved_params")
	server.resumeLegacyOp(opID2, opType)
	end := gatherResumeFallbackCount(t, jobID, "no_saved_params")
	if end != mid {
		t.Fatalf("fallback counter moved (%v -> %v) on a resume that HAD saved params — it must only count fallbacks", mid, end)
	}
}
