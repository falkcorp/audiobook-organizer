// file: core/kernel/identity_mount.go
// version: 0.1.0
// guid: 91c4e7a2-3b58-4d06-8e1f-0a7d5c92b3e8
// last-edited: 2026-11-21

package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"maps"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/internal/modulehttp"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/version"
)

// This file is where the identity boot meets the router (F4-08 B4): stages 12
// and 13 into the chain and the seven packages' routes on core's groups, both
// behind identityBootDeclared, so an app that did not declare the flip keeps
// the router it had: no stage 12, no stage 13, no identity route, and the
// registration refusals (httpapi.ErrNoAuthService) in force.
//
// The order is the point. mountIdentity runs at the end of P5 (services),
// after the services exist and before any module registers (P6), because
// SetAuth is one predicate for two things, "wrap the routes in stages 12 and
// 13" and "stop refusing non-Public routes", and a module's route registered
// before it would be refused for want of an auth service that is about to
// exist. Core's routes and the modules' therefore all take the same stages.

// mountIdentity is the real boot's: auth is set on the router, then the
// packages mount over the services P5 built. Any problem fails the boot.
func (k *Kernel) mountIdentity(ctx context.Context) error {
	id := k.ident
	if id == nil || id.svc.Auth == nil || id.svc.RBAC == nil || k.audit == nil || k.cfgBoot == nil || k.store == nil {
		return errors.New("kernel: identity: the route wiring needs the services P5 built, the audit store, the config store and the storage")
	}
	svc := id.svc
	cat := svc.RBAC.Catalog()
	if cat == nil {
		// Stage 13 reads the catalog to tell a Sensitive scope from an ordinary
		// one; a nil one would treat every refusal as ordinary without a word.
		return errors.New("kernel: identity: the rbac catalog is not frozen, so stage 13 cannot be wired")
	}
	if err := k.router.SetAuth(httpapi.AuthWiring{
		Service:       svc.Auth,
		Client:        httpapi.ClientInfoFrom,
		OwnerRequired: svc.Auth.OwnerRequired,
		ResolveAtom:   k.cfg.ResolveAtom,
		Catalog:       cat,
		Audit:         k.audit,
		Log:           k.coreLog("http"),
	}); err != nil {
		return fmt.Errorf("kernel: identity: stages 12 and 13: %w", err)
	}
	in, err := k.identityRoutesInput(ctx)
	if err != nil {
		return err
	}
	if err := mountIdentityRoutes(k.router, in); err != nil {
		return err
	}
	return nil
}

// identityRoutesInput is everything mountIdentityRoutes takes, from the boot.
func (k *Kernel) identityRoutesInput(ctx context.Context) (identityRoutes, error) {
	svc := k.ident.svc
	store := k.cfgBoot.store
	accept, err := k.acceptDiagnostics()
	if err != nil {
		return identityRoutes{}, err
	}
	public, err := k.openAPIPublic()
	if err != nil {
		return identityRoutes{}, err
	}
	stager, err := k.newResetStager(ctx)
	if err != nil {
		return identityRoutes{}, fmt.Errorf("kernel: identity: the reset stager: %w", err)
	}
	sc, err := k.uiMetricsScope()
	if err != nil {
		return identityRoutes{}, err
	}
	// The acknowledgement of a restore's admin review works over c/backup/'s
	// stored report (id.backupKS, set at P4d) and records in the audit store, in
	// the commit that clears the entry.
	review, err := rbac.NewAdminReview(journal.NewReviewAcks(k.ident.backupKS), k.audit)
	if err != nil {
		return identityRoutes{}, fmt.Errorf("kernel: identity: the admin review: %w", err)
	}
	k.docs = &documents{}
	return identityRoutes{
		Auth: svc.Auth, RBAC: svc.RBAC, Review: review, Audit: k.audit,
		Client:            httpapi.ClientInfoFrom,
		OwnerRequired:     svc.Auth.OwnerRequired,
		AcceptDiagnostics: accept,

		// The kernel builds no OIDC flow: authhttp answers the two OIDC routes
		// unavailable until one exists (identityRoutes.OIDC).
		OIDCCallbackBase: k.cfg.AuthOptions.OIDCCallbackBase,
		Profiles:         svc.Auth,

		Config:         store,
		ConfigReadPerm: configReadPerm(store),
		Stager:         stager,

		Modules:       modulesView{k: k},
		Documents:     k.docs,
		OpenAPIPublic: public,
		ModuleIDs:     k.moduleIDs(),
		ModuleMetrics: sc,
		UIMetrics:     sc,
		Log:           k.coreLog,
	}, nil
}

