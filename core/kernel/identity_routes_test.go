// file: core/kernel/identity_routes_test.go
// version: 0.1.0
// guid: 0d6c3a85-e17b-4f29-9b54-a2f8c71e3d60
// last-edited: 2026-11-21

package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"log/slog"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/internal/audithttp"
	"github.com/falkcorp/mediacore/internal/authhttp"
	"github.com/falkcorp/mediacore/internal/confighttp"
	"github.com/falkcorp/mediacore/internal/modulehttp"
	"github.com/falkcorp/mediacore/internal/rbachttp"
	"github.com/falkcorp/mediacore/internal/storagehttp"
	"github.com/falkcorp/mediacore/internal/uihttp"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// The kernel's mount of the seven F4-05 packages (F4-08 A12). Build refuses
// every non-Public native route until stage 13 is in the chain (B4), so the
// mount is tested as the packages are (seams.d/F4-05.md "Registration
// shape"): the route table, and a Build whose only complaint is the missing
// auth service. The real-chain 401/403 table is B4's.

type rtConfig struct{}

func (rtConfig) Apply(context.Context, config.Patch, config.Actor) (config.Result, error) {
	return config.Result{}, nil
}
func (rtConfig) Schema(func(config.Path) bool) (json.RawMessage, error) { return nil, nil }
func (rtConfig) Keys() iter.Seq2[config.Path, config.KeyState] {
	return func(func(config.Path, config.KeyState) bool) {}
}
func (rtConfig) Lookup(config.Path) (config.KeyState, bool) { return config.KeyState{}, false }

type rtStager struct{}

func (rtStager) StageReset(context.Context, string, storage.ResetRequest) (storage.ResetStaged, error) {
	return storage.ResetStaged{}, nil
}

type rtModules struct{}

func (rtModules) Snapshot(context.Context) (modulehttp.Snapshot, error) {
	return modulehttp.Snapshot{}, nil
}

// routesFx is a complete identityRoutes over A3's in-memory assembly.
func routesFx(t *testing.T) identityRoutes {
	t.Helper()
	f := newIDFx(t)
	svc := f.build(f.inputs())
	return identityRoutes{
		Auth: svc.Auth, RBAC: svc.RBAC, Audit: f.audit,
		Client:            func(*http.Request) auth.ClientInfo { return auth.ClientInfo{} },
		OwnerRequired:     func() bool { return true },
		AcceptDiagnostics: func() bool { return false },
		Config:            rtConfig{},
		ConfigReadPerm:    func(config.Path) (rbac.Permission, bool) { return "", false },
		Stager:            rtStager{},
		Modules:           rtModules{},
		Documents:         modulehttp.DocumentMap{},
		ModuleIDs:         []string{"gotham"},
	}
}

func newRTRouter() *httpapi.Router {
	return httpapi.NewRouter(httpapi.Options{}, httpapi.Deps{Log: slog.New(slog.DiscardHandler)})
}

// routeKeys is "METHOD pattern" for every route on r.
func routeKeys(r *httpapi.Router) []string {
	var out []string
	for ri := range r.Routes() {
		out = append(out, ri.Method+" "+ri.Pattern)
	}
	slices.Sort(out)
	return out
}

// ownTables registers each package alone on its own router with the same
// inputs and returns the routes each one declares.
func ownTables(t *testing.T, in identityRoutes) map[string][]string {
	t.Helper()
	discard := slog.New(slog.DiscardHandler)
	out := map[string][]string{}
	one := func(name string, reg func(r *httpapi.Router) error) {
		r := newRTRouter()
		if err := reg(r); err != nil {
			t.Fatalf("%s alone: %v", name, err)
		}
		out[name] = routeKeys(r)
	}
	one("authhttp", func(r *httpapi.Router) error {
		d := authhttp.FromService(in.Auth, in.RBAC)
		d.OIDCCallbackBase = in.OIDCCallbackBase
		return authhttp.Register(r, d)
	})
	one("rbachttp", func(r *httpapi.Router) error {
		rbachttp.Register(r.CoreGroup(), rbachttp.Deps{Log: discard})
		return nil
	})
	one("audithttp", func(r *httpapi.Router) error {
		audithttp.Register(r.CoreGroup(), audithttp.Deps{Log: discard})
		return nil
	})
	one("confighttp", func(r *httpapi.Router) error {
		confighttp.Register(r.CoreGroup(), confighttp.Deps{Log: discard})
		return nil
	})
	one("storagehttp", func(r *httpapi.Router) error {
		storagehttp.Register(r.CoreGroup(), storagehttp.Deps{Log: discard})
		return nil
	})
	one("modulehttp", func(r *httpapi.Router) error {
		return modulehttp.Register(r.CoreGroup(), modulehttp.Deps{Modules: rtModules{}, Documents: modulehttp.DocumentMap{}})
	})
	one("uihttp", func(r *httpapi.Router) error {
		return uihttp.Register(r.CoreGroup(), uihttp.Deps{})
	})
	return out
}

