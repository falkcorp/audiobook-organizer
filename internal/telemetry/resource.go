// file: internal/telemetry/resource.go
// version: 1.3.0
// guid: 3c4d5e6f-7a8b-9c0d-1e2f-3a4b5c6d7e8f
// last-edited: 2026-10-10

package telemetry

import (
	"strings"

	"github.com/oklog/ulid/v2"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.27.0"
)

// deploymentEnvironmentKey is the older "deployment.environment" key, which
// the 11-PR1 brief and spec 11 §3.3 chose. semconv v1.27.0 names the current
// key DeploymentEnvironmentNameKey ("deployment.environment.name"); 11-PR2,
// which adds the telemetry_environment config key, decides whether to move to
// it. The key stays "deployment.environment"; the value comes from
// Config.Environment and defaults to "prod".
const (
	deploymentEnvironmentKey = attribute.Key("deployment.environment")
	defaultEnvironment       = "prod"
)

// instanceID identifies this process: a random ULID drawn once at start-up.
// It is deliberately not the hostname, so the public repo, the scrape and the
// dashboards never carry one.
var instanceID = ulid.Make().String()

// NewResource creates an OpenTelemetry resource for the service: service.name,
// service.version (Version: build info, else SetVersion), service.instance.id
// (per-process, hostname-free) and deployment.environment, on top of the SDK
// defaults (telemetry.sdk.*). It reads no host or process detectors.
func NewResource(serviceName string) *resource.Resource {
	return NewResourceWithEnvironment(serviceName, "")
}

// NewResourceWithEnvironment is NewResource with an explicit
// deployment.environment value; empty means "prod".
func NewResourceWithEnvironment(serviceName, environment string) *resource.Resource {
	if strings.TrimSpace(environment) == "" {
		environment = defaultEnvironment
	}
	own := resource.NewSchemaless(
		semconv.ServiceNameKey.String(serviceName),
		semconv.ServiceVersionKey.String(Version()),
		semconv.ServiceInstanceIDKey.String(instanceID),
		deploymentEnvironmentKey.String(environment),
	)
	r, err := resource.Merge(resource.Default(), own)
	if err != nil {
		// A schemaless resource cannot conflict on schema URL, so this is
		// unreachable today; keep the service's own attributes regardless.
		return own
	}
	return r
}
