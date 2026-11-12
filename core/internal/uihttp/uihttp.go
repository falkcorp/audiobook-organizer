// file: core/internal/uihttp/uihttp.go
// version: 1.0.1
// guid: 8a4c2f17-6d3b-49e0-b1a5-c7d90e3f6b28
// last-edited: 2026-11-11

// Package uihttp is the HTTP surface the shell reports back through
// (05-http-api §5 item 2; 07 §5.2, §7.6.2, §7.16): POST /api/core/v1/ui/
// diagnostics, where the signed-in shell reports each UI diagnostic once per
// page load, and POST /api/core/v1/ui/csp-report, where a browser reports a
// Content-Security-Policy violation. The core ui package registers no route
// itself (COR2-19): these two live here, like every core capability's.
//
// Both routes take input a caller controls, csp-report from callers with no
// credential at all, so the package keeps three rules the tests pin. No
// submitted string becomes a metric label or a log field: the labels are
// chosen from closed sets (the diagnostic kinds, the CSP directive names, the
// registered module IDs) with an "other" bucket for everything else, so the
// series count is fixed at registration. No submitted document, URL or
// blocked-uri is logged, kept or returned. And every body is bounded by an
// explicit MaxBody.
//
// The package is layer L6 (03 R4, internal/<capability>http), imported only by
// kernel, and holds no package-level mutable state (R5). It is dark until F4-08
// mounts it.
package uihttp

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/rbac"
)

// Deps are what Register needs. All are optional.
type Deps struct {
	// ModuleIDs are the module IDs the app registered, the allow-list of the
	// module label of <ns>_ui_diagnostics_total: a diagnostic naming any other
	// module is counted under "other". The kernel passes every registered ID,
	// enabled or not, as plain strings (module.ID is L7).
	ModuleIDs []string
	// AcceptDiagnostics reports core.ui.diagnostics.accept, read at each
	// request (the key is hot-reloadable, 07 §7.15). Nil means true. When it is
	// false a diagnostic is answered like any other and recorded nowhere, so a
	// fail-open client learns nothing from the setting.
	AcceptDiagnostics func() bool
	// Scope is the core "ui" metrics scope. Nil records nothing.
	Scope *metrics.Scope
	// Log receives one bounded line per diagnostic and a rate-limited line per
	// batch of CSP reports. Nil discards.
	Log *slog.Logger
}

// api is what the handlers share; its fields are fixed at Register.
type api struct {
	modules []string
	accept  func() bool
	metrics *uiMetrics
	log     *slog.Logger
	budget  *logBudget
}

// Body limits. A diagnostic is a kind and a module ID; a real CSP report is
// well under 2 KiB (HEAD's cap is the same 8 KiB).
const (
	diagnosticMaxBody = 4 << 10 // 07 §7.5.2: body <= 4 KiB
	cspMaxBody        = 8 << 10 // 07 §7.6.2: body <= 8 KiB
)

// Register registers the two routes on g, which must be the router's
// CoreGroup(). It returns an error, and registers nothing, for a metrics scope
// that refuses a counter.
func Register(g *httpapi.Group, d Deps) error {
	a, err := newAPI(d)
	if err != nil {
		return err
	}
	httpapi.Handle(g, diagnosticsRoute(), a.reportDiagnostic)
	httpapi.HandleRaw(g, cspRoute(), httpapi.RawDoc{Status: http.StatusNoContent}, http.HandlerFunc(a.reportCSP))
	return nil
}

// newAPI builds the shared state and fills the defaults.
func newAPI(d Deps) (*api, error) {
	m, err := newMetrics(d.Scope, d.ModuleIDs)
	if err != nil {
		return nil, err
	}
	a := &api{modules: append([]string(nil), d.ModuleIDs...), accept: d.AcceptDiagnostics, metrics: m, log: d.Log, budget: newLogBudget()}
	if a.log == nil {
		a.log = slog.New(slog.DiscardHandler)
	}
	if a.accept == nil {
		a.accept = func() bool { return true }
	}
	return a, nil
}

