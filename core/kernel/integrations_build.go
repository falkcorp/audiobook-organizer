// file: core/kernel/integrations_build.go
// version: 0.1.0
// guid: fa9a0cf7-69b6-454e-9f0b-c1f61181b487
// last-edited: 2026-11-22

package kernel

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"slices"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/ai"
	"github.com/falkcorp/mediacore/integrations/download"
	"github.com/falkcorp/mediacore/integrations/notify"
	"github.com/falkcorp/mediacore/integrations/providerhttp"
	"github.com/falkcorp/mediacore/internal/instancestore"
	"github.com/falkcorp/mediacore/library"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/storage"
	"go.opentelemetry.io/otel/attribute"
)

// integrationsCloser is one built part and how to stop it.
type integrationsCloser struct {
	name string
	fn   func(ctx context.Context) error
}

// integrationsServices are the services the F7 flip builds, in build order:
// providerhttp, the integrations Registry, the ai Router, the library Manager,
// the download Pool, the notify Service. Everything is built over explicit
// inputs; nothing reads a package variable.
type integrationsServices struct {
	in  integrationsInputs
	log *slog.Logger

	providerhttp *providerhttp.Registry
	registry     *integrations.Registry
	ai           *ai.Router
	library      library.Manager
	downloads    *download.Pool
	notify       *notify.Service

	closers []integrationsCloser // in build order; close runs them in reverse
}

// integrationsDeps is what a module's Deps takes from the integrations
// services (06-app-sdk Deps.Library, HTTP, Integrations, Downloads, AI,
// Notify). B3-a copies these into module.Deps.
type integrationsDeps struct {
	Library      library.Scope          // the module's owner bound in; nil only for an empty module ID
	HTTP         *providerhttp.Registry // instance-scoped outbound clients
	Integrations *integrations.Registry // the one instance store
	Downloads    *download.Pool
	AI           *ai.Router
	Notify       notify.Publisher // notify.Service.For(module)
}

// buildIntegrations builds every part over in, in the fixed order, and writes
// nothing the parts do not write themselves on construction. A failure part-way
// closes what was already built, in reverse, and returns the failure joined
// with any close error, with no services.
//
// It registers in.Adapters under origin "core" (a core.* adapter ID only comes
// from app.Options.Adapters, 05-metadata M1.4 step 1); module adapters go
// through registerAdapters, and load reads the store once both are done.
func buildIntegrations(ctx context.Context, in integrationsInputs) (*integrationsServices, error) {
	// An interface holding a nil pointer would pass the constructors' own nil
	// checks and fail later; normalize once, here.
	if isNil(in.Audit) {
		in.Audit = nil
	}
	if isNil(in.Secrets) {
		in.Secrets = nil
	}
	s := &integrationsServices{in: in, log: in.logger("integrations")}
	steps := []struct {
		name  string
		build func(ctx context.Context) error
	}{
		{"providerhttp", s.buildProviderHTTP},
		{"integrations", s.buildRegistry},
		{"ai", s.buildAI},
		{"library", s.buildLibrary},
		{"downloads", s.buildDownloads},
		{"notify", s.buildNotify},
	}
	for _, st := range steps {
		if err := st.build(ctx); err != nil {
			err = fmt.Errorf("kernel: integrations: %s: %w", st.name, err)
			return nil, errors.Join(err, s.close(ctx))
		}
		s.log.Debug("integrations: step done", slog.String("part", st.name))
	}
	return s, nil
}

// built records a part as built, so close stops it.
func (s *integrationsServices) built(name string, fn func(ctx context.Context) error) {
	s.closers = append(s.closers, integrationsCloser{name: name, fn: fn})
	s.log.Debug("integrations: part built", slog.String("part", name))
}

