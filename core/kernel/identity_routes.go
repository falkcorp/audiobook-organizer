// file: core/kernel/identity_routes.go
// version: 0.1.0
// guid: 7b2e9d41-5c86-4f13-a0e7-3d18c6b5f924
// last-edited: 2026-11-21

package kernel

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/internal/audithttp"
	"github.com/falkcorp/mediacore/internal/authhttp"
	"github.com/falkcorp/mediacore/internal/confighttp"
	"github.com/falkcorp/mediacore/internal/modulehttp"
	"github.com/falkcorp/mediacore/internal/rbachttp"
	"github.com/falkcorp/mediacore/internal/storagehttp"
	"github.com/falkcorp/mediacore/internal/uihttp"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/rbac"
)

// errIdentityRoutes is a mount refused for a missing input.
var errIdentityRoutes = errors.New("kernel: identity routes: a required input is nil")

// identityRoutes is everything mountIdentityRoutes takes (F4-08 A12, dark
// until B4 mounts it after stage 13 is in the chain). It mounts the seven
// packages seams.d/F4-05.md leaves to the kernel: authhttp (its OIDC pair on
// CoreFixedGroup when OIDCCallbackBase is set, as authhttp decides) and the
// six worker packages on CoreGroup.
//
// The inputs every package shares are taken once and fanned out, so the
// packages cannot disagree: one audit store, one client resolver (the one auth
// itself uses), one owner-required reader (stage 13 must be handed the same
// one), one logger source.
//
// Required, because a nil would open something, lose an audit record, let two
// packages disagree, or leave a route answering 503 for the life of the boot:
// Auth, RBAC, Audit, Client, OwnerRequired, AcceptDiagnostics, Config,
// ConfigReadPerm, Stager, Modules and Documents. In particular a nil
// uihttp.AcceptDiagnostics means "accept", and a nil audit sink in authhttp or
// rbachttp drops core.permission.deny records without a word.
//
// Optional, because a nil closes the routes it serves: OIDC (no provider is
// configured; the OIDC routes answer unavailable), Profiles (PATCH /me answers
// 501 profile_edit_unavailable until a writer exists), OpenAPIPublic (nil is
// false), the two metrics scopes (nil records nothing) and Log (nil discards).
type identityRoutes struct {
	Auth *auth.Service
	RBAC *rbac.Service
	// Audit is the boot's one audit store (buildAudit): authhttp's and
	// rbachttp's denial records, audithttp's reader and export record,
	// storagehttp's reset record.
	Audit *audit.Store
	// Client is auth.Deps.Client, the resolver stage 0 and stage 12 use.
	Client func(*http.Request) auth.ClientInfo
	// OwnerRequired is core.auth.owner.required, read per request; the same
	// function goes to stage 13 (httpapi.Stage13Options.OwnerRequired).
	OwnerRequired func() bool
	// AcceptDiagnostics is core.ui.accept_diagnostics, read per request.
	AcceptDiagnostics func() bool

	OIDC             authhttp.Flow
	OIDCCallbackBase string
	Profiles         authhttp.Profiles

	Config         confighttp.Store
	ConfigReadPerm confighttp.SectionReadPerm
	Stager         storagehttp.Stager

	Modules       modulehttp.ModulesView
	Documents     modulehttp.Documents
	OpenAPIPublic func() bool
	ModuleIDs     []string
	ModuleMetrics *metrics.Scope
	UIMetrics     *metrics.Scope

	// Log returns the logger for one subsystem (Kernel.coreLog).
	Log func(subsystem string) *slog.Logger
}

// missing names every required input that is nil, sorted.
func (in identityRoutes) missing() []string {
	var out []string
	for name, isNil := range map[string]bool{
		"Auth": in.Auth == nil, "RBAC": in.RBAC == nil, "Audit": in.Audit == nil,
		"Client": in.Client == nil, "OwnerRequired": in.OwnerRequired == nil,
		"AcceptDiagnostics": in.AcceptDiagnostics == nil,
		"Config":            in.Config == nil, "ConfigReadPerm": in.ConfigReadPerm == nil,
		"Stager":  in.Stager == nil,
		"Modules": in.Modules == nil, "Documents": in.Documents == nil,
	} {
		if isNil {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// identityDeps is each package's Deps, built from one identityRoutes.
type identityDeps struct {
	Auth    authhttp.Deps
	RBAC    rbachttp.Deps
	Audit   audithttp.Deps
	Config  confighttp.Deps
	Storage storagehttp.Deps
	Module  modulehttp.Deps
	UI      uihttp.Deps
}

// deps fans the shared inputs out to every package that takes them. A
// package whose logger is nil gets a discarding one: confighttp and
// storagehttp keep a nil logger as it is.
func (in identityRoutes) deps() identityDeps {
	log := func(sub string) *slog.Logger {
		if in.Log != nil {
			if l := in.Log(sub); l != nil {
				return l
			}
		}
		return slog.New(slog.DiscardHandler)
	}
	ad := authhttp.FromService(in.Auth, in.RBAC)
	ad.Flow, ad.Profiles, ad.Audit, ad.Client = in.OIDC, in.Profiles, in.Audit, in.Client
	ad.OIDCCallbackBase, ad.Log = in.OIDCCallbackBase, log("authhttp")
	return identityDeps{
		Auth:    ad,
		RBAC:    rbachttp.Deps{Roles: in.RBAC, Audit: in.Audit, Log: log("rbachttp")},
		Audit:   audithttp.Deps{Reader: in.Audit, Recorder: in.Audit, Log: log("audithttp")},
		Config:  confighttp.Deps{Store: in.Config, ReadPerm: in.ConfigReadPerm, Log: log("confighttp")},
		Storage: storagehttp.Deps{Stager: in.Stager, Audit: in.Audit, OwnerRequired: in.OwnerRequired, Log: log("storagehttp")},
		Module: modulehttp.Deps{
			Modules: in.Modules, Documents: in.Documents, OpenAPIPublic: in.OpenAPIPublic,
			Scope: in.ModuleMetrics, Log: log("modulehttp"),
		},
		UI: uihttp.Deps{ModuleIDs: in.ModuleIDs, AcceptDiagnostics: in.AcceptDiagnostics, Scope: in.UIMetrics, Log: log("uihttp")},
	}
}

// mountIdentityRoutes registers the seven packages' routes on r. Every input
// is checked before the first registration, so a refused mount registers
// nothing. The two packages whose Register can still fail on its own
// (modulehttp and uihttp, building their metrics) go first; a failure there
// after modulehttp has registered leaves its routes on r, and the boot that
// called this refuses to serve anyway.
func mountIdentityRoutes(r *httpapi.Router, in identityRoutes) error {
	if r == nil {
		return fmt.Errorf("%w: router", errIdentityRoutes)
	}
	if m := in.missing(); len(m) > 0 {
		return fmt.Errorf("%w: %s", errIdentityRoutes, strings.Join(m, ", "))
	}
	d := in.deps()
	g := r.CoreGroup()
	if err := modulehttp.Register(g, d.Module); err != nil {
		return fmt.Errorf("kernel: identity routes: modulehttp: %w", err)
	}
	if err := uihttp.Register(g, d.UI); err != nil {
		return fmt.Errorf("kernel: identity routes: uihttp: %w", err)
	}
	if err := authhttp.Register(r, d.Auth); err != nil {
		return fmt.Errorf("kernel: identity routes: authhttp: %w", err)
	}
	rbachttp.Register(g, d.RBAC)
	audithttp.Register(g, d.Audit)
	confighttp.Register(g, d.Config)
	storagehttp.Register(g, d.Storage)
	return nil
}
