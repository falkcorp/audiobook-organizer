// file: internal/telemetry/telemetry.go
// version: 4.0.0
// guid: 2b3c4d5e-6f7a-8b9c-0d1e-2f3a4b5c6d7e
// last-edited: 2026-11-08

package telemetry

import (
	"strconv"
	"strings"
	"time"
)

// The provider construction, the process-global installs (meter and tracer
// providers, propagator, error handler, SDK log sink) and the Prometheus
// registry are core's: the kernel builds the Provider inside app.New and
// app.InstallProcessDefaults installs it. What stays here is ABO's
// instrument naming (meter.go, attr.go, views.go) and the env-value parser.

// ParseMetricsInterval parses an OTEL_METRIC_EXPORT_INTERVAL value. The OTel
// standard for that variable is a bare integer count of MILLISECONDS ("60000"),
// and the SDK reads it too, so that form is accepted; a Go duration ("30s",
// "2m") is accepted as well. Empty, unparsable or non-positive input gives 0,
// which core treats as "use the 60s default".
func ParseMetricsInterval(s string) time.Duration {
	s = strings.TrimSpace(s)
	if ms, err := strconv.ParseInt(s, 10, 64); err == nil {
		const ceiling = 24 * time.Hour // keeps the multiplication from overflowing; core clamps later
		if ms <= 0 {
			return 0
		}
		if ms > int64(ceiling/time.Millisecond) {
			return ceiling
		}
		return time.Duration(ms) * time.Millisecond
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0
	}
	return d
}