// close stops every built part in the reverse of the build order and returns
// the joined errors; a failing part does not stop the ones before it. A second
// call closes nothing.
func (s *integrationsServices) close(ctx context.Context) error {
	var errs []error
	for _, c := range slices.Backward(s.closers) {
		if err := c.fn(ctx); err != nil {
			errs = append(errs, fmt.Errorf("kernel: integrations: close %s: %w", c.name, err))
		}
		s.log.Debug("integrations: part closed", slog.String("part", c.name))
	}
	s.closers = nil
	return errors.Join(errs...)
}

// keyspace is the keyspace of owner, or an error that names the missing
// storageOwners entry.
func (s *integrationsServices) keyspace(owner storage.Owner) (storage.Keyspace, error) {
	if s.in.Keyspace == nil {
		return storage.Keyspace{}, fmt.Errorf("no storage handle for owner %s (build after P4a)", owner)
	}
	ks, ok := s.in.Keyspace(owner)
	if !ok {
		return storage.Keyspace{}, fmt.Errorf("owner %s has no keyspace: append integrationsOwners() to storageOwners (F7-09)", owner)
	}
	return ks, nil
}

// scope is the core metrics scope <namespace>.<subsystem>, created once from
// in.Metrics; nil Metrics gives nil, which records nothing.
func (s *integrationsServices) scope(subsystem string, keys ...attribute.Key) (*metrics.Scope, error) {
	if s.in.Metrics == nil {
		return nil, nil
	}
	sc, err := s.in.Metrics.Core(subsystem, keys...)
	if err != nil {
		return nil, fmt.Errorf("%s metrics scope: %w", subsystem, err)
	}
	return sc, nil
}

// The section readers, each defaulting to the zero section.
func (s *integrationsServices) integrationsSection() integrationsSettings {
	if s.in.Integrations == nil {
		return integrationsSettings{}
	}
	return s.in.Integrations()
}

// disabled is core.integrations.disabled, the one switch the Registry,
// providerhttp and the ai Router each read per call.
func (s *integrationsServices) disabled() bool { return s.integrationsSection().Disabled }

// registrySettings is core.integrations as integrations.Settings.
func (s *integrationsServices) registrySettings() integrations.Settings {
	v := s.integrationsSection()
	return integrations.Settings{
		Disabled:            v.Disabled,
		DrainTimeout:        v.DrainTimeout.Std(),
		ProbeTimeout:        v.ProbeTimeout.Std(),
		HealthParallelism:   v.HealthParallelism,
		MaxInstancesPerKind: v.MaxInstancesPerKind,
	}
}

func (s *integrationsServices) providerHTTPSection() providerHTTPSettings {
	if s.in.ProviderHTTP == nil {
		return providerHTTPSettings{}
	}
	return s.in.ProviderHTTP()
}

// providerSettings is core.providerhttp as providerhttp.Settings.
func (s *integrationsServices) providerSettings() providerhttp.Settings {
	v := s.providerHTTPSection()
	return providerhttp.Settings{
		Fallback: integrations.Limits{
			RPS: v.Fallback.RPS, Burst: v.Fallback.Burst,
			MaxRetries: v.Fallback.MaxRetries, Timeout: v.Fallback.Timeout.Std(),
		},
		RetryAfterMax: v.RetryAfterMax.Std(),
		HoldMax:       v.Hold.Max.Std(),
	}
}

// buildProviderHTTP builds providerhttp over c/providerhttp/. LANAllow and
// Disabled come from core.integrations; the settings, the user agent and the
// limits from core.providerhttp.
func (s *integrationsServices) buildProviderHTTP(_ context.Context) error {
	ks, err := s.keyspace(providerhttp.Owner)
	if err != nil {
		return err
	}
	sc, err := s.scope("providerhttp", "kind", "instance", "class", "reason")
	if err != nil {
		return err
	}
	r, err := providerhttp.New(providerhttp.Options{
		Store:     providerhttp.NativeGateStore(ks),
		UserAgent: s.providerHTTPSection().UserAgent,
		Metrics:   sc,
		Log:       s.in.logger("providerhttp"),
		Now:       s.in.now(),
		Settings:  s.providerSettings,
		Disabled:  s.disabled,
		LANAllow:  func() []string { return s.integrationsSection().Egress.LANAllow },
	})
	if err != nil {
		return err
	}
	s.providerhttp = r
	s.built("providerhttp", func(context.Context) error { r.Close(); return nil })
	return nil
}

