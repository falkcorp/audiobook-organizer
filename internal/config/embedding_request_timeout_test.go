// file: internal/config/embedding_request_timeout_test.go
// version: 1.0.0
// guid: 5d2b8f3e-6a41-4c9d-b7e0-9f1c3a2d4e58
// last-edited: 2026-09-11

package config

import (
	"testing"
	"time"
)

func TestResolveEmbeddingRequestTimeout(t *testing.T) {
	tests := []struct {
		name        string
		timeoutSecs int
		want        time.Duration
		why         string
	}{
		{
			name: "unset keeps the historical hardcoded 30 s", timeoutSecs: 0,
			want: DefaultEmbeddingRequestTimeout,
			why:  "an install that never sets the key must see no change from this becoming configurable",
		},
		{
			name: "negative is treated as unset, not as an already-expired budget", timeoutSecs: -5,
			want: DefaultEmbeddingRequestTimeout,
			why:  "a negative duration would make every attempt's child context expire before the request is sent",
		},
		{
			name: "a cold-load budget passes through unchanged", timeoutSecs: 60,
			want: 60 * time.Second,
			why:  "60 s covers the measured 25.28 s cold bge-m3 load with margin and is what an operator would set",
		},
		{
			name: "above the ceiling is clamped, not rejected", timeoutSecs: 600,
			want: EmbeddingRequestTimeoutCeiling,
			why: "clamped rather than rejected because an out-of-range value in a persisted blob must not stop " +
				"the server starting; the ceiling is the largest value that keeps the 3-attempt retry loop under " +
				"the 5m stuck-op watchdog, so 600 would get the OPERATION killed instead of the batch failing",
		},
		{
			name:        "exactly at the ceiling is allowed, not clamped past",
			timeoutSecs: int(EmbeddingRequestTimeoutCeiling / time.Second),
			want:        EmbeddingRequestTimeoutCeiling,
			why:         "an off-by-one in the clamp would silently move an operator's deliberate maximum",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Config{}
			c.Embedding.RequestTimeoutSeconds = tt.timeoutSecs

			if got := c.ResolveEmbeddingRequestTimeout(); got != tt.want {
				t.Errorf("timeout = %s, want %s\nwhy it matters: %s", got, tt.want, tt.why)
			}
		})
	}
}

// The ceiling bounds ONE attempt, but ai.EmbeddingClient.embedBatchRaw makes
// up to three attempts per batch with 1 s + 4 s of backoff between them and
// reports no progress inside that loop, so the stuck-op watchdog (registry
// defaultProgressTimeout, 5m) sees the whole loop. Asserted rather than
// commented because the attempt count, the backoff and the watchdog default
// all live in other packages and nothing else would notice them drifting --
// the failure mode is scans killed for inactivity in prod, silently.
func TestEmbeddingRequestTimeoutCeilingStaysUnderWatchdog(t *testing.T) {
	const (
		watchdogProgressTimeout = 5 * time.Minute
		embedAttempts           = 3
		embedBackoffTotal       = 1*time.Second + 4*time.Second
	)

	worstCase := embedAttempts*EmbeddingRequestTimeoutCeiling + embedBackoffTotal
	if worstCase >= watchdogProgressTimeout {
		t.Fatalf("%d attempts x EmbeddingRequestTimeoutCeiling (%s) + %s backoff = %s >= the registry's "+
			"ProgressTimeout (%s): a dead backend would get the operation killed for inactivity "+
			"instead of the batch simply failing",
			embedAttempts, EmbeddingRequestTimeoutCeiling, embedBackoffTotal, worstCase, watchdogProgressTimeout)
	}
}
