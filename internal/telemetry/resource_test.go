// file: internal/telemetry/resource_test.go
// version: 1.0.0
// guid: 824d0525-cb06-42a0-900b-7a3b1e412347
// last-edited: 2026-10-09

package telemetry

import (
	"os"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
)

func resourceAttrs(r *resource.Resource) map[attribute.Key]string {
	out := map[attribute.Key]string{}
	for _, kv := range r.Attributes() {
		out[kv.Key] = kv.Value.String()
	}
	return out
}

func TestResource_VersionFromBuildInfo(t *testing.T) {
	stubVersion(t, "v1.2.3", "v0.0.1")
	if got := resourceAttrs(NewResource("svc"))["service.version"]; got != "v1.2.3" {
		t.Errorf("service.version = %q, want the build-info version v1.2.3", got)
	}

	stubVersion(t, "(devel)", "v0.0.1")
	if got := resourceAttrs(NewResource("svc"))["service.version"]; got != "v0.0.1" {
		t.Errorf("service.version = %q, want the SetVersion fallback v0.0.1 when build info says (devel)", got)
	}

	stubVersion(t, "(devel)", "")
	if got := resourceAttrs(NewResource("svc"))["service.version"]; got != unknownVersion {
		t.Errorf("service.version = %q with no version source; want %q, never a hard-coded release", got, unknownVersion)
	}
}

func TestResource_NoHostname(t *testing.T) {
	attrs := resourceAttrs(NewResource("svc"))
	if attrs["service.name"] != "svc" {
		t.Errorf("service.name = %q, want svc", attrs["service.name"])
	}
	if attrs["deployment.environment"] != "prod" {
		t.Errorf("deployment.environment = %q, want prod", attrs["deployment.environment"])
	}
	id := attrs["service.instance.id"]
	if id == "" {
		t.Fatal("service.instance.id is missing")
	}
	if again := resourceAttrs(NewResource("svc"))["service.instance.id"]; again != id {
		t.Errorf("service.instance.id changed within one process: %q then %q", id, again)
	}
	host, _ := os.Hostname()
	for k, v := range attrs {
		if strings.HasPrefix(string(k), "host.") {
			t.Errorf("resource carries host attribute %s=%q", k, v)
		}
		if host != "" && strings.Contains(v, host) {
			t.Errorf("resource attribute %s=%q contains the hostname", k, v)
		}
	}
}