// buildRegistry builds the integrations Registry over c/integrations/ with
// providerhttp as its outbound layer: Outbound builds each instance's client
// and gate, ReleaseAuthHold ends the hold old credentials earned after a Put
// that wrote a new secret, and Forget drops the outbound state of a deleted
// instance. HealthChanged publishes the health topic on a status move; Probed
// is the input (nil until Q7).
func (s *integrationsServices) buildRegistry(_ context.Context) error {
	ks, err := s.keyspace(instancestore.Owner)
	if err != nil {
		return err
	}
	sc, err := s.scope("integrations", "kind", "adapter", "instance", "status")
	if err != nil {
		return err
	}
	// A typed-nil audit logger would satisfy the Registry's nil check.
	var rec integrations.AuditRecorder
	if s.in.Audit != nil {
		rec = s.in.Audit
	}
	var secret integrations.SecretStore
	if s.in.Secrets != nil {
		secret = s.in.Secrets
	}
	ph := s.providerhttp
	r, err := integrations.New(integrations.Options{
		Store:             integrations.NativeInstanceStore(ks),
		NewBatch:          ks.NewBatch,
		Secrets:           secret,
		Audit:             rec,
		Outbound:          ph.Outbound,
		ReleaseAuthHold:   ph.ReleaseAuthHold,
		Forget:            ph.Forget,
		HealthChanged:     publishHealth(s.in.Publisher),
		Probed:            s.in.Probed,
		Settings:          s.registrySettings,
		OperatorInstances: s.in.OperatorInstances,
		Metrics:           sc,
		Logger:            s.in.logger("integrations"),
		Now:               s.in.now(),
	})
	if err != nil {
		return err
	}
	s.registry = r
	s.built("integrations", r.Close)
	if len(s.in.Adapters) > 0 {
		if err := s.registerAdapters("core", s.in.Adapters...); err != nil {
			return err
		}
	}
	return nil
}

// registerAdapters registers ds under origin: "core" for app.Options.Adapters,
// a module ID for its Registrar.Adapter. Valid before load only.
func (s *integrationsServices) registerAdapters(origin string, ds ...integrations.Descriptor) error {
	return s.registry.Register(origin, ds...)
}

// load reads the instance store and applies the operator overlay (M1.4 step 2),
// once, after every adapter is registered. overlay nil reads in.Overlay. The
// Registry serves a store it cannot read as closed for stored instances and
// returns the cause for the kernel to log; it is not a boot failure.
func (s *integrationsServices) load(ctx context.Context, overlay iter.Seq2[config.Path, config.OperatorValue]) error {
	if overlay == nil {
		overlay = s.in.Overlay
	}
	return s.registry.Load(ctx, overlay)
}

// probeAll is the body of the core.integrations.health task: every instance is
// probed, at most core.integrations.health_parallelism at once. Each status
// move publishes the health topic through the Registry's HealthChanged hook.
func (s *integrationsServices) probeAll(ctx context.Context) error {
	return s.registry.ProbeAll(ctx)
}

// depsFor is what module's Deps takes from the integrations services. An empty
// module ID has no library Scope (library.NewScope refuses an empty owner).
func (s *integrationsServices) depsFor(module string) integrationsDeps {
	d := integrationsDeps{
		HTTP: s.providerhttp, Integrations: s.registry, Downloads: s.downloads, AI: s.ai,
		Notify: s.notify.For(module),
	}
	if sc, err := library.NewScope(s.library, storage.Owner(module)); err == nil {
		d.Library = sc
	} else {
		s.log.Warn("integrations: no library scope for module", slog.String("module", module), slog.Any("err", err))
	}
	return d
}
