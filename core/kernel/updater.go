// file: core/kernel/updater.go
// version: 0.1.0
// guid: cb31ce6d-094c-4aa1-b5ce-2b28612ebca6
// last-edited: 2026-11-22

package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/integrations/notify"
	"github.com/falkcorp/mediacore/integrations/providerhttp"
	"github.com/falkcorp/mediacore/internal/updaterhttp"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/updater"
)

// The kernel's updater adapter (F8-04b slice 8b-3; 05-updater §5U.3, §5U.10,
// §5U.12; seams.d/F8-02.md, "Left for F8-04"). It is dark: it holds
// constructors only. Nothing here is called from boot.go or kernel.go, and
// nothing is registered: no topic in coreTopics, no owner in storageOwners, no
// component in the health registry, no route, no task. The F8-04b flip turns
// each constructor into one call site:
//
//   - coreTopics: append updaterTopicSpecs()...
//   - storageOwners: append updaterOwnerSpec()
//   - registerCoreComponents: coreComponentSources.Updater = updaterHealthSource(u)
//   - the served App's updater: newKernelUpdater(updaterOptions(in), svc, ...)
//   - the router's core group: mountUpdater(r.CoreGroup(), u, newUpdaterStarter(client), log)
//
// The app, not this file, names ABO: AppID, BinaryName, the release Source and
// the asset namer all arrive as parameters (ABO's come from
// internal/books/options.go through app.Options.Updater), so core holds no
// second literal of them.

// updaterOwnerID is core/updater's owner in the storage claim ledger: the
// owner of the c/updater/ keyspace, whose one key is "seen" (5U.4).
const updaterOwnerID storage.Owner = "core/updater"

// updaterInputs are what the app hands over: app.Options.Updater's fields plus
// the core.updater section's default mode. updater.New refuses a blank AppID or
// BinaryName and a nil Source, so they are not re-checked here. A nil
// AssetName leaves core's RawBinary default; ABO sets it (FetchOptions, owner
// ruling O-5).
type updaterInputs struct {
	AppID       string
	BinaryName  string
	Source      updater.Source
	AssetName   func(r updater.Release, goos, goarch string) string
	DefaultMode updater.Mode
}

// updaterOptions is the updater.Options for in.
func updaterOptions(in updaterInputs) updater.Options {
	return updater.Options{
		AppID: in.AppID, BinaryName: in.BinaryName, Source: in.Source,
		AssetName: in.AssetName, DefaultMode: in.DefaultMode,
	}
}

// updaterServices are the kernel services the updater takes (updater.Deps).
// HTTP and Notify are the two hooks F7-09 fills in, and both may be nil:
//
//   - HTTP nil: the release source uses its own https-only client with a 30 s
//     header timeout (core/updater/github), which is HEAD's behaviour. Nothing
//     sets it from a providerhttp registry until 05-updater §5U.13 item 7 names
//     the integrations.Ref.
//   - Notify nil: no core.update.found.v1 notification is staged
//     (core/updater/tasks.go), which is HEAD's behaviour: HEAD's updater sends
//     none. The topic itself is still published on the bus.
//
// Restarter, Events, Store and ReaderCheck are required: an apply without a
// restarter or a reader check would have nothing to refuse with, and the check
// task reads and writes the c/updater/ store. Health is unused by the updater
// (the kernel registers it as a probe, updaterHealthSource); Log may be nil.
type updaterServices struct {
	HTTP        *providerhttp.Client
	Notify      *notify.Service
	Restarter   updater.Restarter
	Health      *health.Registry
	Events      *events.Bus
	Store       storage.Keyspace
	ReaderCheck updater.ReaderCheck
	Log         *slog.Logger
}

// updaterDeps is updater.Deps for s, or an error naming the first required
// service that is missing.
func updaterDeps(s updaterServices) (updater.Deps, error) {
	switch {
	case s.Restarter == nil:
		return updater.Deps{}, errors.New("kernel: updater needs a Restarter")
	case s.Events == nil:
		return updater.Deps{}, errors.New("kernel: updater needs the event bus")
	case s.Store.Owner() != updaterOwnerID:
		// Keyspace is a struct, so "missing" is the zero value, and a handle
		// of another owner would put the "seen" key in that owner's rows.
		return updater.Deps{}, fmt.Errorf("kernel: updater needs the %s keyspace (c/updater/), got owner %q", updaterOwnerID, s.Store.Owner())
	case s.ReaderCheck == nil:
		return updater.Deps{}, errors.New("kernel: updater needs a ReaderCheck")
	}
	return updater.Deps{
		HTTP: s.HTTP, Notify: s.Notify, Restarter: s.Restarter, Health: s.Health,
		Events: s.Events, Store: s.Store, ReaderCheck: s.ReaderCheck, Log: s.Log,
	}, nil
}

