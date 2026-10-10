// file: internal/telemetry/meter_test.go
// version: 1.0.0
// guid: b96ec399-4cc6-465c-9358-3a370b9821b3
// last-edited: 2026-10-09

package telemetry

import (
	"context"
	"runtime/debug"
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// stubVersion makes Version() deterministic for one test: build info reports
// buildInfoVersion (ok=false when it is "-"), and SetVersion was given
// fallback. Both are restored afterwards.
func stubVersion(t *testing.T, buildInfoVersion, fallback string) {
	t.Helper()
	savedRead := readBuildInfo
	versionMu.RLock()
	savedFallback := fallbackVersion
	versionMu.RUnlock()
	readBuildInfo = func() (*debug.BuildInfo, bool) {
		if buildInfoVersion == "-" {
			return nil, false
		}
		return &debug.BuildInfo{Main: debug.Module{Path: "example.test/module", Version: buildInfoVersion}}, true
	}
	SetVersion(fallback)
	t.Cleanup(func() {
		readBuildInfo = savedRead
		SetVersion(savedFallback)
	})
}

func TestMeter_ScopeAndVersion(t *testing.T) {
	stubVersion(t, "v1.2.3", "ignored")

	// A private provider, never otel.SetMeterProvider: the first global set
	// in a test binary binds the OTel delegate for the whole process.
	reader := sdkmetric.NewManualReader()
	c, err := meterFrom(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)), "aidispatch").
		Int64Counter("audiobook_organizer.test.things")
	if err != nil {
		t.Fatal(err)
	}
	c.Add(context.Background(), 1)

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	if len(rm.ScopeMetrics) != 1 {
		t.Fatalf("got %d scopes, want 1", len(rm.ScopeMetrics))
	}
	scope := rm.ScopeMetrics[0].Scope
	if scope.Name != "audiobook-organizer/aidispatch" {
		t.Errorf("scope name %q, want audiobook-organizer/aidispatch", scope.Name)
	}
	if scope.Version != "v1.2.3" {
		t.Errorf("scope version %q, want the build-info version v1.2.3", scope.Version)
	}
}

func TestVersion_Sources(t *testing.T) {
	for _, tc := range []struct{ buildInfo, fallback, want string }{
		{"v1.2.3", "v0.0.1", "v1.2.3"},
		{"(devel)", "v0.0.1", "v0.0.1"},
		{"", "v0.0.1", "v0.0.1"},
		{"-", "v0.0.1", "v0.0.1"},
		{"(devel)", "", "unknown"},
	} {
		stubVersion(t, tc.buildInfo, tc.fallback)
		if got := Version(); got != tc.want {
			t.Errorf("build info %q, SetVersion %q: Version() = %q, want %q", tc.buildInfo, tc.fallback, got, tc.want)
		}
	}
}