// TestMountIdentityRoutes_MountsEveryPackagesTable: the mount registers
// exactly the union of the seven packages' own tables, none of them twice,
// with the OIDC pair on the native path or on the fixed group as the
// callback base says.
func TestMountIdentityRoutes_MountsEveryPackagesTable(t *testing.T) {
	for _, base := range []string{"", "/api/v1/auth/oauth"} {
		t.Run("callback base "+base, func(t *testing.T) {
			in := routesFx(t)
			in.OIDCCallbackBase = base
			r := newRTRouter()
			if err := mountIdentityRoutes(r, in); err != nil {
				t.Fatalf("mount: %v", err)
			}
			got := routeKeys(r)
			var want []string
			for name, keys := range ownTables(t, in) {
				if len(keys) == 0 {
					t.Errorf("%s declares no routes", name)
				}
				want = append(want, keys...)
			}
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Fatalf("mounted routes differ from the packages' tables:\n got %v\nwant %v", got, want)
			}
			if c := slices.Compact(slices.Clone(want)); len(c) != len(want) {
				t.Fatalf("two packages declare the same route: %v", want)
			}
			fixed := false
			for _, k := range got {
				if strings.Contains(k, "/api/v1/auth/oauth/") {
					fixed = true
				}
			}
			if fixed != (base != "") {
				t.Errorf("OIDC on the fixed group = %v with callback base %q", fixed, base)
			}
		})
	}
}

// TestMountIdentityRoutes_BuildFailsOnlyForWantOfAuth: with everything
// mounted, Build's only complaint is one ErrNoAuthService per non-Public
// route: no collision between the packages, no foreign scope, no schema
// error. The day stage 13 is in the chain (B4) this is the whole of what
// stands between the mount and a served router.
func TestMountIdentityRoutes_BuildFailsOnlyForWantOfAuth(t *testing.T) {
	in := routesFx(t)
	in.OIDCCallbackBase = "/api/v1/auth/oauth"
	r := newRTRouter()
	if err := mountIdentityRoutes(r, in); err != nil {
		t.Fatalf("mount: %v", err)
	}
	nonPublic := 0
	for ri := range r.Routes() {
		if ri.Perm.Kind() != rbac.KindPublic {
			nonPublic++
		}
	}
	if nonPublic == 0 {
		t.Fatal("no non-Public route was mounted")
	}
	_, err := r.Build()
	if err == nil {
		t.Fatal("Build succeeded with non-Public routes and no auth service")
	}
	parts := []error{err}
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		parts = j.Unwrap()
	}
	if len(parts) != nonPublic {
		t.Errorf("Build returned %d errors, want one per non-Public route (%d): %v", len(parts), nonPublic, err)
	}
	for _, p := range parts {
		if !errors.Is(p, httpapi.ErrNoAuthService) {
			t.Errorf("Build error is not ErrNoAuthService: %v", p)
		}
	}
}

// TestMountIdentityRoutes_RefusesAMissingInput: each required input, nil,
// refuses the mount, names itself, and registers nothing.
func TestMountIdentityRoutes_RefusesAMissingInput(t *testing.T) {
	full := routesFx(t)
	for name, drop := range map[string]func(*identityRoutes){
		"Auth":              func(in *identityRoutes) { in.Auth = nil },
		"RBAC":              func(in *identityRoutes) { in.RBAC = nil },
		"Audit":             func(in *identityRoutes) { in.Audit = nil },
		"Client":            func(in *identityRoutes) { in.Client = nil },
		"OwnerRequired":     func(in *identityRoutes) { in.OwnerRequired = nil },
		"AcceptDiagnostics": func(in *identityRoutes) { in.AcceptDiagnostics = nil },
		"Config":            func(in *identityRoutes) { in.Config = nil },
		"ConfigReadPerm":    func(in *identityRoutes) { in.ConfigReadPerm = nil },
		"Stager":            func(in *identityRoutes) { in.Stager = nil },
		"Modules":           func(in *identityRoutes) { in.Modules = nil },
		"Documents":         func(in *identityRoutes) { in.Documents = nil },
	} {
		t.Run(name, func(t *testing.T) {
			in := full
			drop(&in)
			r := newRTRouter()
			err := mountIdentityRoutes(r, in)
			if !errors.Is(err, errIdentityRoutes) || !strings.Contains(err.Error(), name) {
				t.Fatalf("err = %v, want errIdentityRoutes naming %s", err, name)
			}
			if keys := routeKeys(r); len(keys) != 0 {
				t.Errorf("a refused mount registered %v", keys)
			}
		})
	}
	if err := mountIdentityRoutes(nil, full); !errors.Is(err, errIdentityRoutes) {
		t.Errorf("nil router: err = %v", err)
	}
}