// newKernelUpdater builds the updater for in and s, with the updater.Options
// (WithVersion, WithStateDir, WithSettings and the rest) the caller passes.
func newKernelUpdater(in updaterInputs, s updaterServices, with ...updater.Option) (*updater.Updater, error) {
	d, err := updaterDeps(s)
	if err != nil {
		return nil, err
	}
	return updater.New(updaterOptions(in), d, with...)
}

// updaterTopicSpecs are the topics the updater adds to core's catalog
// (core.update.found.v1). The flip appends them to coreTopics; until then the
// catalog does not list them.
func updaterTopicSpecs() []events.TopicSpec {
	return []events.TopicSpec{updater.FoundTopicSpec()}
}

// updaterOwnerSpec is the storage claim of core/updater: the c/updater/
// keyspace and nothing else (no claims, no migrations; the one row, "seen", is
// written by the check task). Registering a new owner takes a one-time write in
// the claim ledger at P4b, which belongs to the flip that appends this spec to
// storageOwners.
func updaterOwnerSpec() storage.OwnerSpec {
	return storage.OwnerSpec{Owner: updaterOwnerID, Spec: storage.Spec{Keyspace: true}, Enabled: true}
}

// updaterHealthSource is coreComponentSources.Updater: the core.updater
// component's probe over u (05-updater §5U.9). A nil u yields a nil source, so
// registerCoreComponents leaves the component out: core.updater is registered
// only when the app ships an updater.
func updaterHealthSource(u *updater.Updater) func(ctx context.Context) health.Result {
	if u == nil {
		return nil
	}
	return u.Probe
}

// updaterStarter is updaterhttp.Starter over the executor: the three routes
// that enqueue a task start it as the request's principal, and nothing runs on
// the request goroutine.
type updaterStarter struct{ tasks exportStarter }

var _ updaterhttp.Starter = updaterStarter{}

// newUpdaterStarter is the Starter the routes enqueue through. tasks is the
// executor's tasks.Client; nil makes every start fail rather than panic.
func newUpdaterStarter(tasks exportStarter) updaterhttp.Starter { return updaterStarter{tasks: tasks} }

// errUpdaterActor is a start whose requester is not the request's principal.
// The executor stamps the run from the principal in ctx, so the mismatch is
// refused before a run that names someone else is queued.
var errUpdaterActor = errors.New("kernel: updater start names a different person than the request")

func (s updaterStarter) start(ctx context.Context, kind tasks.Kind, params any, requestedBy string) (string, error) {
	if s.tasks == nil {
		return "", errors.New("kernel: updater starter has no executor")
	}
	pr, ok := auth.PrincipalFrom(ctx)
	if requestedBy == "" || !ok || pr == nil || pr.User == nil || string(pr.User.ID) != requestedBy {
		return "", errUpdaterActor
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return "", fmt.Errorf("kernel: updater params: %w", err)
	}
	ref, err := s.tasks.Start(ctx, kind, tasks.StartRequest{Mode: tasks.ModeRequestLive, Params: raw, Trigger: tasks.TriggerManual})
	if err != nil {
		return "", err
	}
	return string(ref.ID), nil
}

func (s updaterStarter) StartCheck(ctx context.Context, requestedBy string) (string, error) {
	return s.start(ctx, updater.KindCheck, updater.Empty{}, requestedBy)
}

func (s updaterStarter) StartApply(ctx context.Context, p updater.ApplyParams, requestedBy string) (string, error) {
	return s.start(ctx, updater.KindApply, p, requestedBy)
}

func (s updaterStarter) StartRollback(ctx context.Context, requestedBy string) (string, error) {
	return s.start(ctx, updater.KindRollback, updater.Empty{}, requestedBy)
}

// mountUpdater registers the four updater routes (/updater, /updater/check,
// /updater/apply, /updater/rollback) on g, which must be the router's
// CoreGroup(). It refuses a nil group and a nil updater: an app with no
// updater mounts no routes (05-updater §5U.3), not routes that answer 503.
// Every route repeats its permission and guards in its handler
// (updaterhttp), so a mounting mistake never opens one.
func mountUpdater(g *httpapi.Group, u *updater.Updater, st updaterhttp.Starter, log *slog.Logger) error {
	if g == nil || u == nil {
		return errors.New("kernel: updater mount needs the core group and the updater")
	}
	updaterhttp.Register(g, updaterhttp.Deps{Updater: u, Starter: st, Log: log})
	return nil
}
