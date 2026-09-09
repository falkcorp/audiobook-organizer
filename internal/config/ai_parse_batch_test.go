// file: internal/config/ai_parse_batch_test.go
// version: 1.1.0
// guid: 4a1e77b2-9c30-4d6f-b8a5-1e29c4f0d7e6
// last-edited: 2026-09-09

package config

import (
	"testing"
	"time"
)

func TestResolveAIParseBatch(t *testing.T) {
	tests := []struct {
		name        string
		size        int
		timeoutSecs int
		workers     int
		wantSize    int
		wantTimeout time.Duration
		wantWorkers int
		why         string
	}{
		{
			name: "unset keeps the historical hardcoded pair", size: 0, timeoutSecs: 0, workers: 0,
			wantSize: DefaultAIParseBatchSize, wantTimeout: DefaultAIParseBatchTimeout,
			wantWorkers: DefaultAIParseBatchWorkers,
			why:         "an install pointed at a hosted API must see no change from this becoming configurable",
		},
		{
			name: "negative is treated as unset, not as a tiny batch", size: -5, timeoutSecs: -5,
			wantSize: DefaultAIParseBatchSize, wantTimeout: DefaultAIParseBatchTimeout, wantWorkers: DefaultAIParseBatchWorkers,
			why: "a negative batch size would make the split loop produce zero batches and parse nothing",
		},
		{
			name: "the CPU-backend pair passes through unchanged", size: 4, timeoutSecs: 90,
			wantSize: 4, wantTimeout: 90 * time.Second, wantWorkers: DefaultAIParseBatchWorkers,
			why: "4 books x ~10s/book on a CPU 7B is ~40s, comfortably inside 90s",
		},
		{
			name: "timeout is clamped below the watchdog's 5m ProgressTimeout", size: 20, timeoutSecs: 600,
			wantSize: 20, wantTimeout: AIParseBatchTimeoutCeiling, wantWorkers: DefaultAIParseBatchWorkers,
			why: "above 5m the op is KILLED for inactivity instead of the batch merely failing, which is worse",
		},
		{
			name: "batch size is clamped to the ceiling", size: 5000, timeoutSecs: 60,
			wantSize: AIParseBatchSizeCeiling, wantTimeout: 60 * time.Second, wantWorkers: DefaultAIParseBatchWorkers,
			why: "an unbounded batch overruns a small model's context and loses the whole JSON array, not one entry",
		},
		{
			name: "exactly at the ceilings is allowed, not clamped past",
			size: AIParseBatchSizeCeiling, timeoutSecs: int(AIParseBatchTimeoutCeiling / time.Second),
			wantSize: AIParseBatchSizeCeiling, wantTimeout: AIParseBatchTimeoutCeiling, wantWorkers: DefaultAIParseBatchWorkers,
			why: "an off-by-one in the clamp would silently move an operator's deliberate maximum",
		},
		{
			name: "workers=1 for a serial backend passes through", size: 4, timeoutSecs: 90, workers: 1,
			wantSize: 4, wantTimeout: 90 * time.Second, wantWorkers: 1,
			why: "against a one-at-a-time backend extra workers only queue, and a queued batch burns its own deadline",
		},
		{
			name: "workers is clamped to the ceiling", size: 20, timeoutSecs: 30, workers: 999,
			wantSize: 20, wantTimeout: 30 * time.Second, wantWorkers: AIParseBatchWorkersCeiling,
			why: "past the ceiling the per-worker pacing delay stops bounding the aggregate request rate",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Config{}
			c.AIBackend.ParseBatchSize = tt.size
			c.AIBackend.ParseBatchTimeoutSeconds = tt.timeoutSecs
			c.AIBackend.ParseBatchWorkers = tt.workers

			got := c.ResolveAIParseBatch()

			if got.Size != tt.wantSize {
				t.Errorf("size = %d, want %d\nwhy it matters: %s", got.Size, tt.wantSize, tt.why)
			}
			if got.Workers != tt.wantWorkers {
				t.Errorf("workers = %d, want %d\nwhy it matters: %s", got.Workers, tt.wantWorkers, tt.why)
			}
			if got.Timeout != tt.wantTimeout {
				t.Errorf("timeout = %s, want %s\nwhy it matters: %s", got.Timeout, tt.wantTimeout, tt.why)
			}
		})
	}
}

// The ceiling is not an arbitrary number: it has to stay under the operations
// registry's defaultProgressTimeout (5m), because runAIBatchPhase reports
// progress once per batch and a batch may consume the entire timeout.
//
// Asserted rather than commented, because the registry's default lives in
// another package and nothing else would notice if the two drifted -- and the
// drift is silent, showing up only as scans killed for inactivity in prod.
func TestAIParseBatchTimeoutCeilingStaysUnderWatchdog(t *testing.T) {
	const watchdogProgressTimeout = 5 * time.Minute

	if AIParseBatchTimeoutCeiling >= watchdogProgressTimeout {
		t.Fatalf("AIParseBatchTimeoutCeiling (%s) >= the registry's ProgressTimeout (%s): "+
			"a batch may occupy the whole timeout, so at or above this the watchdog kills the "+
			"operation instead of the batch simply failing",
			AIParseBatchTimeoutCeiling, watchdogProgressTimeout)
	}
}
