// file: internal/telemetry/resource_test.go
// version: 2.0.0
// guid: 824d0525-cb06-42a0-900b-7a3b1e412347
// last-edited: 2026-11-07

package telemetry

// The resource is built by core (core/metrics/resource.go, tested there).
// These tests pin what ABO feeds it, and read the result back off the served
// target_info family of a Provider built from ABO's own options.

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/metrics"
)

func TestResource_VersionFromBuildInfo(t *testing.T) {
	cfg := LoadConfig("svc", "")
	stubVersion(t, "v1.2.3", "v0.0.1")
	if got := metricsOptions(cfg).ServiceVersion; got != "v1.2.3" {
		t.Errorf("service.version = %q, want the build-info version v1.2.3", got)
	}

	stubVersion(t, "(devel)", "v0.0.1")
	if got := metricsOptions(cfg).ServiceVersion; got != "v0.0.1" {
		t.Errorf("service.version = %q, want the SetVersion fallback v0.0.1 when build info says (devel)", got)
	}

	stubVersion(t, "(devel)", "")
	if got := metricsOptions(cfg).ServiceVersion; got != unknownVersion {
		t.Errorf("service.version = %q with no version source; want %q, never a hard-coded release", got, unknownVersion)
	}
}

// targetInfo returns the label pairs of the target_info series p serves.
func targetInfo(t *testing.T, p *metrics.Provider) map[string]string {
	t.Helper()
	mfs, err := p.Gatherer().Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "target_info" {
			continue
		}
		out := map[string]string{}
		for _, lp := range mf.GetMetric()[0].GetLabel() {
			out[lp.GetName()] = lp.GetValue()
		}
		return out
	}
	t.Fatal("no target_info family")
	return nil
}

func TestResource_NoHostname(t *testing.T) {
	ctx := context.Background()
	p, err := metrics.New(ctx, metricsProviderOptions(LoadConfig("svc", "")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Shutdown(ctx) })
	attrs := targetInfo(t, p)
	if attrs["service_name"] != "svc" {
		t.Errorf("service.name = %q, want svc", attrs["service_name"])
	}
	if attrs["deployment_environment"] != metrics.DefaultEnvironment {
		t.Errorf("deployment.environment = %q, want %q", attrs["deployment_environment"], metrics.DefaultEnvironment)
	}
	id := attrs["service_instance_id"]
	if id == "" || id != instanceID {
		t.Fatalf("service.instance.id = %q, want this process's instanceID %q", id, instanceID)
	}
	// Every Provider the façade builds reports the same instance.
	if again := metricsOptions(LoadConfig("svc", "tempo:4317")).InstanceID; again != id {
		t.Errorf("service.instance.id changed within one process: %q then %q", id, again)
	}
	host, _ := os.Hostname()
	for k, v := range attrs {
		if strings.HasPrefix(k, "host_") {
			t.Errorf("resource carries host attribute %s=%q", k, v)
		}
		if host != "" && strings.Contains(v, host) {
			t.Errorf("resource attribute %s=%q contains the hostname", k, v)
		}
	}
}