// TestMountIdentityRoutes_OptionalInputsMayBeNil: the inputs whose nil closes
// what they serve (no OIDC provider, no profile writer, OpenAPI not public,
// no metrics, no logger) do not refuse the mount.
func TestMountIdentityRoutes_OptionalInputsMayBeNil(t *testing.T) {
	in := routesFx(t)
	in.OIDC, in.Profiles, in.OpenAPIPublic, in.ModuleMetrics, in.UIMetrics, in.Log, in.ModuleIDs = nil, nil, nil, nil, nil, nil, nil
	if err := mountIdentityRoutes(newRTRouter(), in); err != nil {
		t.Fatalf("mount with every optional input nil: %v", err)
	}
	in.Log = func(string) *slog.Logger { return nil }
	if err := mountIdentityRoutes(newRTRouter(), in); err != nil {
		t.Fatalf("mount with a Log that returns nil: %v", err)
	}
}

// fnPtr identifies a func value, for "the same function was passed".
func fnPtr(f any) uintptr { return reflect.ValueOf(f).Pointer() }

// TestIdentityDeps_FansOutTheSharedInputs: the one audit store reaches every
// package that records or reads (authhttp's and rbachttp's denial records,
// audithttp's reader and export record, storagehttp's reset record), the one
// owner-required reader reaches storagehttp, the one client resolver reaches
// authhttp, AcceptDiagnostics reaches uihttp, and every package has a logger
// even when Log is nil.
func TestIdentityDeps_FansOutTheSharedInputs(t *testing.T) {
	in := routesFx(t)
	in.Profiles = rtProfiles{}
	d := in.deps()
	for name, got := range map[string]any{
		"authhttp.Audit": d.Auth.Audit, "rbachttp.Audit": d.RBAC.Audit,
		"audithttp.Reader": d.Audit.Reader, "audithttp.Recorder": d.Audit.Recorder,
		"storagehttp.Audit": d.Storage.Audit,
	} {
		if got != any(in.Audit) {
			t.Errorf("%s is %v, not the boot's audit store", name, got)
		}
	}
	if d.Storage.OwnerRequired == nil || fnPtr(d.Storage.OwnerRequired) != fnPtr(in.OwnerRequired) {
		t.Error("storagehttp.OwnerRequired is not the shared owner-required reader")
	}
	if d.Auth.Client == nil || fnPtr(d.Auth.Client) != fnPtr(in.Client) {
		t.Error("authhttp.Client is not the shared client resolver")
	}
	if d.UI.AcceptDiagnostics == nil || fnPtr(d.UI.AcceptDiagnostics) != fnPtr(in.AcceptDiagnostics) {
		t.Error("uihttp.AcceptDiagnostics is not core.ui.accept_diagnostics")
	}
	if d.RBAC.Roles != any(in.RBAC) || d.Auth.Dominance != any(in.RBAC) || d.Auth.Entry != any(in.Auth) {
		t.Error("the services are not the assembly's")
	}
	if d.Auth.Profiles != any(in.Profiles) || d.Config.Store != any(in.Config) || d.Storage.Stager != any(in.Stager) {
		t.Error("an input did not reach its package")
	}
	if d.Config.ReadPerm == nil || fnPtr(d.Config.ReadPerm) != fnPtr(in.ConfigReadPerm) {
		t.Error("confighttp.ReadPerm is not the kernel's section rule")
	}
	in.Log = nil
	d = in.deps()
	for name, l := range map[string]*slog.Logger{
		"authhttp": d.Auth.Log, "rbachttp": d.RBAC.Log, "audithttp": d.Audit.Log, "confighttp": d.Config.Log,
		"storagehttp": d.Storage.Log, "modulehttp": d.Module.Log, "uihttp": d.UI.Log,
	} {
		if l == nil {
			t.Errorf("%s has a nil logger with Log nil", name)
		}
	}
}

type rtProfiles struct{}

func (rtProfiles) UpdateProfile(context.Context, *auth.Principal, authhttp.ProfileUpdate) (*auth.User, error) {
	return nil, nil
}
