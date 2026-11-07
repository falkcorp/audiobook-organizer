// file: internal/telemetry/resource.go
// version: 2.0.0
// guid: 3c4d5e6f-7a8b-9c0d-1e2f-3a4b5c6d7e8f
// last-edited: 2026-11-07

package telemetry

import "github.com/oklog/ulid/v2"

// The resource itself (service.name, service.version, service.instance.id,
// deployment.environment over the SDK defaults, no host detector) is built by
// core (mediacore/metrics, 05-metrics §4). ABO supplies its values through
// metricsOptions: the service name from Config, the version from Version(),
// and this process's instance ID.

// instanceID identifies this process: a random ULID drawn once at start-up
// and handed to every core Provider this façade builds, so the metrics and
// tracing halves report one service.instance.id. It is deliberately not the
// hostname, so the public repo, the scrape and the dashboards never carry one.
var instanceID = ulid.Make().String()
