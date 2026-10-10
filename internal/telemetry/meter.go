// file: internal/telemetry/meter.go
// version: 1.0.1
// guid: 3e198161-ea71-41ea-aec3-4900357db30b
// last-edited: 2026-10-10

package telemetry

import (
	"runtime/debug"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// ScopePrefix is prepended to every meter name handed out by Meter.
const ScopePrefix = "audiobook-organizer/"

// Meter returns the meter for a package. name is the Go package path suffix
// ("aidispatch", "operations/registry", "plugins/deluge"); the scope is
// "audiobook-organizer/<name>" and is NOT exported as a label (scope labels
// are off, see initMetrics), so it only identifies the instrument in OTLP
// consumers.
//
// Instrument naming (enforced by TestInstrumentNames in
// internal/telemetry/contract over the series golden):
//   - dotted, lowercase, prefixed: "audiobook_organizer.<area>.<what>"
//     (the exporter turns dots into underscores: audiobook_organizer_<area>_<what>);
//   - counters are nouns ("...removed"), never "..._total": the exporter appends it;
//   - histograms name the quantity ("...duration") and set WithUnit("s"), which
//     yields "<name>_seconds"; sizes use WithUnit("By") -> "_bytes". Every
//     histogram needs an entry in the views table (views.go);
//   - gauges whose legacy Prometheus name ends in "_total" (books_total,
//     search_index_docs_total, import_paths_total) keep that literal name:
//     the exporter adds no suffix to a gauge; they use a bracket unit
//     ("{book}"), never "1", which would append "_ratio";
//   - attribute keys come from attr.go;
//   - the aidispatch family ("ai_dispatch.") and the AI call family ("ai.")
//     are the unprefixed legacy exceptions.
func Meter(name string) metric.Meter {
	return meterFrom(otel.GetMeterProvider(), name)
}

// MeterFrom is Meter against an explicit provider, for a component that takes
// its provider by injection so a test can pass a private one.
func MeterFrom(mp metric.MeterProvider, name string) metric.Meter { return meterFrom(mp, name) }

// meterFrom is Meter against an explicit provider. Tests use it with a private
// provider: calling otel.SetMeterProvider in a test binary binds the global
// delegate to that provider for the rest of the process.
func meterFrom(mp metric.MeterProvider, name string) metric.Meter {
	return mp.Meter(ScopePrefix+name, metric.WithInstrumentationVersion(Version()))
}

// readBuildInfo is debug.ReadBuildInfo, replaceable by tests.
var readBuildInfo = debug.ReadBuildInfo

var (
	versionMu       sync.RWMutex
	fallbackVersion string
)

// SetVersion records the version the binary was stamped with at link time
// (main.version, set by -ldflags "-X main.version=..."). Every build path sets
// only main.version, so main.go hands it over here; a telemetry-local ldflags
// variable would never be populated. Call it before InitOTEL.
func SetVersion(v string) {
	versionMu.Lock()
	fallbackVersion = v
	versionMu.Unlock()
}

// unknownVersion is reported when neither build info nor SetVersion supplied
// a version.
const unknownVersion = "unknown"

// Version is the service version for the OTel resource and the meter
// instrumentation version: the main module version from the binary's build
// info, or, when that is empty or "(devel)" (a plain `go build` or `go test`
// outside a tagged module), the value passed to SetVersion.
func Version() string {
	if bi, ok := readBuildInfo(); ok && bi != nil {
		if v := bi.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	versionMu.RLock()
	v := fallbackVersion
	versionMu.RUnlock()
	if v != "" {
		return v
	}
	return unknownVersion
}
