// file: core/kernel/metrics_mount.go
// version: 0.1.0
// guid: 14a8b4ae-645c-411f-beca-7d2267aa089f
// last-edited: 2026-11-20

package kernel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/rbac"
)

// The /metrics mount (05-metrics §6, M5; 05-http-api §3.5). It is dark: boot
// does not call mountMetrics until the F8-04 fold, and until then ABO's gin
// /metrics route serves the same Provider.Handler().
//
// Everything the mount adds to the App's registry is added INSIDE
// mountMetrics, never by a constructor or at boot: the <namespace>_build_info
// gauge registered at boot would be a new family on today's gin /metrics
// (which serves this Provider), so the mount would not be dark.

// metricsPermission is the scope /metrics requires (05-metrics §6, SEC2-14).
const metricsPermission rbac.Permission = "core:metrics.read"

// defaultMetricsPath is core.metrics.path's default.
const defaultMetricsPath = "/metrics"

// buildInfoSubsystem names the build_info scope: Provider.Core("build") has the
// prefix "<namespace>.build", and its one instrument "<namespace>.build.info"
// is exported as <namespace>_build_info (09 §9.10).
const buildInfoSubsystem = "build"

// buildInfoVersion is the build_info gauge's one label (09 §9.10).
const buildInfoVersion attribute.Key = "version"

// infraMounter is the one Router method the mount calls (*httpapi.Router).
type infraMounter interface {
	Infra(pattern string, perm rbac.Requirement, h http.Handler)
}

// mountMetrics mounts p's handler at path ("" = /metrics) as a Router.Infra
// route requiring core:metrics.read, and registers the <namespace>_build_info
// gauge (label version = the app's release version, value 1) on p. It is
// called once per Provider: a second call fails, because the build_info
// scope is already taken.
//
// The route's permission is recorded on the route for the chain's stages
// 10-13 (SEC2-14), and the handler repeats it, as every core route does, so a
// mounting mistake never opens the scrape: no principal is 401, a principal
// that lacks the scope is 403. The probes /livez and /readyz stay Public and
// are not mounted here (healthhttp.RegisterProbes).
//
// Stage 9 skips compression only for the exact pattern "/metrics"
// (httpapi.Router), so a non-default path is compressed by the chain as well
// as by promhttp; F8-04's fold passes the default.
func mountMetrics(r infraMounter, p *metrics.Provider, path, version string, log *slog.Logger) error {
	if r == nil || p == nil {
		return errors.New("kernel: metrics mount needs the router and the Provider")
	}
	if path == "" {
		path = defaultMetricsPath
	}
	if err := registerBuildInfo(p, version); err != nil {
		return err
	}
	r.Infra(http.MethodGet+" "+path, rbac.Need(metricsPermission), metricsGate(p.Handler(), log))
	return nil
}

// registerBuildInfo registers <namespace>_build_info{version} = 1 on p.
func registerBuildInfo(p *metrics.Provider, version string) error {
	scope, err := p.Core(buildInfoSubsystem, buildInfoVersion)
	if err != nil {
		return fmt.Errorf("kernel: build_info scope: %w", err)
	}
	attrs := metric.WithAttributes(buildInfoVersion.String(version))
	_, err = scope.Meter().Int64ObservableGauge(scope.Prefix()+".info",
		metric.WithDescription("The running build: always 1, labelled with the app's release version."),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(1, attrs)
			return nil
		}))
	if err != nil {
		return fmt.Errorf("kernel: build_info gauge: %w", err)
	}
	return nil
}

// metricsGate repeats the route's permission in front of h, in the order stage
// 13 refuses: a principal (401), then core:metrics.read by both of the
// checker's answers (403). Holding another scope, core:system.manage
// included, is not enough: the scope is explicit (05-metrics §6, R6).
func metricsGate(h http.Handler, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := auth.PrincipalFrom(r.Context())
		switch {
		case !ok || p == nil:
			httpapi.WriteError(w, r, &httpapi.Error{Status: http.StatusUnauthorized, Code: "authentication_required", Detail: "authentication is required"}, log)
			return
		case p.Perms == nil || !p.Perms.Has(metricsPermission) || !p.Perms.Satisfies(rbac.Need(metricsPermission)):
			httpapi.WriteError(w, r, httpapi.Forbidden(metricsPermission), log)
			return
		}
		h.ServeHTTP(w, r)
	})
}
