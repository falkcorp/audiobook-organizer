// file: core/kernel/integrations_flip.go
// version: 0.1.0
// guid: f0cb8e45-ce22-46fa-b116-ef567592f49a
// last-edited: 2026-11-22

package kernel

import (
	"context"
	"iter"
	"log/slog"
	"reflect"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/ai"
	"github.com/falkcorp/mediacore/integrations/notify"
	"github.com/falkcorp/mediacore/integrations/providerhttp"
	"github.com/falkcorp/mediacore/internal/instancestore"
	"github.com/falkcorp/mediacore/library"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/outbox"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// The kernel's integrations constructors (F7-09 B3-b; 05-metadata M1.4, 06
// section 6.10 F7 row). This file is dark: it holds constructors only, and the
// only callers of any of them are tests. Nothing here is called from boot.go
// or kernel.go, and nothing is registered: no owner in storageOwners, no topic
// in coreTopics, no route, no task, no module Deps member. The F7 flip (B3-a)
// turns each constructor into one call site:
//
//   - storageOwners: append integrationsOwners()...;
//   - P5, after the events bus and the outbox exist: buildIntegrations(ctx,
//     in) once. Its in.Catalog and in.Publisher are lazy, so it may run before
//     P7 declares topics;
//   - P6: registerAdapters("core", ...) is already done by buildIntegrations
//     for in.Adapters; each enabled module's Registrar.Adapter goes through
//     registerAdapters(moduleID, ...), then load(ctx, overlay) once, then
//     depsFor(moduleID) fills that module's Deps (Library, HTTP, Integrations,
//     Downloads, AI, Notify);
//   - P7: coreTopics appends integrationsTopicSpecs()...; the outbox registers
//     notifyConsumer(s) under "core"; then freezeAI() once every module has
//     registered its AI endpoints;
//   - the task registrations, built where they are registered (tasklint: a
//     dark helper that builds a tasks.Definition fails TestTasklint_Core, so
//     none is built here): core.integrations.health runs s.probeAll,
//     core.library.transfer-recover is library.TransferRecoverTask over
//     s.library, release-holds and batch-poll are the D1 definitions;
//   - the router's core group: mountIntegrationRoutes(r.CoreGroup(), s) and
//     mountLibraryRoutes(r.CoreGroup(), s.library, log), with
//     describeIntegrationRoutes(r) for Describe and OpenAPI;
//   - stop: s.close(ctx), which closes in the reverse of the build order.
//
// The app, not this file, names ABO: its adapters, its operator instances and
// its notify/AI wiring arrive as inputs.

// integrationsOwners are the storage owners of the integrations flip, claimed
// in the store's ledger when the flip appends them to storageOwners: the
// instance store (c/integrations/), providerhttp's gate state, the AI job
// store, notify's rule store and library's journal. None declares a claim or a
// level step, so none raises the storage level
// (TestIntegrationsOwners_NoClaimsNoLevel).
func integrationsOwners() []storage.OwnerSpec {
	return []storage.OwnerSpec{
		{Owner: instancestore.Owner, Spec: instancestore.StorageSpec(), Enabled: true},
		{Owner: providerhttp.Owner, Spec: providerhttp.StorageSpec(), Enabled: true},
		{Owner: ai.Owner, Spec: ai.StorageSpec(), Enabled: true},
		{Owner: notify.Owner, Spec: notify.StorageSpec(), Enabled: true},
		{Owner: library.Owner, Spec: library.StorageSpec(), Enabled: true},
	}
}

// integrationsTopicSpecs are the topics the integrations flip adds to core's
// catalog: core.integration.health.changed.v1 (05-metadata M1.4 step 5) and
// the durable notify delivery topic, which no other file declares.
func integrationsTopicSpecs() []events.TopicSpec {
	return []events.TopicSpec{integrationHealthTopicSpec(), notify.DeliveryTopicSpec()}
}

// integrationHealthTopicName is the topic the Registry's HealthChanged hook
// publishes on.
const integrationHealthTopicName = "core.integration.health.changed.v1"

// integrationHealthPerm is the permission that observes the topic: the same
// one the integration read routes declare, so a stream or a notify rule shows
// an instance's health to exactly the callers that may read it.
const integrationHealthPerm rbac.Permission = "core:integrations.read"

// integrationHealthChanged is the payload: fixed tokens only. It carries no
// Health.Message, remote version, latency or hold text, because those can hold
// text a remote chose and the topic reaches realtime streams and notify
// channels.
type integrationHealthChanged struct {
	Kind     string `json:"kind"`     // the instance's kind (ai, download, indexer, metadata, notify)
	Instance string `json:"instance"` // the instance ID
	Status   string `json:"status"`   // HealthStatus.String(): ok, degraded, down, auth_failed, misconfigured, disabled
}

// integrationHealthTopic is the bus-only topic (not durable: a missed move is
// re-observed by the next probe).
func integrationHealthTopic() events.Topic[integrationHealthChanged] {
	return events.NewTopic[integrationHealthChanged](integrationHealthTopicName)
}

// integrationHealthTopicSpec is the catalog entry: notifiable (05-notify 3:
// core's notifiable topics are declared by their owning files) at warning
// severity, observed with core:integrations.read.
func integrationHealthTopicSpec() events.TopicSpec {
	return integrationHealthTopic().Spec(events.TopicSpec{
		Permission: rbac.Need(integrationHealthPerm),
		Notify:     &events.NotifyMeta{Title: "Integration health changed", DefaultSeverity: "warning"},
	})
}

// integrationsInputs are what the flip hands buildIntegrations. Every reader
// is called when its value is needed, so a hot reload takes effect; a nil
// reader reads the section's zero value, which every constructor defaults.
type integrationsInputs struct {
	AppID string // app.Options.AppID: the "app" of a webhook body

	// Keyspace is storage.DB.Keyspace: the keyspace of an owner the store
	// opened. A missing one is named by the error (the owner was not appended
	// to storageOwners).
	Keyspace func(storage.Owner) (storage.Keyspace, bool)
	Secrets  integrations.SecretStore

	// The core.* sections the constructors read.
	Integrations func() integrationsSettings
	ProviderHTTP func() providerHTTPSettings
	AI           func() aiSettings
	Notify       func() notifySettings
	Library      func() librarySettings
	Download     func() downloadSettings

	Metrics *metrics.Provider // each constructor creates its own core scope from it, once

	// Catalog and Publisher are lazy: the bus and its catalog are built before
	// P7 declares topics, and a read before that returns nil. Publisher is
	// what HealthChanged publishes through; nil is a publish that goes
	// nowhere.
	Catalog   func() events.Catalog
	Publisher func() events.Publisher

	Outbox *outbox.Outbox
	Stager tasks.Stager
	Audit  audit.Logger

	// Adapters is app.Options.Adapters: the opt-in core adapters, registered
	// with origin "core". OperatorInstances is the app's M1.10 table.
	Adapters          []integrations.Descriptor
	OperatorInstances map[integrations.Ref]integrations.AdapterID
	// Overlay is the operator overlay load reads when its own is nil.
	Overlay iter.Seq2[config.Path, config.OperatorValue]

	// Library's own inputs (library.Options): the install the sentinel names,
	// whether a module is enabled and whether it holds a capability, the
	// seeding oracles of the download adapters, and the paths walkers skip.
	InstallID      string
	ModuleEnabled  func(owner storage.Owner) bool
	HasCapability  func(owner storage.Owner, capability string) bool
	LibraryOracles []library.ProtectionOracle
	ExcludePaths   []string

	// Probed, when set, is the Registry's Probed hook: called after every probe.
	// It is nil until the owner rules on Router.ObserveProbe (F7-09 B3 brief,
	// Q7), and it is an input so the ruling is one assignment.
	Probed func(ref integrations.Ref, h integrations.Health)

	Log func(component string) *slog.Logger // nil: every component logs nowhere
	Now func() time.Time                    // nil: time.Now
}

func (in integrationsInputs) logger(component string) *slog.Logger {
	if in.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	if l := in.Log(component); l != nil {
		return l
	}
	return slog.New(slog.DiscardHandler)
}

func (in integrationsInputs) now() func() time.Time {
	if in.Now == nil {
		return time.Now
	}
	return in.Now
}

// lazyCatalog is an events.Catalog that reads the real one when it is asked,
// so a service built before the bus is frozen sees the topics declared since.
// Before the catalog exists every topic is unknown and the list is empty.
type lazyCatalog struct{ get func() events.Catalog }

func (c lazyCatalog) Lookup(name string) (events.TopicSpec, bool) {
	if c.get != nil {
		if cat := c.get(); cat != nil {
			return cat.Lookup(name)
		}
	}
	return events.TopicSpec{}, false
}

func (c lazyCatalog) All() iter.Seq[events.TopicSpec] {
	return func(yield func(events.TopicSpec) bool) {
		if c.get == nil {
			return
		}
		if cat := c.get(); cat != nil {
			for s := range cat.All() {
				if !yield(s) {
					return
				}
			}
		}
	}
}

// isNil reports whether v is nil or an interface holding a nil pointer, map,
// slice, func or channel. The route and the Registry check an interface
// against nil to fail closed, and an interface holding a nil pointer passes
// that check and fails later (seams.d/F7-10.md "Typed nil at Register"), so
// every pointer the kernel passes as an interface goes through this first.
func isNil(v any) bool {
	if v == nil {
		return true
	}
	switch rv := reflect.ValueOf(v); rv.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan, reflect.Interface:
		return rv.IsNil()
	}
	return false
}

// publishHealth returns the Registry's HealthChanged hook: one event per
// status move on integrationHealthTopic, keyed by the instance. The Registry
// calls the hook after it released its health lock, so the publish (which never
// blocks and never fails) is the whole body. It runs on the probing goroutine.
func publishHealth(pub func() events.Publisher) func(ref integrations.Ref, h integrations.Health) {
	return func(ref integrations.Ref, h integrations.Health) {
		if pub == nil {
			return
		}
		p := pub()
		if p == nil {
			return
		}
		events.Publish(p, context.Background(), integrationHealthTopic(), ref.String(), integrationHealthChanged{
			Kind: string(ref.Kind), Instance: string(ref.Instance), Status: h.Status.String(),
		})
	}
}