// configReadPerm is confighttp.SectionReadPerm over the store's registrations.
func configReadPerm(st *config.Store) func(config.Path) (rbac.Permission, bool) {
	return func(section config.Path) (rbac.Permission, bool) {
		p, ok := st.ReadPermission(section)
		return rbac.Permission(p), ok
	}
}

// acceptDiagnostics is uihttp's core.ui.diagnostics.accept, read per request.
func (k *Kernel) acceptDiagnostics() (func() bool, error) {
	h, ok := handleOf[uiSettings](k.cfgBoot.handles)
	if !ok {
		return nil, errors.New("kernel: identity: the core.ui section is not registered")
	}
	return func() bool { return h.Get().Diagnostics.Accept }, nil
}

// openAPIPublic is modulehttp's core.http.openapi.public, read per request.
func (k *Kernel) openAPIPublic() (func() bool, error) {
	h, ok := handleOf[httpSettings](k.cfgBoot.handles)
	if !ok {
		return nil, errors.New("kernel: identity: the core.http section is not registered")
	}
	return func() bool { return h.Get().OpenAPI.Public }, nil
}

// moduleIDs is every compiled module's ID, in catalog order.
func (k *Kernel) moduleIDs() []string {
	ids := make([]string, 0, len(k.entries))
	for _, e := range k.entries {
		ids = append(ids, string(e.man.ID))
	}
	return ids
}

// verifiedMethod is taskhttp.Deps.VerifiedMethod: the kernel's rule over auth's
// key records (verifiedMethodRule) when the app declared identity, nil
// otherwise, which keeps taskhttp's own default (an interactive session only).
func (k *Kernel) verifiedMethod() func(*auth.Principal, rbac.Requirement) bool {
	if k.ident == nil || k.ident.svc.Auth == nil {
		return nil
	}
	return verifiedMethodRule(k.cfg.Authenticators, k.ident.svc.Auth.APIKeys)
}

// documents is modulehttp.Documents over the OpenAPI documents of the built
// router. The router is Built after the routes mount, so the map is filled by
// handlerPhase (fillDocuments) and read only by requests, which cannot arrive
// before the handler exists; until then, and in a boot that never Builds, no
// owner has a document.
type documents struct {
	m atomic.Pointer[modulehttp.DocumentMap]
}

// OpenAPI implements modulehttp.Documents.
func (d *documents) OpenAPI(owner string) ([]byte, bool) {
	m := d.m.Load()
	if m == nil {
		return nil, false
	}
	return m.OpenAPI(owner)
}

// fillDocuments builds the documents once, after Router.Build: core's and each
// enabled module's, the set Describe returns.
func (k *Kernel) fillDocuments() error {
	if k.docs == nil {
		return nil
	}
	d, err := k.describe()
	if err != nil {
		return err
	}
	m := modulehttp.DocumentMap{"core": d.Core}
	maps.Copy(m, d.OpenAPI)
	k.docs.m.Store(&m)
	return nil
}

// modulesView is modulehttp.ModulesView over the kernel's catalog.
type modulesView struct{ k *Kernel }

// Snapshot reads the catalog, the stored enablement and the served bundle at
// one moment. It cannot fail today: every input is memory the boot holds.
func (v modulesView) Snapshot(context.Context) (modulehttp.Snapshot, error) {
	k := v.k
	s := modulehttp.Snapshot{
		CoreVersion: version.Version,
		App: modulehttp.App{
			ID: k.cfg.AppID, DisplayName: k.cfg.DisplayName, Version: k.cfg.Version,
			UIBuildID: k.cfg.UI.BuildID(),
		},
		RestartRequired: v.restartRequired(),
		Modules:         make([]modulehttp.Module, 0, len(k.entries)),
	}
	for _, e := range k.entries {
		u := e.man.UI
		s.Modules = append(s.Modules, modulehttp.Module{
			ID: string(e.man.ID), Version: e.man.Version, DisplayName: e.man.DisplayName, Enabled: e.enabled,
			UI: modulehttp.UI{
				Pages: u.Pages, Nav: u.Nav, SettingsPanels: u.SettingsPanels, Widgets: u.Widgets, Banners: u.Banners,
				TopBarItems: u.TopBarItems, UserPage: u.UserPage, ActivityPanes: u.ActivityPanes, SetupSteps: u.SetupSteps,
			},
		})
	}
	return s, nil
}

