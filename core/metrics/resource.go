// file: core/metrics/resource.go
// version: 1.0.0
// guid: 3ffbaeec-e5d8-4cb0-8d79-73350b4a3e57
// last-edited: 2026-11-07

package metrics

import (
	"crypto/rand"
	"encoding/hex"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.27.0"
)

// DefaultEnvironment is the deployment.environment value when
// Options.Environment is empty (core.metrics.environment's default).
const DefaultEnvironment = "prod"

// deploymentEnvironmentKey is the older "deployment.environment" key, which
// ABO's dashboards and target_info already carry; semconv v1.27.0's current
// name is deployment.environment.name. Moving to it is a target_info label
// rename and needs its own PR.
const deploymentEnvironmentKey = attribute.Key("deployment.environment")

type resourceOption = *resource.Resource

// newResource builds the Provider's resource: service.name, service.version,
// service.instance.id and deployment.environment over the SDK defaults
// (telemetry.sdk.*, and OTEL_RESOURCE_ATTRIBUTES / OTEL_SERVICE_NAME as
// resource.Default reads them). No host or process detector runs, so no
// hostname reaches target_info.
func newResource(serviceName, version, instanceID, environment string) *resource.Resource {
	if strings.TrimSpace(environment) == "" {
		environment = DefaultEnvironment
	}
	own := resource.NewSchemaless(
		semconv.ServiceNameKey.String(serviceName),
		semconv.ServiceVersionKey.String(version),
		semconv.ServiceInstanceIDKey.String(instanceID),
		deploymentEnvironmentKey.String(environment),
	)
	r, err := resource.Merge(resource.Default(), own)
	if err != nil {
		// A schemaless resource cannot conflict on schema URL, so this is
		// unreachable; keep the service's own attributes regardless.
		return own
	}
	return r
}

// randomInstanceID is a random 128-bit hex string, the instance ID of a
// Provider whose Options name none.
func randomInstanceID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never returns an error
	return hex.EncodeToString(b[:])
}
