// file: internal/telemetry/attr.go
// version: 1.1.0
// guid: 7f188355-4355-41fd-ac81-763e2e6c74a6
// last-edited: 2026-10-10

package telemetry

import (
	"slices"
	"strings"

	"go.opentelemetry.io/otel/attribute"
)

// Attribute keys allowed on metric instruments (spec 11 §3.5, rule C3). A new
// key is a reviewed addition to this file. Two lists:
//
//   - AttributeKeys: what a NEW instrument created through Meter may use.
//     TestInstrumentNames in internal/telemetry/contract rejects any other key.
//   - ScrapeOnlyAttributeKeys: keys that exist on /metrics today but no new
//     instrument may adopt (the legacy op_id label, and the semconv keys
//     otelgin sets). Only the golden and scrape checks accept them.
//
// TestAttributeKeysAllowlisted fails when any label on the golden or the
// /metrics scrape is in neither list (dots compared as underscores, the
// exporter's translation).
//
// Cardinality rule C1: a key's value set must be closed and small
// (enumerations, plugin names, fixer ids, def ids). Never op/run ids, book or
// file ids, paths, user ids, URLs, free text or timestamps.
const (
	Outcome    = attribute.Key("outcome")
	DefID      = attribute.Key("def_id")
	Provider   = attribute.Key("provider")
	Model      = attribute.Key("model")
	Task       = attribute.Key("task")
	Reason     = attribute.Key("reason")
	Direction  = attribute.Key("direction")
	Class      = attribute.Key("class")
	Kind       = attribute.Key("kind")
	Plugin     = attribute.Key("plugin")
	State      = attribute.Key("state")
	Phase      = attribute.Key("phase")
	Accepted   = attribute.Key("accepted")
	Capability = attribute.Key("capability")
	Endpoint   = attribute.Key("endpoint")
)

// Keys already carried by the client_golang families pinned in the series
// golden. They are listed so those families can move to OTel with identical
// label sets.
const (
	Type    = attribute.Key("type")
	Field   = attribute.Key("field")
	Alias   = attribute.Key("alias")
	Entry   = attribute.Key("entry")
	Cache   = attribute.Key("cache")
	Scope   = attribute.Key("scope")
	Backend = attribute.Key("backend")
	OpType  = attribute.Key("op_type")
	Shape   = attribute.Key("shape")
	Source  = attribute.Key("source")
	View    = attribute.Key("view")
	Fixer   = attribute.Key("fixer")
	Store   = attribute.Key("store")
	Level   = attribute.Key("level")
	// OpID breaks rule C1 (one value per operation run). It exists only on
	// the legacy op_items_processed/op_items_total gauges, which 11-PR4
	// replaces; it is scrape-only, so no new instrument may use it.
	OpID = attribute.Key("op_id")
)

// Keys otelgin sets from the OTel HTTP semantic conventions. They are not
// ours to name, but they reach /metrics, so the scrape-only list names them.
const (
	HTTPRequestMethod      = attribute.Key("http.request.method")
	HTTPResponseStatusCode = attribute.Key("http.response.status_code")
	HTTPRoute              = attribute.Key("http.route")
	NetworkProtocolName    = attribute.Key("network.protocol.name")
	NetworkProtocolVersion = attribute.Key("network.protocol.version")
	ServerAddress          = attribute.Key("server.address")
	ServerPort             = attribute.Key("server.port")
	URLScheme              = attribute.Key("url.scheme")
	ErrorType              = attribute.Key("error.type")
)

// instrumentKeys may be used by any instrument created through Meter.
var instrumentKeys = []attribute.Key{
	Outcome, DefID, Provider, Model, Task, Reason, Direction, Class, Kind, Plugin,
	State, Phase, Accepted, Capability, Endpoint,
	Type, Field, Alias, Entry, Cache, Scope, Backend, OpType, Shape, Source, View,
	Fixer, Store, Level,
}

// scrapeOnlyKeys are on /metrics today but closed to new instruments.
var scrapeOnlyKeys = []attribute.Key{
	OpID,
	HTTPRequestMethod, HTTPResponseStatusCode, HTTPRoute, NetworkProtocolName,
	NetworkProtocolVersion, ServerAddress, ServerPort, URLScheme, ErrorType,
}

// AttributeKeys returns the keys a new instrument may use, sorted.
func AttributeKeys() []attribute.Key {
	out := slices.Clone(instrumentKeys)
	slices.Sort(out)
	return out
}

// ScrapeOnlyAttributeKeys returns the keys accepted on the golden and the
// scrape but rejected on a new instrument, sorted.
func ScrapeOnlyAttributeKeys() []attribute.Key {
	out := slices.Clone(scrapeOnlyKeys)
	slices.Sort(out)
	return out
}

// PrometheusLabelName is the label name the Prometheus exporter writes for an
// attribute key: dots become underscores.
func PrometheusLabelName(k attribute.Key) string {
	return strings.ReplaceAll(string(k), ".", "_")
}