// diagnosticsRoute is POST /ui/diagnostics: 07 §7.5.2 gives AnyAuthenticated,
// "rate-limited" and a 4 KiB body. It names no rate class beyond that, so the
// default per-client bucket applies, and no guard: a report is not a
// credential change, and Guards.Interactive would refuse the auth-disabled
// principal and API keys, whose shells still report. Lenient, because a later
// shell may add members; none is read or kept.
func diagnosticsRoute() httpapi.Route {
	return httpapi.Route{
		Method: http.MethodPost, Path: "/ui/diagnostics", Name: "reportUIDiagnostic", Perm: rbac.AnyAuthenticated,
		Tags: []string{"ui"}, MaxBody: diagnosticMaxBody, Lenient: true,
		Summary: "Report one UI diagnostic",
		Doc:     "The shell reports each diagnostic kind once per page load (07 §7.5.2). Only kind and module are read, and both are matched against closed lists: an unknown kind or module is counted as other, and no submitted text is logged or kept. Answers 204 whether or not the server accepts diagnostics.",
	}
}

// cspRoute is POST /ui/csp-report: 07 §7.6.2 gives Public, the RateAuth class
// and an 8 KiB body. A Public route has no principal, so there is nothing to
// guard.
func cspRoute() httpapi.Route {
	return httpapi.Route{
		Method: http.MethodPost, Path: "/ui/csp-report", Name: "reportCSPViolation", Perm: rbac.Public,
		Tags: []string{"ui"}, MaxBody: cspMaxBody, RateClass: httpapi.RateAuth,
		Summary: "Receive a browser's Content-Security-Policy violation report",
		Doc:     "Accepts application/csp-report (report-uri), application/reports+json (Reporting API, an array) and application/json, at most ten reports per request. Public because a browser sends a report with no credential. Only the violated directive is read, matched against the CSP directive names (anything else is counted as other); no URL or blocked-uri is logged or kept. Answers 204 for any well-formed request, whether or not it held a report.",
	}
}

// ---- POST /ui/diagnostics ---------------------------------------------------

// UIDiagnostic is one diagnostic the shell reports. Kind is a diagnostic kind
// of 07 §7.5.2 (core-version-incompatible, ui-missing, version-incompatible,
// undeclared-descriptor, module-not-enabled, descriptor-without-ui,
// app-id-mismatch); Module is the module it concerns, empty for a core one.
type UIDiagnostic struct {
	Kind   string `json:"kind"`
	Module string `json:"module,omitempty"`
}

type reportDiagnosticReq struct {
	Body UIDiagnostic `body:"json"`
}

// labelMax bounds kind and module; the longest kind is 25 characters and a
// module ID is far shorter than 64.
const labelMax = 64

// Validate bounds the two strings. Neither is echoed.
func (r *reportDiagnosticReq) Validate() error {
	var errs []httpapi.FieldError
	if r.Body.Kind == "" {
		errs = append(errs, httpapi.Field("/body/kind", "required", "is required"))
	} else if len(r.Body.Kind) > labelMax {
		errs = append(errs, httpapi.Field("/body/kind", "too_long", "must be at most 64 bytes"))
	}
	if len(r.Body.Module) > labelMax {
		errs = append(errs, httpapi.Field("/body/module", "too_long", "must be at most 64 bytes"))
	}
	if len(errs) > 0 {
		return httpapi.Invalid(errs...)
	}
	return nil
}

func (a *api) reportDiagnostic(ctx context.Context, in *httpapi.In[reportDiagnosticReq]) (httpapi.NoContent, error) {
	// Stage 13 answers this first once it exists (F4-08). Until then the
	// handler applies AnyAuthenticated itself: no principal is 401; one with no
	// compiled permission set, or a Confined one (05-rbac 4.10), is 403.
	switch p := in.Principal; {
	case p == nil:
		return httpapi.NoContent{}, &httpapi.Error{Status: http.StatusUnauthorized, Code: "authentication_required", Detail: "sign in to use this route"}
	case p.Perms == nil || !p.Perms.Satisfies(rbac.AnyAuthenticated):
		return httpapi.NoContent{}, &httpapi.Error{Status: http.StatusForbidden, Code: "permission_denied", Detail: "this route needs a signed-in caller"}
	}
	if !a.accept() {
		return httpapi.NoContent{}, nil
	}
	kind, module := kindLabel(in.Req.Body.Kind), moduleLabel(a.modules, in.Req.Body.Module)
	a.metrics.recordDiagnostic(ctx, kind, module)
	a.log.Info("ui diagnostic", "kind", kind, "module", module)
	return httpapi.NoContent{}, nil
}