// restartRequired reports that a module's stored enablement differs from the
// running state (06 §6.5.3): core.modules.<id>.enabled is a boot key, so a
// change is stored (Pending) and takes effect at the next start. A section
// the store does not hold, or a module it has no row for, is not a difference.
func (v modulesView) restartRequired() bool {
	h, ok := handleOf[map[string]moduleSettings](v.k.cfgBoot.handles)
	if !ok {
		return false
	}
	stored := *h.Pending()
	for _, e := range v.k.entries {
		if s, found := stored[string(e.man.ID)]; found && s.Enabled != e.enabled {
			return true
		}
	}
	return false
}

// describeIdentity is Describe's counterpart of mountIdentity: the same stage
// wiring and the same mount, over inputs that cannot serve, because Describe
// opens no store and builds no service (COR2-13) and its OpenAPI document must
// still carry the identity routes and the non-Public routes of the modules the
// router would otherwise refuse. Nothing here is ever called: Describe Builds
// and renders the router, it never serves it.
func (k *Kernel) describeIdentity() error {
	review, err := rbac.NewAdminReview(journal.NewReviewAcks(storage.Keyspace{}), new(audit.Store))
	if err != nil {
		return fmt.Errorf("kernel: describe: the admin review: %w", err)
	}
	if err := k.router.SetAuth(httpapi.AuthWiring{Service: inertAuthenticator{}, ResolveAtom: k.cfg.ResolveAtom}); err != nil {
		return fmt.Errorf("kernel: describe: stages 12 and 13: %w", err)
	}
	return mountIdentityRoutes(k.router, identityRoutes{
		Auth: new(auth.Service), RBAC: new(rbac.Service), Review: review, Audit: new(audit.Store),
		Client:            httpapi.ClientInfoFrom,
		OwnerRequired:     func() bool { return true },
		AcceptDiagnostics: func() bool { return false },
		OIDCCallbackBase:  k.cfg.AuthOptions.OIDCCallbackBase,
		Config:            inertConfig{},
		ConfigReadPerm:    func(config.Path) (rbac.Permission, bool) { return "", false },
		Stager:            inertStager{},
		Modules:           inertModules{},
		Documents:         modulehttp.DocumentMap{},
	})
}

// inertAuthenticator is the Authenticator of a router that is described, not
// served: stage 12 refuses every request.
type inertAuthenticator struct{}

func (inertAuthenticator) Middleware() func(http.Handler) http.Handler {
	return func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "described, not served", http.StatusServiceUnavailable)
		})
	}
}

func (inertAuthenticator) SessionCookie(auth.ClientInfo, string, time.Time) *http.Cookie {
	return &http.Cookie{}
}

var errNotServed = errors.New("kernel: described, not served")

type inertConfig struct{}

func (inertConfig) Apply(context.Context, config.Patch, config.Actor) (config.Result, error) {
	return config.Result{}, errNotServed
}
func (inertConfig) Schema(func(config.Path) bool) (json.RawMessage, error) { return nil, errNotServed }
func (inertConfig) Keys() iter.Seq2[config.Path, config.KeyState] {
	return func(func(config.Path, config.KeyState) bool) {}
}
func (inertConfig) Lookup(config.Path) (config.KeyState, bool) { return config.KeyState{}, false }

type inertStager struct{}

func (inertStager) StageReset(context.Context, string, storage.ResetRequest) (storage.ResetStaged, error) {
	return storage.ResetStaged{}, errNotServed
}

type inertModules struct{}

func (inertModules) Snapshot(context.Context) (modulehttp.Snapshot, error) {
	return modulehttp.Snapshot{}, errNotServed
}
